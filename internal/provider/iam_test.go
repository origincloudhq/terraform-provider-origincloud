package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	iamv1beta1 "go.origincloud.eu/sdk/iam/v1beta1"
)

const (
	ciServiceAccount = "origincloud_iam_service_account.ci"
	ciBinding        = "origincloud_iam_role_binding.ci"
	anaBinding       = "origincloud_iam_role_binding.ana"
)

// requestID is the form of the request ids the SDK sends: a version 4
// UUID.
var requestID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// iamProvider configures the provider calling fake.
func iamProvider(fake *fakeIAM) string {
	return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}
`, fake.URL)
}

// iamConfig returns a configuration of the provider calling fake, with a
// service account named displayName that holds the viewer role in a
// project, and a binding of the editor role to member on the whole
// organization.
func iamConfig(fake *fakeIAM, displayName, member string) string {
	return iamProvider(fake) + fmt.Sprintf(`
resource "origincloud_iam_service_account" "ci" {
  organization = "acme"
  display_name = %q
}

resource "origincloud_iam_role_binding" "ci" {
  organization = "acme"
  member       = origincloud_iam_service_account.ci.member
  role         = "roles/viewer"
  project      = "projects/acme-prod"
}

resource "origincloud_iam_role_binding" "ana" {
  organization = "acme"
  member       = %q
  role         = "roles/editor"
}
`, displayName, member)
}

// checkClientSecret checks that the service account's client secret in the
// state is the one the fake generated for it.
func checkClientSecret(fake *fakeIAM) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		name := s.RootModule().Resources[ciServiceAccount].Primary.ID
		fake.mu.Lock()
		want := fake.secrets[name]
		fake.mu.Unlock()
		if want == "" {
			return fmt.Errorf("the fake generated no secret for %s", name)
		}
		return resource.TestCheckResourceAttr(ciServiceAccount, "client_secret", want)(s)
	}
}

func TestServiceAccountsAndRoleBindingsAreCreatedImportedReplacedAndDestroyed(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	created := iamConfig(fake, "CI", "user:Ana@Example.com")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created; the plan after it is empty, which the test framework
			// checks after every step. The client secret, which the API
			// answers with only now, goes into the state as sensitive, and
			// the user's address stays as configured.
			{
				Config: created,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(ciServiceAccount, "name", regexp.MustCompile(`^organizations/acme/serviceAccounts/sa-[0-9]+$`)),
					resource.TestCheckResourceAttrPair(ciServiceAccount, "id", ciServiceAccount, "name"),
					resource.TestMatchResourceAttr(ciServiceAccount, "member", regexp.MustCompile(`^serviceAccount:sa-[0-9]+$`)),
					resource.TestMatchResourceAttr(ciServiceAccount, "client_id", regexp.MustCompile(`^client-sa-[0-9]+$`)),
					resource.TestCheckResourceAttr(ciServiceAccount, "state", "ACTIVE"),
					resource.TestCheckResourceAttr(ciServiceAccount, "creator", "user:me@example.com"),
					resource.TestCheckNoResourceAttr(ciServiceAccount, "delete_time"),
					checkClientSecret(fake),
					resource.TestMatchResourceAttr(ciBinding, "name", regexp.MustCompile(`^organizations/acme/roleBindings/rb-[0-9]+$`)),
					resource.TestCheckResourceAttrPair(ciBinding, "member", ciServiceAccount, "member"),
					resource.TestCheckResourceAttr(ciBinding, "project", "projects/acme-prod"),
					resource.TestCheckResourceAttr(anaBinding, "member", "user:Ana@Example.com"),
					resource.TestCheckResourceAttr(anaBinding, "project", ""),
					resource.TestCheckResourceAttr(anaBinding, "creator", "user:me@example.com"),
					resource.TestCheckResourceAttr(anaBinding, "create_time", "2026-11-01T12:00:00Z"),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectSensitiveValue(ciServiceAccount, tfjsonpath.New("client_secret")),
				},
			},
			// Refreshed, the secret stays, though no read answers with it,
			// and nothing is planned.
			{
				RefreshState: true,
				Check:        checkClientSecret(fake),
			},
			{
				Config: created,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
				Check: checkClientSecret(fake),
			},
			// A plan that would output it unmarked is refused.
			{
				Config: created + `
