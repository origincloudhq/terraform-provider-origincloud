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
	"time"

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
	"google.golang.org/protobuf/types/known/timestamppb"

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
	// keys are served too, under projects/{project}/keys.
	keys     map[string]*firewallsv1.Key
	keyMasks []string
	// vaults, and their quotas, too. A deleted vault's id is never used
	// again: tombstones are its names.
	vaults     map[string]*firewallsv1.Vault
	tombstones map[string]bool
	quotas     map[string]*firewallsv1.VaultQuota
	quotaMasks []string
}

func newFakeFirewalls(t *testing.T) *fakeFirewalls {
	f := &fakeFirewalls{firewalls: map[string]*firewallsv1.Firewall{}, vaults: map[string]*firewallsv1.Vault{}, tombstones: map[string]bool{}, quotas: map[string]*firewallsv1.VaultQuota{}, keys: map[string]*firewallsv1.Key{
		// A public key, in the other pattern.
		"keys/pub-1": {Name: "keys/pub-1", State: firewallsv1.Key_ENABLED, Algorithm: proto.String("ed25519"), Usage: &firewallsv1.Key_Sign{Sign: true}},
	}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeFirewalls) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	body, _ := io.ReadAll(r.Body)
	if path == "vaults" || strings.HasPrefix(path, "vaults/") {
		f.serveVault(w, r, path, body)
		return
	}
	if strings.HasPrefix(path, "keys/") || strings.Contains(path, "/keys") {
		f.serveKey(w, r, path, body)
		return
	}
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
				if field == nil || field.Name() == "name" || field.Name() == "status" || field.Name() == "placement" || field.Name() == "peers" {
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

// settle sets what the API sets: the sets sorted, the rules' ids, the
// peers' latencies, the status, a placement when there is none and its
// host, and an options' tier and slots when they are left out.
func (f *fakeFirewalls) settle(firewall *firewallsv1.Firewall) {
	slices.Sort(firewall.Tags)
	slices.Sort(firewall.Modes)
	if spec := firewall.GetSpec(); spec != nil {
		slices.SortFunc(spec.Interfaces, func(a, b *firewallsv1.Interface) int { return cmp.Compare(a.GetSubnet(), b.GetSubnet()) })
	}
	for i, rule := range firewall.GetRules() {
		rule.Id = fmt.Sprintf("rule-%d", i+1)
	}
	if firewall.Placement == nil {
		firewall.Placement = &firewallsv1.Placement{Group: "auto"}
	}
	firewall.Placement.Host = "host-" + firewall.Placement.GetGroup()
	if options := firewall.GetOptions(); options != nil {
		if options.Tier == "" {
			options.Tier = "standard"
		}
		if options.Slots == 0 {
			options.Slots = 4
		}
	}
	for _, peer := range firewall.GetPeers() {
		peer.Latency = int32(len(peer.GetAddress()))
		if peer.Port == 0 {
			peer.Port = 443
		}
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

// keyCreated is when the fake creates every key.
var keyCreated = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// serveKey serves the keys. A key is created at once, with a secret only
// the create answers with, and the algorithm and bits the API chooses
// when they are left out. Deleting one leaves it TERMINATED, still
// readable.
func (f *fakeFirewalls) serveKey(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	name, method, _ := strings.Cut(path, ":")
	switch {
	case strings.HasSuffix(path, "/keys") && r.Method == http.MethodGet:
		list := &firewallsv1.ListKeysResponse{}
		for _, name := range slices.Sorted(maps.Keys(f.keys)) {
			if strings.HasPrefix(name, path+"/") {
				list.Keys = append(list.Keys, f.keys[name])
			}
		}
		writeMessage(w, list)
		return
	case strings.HasSuffix(path, "/keys") && r.Method == http.MethodPost:
		key := new(firewallsv1.Key)
		if err := protojson.Unmarshal(body, key); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		f.next++
		key.Name = fmt.Sprintf("%s/k-%d", path, f.next)
		key.CreateTime = timestamppb.New(keyCreated)
		key.State = firewallsv1.Key_ENABLED
		f.settleKey(key)
		f.keys[key.Name] = key
		answer := proto.Clone(key).(*firewallsv1.Key)
		answer.Secret = fmt.Sprintf("secret-%d", f.next)
		if imported := answer.GetImported(); imported != nil {
			imported.ImportTime = timestamppb.New(keyCreated)
		}
		writeMessage(w, answer)
		return
	}
	key, ok := f.keys[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch {
	case method == "enable" || method == "disable":
		key.State = firewallsv1.Key_ENABLED
		if method == "disable" {
			key.State = firewallsv1.Key_DISABLED
		}
		result, _ := anypb.New(key)
		writeMessage(w, &longrunningpb.Operation{Name: name + "/operations/op", Done: true, Result: &longrunningpb.Operation_Response{Response: result}})
	case r.Method == http.MethodGet:
		writeMessage(w, key)
	case r.Method == http.MethodPatch:
		update := new(firewallsv1.Key)
		if err := protojson.Unmarshal(body, update); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		mask := r.URL.Query().Get("updateMask")
		f.keyMasks = append(f.keyMasks, mask)
		fields := key.ProtoReflect().Descriptor().Fields()
		for _, path := range strings.Split(mask, ",") {
			field := fields.ByJSONName(path)
			if field == nil || field.Name() == "name" || field.Name() == "state" || field.Name() == "secret" || field.Name() == "create_time" {
				writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", path+" can't be updated")
				return
			}
			if update.ProtoReflect().Has(field) {
				key.ProtoReflect().Set(field, update.ProtoReflect().Get(field))
			} else if key.ProtoReflect().Has(field) {
				key.ProtoReflect().Clear(field)
			}
		}
		f.settleKey(key)
		if imported := key.GetImported(); imported != nil && imported.ImportTime == nil {
			imported.ImportTime = timestamppb.New(keyCreated)
		}
		writeMessage(w, key)
	case r.Method == http.MethodDelete && key.GetAlgorithm() == "doomed":
		// Terminated, but the delete fails.
		key.State = firewallsv1.Key_TERMINATED
		writeError(w, http.StatusServiceUnavailable, "INTERNAL", "the delete failed after terminating "+name)
	case r.Method == http.MethodDelete:
		key.State = firewallsv1.Key_TERMINATED
		result, _ := anypb.New(&emptypb.Empty{})
		writeMessage(w, &longrunningpb.Operation{Name: name + "/operations/op", Done: true, Result: &longrunningpb.Operation_Response{Response: result}})
	}
}

// settleKey sets the values the API chooses for what was left out, and
// sorts the sets.
func (f *fakeFirewalls) settleKey(key *firewallsv1.Key) {
	if key.Algorithm == nil {
		key.Algorithm = proto.String("ed25519")
	}
	if key.Bits == nil {
		key.Bits = proto.Int32(256)
	}
	windows := append(append([]*firewallsv1.Window{}, key.Pools...), key.Frozen...)
	if key.Primary != nil {
		windows = append(windows, key.Primary)
	}
	for _, w := range windows {
		if w.Weight == nil {
			w.Weight = proto.Int32(int32(10 * len(w.GetName())))
		}
	}
	slices.SortFunc(key.Pools, func(a, b *firewallsv1.Window) int { return cmp.Compare(a.GetName(), b.GetName()) })
	slices.SortFunc(key.Frozen, func(a, b *firewallsv1.Window) int { return cmp.Compare(a.GetName(), b.GetName()) })
}

// terminate terminates a key, as an action outside Terraform.
func (f *fakeFirewalls) terminate(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[name].State = firewallsv1.Key_TERMINATED
}

func (f *fakeFirewalls) key(name string) *firewallsv1.Key {
	f.mu.Lock()
	defer f.mu.Unlock()
	return proto.Clone(f.keys[name]).(*firewallsv1.Key)
}

func (f *fakeFirewalls) lastKeyMask() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.keyMasks) == 0 {
		return ""
	}
	return f.keyMasks[len(f.keyMasks)-1]
}

// serveVault serves the vaults, created with the id the client chose,
// which a deleted vault keeps, and their quotas, which exist with them,
// without budgets until some are set. An update of a quota unsets a field
// its mask names and its body leaves out, and refuses an empty mask.
func (f *fakeFirewalls) serveVault(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	switch {
	case path == "vaults" && r.Method == http.MethodPost:
		vault := new(firewallsv1.Vault)
		if err := protojson.Unmarshal(body, vault); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		id := r.URL.Query().Get("vaultId")
		if id == "" {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "vault_id is required")
			return
		}
		vault.Name = "vaults/" + id
		if _, ok := f.vaults[vault.Name]; ok || f.tombstones[vault.Name] {
			writeError(w, http.StatusConflict, "ALREADY_EXISTS", vault.Name+" exists")
			return
		}
		vault.CreateTime = timestamppb.New(keyCreated)
		f.vaults[vault.Name] = vault
		// No budgets until some are set.
		f.quotas[vault.Name+"/quota"] = &firewallsv1.VaultQuota{Name: vault.Name + "/quota"}
		writeMessage(w, vault)
	case path == "vaults" && r.Method == http.MethodGet:
		owner := r.URL.Query().Get("owner")
		if owner == "" {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "owner is required")
			return
		}
		list := &firewallsv1.ListVaultsResponse{}
		for _, name := range slices.Sorted(maps.Keys(f.vaults)) {
			if f.vaults[name].GetOwner() == owner {
				list.Vaults = append(list.Vaults, f.vaults[name])
			}
		}
		writeMessage(w, list)
	case strings.HasSuffix(path, "/quota"):
		quota, ok := f.quotas[path]
		if !ok {
			writeError(w, http.StatusNotFound, "NOT_FOUND", path+" doesn't exist")
			return
		}
		if r.Method == http.MethodPatch {
			update := new(firewallsv1.VaultQuota)
			if err := protojson.Unmarshal(body, update); err != nil {
				writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
				return
			}
			mask := r.URL.Query().Get("updateMask")
			if mask == "" {
				writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "update_mask is required")
				return
			}
			f.quotaMasks = append(f.quotaMasks, mask)
			for _, field := range strings.Split(mask, ",") {
				switch field {
				case "maxSecrets":
					quota.MaxSecrets = update.MaxSecrets
				case "maxVersions":
					quota.MaxVersions = update.MaxVersions
				default:
					writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", field+" can't be updated")
					return
				}
			}
		}
		writeMessage(w, quota)
	default:
		vault, ok := f.vaults[path]
		if !ok {
			writeError(w, http.StatusNotFound, "NOT_FOUND", path+" doesn't exist")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeMessage(w, vault)
		case http.MethodPatch:
			update := new(firewallsv1.Vault)
			if err := protojson.Unmarshal(body, update); err != nil || r.URL.Query().Get("updateMask") != "displayName" {
				writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "only display_name can be updated")
				return
			}
			vault.DisplayName = update.GetDisplayName()
			writeMessage(w, vault)
		case http.MethodDelete:
			delete(f.vaults, path)
			delete(f.quotas, path+"/quota")
			f.tombstones[path] = true
			writeMessage(w, &emptypb.Empty{})
		}
	}
}

func (f *fakeFirewalls) quota(name string) (*firewallsv1.VaultQuota, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q, ok := f.quotas[name]
	if !ok {
		return nil, slices.Clone(f.quotaMasks)
	}
	return proto.Clone(q).(*firewallsv1.VaultQuota), slices.Clone(f.quotaMasks)
}
