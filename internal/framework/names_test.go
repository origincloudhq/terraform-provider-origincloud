package framework

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	volume     = "projects/p/zones/eu-central1-az1/volumes/vol-1"
	volumeFull = "//pvs.origincloudapis.eu/" + volume
)

func TestAFullNameIsTheRelativeNameOnTheHostOfItsService(t *testing.T) {
	for _, tc := range []struct {
		service, name, want string
		fails               bool
	}{
		{"pvs", volume, volumeFull, false},
		{"pvs", volumeFull, volumeFull, false}, // full already
		{"widgets", "projects/p/widgets/w", "//widgets.origincloudapis.eu/projects/p/widgets/w", false}, // a service the SDK doesn't know
		{"pvs", "", "", true},
		{"pvs", "//pvs.eu-central1-az1.origincloudapis.eu/" + volume, "", true}, // a zonal host isn't a name
		{"pvs", "urn:origin:cloud:storage:volume/vol-1", "", true},
		{"pvs", "projects//volumes", "", true},
	} {
		got, err := FullName(tc.service, tc.name)
		if tc.fails != (err != nil) || got.ValueString() != tc.want {
			t.Errorf("FullName(%q, %q) = %q, %v", tc.service, tc.name, got.ValueString(), err)
		}
	}
}

func TestAnImportedNameMayBeFullOrRelative(t *testing.T) {
	for _, tc := range []struct {
		id, want string
		fails    bool
	}{
		{volumeFull, volume, false},
		{volume, volume, false},
		{"//compute.origincloudapis.eu/" + volume, "", true},                            // another service's
		{"//pvs.eu-central1-az1.origincloudapis.eu/" + volume, "", true},                // a zonal host
		{"projects/p/zones/eu-central1-az1/snapshots/snap-1", "", true},                 // not a volume
		{"//pvs.origincloudapis.eu/projects/p/zones/eu-central1-az1/volumes", "", true}, // a collection
		{"vol-1", "", true},
	} {
		r := &Resource[struct{}, *wrapperspb.StringValue]{spec: ResourceSpec[struct{}, *wrapperspb.StringValue]{
			Service: "pvs", Pattern: "projects/{project}/zones/{zone}/volumes/{volume}",
		}}
		got, err := r.importName(tc.id)
		if tc.fails != (err != nil) || got != tc.want {
			t.Errorf("importName(%q) = %q, %v", tc.id, got, err)
		}
	}
}