output "secret" {
  value = origincloud_iam_service_account.ci.client_secret
}
`,
				ExpectError: regexp.MustCompile(`Output refers to sensitive values`),
			},
			// Imported, a service account has no secret, and a binding has
			// its member as the API answers it.
			{
				Config:                  created,
				ResourceName:            ciServiceAccount,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"client_secret"},
			},
			{
				Config:       created,
				ResourceName: ciServiceAccount,
				ImportState:  true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if secret := states[0].Attributes["client_secret"]; secret != "" {
						return fmt.Errorf("an imported service account has the client secret %q", secret)
					}
					return nil
				},
			},
			{
				Config:            created,
				ResourceName:      ciBinding,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config:       created,
				ResourceName: anaBinding,
				ImportState:  true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if member := states[0].Attributes["member"]; member != "user:ana@example.com" {
						return fmt.Errorf("the imported binding's member is %q", member)
					}
					return nil
				},
			},
			{
				Config:        created,
				ResourceName:  anaBinding,
				ImportState:   true,
				ImportStateId: "rb-1",
				ExpectError:   regexp.MustCompile(`"rb-1" isn't a name of the form\s+organizations/\{organization\}/roleBindings/\{role_binding\}`),
			},
			// A new display name replaces the service account, and with it
			// the binding of its member; a new secret comes with it.
			{
				Config: iamConfig(fake, "CI pipeline", "user:Ana@Example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(ciServiceAccount, plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction(ciBinding, plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionNoop),
					plancheck.ExpectUnknownValue(ciServiceAccount, tfjsonpath.New("client_secret")),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(ciServiceAccount, "display_name", "CI pipeline"),
					checkClientSecret(fake),
				),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for name, a := range fake.serviceAccounts {
				if a.GetState() != iamv1beta1.ServiceAccount_DELETED {
					return fmt.Errorf("%s is %s", name, a.GetState())
				}
			}
			for name := range fake.bindings {
				if name != "organizations/acme/roleBindings/rb-0" {
					return fmt.Errorf("%s is left", name)
				}
			}
			// Every create and every delete of a binding sent a request id
			// of its own, as the SDK gives one to a call that has none;
			// DeleteServiceAccount takes none.
			seen := map[string]bool{}
			for i, call := range fake.calls {
				id := fake.requestIDs[i]
				if strings.HasPrefix(call, "DELETE ") && strings.Contains(call, "/serviceAccounts/") {
					if id != "" {
						return fmt.Errorf("%s sent the request id %q", call, id)
					}
					continue
				}
				if !requestID.MatchString(id) || seen[id] {
					return fmt.Errorf("%s sent the request id %q, which isn't a new UUID", call, id)
				}
				seen[id] = true
			}
			if len(seen) != 8 {
				return fmt.Errorf("%d mutations sent a request id, not 8: %v", len(seen), fake.calls)
			}
			return nil
		},
	})
}

