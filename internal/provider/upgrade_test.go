package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/origincloudhq/terraform-provider-origincloud/internal/framework"
	pvsv1beta1 "go.origincloud.eu/sdk/pvs/v1beta1"
)

// recordedResource is a managed resource of a state file.
type recordedResource struct {
	Mode      string `json:"mode"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Instances []struct {
		SchemaVersion int64          `json:"schema_version"`
		Attributes    map[string]any `json:"attributes"`
	} `json:"instances"`
}

// recordedState returns the managed resources of the state file Terraform
// wrote with release v0.18.0, for a volume and a snapshot of it, whose ids
// are their relative names, with urns.
func recordedState(t *testing.T) []recordedResource {
	t.Helper()
	raw, err := os.ReadFile("testdata/v0.18.0/pvs.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Resources []recordedResource `json:"resources"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	var managed []recordedResource
	for _, r := range file.Resources {
		if r.Mode == "managed" {
			managed = append(managed, r)
		}
	}
	return managed
}

// plannedByTerraform runs the calls Terraform makes to plan a resource:
// the proposed new state is the configuration over the prior state's
// computed attributes, which the provider plans the changes from.
func plannedByTerraform(t *testing.T, provider tfprotov6.ProviderServer, typeName string, schema *tfprotov6.Schema, prior tftypes.Value, set map[string]bool, written map[string]string) *tfprotov6.PlanResourceChangeResponse {
	t.Helper()
	typ := schema.ValueType()
	attributes := map[string]tftypes.Value{}
	if err := prior.As(&attributes); err != nil {
		t.Fatal(err)
	}
	config, proposed := map[string]tftypes.Value{}, map[string]tftypes.Value{}
	for name, v := range attributes {
		null := tftypes.NewValue(v.Type(), nil)
		if set[name] {
			if w, ok := written[name]; ok {
				v = tftypes.NewValue(tftypes.String, w) // as the configuration writes it
			}
			config[name], proposed[name] = v, v
			continue
		}
		config[name], proposed[name] = null, v // computed: the prior value stays
	}
	encode := func(v map[string]tftypes.Value) *tfprotov6.DynamicValue {
		d, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, v))
		if err != nil {
			t.Fatal(err)
		}
		return &d
	}
	state, err := tfprotov6.NewDynamicValue(typ, prior)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         typeName,
		PriorState:       &state,
		ProposedNewState: encode(proposed),
		Config:           encode(config),
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A state written by v0.18.0, whose ids are relative names and which holds
// urns, plans no changes for the configuration that wrote it once the
// references in it are full names: upgraded alone, and upgraded and
// refreshed.
func TestAStateOfV0180UpgradesAndPlansNoChanges(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	ctx := context.Background()
	const (
		volume   = "projects/p-1/zones/eu-central1-az1/volumes/vol-1"
		snapshot = "projects/p-1/zones/eu-central1-az1/snapshots/snap-2"
	)
	// What the arguments of the configuration set; every other attribute is
	// computed. The configuration refers to the volume by its id, which is
	// its full name now.
	configured := map[string]map[string]bool{
		"origincloud_pvs_volume":   {"project": true, "zone": true, "size_gib": true, "tags": true},
		"origincloud_pvs_snapshot": {"project": true, "zone": true, "source_volume": true, "tags": true},
	}
	written := map[string]string{"source_volume": full("pvs", volume)}

	for _, refresh := range []bool{false, true} {
		name := "upgraded"
		if refresh {
			name = "upgraded and refreshed"
		}
		t.Run(name, func(t *testing.T) {
			fake := newFakePVS(t)
			// The API as it is now: full names in references.
			fake.volumes[volume] = &pvsv1beta1.Volume{
				Name: volume, SizeGib: 5, StorageClass: full("pvs", "storageClasses/gp1"), State: pvsv1beta1.Volume_AVAILABLE,
				ResizeState: pvsv1beta1.Volume_RESIZE_COMPLETED, Creator: full("iam", "users/u-1"), Tags: map[string]string{"env": "test"},
			}
			fake.snapshots[snapshot] = &pvsv1beta1.Snapshot{
				Name: snapshot, SourceVolume: full("pvs", volume), SourceSizeGib: 5, State: pvsv1beta1.Snapshot_COMPLETED,
				ProtectionState: pvsv1beta1.Snapshot_UNPROTECTED, Creator: full("iam", "users/u-1"), Tags: map[string]string{"kept": "yes"},
			}

			provider, err := factories()["origincloud"]()
			if err != nil {
				t.Fatal(err)
			}
			schemas, err := provider.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
			if err != nil {
				t.Fatal(err)
			}
			providerType := schemas.Provider.ValueType()
			attributes := map[string]tftypes.Value{}
			for _, a := range schemas.Provider.Block.Attributes {
				attributes[a.Name] = tftypes.NewValue(a.ValueType(), nil)
			}
			attributes["endpoint"] = tftypes.NewValue(tftypes.String, fake.URL)
			providerConfig, err := tfprotov6.NewDynamicValue(providerType, tftypes.NewValue(providerType, attributes))
			if err != nil {
				t.Fatal(err)
			}
			if resp, err := provider.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: &providerConfig}); err != nil || len(resp.Diagnostics) > 0 {
				t.Fatalf("configuring the provider: %v %v", err, resp.Diagnostics)
			}

			for _, r := range recordedState(t) {
				if v := r.Instances[0].SchemaVersion; v != 0 {
					t.Fatalf("%s.%s of the recorded state has schema version %d, not the one of v0.18.0", r.Type, r.Name, v)
				}
				raw, err := json.Marshal(r.Instances[0].Attributes)
				if err != nil {
					t.Fatal(err)
				}
				schema := schemas.ResourceSchemas[r.Type]
				if schema == nil {
					t.Fatalf("no schema of %s", r.Type)
				}
				if schema.Version != framework.StateVersion {
					t.Errorf("%s has schema version %d, not the current", r.Type, schema.Version)
				}
				upgraded, err := provider.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
					TypeName: r.Type, Version: 0, RawState: &tfprotov6.RawState{JSON: raw},
				})
				if err != nil || len(upgraded.Diagnostics) > 0 {
					t.Fatalf("upgrading %s: %v %v", r.Type, err, upgraded.Diagnostics)
				}
				state, err := upgraded.UpgradedState.Unmarshal(schema.ValueType())
				if err != nil {
					t.Fatal(err)
				}
				var attrs map[string]tftypes.Value
				if err := state.As(&attrs); err != nil {
					t.Fatal(err)
				}
				var id string
				if err := attrs["id"].As(&id); err != nil || id != full("pvs", r.Instances[0].Attributes["name"].(string)) {
					t.Fatalf("%s's upgraded id is %q (%v), not its full name", r.Type, id, err)
				}
				if _, ok := attrs["urn"]; ok {
					t.Errorf("%s still has a urn", r.Type)
				}

				if refresh {
					read, err := provider.ReadResource(ctx, &tfprotov6.ReadResourceRequest{TypeName: r.Type, CurrentState: upgraded.UpgradedState})
					if err != nil || len(read.Diagnostics) > 0 {
						t.Fatalf("reading %s: %v %v", r.Type, err, read.Diagnostics)
					}
					if state, err = read.NewState.Unmarshal(schema.ValueType()); err != nil {
						t.Fatal(err)
					}
				}

				planned := plannedByTerraform(t, provider, r.Type, schema, state, configuredFor(t, configured, r.Type), written)
				if len(planned.Diagnostics) > 0 {
					t.Fatalf("planning %s: %v", r.Type, planned.Diagnostics)
				}
				if len(planned.RequiresReplace) > 0 {
					t.Errorf("%s would be replaced for %v", r.Type, planned.RequiresReplace)
				}
				after, err := planned.PlannedState.Unmarshal(schema.ValueType())
				if err != nil {
					t.Fatal(err)
				}
				if !after.Equal(state) {
					t.Errorf("%s plans changes:\nstate: %v\nplan:  %v", r.Type, state, after)
				}
			}
		})
	}
}

