package testapi

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"go.origincloud.eu/sdk/core"
	longrunningv1beta1 "go.origincloud.eu/sdk/longrunning/v1beta1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/origincloudhq/terraform-provider-origincloud/internal/testapi/firewallsv1"
)

// testProvider serves the test API's resources and data sources, calling
// the endpoint its configuration names.
type testProvider struct{}

func (testProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "origincloud"
}

func (testProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"endpoint": schema.StringAttribute{Required: true},
	}}
}

func (testProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg struct {
		Endpoint types.String `tfsdk:"endpoint"`
	}
	if resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...); resp.Diagnostics.HasError() {
		return
	}
	client, err := core.NewClient(ctx, core.WithEndpoint(cfg.Endpoint.ValueString()))
	if err != nil {
		resp.Diagnostics.AddError("Couldn't configure the test provider", err.Error())
		return
	}
	resp.ResourceData, resp.DataSourceData = client, client
}

func (testProvider) Resources(context.Context) []func() resource.Resource { return serviceResources() }

func (testProvider) DataSources(context.Context) []func() datasource.DataSource {
	return serviceDataSources()
}

// fakeFirewalls serves the test API from memory. Like an API that keeps
// the order of ordered fields only, it answers with sets sorted: tags,
// modes and each spec's interfaces. It numbers each rule's output-only id,
// and an update replaces each top-level field its mask names, whole.
type fakeFirewalls struct {
	*httptest.Server

	mu        sync.Mutex
	next      int
	firewalls map[string]*firewallsv1.Firewall
	// masks are the update masks received, in order.
	masks []string
	// updates are the firewalls the updates sent, in order.
	updates []*firewallsv1.Firewall
}

func newFakeFirewalls(t *testing.T) *fakeFirewalls {
	f := &fakeFirewalls{firewalls: map[string]*firewallsv1.Firewall{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeFirewalls) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	body, _ := io.ReadAll(r.Body)
	switch {
	case strings.HasSuffix(path, "/firewalls") && r.Method == http.MethodPost:
		firewall := new(firewallsv1.Firewall)
		if err := protojson.Unmarshal(body, firewall); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		f.next++
		firewall.Name = fmt.Sprintf("%s/fw-%d", path, f.next)
		f.settle(firewall)
		f.firewalls[firewall.Name] = firewall
		metadata, _ := anypb.New(&longrunningv1beta1.OperationMetadata{Target: firewall.Name})
		result, _ := anypb.New(firewall)
		writeMessage(w, &longrunningpb.Operation{Name: firewall.Name + "/operations/op", Done: true, Metadata: metadata, Result: &longrunningpb.Operation_Response{Response: result}})
	case strings.HasSuffix(path, "/firewalls") && r.Method == http.MethodGet:
		list := &firewallsv1.ListFirewallsResponse{}
		for _, name := range slices.Sorted(maps.Keys(f.firewalls)) {
			list.Firewalls = append(list.Firewalls, f.firewalls[name])
		}
		writeMessage(w, list)
	default:
		firewall, ok := f.firewalls[path]
		if !ok {
			writeError(w, http.StatusNotFound, "NOT_FOUND", path+" doesn't exist")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeMessage(w, firewall)
		case http.MethodPatch:
			update := new(firewallsv1.Firewall)
			if err := protojson.Unmarshal(body, update); err != nil {
				writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
				return
			}
			mask := r.URL.Query().Get("updateMask")
			f.masks = append(f.masks, mask)
			f.updates = append(f.updates, update)
			fields := firewall.ProtoReflect().Descriptor().Fields()
			for _, name := range strings.Split(mask, ",") {
				field := fields.ByJSONName(name)
				if field == nil || field.Name() == "name" || field.Name() == "status" {
					writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", name+" can't be updated")
					return
				}
				if update.ProtoReflect().Has(field) {
					firewall.ProtoReflect().Set(field, update.ProtoReflect().Get(field))
				} else {
					firewall.ProtoReflect().Clear(field)
				}
			}
			f.settle(firewall)
			writeMessage(w, firewall)
		case http.MethodDelete:
			delete(f.firewalls, path)
			writeMessage(w, &emptypb.Empty{})
		}
	}
}

// settle sets what the API sets: the sets sorted, the rules' ids and the
// status.
func (f *fakeFirewalls) settle(firewall *firewallsv1.Firewall) {
	slices.Sort(firewall.Tags)
	slices.Sort(firewall.Modes)
	if spec := firewall.GetSpec(); spec != nil {
		slices.SortFunc(spec.Interfaces, func(a, b *firewallsv1.Interface) int { return cmp.Compare(a.GetSubnet(), b.GetSubnet()) })
	}
	for i, rule := range firewall.GetRules() {
		rule.Id = fmt.Sprintf("rule-%d", i+1)
	}
	firewall.Status = &firewallsv1.Status{State: firewallsv1.Mode_OPEN, RuleCount: int32(len(firewall.GetRules()))}
}

// reverseRules reverses a firewall's rules, as a change made outside
// Terraform.
func (f *fakeFirewalls) reverseRules(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	slices.Reverse(f.firewalls[name].Rules)
}

func (f *fakeFirewalls) lastMask() (string, *firewallsv1.Firewall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.masks) == 0 {
		return "", nil
	}
	return f.masks[len(f.masks)-1], f.updates[len(f.updates)-1]
}

func (f *fakeFirewalls) updateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.masks)
}

func writeMessage(w http.ResponseWriter, m proto.Message) {
	data, _ := protojson.Marshal(m)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, code int, status, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"status":%q,"message":%q}}`, code, status, message)
}