// A binding holds on to its user's account: the API answers with the
// account's current address, in its own case, which neither replaces it
// nor plans a change. Only another member, or a user whose account is
// gone, replaces it.
func TestARoleBindingKeepsItsMemberAsConfiguredWhileItNamesTheSameAccount(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	config := func(member string) string {
		return iamProvider(fake) + fmt.Sprintf(`
resource "origincloud_iam_role_binding" "ana" {
  organization = "acme"
  member       = %q
  role         = "roles/editor"
}
`, member)
	}
	bindingMutations := func(want ...string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			var got []string
			for _, call := range fake.mutations() {
				got = append(got, strings.Fields(call)[0])
			}
			if !slices.Equal(got, want) {
				return fmt.Errorf("the mutations were %v, not %v", fake.mutations(), want)
			}
			return nil
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: config("user:Ana@Example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(anaBinding, "member", "user:Ana@Example.com"),
					bindingMutations("POST"),
				),
			},
			// Written in another case, it is the same member: the state
			// takes the new spelling without calling the API.
			{
				Config: config("user:ana@EXAMPLE.COM"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(anaBinding, "member", "user:ana@EXAMPLE.COM"),
					bindingMutations("POST"),
				),
			},
			// The user's address changes: the API answers with the new
			// one, and the state keeps the member as configured.
			{
				PreConfig: func() { fake.changeEmail("u-ana", "ana@new.example") },
				Config:    config("user:ana@EXAMPLE.COM"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
				Check: resource.TestCheckResourceAttr(anaBinding, "member", "user:ana@EXAMPLE.COM"),
			},
			// Configured with the new address, as the API answers it in any
			// case, it is still the same binding.
			{
				Config: config("user:Ana@New.Example"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(anaBinding, "member", "user:Ana@New.Example"),
					bindingMutations("POST"),
				),
			},
			// Without a verified address, it is answered as userId:, and
			// the state keeps it as configured.
			{
				PreConfig: func() { fake.unverify("u-ana") },
				Config:    config("user:Ana@New.Example"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
				Check: resource.TestCheckResourceAttr(anaBinding, "member", "user:Ana@New.Example"),
			},
			// The old address isn't the member any more.
			{
				Config:             config("user:ana@example.com"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPreRefresh: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionReplace),
				}},
			},
			// Another user replaces it.
			{
				Config: config("user:bo@example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionReplace),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(anaBinding, "member", "user:bo@example.com"),
					bindingMutations("POST", "DELETE", "POST"),
				),
			},
			// Once the user's account is gone, the binding is no one's: the
			// state takes the API's answer, and the binding is replaced,
			// which the API refuses while no account has the address.
			{
				PreConfig: func() { fake.deleteUser("u-bo") },
				Config:    config("user:bo@example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionReplace),
					plancheck.ExpectKnownValue(anaBinding, tfjsonpath.New("member"), knownvalue.StringExact("user:bo@example.com")),
				}},
				ExpectError: regexp.MustCompile(`no account has the verified address bo@example.com`),
			},
		},
	})
}