func configuredFor(t *testing.T, configured map[string]map[string]bool, typeName string) map[string]bool {
	t.Helper()
	set, ok := configured[typeName]
	if !ok {
		t.Fatalf("no configured arguments for %s", typeName)
	}
	return set
}

// A state of schema version 1, written before a child's parent attribute
// took the parent's full name, holds the parent's bare id. It is upgraded
// to the full name, which the configuration writes, and plans no changes,
// without a refresh; one of version 0 gets there as well. One of version
// 2, written before a rule's peers were a oneof, holds the peers not set
// as "" and false: they become null, so that the rule isn't replaced.
//
// The state is written by hand after the form of version 1: the parent is
// a bare id, the id and the references are full names.
func TestAStateWithABareParentIsUpgradedAndPlansNoChanges(t *testing.T) {
	ctx := context.Background()
	const (
		group = "projects/p-1/regions/eu-central1/securityGroups/sg-1"
		rule  = group + "/rules/rule-1"
	)
	attributes := map[string]any{
		"id": full("network", rule), "name": rule,
		"project": "p-1", "region": "eu-central1", "security_group": "sg-1",
		"direction": "INGRESS", "protocol": "TCP", "first_port": 22, "last_port": 0,
		"cidr_block": "10.0.0.0/8", "peer_security_group": "", "same_group": false, "description": "",
	}
	provider, err := factories()["origincloud"]()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := provider.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	const typeName = "origincloud_network_security_group_rule"
	schema := schemas.ResourceSchemas[typeName]

	for _, version := range []int64{0, 1, 2} {
		t.Run(fmt.Sprintf("from version %d", version), func(t *testing.T) {
			old := maps.Clone(attributes)
			if version == 2 {
				old["security_group"] = full("network", group)
			}
			if version == 0 {
				old["id"] = rule
				old["urn"] = "urn:origincloud:network:securityGroupRule/" + rule
			}
			raw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			upgraded, err := provider.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
				TypeName: typeName, Version: version, RawState: &tfprotov6.RawState{JSON: raw},
			})
			if err != nil || len(upgraded.Diagnostics) > 0 {
				t.Fatalf("upgrading: %v %v", err, upgraded.Diagnostics)
			}
			state, err := upgraded.UpgradedState.Unmarshal(schema.ValueType())
			if err != nil {
				t.Fatal(err)
			}
			var attrs map[string]tftypes.Value
			if err := state.As(&attrs); err != nil {
				t.Fatal(err)
			}
			var parent string
			if err := attrs["security_group"].As(&parent); err != nil || parent != full("network", group) {
				t.Fatalf("the upgraded parent is %q (%v), not the group's full name", parent, err)
			}
			if !attrs["peer_security_group"].IsNull() || !attrs["same_group"].IsNull() {
				t.Errorf("the peers not set are %v and %v, not null", attrs["peer_security_group"], attrs["same_group"])
			}

			set := map[string]bool{"project": true, "region": true, "security_group": true, "direction": true, "protocol": true, "first_port": true, "cidr_block": true}
			planned := plannedByTerraform(t, provider, typeName, schema, state, set, map[string]string{"security_group": full("network", group)})
			if len(planned.Diagnostics) > 0 {
				t.Fatalf("planning: %v", planned.Diagnostics)
			}
			if len(planned.RequiresReplace) > 0 {
				t.Errorf("the rule would be replaced for %v", planned.RequiresReplace)
			}
			after, err := planned.PlannedState.Unmarshal(schema.ValueType())
			if err != nil {
				t.Fatal(err)
			}
			if !after.Equal(state) {
				t.Errorf("the rule plans changes:\nstate: %v\nplan:  %v", state, after)
			}
		})
	}
}