func TestAStateOfVersion0IsUpgradedToTheFullNameWithoutTheUrn(t *testing.T) {
	old := `{
		"id": "` + volume + `",
		"urn": "urn:origincloud:pvs:volume/` + volume + `",
		"name": "` + volume + `",
		"storage_class": "storageClasses/gp1",
		"source_snapshot": "",
		"source_volume": null,
		"size_gib": 5,
		"tags": {"env": "test"}
	}`
	upgraded, err := UpgradeRawState("pvs", map[string]string{"storage_class": "pvs", "source_snapshot": "pvs", "source_volume": "pvs", "missing": "pvs"}, []byte(old))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(upgraded, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id":              volumeFull,
		"name":            volume, // stays relative
		"storage_class":   "//pvs.origincloudapis.eu/storageClasses/gp1",
		"source_snapshot": "", // empty stays
		"source_volume":   nil,
		"size_gib":        float64(5),
		"tags":            map[string]any{"env": "test"},
	}
	if len(got) != len(want) {
		t.Errorf("the upgraded state is %v, want %v", got, want)
	}
	for k, w := range want {
		if g, ok := got[k]; !ok || !equalJSON(g, w) {
			t.Errorf("%s is %v, want %v", k, g, w)
		}
	}
	if _, ok := got["urn"]; ok {
		t.Error("the urn is still there")
	}
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// An upgrade is permanent and safe to repeat: a full name, and a value that
// isn't a name, stay as they are.
func TestUpgradingAStateTwiceChangesNothingTheSecondTime(t *testing.T) {
	once, err := UpgradeRawState("pvs", map[string]string{"creator": "iam"}, []byte(`{"id":"`+volume+`","creator":"user:abc","urn":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	twice, err := UpgradeRawState("pvs", map[string]string{"creator": "iam"}, once)
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Errorf("upgraded once %s, twice %s", once, twice)
	}
	var got map[string]string
	_ = json.Unmarshal(once, &got)
	if got["creator"] != "user:abc" || got["id"] != volumeFull {
		t.Errorf("upgraded %v", got)
	}
}

func TestAnUpgradedStateIsReadWithTheCurrentSchema(t *testing.T) {
	r := &Resource[struct{}, *wrapperspb.StringValue]{spec: ResourceSpec[struct{}, *wrapperspb.StringValue]{
		Service: "pvs",
		Schema: schema.Schema{Attributes: map[string]schema.Attribute{
			"id":    schema.StringAttribute{Computed: true},
			"name":  schema.StringAttribute{Computed: true},
			"added": schema.StringAttribute{Computed: true}, // a later release's
		}},
	}}
	req := resource.UpgradeStateRequest{RawState: &tfprotov6.RawState{JSON: []byte(`{"id":"` + volume + `","name":"` + volume + `","urn":"urn:x"}`)}}
	resp := &resource.UpgradeStateResponse{}
	r.upgradeFromVersion0(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	typ := r.spec.Schema.Type().TerraformType(context.Background())
	value, err := resp.DynamicValue.Unmarshal(typ)
	if err != nil {
		t.Fatal(err)
	}
	var attrs map[string]tftypes.Value
	if err := value.As(&attrs); err != nil {
		t.Fatal(err)
	}
	var id string
	_ = attrs["id"].As(&id)
	if id != volumeFull || !attrs["added"].IsNull() || len(attrs) != 3 {
		t.Errorf("upgraded to %v", value)
	}
	if resp := (&Resource[struct{}, *wrapperspb.StringValue]{spec: r.spec}); resp.UpgradeState(context.Background())[0].StateUpgrader == nil {
		t.Error("no upgrader from version 0")
	}
}

func TestAReferenceMustBeAFullName(t *testing.T) {
	for _, tc := range []struct {
		value types.String
		valid bool
	}{
		{types.StringValue(volumeFull), true},
		{types.StringValue("//iam.origincloudapis.eu/users/u-1"), true},
		{types.StringNull(), true},
		{types.StringUnknown(), true}, // not known until apply
		{types.StringValue(""), true}, // an empty reference is none
		{types.StringValue(volume), false},
		{types.StringValue("vol-1"), false},
		{types.StringValue("urn:origin:cloud:storage:volume/vol-1"), false},
		{types.StringValue("//pvs.eu-central1-az1.origincloudapis.eu/" + volume), false},
	} {
		resp := &validator.StringResponse{}
		FullNameReference().ValidateString(context.Background(), validator.StringRequest{ConfigValue: tc.value}, resp)
		if resp.Diagnostics.HasError() == tc.valid {
			t.Errorf("%v: %v", tc.value, resp.Diagnostics)
		}
	}
}

func TestTheElementsOfAListOfReferencesMustBeFullNames(t *testing.T) {
	list := func(values ...string) types.List {
		elements := make([]types.String, len(values))
		for i, v := range values {
			elements[i] = types.StringValue(v)
		}
		l, _ := types.ListValueFrom(context.Background(), types.StringType, elements)
		return l
	}
	for _, tc := range []struct {
		list  types.List
		valid bool
	}{
		{list(volumeFull, "//iam.origincloudapis.eu/users/u-1"), true},
		{list(), true},
		{list(volumeFull, volume), false},
	} {
		resp := &validator.ListResponse{}
		ElementCheck{FullNames: true}.ValidateList(context.Background(), validator.ListRequest{ConfigValue: tc.list}, resp)
		if resp.Diagnostics.HasError() == tc.valid {
			t.Errorf("%v: %v", tc.list, resp.Diagnostics)
		}
	}
}

const (
	oldPolicy = "//network.origincloudapis.eu/projects/p/regions/r/dnsPolicies/office"
	newPolicy = "//network.origincloudapis.eu/projects/p/regions/r/resolverPolicies/office"
)

func TestAMovedStateIsRenamed(t *testing.T) {
	move := Move{
		TypeName:   "_network_dns_policy_association",
		Attributes: map[string]string{"dns_policy": "resolver_policy"},
		Segments:   map[string]string{"dnsPolicies": "resolverPolicies", "dnsPolicyAssociations": "resolverPolicyAssociations"},
	}
	raw := `{"id":"//network.origincloudapis.eu/projects/p/regions/r/vpcs/v/dnsPolicyAssociations/office","dns_policy":"` + oldPolicy + `","vpc":null,"count":3}`
	moved, err := MoveRawState(move, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(moved, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id":              "//network.origincloudapis.eu/projects/p/regions/r/vpcs/v/resolverPolicyAssociations/office",
		"resolver_policy": newPolicy,
		"vpc":             nil,
		"count":           float64(3),
	}
	if !equalJSON(got, want) {
		t.Errorf("moved to %s", moved)
	}
}

func TestAResourceTakesTheStateOfTheTypeItReplaces(t *testing.T) {
	r := &Resource[struct{}, *wrapperspb.StringValue]{spec: ResourceSpec[struct{}, *wrapperspb.StringValue]{
		TypeName: "_network_resolver_policy",
		Schema: schema.Schema{Attributes: map[string]schema.Attribute{
			"id":    schema.StringAttribute{Computed: true},
			"added": schema.StringAttribute{Computed: true},
		}},
		MovedFrom: []Move{{TypeName: "_network_dns_policy", Segments: map[string]string{"dnsPolicies": "resolverPolicies"}}},
	}}
	movers := r.MoveState(context.Background())
	if len(movers) != 1 {
		t.Fatalf("%d movers", len(movers))
	}
	move := func(typeName, provider string) *resource.MoveStateResponse {
		req := resource.MoveStateRequest{
			SourceTypeName:        typeName,
			SourceProviderAddress: provider,
			SourceRawState:        &tfprotov6.RawState{JSON: []byte(`{"id":"` + oldPolicy + `","gone":"x"}`)},
		}
		resp := &resource.MoveStateResponse{TargetState: tfsdk.State{Schema: r.spec.Schema}}
		movers[0].StateMover(context.Background(), req, resp)
		return resp
	}
	resp := move("origincloud_network_dns_policy", "registry.terraform.io/origincloudhq/origincloud")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var id types.String
	resp.Diagnostics.Append(resp.TargetState.GetAttribute(context.Background(), path.Root("id"), &id)...)
	if id.ValueString() != newPolicy {
		t.Errorf("moved to %q", id.ValueString())
	}
	var added types.String
	resp.Diagnostics.Append(resp.TargetState.GetAttribute(context.Background(), path.Root("added"), &added)...)
	if !added.IsNull() {
		t.Errorf("the attribute the old type didn't have is %q", added.ValueString())
	}
	// Another type, or another provider's, isn't this mover's to take.
	for _, other := range [][2]string{
		{"origincloud_network_vpc", "registry.terraform.io/origincloudhq/origincloud"},
		{"origincloud_network_dns_policy", "registry.terraform.io/example/other"},
	} {
		if resp := move(other[0], other[1]); !resp.TargetState.Raw.IsNull() {
			t.Errorf("%v was moved", other)
		}
	}
}

const (
	group     = "projects/p/regions/r/securityGroups/web"
	groupFull = "//network.origincloudapis.eu/" + group
	rule      = group + "/rules/ssh"
)

// A state that holds a child's parent as its bare id gets the parent's full
// name, from the child's own name; a full name stays, and so does a state
// without a name or a parent.
func TestAParentOfVersion1IsUpgradedToItsFullName(t *testing.T) {
	for _, tc := range []struct{ name, state, want string }{
		{"a bare id", `{"id":"//network.origincloudapis.eu/` + rule + `","name":"` + rule + `","security_group":"web","project":"p"}`, groupFull},
		{"a name that is full", `{"name":"//network.origincloudapis.eu/` + rule + `","security_group":"web"}`, groupFull},
		{"a full name already", `{"name":"` + rule + `","security_group":"` + groupFull + `"}`, groupFull},
		{"no parent", `{"name":"` + rule + `","security_group":null}`, ""},
		{"no name", `{"security_group":"web"}`, "web"},
	} {
		upgraded, err := UpgradeRawParent("network", "security_group", []byte(tc.state))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var got map[string]any
		if err := json.Unmarshal(upgraded, &got); err != nil {
			t.Fatal(err)
		}
		if g, _ := got["security_group"].(string); g != tc.want {
			t.Errorf("%s: the parent is %q, not %q", tc.name, g, tc.want)
		}
		again, err := UpgradeRawParent("network", "security_group", upgraded)
		if err != nil || string(again) != string(upgraded) {
			t.Errorf("%s: upgraded twice %s, %v", tc.name, again, err)
		}
	}
	// A resource without a parent attribute has nothing to upgrade.
	if got, err := UpgradeRawParent("pvs", "", []byte(`{"a":1}`)); err != nil || string(got) != `{"a":1}` {
		t.Errorf("upgraded to %s, %v", got, err)
	}
}

func TestAParentsFullNameAndPath(t *testing.T) {
	if got := ParentOf("network", rule); got.ValueString() != groupFull {
		t.Errorf("the parent of %s is %v", rule, got)
	}
	if got := ParentOf("network", "vaults/v"); !got.IsNull() {
		t.Errorf("a name of one segment pair has the parent %v", got)
	}
	// The request takes the relative name, which a wildcard may end.
	for _, tc := range []struct{ parent, want string }{
		{groupFull, group},
		{"//network.origincloudapis.eu/projects/p/regions/r/securityGroups/-", "projects/p/regions/r/securityGroups/-"},
		{"web", "web"}, // refused by the plan, so never sent
	} {
		if got := ParentPath(types.StringValue(tc.parent)); got != tc.want {
			t.Errorf("ParentPath(%q) = %q, want %q", tc.parent, got, tc.want)
		}
	}
}

func TestAParentReferenceMustBeTheParentsFullName(t *testing.T) {
	const pattern = "projects/{project}/regions/{region}/securityGroups/{security_group}"
	config := tfsdk.Config{
		Schema: schema.Schema{Attributes: map[string]schema.Attribute{
			"project":        schema.StringAttribute{Optional: true},
			"region":         schema.StringAttribute{Optional: true},
			"security_group": schema.StringAttribute{Optional: true},
		}},
		Raw: tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{
			"project": tftypes.String, "region": tftypes.String, "security_group": tftypes.String,
		}}, map[string]tftypes.Value{
			"project": tftypes.NewValue(tftypes.String, "p"), "region": tftypes.NewValue(tftypes.String, nil), "security_group": tftypes.NewValue(tftypes.String, nil),
		}),
	}
	for _, tc := range []struct {
		value types.String
		valid bool
	}{
		{types.StringValue(groupFull), true},
		{types.StringValue("//network.origincloudapis.eu/projects/p/regions/r/securityGroups/-"), true},
		{types.StringNull(), true},
		{types.StringUnknown(), true},
		{types.StringValue("web"), false},
		{types.StringValue(group), false},
		{types.StringValue("//compute.origincloudapis.eu/" + group), false},
		{types.StringValue("//network.origincloudapis.eu/projects/p/regions/r/vpcs/v"), false},
		{types.StringValue("//network.origincloudapis.eu/projects/other/regions/r/securityGroups/web"), false}, // the project is p
	} {
		resp := &validator.StringResponse{}
		ParentReference("network", pattern, "security group").ValidateString(context.Background(),
			validator.StringRequest{Path: path.Root("security_group"), Config: config, ConfigValue: tc.value}, resp)
		if resp.Diagnostics.HasError() == tc.valid {
			t.Errorf("%v: %v", tc.value, resp.Diagnostics)
		}
	}
}

// A state written while a oneof's members were plain fields held the ones
// not set as their zero values: with exactly one other value, it is the
// member set, and the others become null. Any other state is left as it
// is, such as one whose members are null already, or whose member set
// holds a zero value itself.
func TestUpgradeRawOneofsNullsTheMembersNotSet(t *testing.T) {
	oneofs := [][]string{{"cidr_block", "peer_security_group", "same_group"}}
	for _, tc := range []struct{ name, state, want string }{
		{"a range", `{"cidr_block":"10.0.0.0/8","peer_security_group":"","same_group":false,"protocol":""}`,
			`{"cidr_block":"10.0.0.0/8","peer_security_group":null,"protocol":"","same_group":null}`},
		{"the same group", `{"cidr_block":"","peer_security_group":"","same_group":true}`,
			`{"cidr_block":null,"peer_security_group":null,"same_group":true}`},
		{"already null", `{"cidr_block":"10.0.0.0/8","peer_security_group":null,"same_group":null}`,
			`{"cidr_block":"10.0.0.0/8","peer_security_group":null,"same_group":null}`},
		{"none set", `{"cidr_block":"","peer_security_group":"","same_group":false}`,
			`{"cidr_block":"","peer_security_group":"","same_group":false}`},
		{"two set", `{"cidr_block":"10.0.0.0/8","peer_security_group":"//x","same_group":false}`,
			`{"cidr_block":"10.0.0.0/8","peer_security_group":"//x","same_group":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UpgradeRawOneofs(oneofs, []byte(tc.state))
			if err != nil {
				t.Fatal(err)
			}
			var gotV, wantV any
			if err := json.Unmarshal(got, &gotV); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &wantV); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotV, wantV) {
				t.Errorf("upgraded to %s, want %s", got, tc.want)
			}
		})
	}
}