// Imported, a binding has its member as the API answers it; a
// configuration that writes it in another case updates the state in place.
func TestAnImportedRoleBindingTakesTheConfiguredSpelling(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	name := fake.bind("acme", "u-ana", "roles/editor")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: iamProvider(fake) + fmt.Sprintf(`
import {
  to = origincloud_iam_role_binding.ana
  id = %q
}

resource "origincloud_iam_role_binding" "ana" {
  organization = "acme"
  member       = "user:Ana@Example.com"
  role         = "roles/editor"
}
`, name),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(anaBinding, "name", name),
					resource.TestCheckResourceAttr(anaBinding, "member", "user:Ana@Example.com"),
					func(*terraform.State) error {
						if calls := fake.mutations(); len(calls) > 0 {
							return fmt.Errorf("the import called %v", calls)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestIAMDataSourcesReadEveryPage(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	resources := func(retired bool) string {
		config := iamConfig(fake, "CI", "user:ana@example.com")
		if retired {
			config += `
resource "origincloud_iam_service_account" "retired" {
  organization = "acme"
  display_name = "Retired"
  depends_on   = [origincloud_iam_service_account.ci]
}
`
		}
		return config
	}
	lists := resources(false) + `
data "origincloud_iam_roles" "all" {}

data "origincloud_iam_role" "viewer" {
  name = "roles/viewer"
}

data "origincloud_iam_permissions" "all" {}

data "origincloud_iam_permission" "create" {
  name = "permissions/pvs.volume.create"
}

data "origincloud_iam_user" "me" {
  name = "users/me"
}

data "origincloud_iam_role_bindings" "acme" {
  organization = "acme"
}

data "origincloud_iam_role_bindings" "mine" {
  organization = "-"
}

data "origincloud_iam_service_accounts" "acme" {
  organization = "acme"
}

data "origincloud_iam_audit_log_entries" "acme" {
  organization = "acme"
}

data "origincloud_iam_audit_log_entry" "first" {
  name = "organizations/acme/auditLogEntries/e-1"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: resources(true)},
			// The retired service account is deleted, and left out of
			// the list.
			{Config: resources(false)},
			// The fake answers one binding, service account and entry per
			// page: all are read.
			{
				Config: lists,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.origincloud_iam_roles.all", "roles.#", "3"),
					resource.TestCheckResourceAttr("data.origincloud_iam_roles.all", "roles.0.name", "roles/owner"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role.viewer", "display_name", "Viewer"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role.viewer", "included_permissions.#", "1"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role.viewer", "included_permissions.0", "permissions/pvs.volume.list"),
					resource.TestCheckResourceAttr("data.origincloud_iam_permissions.all", "permissions.#", "5"),
					resource.TestCheckResourceAttr("data.origincloud_iam_permission.create", "service", "pvs"),
					resource.TestCheckResourceAttr("data.origincloud_iam_permission.create", "resource_type", "volume"),
					resource.TestCheckResourceAttr("data.origincloud_iam_permission.create", "action", "create"),
					// users/me is answered with the user's own name.
					resource.TestCheckResourceAttr("data.origincloud_iam_user.me", "name", "users/u-me"),
					resource.TestCheckResourceAttr("data.origincloud_iam_user.me", "member", "user:me@example.com"),
					resource.TestCheckResourceAttr("data.origincloud_iam_user.me", "email_verified", "true"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.acme", "role_bindings.#", "3"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.acme", "role_bindings.0.member", "user:me@example.com"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.acme", "role_bindings.0.creator", "platform"),
					resource.TestCheckTypeSetElemNestedAttrs("data.origincloud_iam_role_bindings.acme", "role_bindings.*", map[string]string{
						"member": "user:ana@example.com", "role": "roles/editor", "project": "",
					}),
					resource.TestCheckTypeSetElemAttrPair("data.origincloud_iam_role_bindings.acme", "role_bindings.*.name", ciBinding, "name"),
					// The caller's own, shown by their id.
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.mine", "role_bindings.#", "1"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.mine", "role_bindings.0.member", "userId:u-me"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.mine", "role_bindings.0.role", "roles/owner"),
					resource.TestCheckResourceAttr("data.origincloud_iam_service_accounts.acme", "service_accounts.#", "1"),
					resource.TestCheckResourceAttrPair("data.origincloud_iam_service_accounts.acme", "service_accounts.0.name", ciServiceAccount, "name"),
					resource.TestCheckNoResourceAttr("data.origincloud_iam_service_accounts.acme", "service_accounts.0.client_secret"),
					// Most recent first: the retired service account's
					// deletion, after the four creates.
					resource.TestCheckResourceAttr("data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.#", "5"),
					resource.TestCheckResourceAttr("data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.0.action", "iam.service_account.revoke"),
					resource.TestCheckResourceAttr("data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.0.details.%", "0"),
					resource.TestCheckResourceAttrPair("data.origincloud_iam_audit_log_entry.first", "action", "data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.4.action"),
					resource.TestCheckResourceAttrPair("data.origincloud_iam_audit_log_entry.first", "target", "data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.4.target"),
					resource.TestCheckResourceAttr("data.origincloud_iam_audit_log_entry.first", "outcome", "SUCCEEDED"),
				),
			},
		},
	})
}

func TestARegionalProviderCallsIAMAtItsGlobalHost(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	t.Setenv("ORIGINCLOUD_REGION", "")
	// Only this host answers: an IAM call to the region's front door
	// would fail.
	h := &hosts{servers: map[string]*httptest.Server{"iam.origincloudapis.eu": newFakeIAM(t).Server}}
	regional := map[string]func() (tfprotov6.ProviderServer, error){
		"origincloud": providerserver.NewProtocol6WithError(&originCloud{version: "test", httpClient: &http.Client{Transport: h}}),
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: regional,
		Steps: []resource.TestStep{
			{
				Config: `
provider "origincloud" {
  region = "eu-central1"
}

resource "origincloud_iam_service_account" "ci" {
  organization = "acme"
  display_name = "CI"
}

resource "origincloud_iam_role_binding" "ci" {
  organization = "acme"
  member       = origincloud_iam_service_account.ci.member
  role         = "roles/viewer"
}

data "origincloud_iam_roles" "all" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.origincloud_iam_roles.all", "roles.#", "3"),
					resource.TestCheckResourceAttrPair(ciBinding, "member", ciServiceAccount, "member"),
				),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			for _, want := range []string{
				"iam.origincloudapis.eu /v1beta1/roles",
				"iam.origincloudapis.eu /v1beta1/organizations/acme/serviceAccounts",
				"iam.origincloudapis.eu /v1beta1/organizations/acme/roleBindings",
			} {
				if !slices.Contains(h.calls(), want) {
					return fmt.Errorf("no call %q in %v", want, h.calls())
				}
			}
			for _, call := range h.calls() {
				if !strings.HasPrefix(call, "iam.origincloudapis.eu ") {
					return fmt.Errorf("a call went to another host: %q", call)
				}
			}
			return nil
		},
	})
}
