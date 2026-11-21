package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// The API renamed DnsPolicy to ResolverPolicy: a `moved` block from the old
// resource types carries a state across, renaming the attribute and the
// names the API now answers to.
func TestADNSPolicyStateMovesToTheResolverPolicyTypes(t *testing.T) {
	ctx := context.Background()
	provider, err := factories()["origincloud"]()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := provider.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	const (
		oldPolicy      = "//network.origincloudapis.eu/projects/p-1/regions/eu-central1/dnsPolicies/office"
		newPolicy      = "//network.origincloudapis.eu/projects/p-1/regions/eu-central1/resolverPolicies/office"
		oldAssociation = "//network.origincloudapis.eu/projects/p-1/regions/eu-central1/vpcs/v-1/dnsPolicyAssociations/office"
		newAssociation = "//network.origincloudapis.eu/projects/p-1/regions/eu-central1/vpcs/v-1/resolverPolicyAssociations/office"
	)
	for _, tc := range []struct {
		from, to, state string
		want            map[string]string
	}{
		{
			from: "origincloud_network_dns_policy",
			to:   "origincloud_network_resolver_policy",
			state: `{"id":"` + oldPolicy + `","name":"projects/p-1/regions/eu-central1/dnsPolicies/office",` +
				`"project":"p-1","region":"eu-central1","relay_enabled":true,"upstream_servers":["192.0.2.53"],"project_default":false}`,
			want: map[string]string{
				"id":      newPolicy,
				"name":    "projects/p-1/regions/eu-central1/resolverPolicies/office",
				"project": "p-1",
				"region":  "eu-central1",
			},
		},
		{
			from: "origincloud_network_dns_policy_association",
			to:   "origincloud_network_resolver_policy_association",
			state: `{"id":"` + oldAssociation + `","name":"projects/p-1/regions/eu-central1/vpcs/v-1/dnsPolicyAssociations/office",` +
				`"project":"p-1","region":"eu-central1","vpc":"v-1","dns_policy":"` + oldPolicy + `"}`,
			want: map[string]string{
				"id":              newAssociation,
				"name":            "projects/p-1/regions/eu-central1/vpcs/v-1/resolverPolicyAssociations/office",
				"vpc":             "//network.origincloudapis.eu/projects/p-1/regions/eu-central1/vpcs/v-1",
				"resolver_policy": newPolicy,
			},
		},
	} {
		t.Run(tc.to, func(t *testing.T) {
			moved, err := provider.MoveResourceState(ctx, &tfprotov6.MoveResourceStateRequest{
				SourceProviderAddress: "registry.terraform.io/origincloudhq/origincloud",
				SourceTypeName:        tc.from,
				SourceSchemaVersion:   1,
				SourceState:           &tfprotov6.RawState{JSON: []byte(tc.state)},
				TargetTypeName:        tc.to,
			})
			if err != nil || len(moved.Diagnostics) > 0 {
				t.Fatalf("moving %s: %v %v", tc.from, err, moved.Diagnostics)
			}
			schema := schemas.ResourceSchemas[tc.to]
			if schema == nil {
				t.Fatalf("no schema of %s", tc.to)
			}
			state, err := moved.TargetState.Unmarshal(schema.ValueType())
			if err != nil {
				t.Fatal(err)
			}
			var attrs map[string]tftypes.Value
			if err := state.As(&attrs); err != nil {
				t.Fatal(err)
			}
			for name, want := range tc.want {
				var got string
				if err := attrs[name].As(&got); err != nil || got != want {
					t.Errorf("%s is %q (%v), want %q", name, got, err, want)
				}
			}
			if _, ok := attrs["dns_policy"]; ok {
				t.Error("the old attribute is still there")
			}
			// What the old type had no value for is null, for the refresh
			// that follows to set.
			var creator *string
			if err := attrs["creator"].As(&creator); err != nil || creator != nil {
				t.Errorf("creator is %v (%v)", creator, err)
			}
		})
	}

	// A state of another type isn't moved to these.
	moved, err := provider.MoveResourceState(ctx, &tfprotov6.MoveResourceStateRequest{
		SourceProviderAddress: "registry.terraform.io/origincloudhq/origincloud",
		SourceTypeName:        "origincloud_network_vpc",
		SourceState:           &tfprotov6.RawState{JSON: []byte(`{}`)},
		TargetTypeName:        "origincloud_network_resolver_policy",
	})
	if err == nil && len(moved.Diagnostics) == 0 && moved.TargetState != nil {
		t.Error("a VPC's state was moved to a resolver policy")
	}
}
