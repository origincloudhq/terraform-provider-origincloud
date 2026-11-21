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
	deployAccount    = "origincloud_iam_service_account.deploy"
	githubCredential = "origincloud_iam_federated_credential.github"
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
  role         = "//iam.origincloudapis.eu/roles/viewer"
  project      = "//resourcemanager.origincloudapis.eu/projects/acme-prod"
}

resource "origincloud_iam_role_binding" "ana" {
  organization = "acme"
  member       = %q
  role         = "//iam.origincloudapis.eu/roles/editor"
}
`, displayName, member)
}

// checkClientSecret checks that the service account's client secret in the
// state is the one the fake generated for it.
func checkClientSecret(fake *fakeIAM) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		name := s.RootModule().Resources[ciServiceAccount].Primary.Attributes["name"]
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
	created := iamConfig(fake, "CI", "//iam.origincloudapis.eu/users/u-ana")

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
					checkFullName(ciServiceAccount),
					resource.TestMatchResourceAttr(ciServiceAccount, "member", regexp.MustCompile(`^//iam\.origincloudapis\.eu/organizations/acme/serviceAccounts/sa-[0-9]+$`)),
					resource.TestMatchResourceAttr(ciServiceAccount, "client_id", regexp.MustCompile(`^client-sa-[0-9]+$`)),
					resource.TestCheckResourceAttr(ciServiceAccount, "state", "ACTIVE"),
					resource.TestCheckResourceAttr(ciServiceAccount, "creator", "//iam.origincloudapis.eu/users/u-me"),
					resource.TestCheckNoResourceAttr(ciServiceAccount, "delete_time"),
					checkClientSecret(fake),
					resource.TestMatchResourceAttr(ciBinding, "name", regexp.MustCompile(`^organizations/acme/roleBindings/rb-[0-9]+$`)),
					resource.TestCheckResourceAttrPair(ciBinding, "member", ciServiceAccount, "member"),
					resource.TestCheckResourceAttr(ciBinding, "project", "//resourcemanager.origincloudapis.eu/projects/acme-prod"),
					resource.TestCheckResourceAttr(anaBinding, "member", "//iam.origincloudapis.eu/users/u-ana"),
					resource.TestCheckResourceAttr(anaBinding, "project", ""),
					resource.TestCheckResourceAttr(anaBinding, "creator", "//iam.origincloudapis.eu/users/u-me"),
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
					if member := states[0].Attributes["member"]; member != "//iam.origincloudapis.eu/users/u-ana" {
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
				Config: iamConfig(fake, "CI pipeline", "//iam.origincloudapis.eu/users/u-ana"),
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

// A service account create whose answer is lost is sent again with the
// same request id; the secret went out with the lost answer only, so the
// API refuses the repeat with CONFLICT, naming the account in the
// metadata. The apply fails saying so, the state holds the account,
// tainted, and the next apply replaces it with one that has a secret.
func TestAServiceAccountCreateWhoseAnswerIsLostIsReplacedByTheNextApply(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	fake.loseCreateAnswer = true
	config := iamProvider(fake) + `
resource "origincloud_iam_service_account" "ci" {
  organization = "acme"
  display_name = "CI"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)CONFLICT:\s+the\s+service\s+account\s+organizations/acme/serviceAccounts/sa-1\s+was\s+created\s+by\s+an\s+earlier\s+request.*tainted:\s+the\s+next\s+apply\s+replaces\s+it`),
			},
			{
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPreRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(ciServiceAccount, plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(ciServiceAccount, tfjsonpath.New("name"), knownvalue.StringExact("organizations/acme/serviceAccounts/sa-2")),
				},
				Check: checkClientSecret(fake),
			},
		},
	})

	calls, ids := fake.mutations(), fake.mutationRequestIDs()
	want := []string{
		"POST organizations/acme/serviceAccounts", "POST organizations/acme/serviceAccounts",
		"DELETE organizations/acme/serviceAccounts/sa-1", "POST organizations/acme/serviceAccounts",
		"DELETE organizations/acme/serviceAccounts/sa-2",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("mutations %v, want %v", calls, want)
	}
	if !requestID.MatchString(ids[0]) || ids[1] != ids[0] || ids[3] == ids[0] {
		t.Errorf("request ids %q, want the same new one twice, then another", ids)
	}
}

// federatedConfig returns a configuration of the provider calling fake,
// with a WORKLOAD service account and a federated credential on it that
// trusts GitHub Actions' tokens of one repository's main branch; credential
// is the credential's attributes after its service account.
func federatedConfig(fake *fakeIAM, credential string) string {
	return iamProvider(fake) + fmt.Sprintf(`
resource "origincloud_iam_service_account" "deploy" {
  organization = "acme"
  display_name = "Deploy"
  type         = "WORKLOAD"
}

resource "origincloud_iam_federated_credential" "github" {
  organization    = "acme"
  service_account = basename(origincloud_iam_service_account.deploy.name)
  %s
}
`, credential)
}

const githubActions = `display_name = "GitHub Actions"
  issuer       = "https://token.actions.githubusercontent.com"
  conditions   = { repository_id = "123456", ref = "refs/heads/main" }`

func TestFederatedCredentialsAreCreatedImportedReplacedAndDestroyed(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	created := federatedConfig(fake, githubActions)
	// replaces checks that config plans to replace the credential only.
	replaces := func(config string) resource.TestStep {
		return resource.TestStep{
			Config:             config,
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
			ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPreRefresh: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(githubCredential, plancheck.ResourceActionReplace),
				plancheck.ExpectResourceAction(deployAccount, plancheck.ResourceActionNoop),
			}},
		}
	}
	var first string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created; the audience left out is the API's default, which
			// the plan after it keeps. A WORKLOAD service account has no
			// client id or secret.
			{
				Config: created,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(deployAccount, "type", "WORKLOAD"),
					resource.TestCheckResourceAttr(deployAccount, "client_id", ""),
					resource.TestMatchResourceAttr(githubCredential, "name", regexp.MustCompile(`^organizations/acme/serviceAccounts/sa-[0-9]+/federatedCredentials/fc-[0-9]+$`)),
					checkFullName(githubCredential),
					resource.TestCheckResourceAttr(githubCredential, "organization", "acme"),
					resource.TestMatchResourceAttr(githubCredential, "service_account", regexp.MustCompile(`^sa-[0-9]+$`)),
					resource.TestCheckResourceAttr(githubCredential, "audience", "https://sts.origincloudapis.eu"),
					resource.TestCheckResourceAttr(githubCredential, "conditions.%", "2"),
					resource.TestCheckResourceAttr(githubCredential, "conditions.repository_id", "123456"),
					resource.TestCheckResourceAttr(githubCredential, "state", "ACTIVE"),
					resource.TestCheckResourceAttr(githubCredential, "creator", "//iam.origincloudapis.eu/users/u-me"),
					resource.TestCheckResourceAttr(githubCredential, "create_time", "2026-11-01T12:00:00Z"),
					resource.TestCheckNoResourceAttr(githubCredential, "delete_time"),
					func(s *terraform.State) error {
						first = s.RootModule().Resources[githubCredential].Primary.Attributes["name"]
						return nil
					},
				),
			},
			// The default audience configured is no change.
			{
				Config: federatedConfig(fake, githubActions+`
  audience     = "https://sts.origincloudapis.eu"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
			{
				Config:            created,
				ResourceName:      githubCredential,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config:        created,
				ResourceName:  githubCredential,
				ImportState:   true,
				ImportStateId: "fc-1",
				ExpectError:   regexp.MustCompile(`"fc-1" isn't a name of the form\s+organizations/\{organization\}/serviceAccounts/\{service_account\}/federatedCredentials/\{federated_credential\}`),
			},
			// A credential is never updated: any change replaces it.
			replaces(federatedConfig(fake, strings.Replace(githubActions, `"GitHub Actions"`, `"GitHub"`, 1))),
			replaces(federatedConfig(fake, githubActions+`
  audience     = "https://example.com"`)),
			replaces(federatedConfig(fake, `display_name = "GitHub Actions"
  issuer       = "https://token.actions.example.com"
  conditions   = { sub = "repo:acme/app:ref:refs/heads/main" }`)),
			{
				Config: federatedConfig(fake, strings.Replace(githubActions, `"refs/heads/main"`, `"refs/heads/release"`, 1)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(githubCredential, plancheck.ResourceActionDestroyBeforeCreate),
					plancheck.ExpectResourceAction(deployAccount, plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(githubCredential, "conditions.ref", "refs/heads/release"),
					func(s *terraform.State) error {
						if name := s.RootModule().Resources[githubCredential].Primary.Attributes["name"]; name == first {
							return fmt.Errorf("%s wasn't replaced", name)
						}
						fake.mu.Lock()
						defer fake.mu.Unlock()
						if state := fake.federated[first].GetState(); state != iamv1beta1.FederatedCredential_DELETED {
							return fmt.Errorf("the replaced %s is %s", first, state)
						}
						return nil
					},
				),
			},
			// Deleted outside Terraform, it is DELETED: it leaves the
			// state at the refresh, and the apply creates it again.
			{
				PreConfig: func() {
					fake.mu.Lock()
					var name string
					for n, c := range fake.federated {
						if c.GetState() == iamv1beta1.FederatedCredential_ACTIVE {
							name = n
						}
					}
					fake.mu.Unlock()
					fake.deleteFederatedCredential(name)
				},
				Config: created,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(githubCredential, plancheck.ResourceActionCreate),
				}},
				Check: resource.TestCheckResourceAttr(githubCredential, "state", "ACTIVE"),
			},
			// The list leaves the deleted ones out.
			{
				Config: created + `
data "origincloud_iam_federated_credentials" "deploy" {
  organization    = "acme"
  service_account = origincloud_iam_federated_credential.github.service_account
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.origincloud_iam_federated_credentials.deploy", "federated_credentials.#", "1"),
					resource.TestCheckResourceAttrPair("data.origincloud_iam_federated_credentials.deploy", "federated_credentials.0.name", githubCredential, "name"),
				),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for name, c := range fake.federated {
				if c.GetState() != iamv1beta1.FederatedCredential_DELETED {
					return fmt.Errorf("%s is %s", name, c.GetState())
				}
			}
			var credentialCalls []string
			for _, call := range fake.calls {
				if strings.Contains(call, "/federatedCredentials") {
					credentialCalls = append(credentialCalls, strings.Fields(call)[0])
				}
			}
			// Created, replaced, created again after the delete outside
			// Terraform, and destroyed; never updated.
			if want := []string{"POST", "DELETE", "POST", "POST", "DELETE"}; !slices.Equal(credentialCalls, want) {
				return fmt.Errorf("the federated credential calls were %v, not %v", credentialCalls, want)
			}
			return nil
		},
	})
}

// The API's refusals of a federated credential fail the apply with its
// message.
func TestAFederatedCredentialTheAPIRefusesFailsTheApply(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// No condition on an identity that never changes.
			{
				Config: federatedConfig(fake, `display_name = "GitHub Actions"
  issuer       = "https://token.actions.githubusercontent.com"
  conditions   = { ref = "refs/heads/main" }`),
				ExpectError: regexp.MustCompile(`(?s)INVALID_ARGUMENT:\s+the\s+conditions\s+must\s+name\s+one\s+of\s+repository_id,\s+repository_owner_id`),
			},
			// An EXTERNAL service account has its secret instead.
			{
				Config: iamProvider(fake) + `
resource "origincloud_iam_service_account" "ci" {
  organization = "acme"
  display_name = "CI"
}

resource "origincloud_iam_federated_credential" "gitlab" {
  organization    = "acme"
  service_account = basename(origincloud_iam_service_account.ci.name)
  display_name    = "GitLab CI"
  issuer          = "https://gitlab.com"
  conditions      = { project_id = "42" }
}
`,
				ExpectError: regexp.MustCompile(`(?s)FAILED_PRECONDITION:.*isn't\s+an\s+active\s+WORKLOAD\s+service\s+account`),
			},
		},
	})
}

func TestIAMDataSourcesReadEveryPage(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	resources := func(retired bool) string {
		config := iamConfig(fake, "CI", "//iam.origincloudapis.eu/users/u-ana")
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
					resource.TestCheckResourceAttr("data.origincloud_iam_user.me", "member", "//iam.origincloudapis.eu/users/u-me"),
					resource.TestCheckResourceAttr("data.origincloud_iam_user.me", "email_verified", "true"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.acme", "role_bindings.#", "3"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.acme", "role_bindings.0.member", "//iam.origincloudapis.eu/users/u-me"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.acme", "role_bindings.0.creator", "platform"),
					resource.TestCheckTypeSetElemNestedAttrs("data.origincloud_iam_role_bindings.acme", "role_bindings.*", map[string]string{
						"member": "//iam.origincloudapis.eu/users/u-ana", "role": "roles/editor", "project": "",
					}),
					resource.TestCheckTypeSetElemAttrPair("data.origincloud_iam_role_bindings.acme", "role_bindings.*.name", ciBinding, "name"),
					// The caller's own, shown by their id.
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.mine", "role_bindings.#", "1"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.mine", "role_bindings.0.member", "//iam.origincloudapis.eu/users/u-me"),
					resource.TestCheckResourceAttr("data.origincloud_iam_role_bindings.mine", "role_bindings.0.role", "//iam.origincloudapis.eu/roles/owner"),
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
  role         = "//iam.origincloudapis.eu/roles/viewer"
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

const (
	ciKey  = "origincloud_iam_service_account_key.ci"
	ciKeys = "data.origincloud_iam_service_account_keys.ci"
)

// keyConfig returns a configuration of the provider calling fake, with an
// EXTERNAL service account and keys on it: one, or none when none is set,
// and more after them.
func keyConfig(fake *fakeIAM, display string, keys bool, more string) string {
	config := iamProvider(fake) + fmt.Sprintf(`
resource "origincloud_iam_service_account" "ci" {
  organization = "acme"
  display_name = %q
}
`, display)
	if keys {
		config += `
resource "origincloud_iam_service_account_key" "ci" {
  organization    = "acme"
  service_account = basename(origincloud_iam_service_account.ci.name)
}
`
	}
	return config + more
}

// checkCredentialsFile checks that the key's credentials file in the state
// is the one the fake generated for it.
func checkCredentialsFile(fake *fakeIAM) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		name := s.RootModule().Resources[ciKey].Primary.Attributes["name"]
		fake.mu.Lock()
		want := fake.credentials[name]
		fake.mu.Unlock()
		if want == "" {
			return fmt.Errorf("the fake generated no credentials file for %s", name)
		}
		return resource.TestCheckResourceAttr(ciKey, "credentials_file", want)(s)
	}
}

func TestServiceAccountKeysAreCreatedImportedReplacedAndDeleted(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	created := keyConfig(fake, "CI", true, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created; the credentials file, which the API answers with
			// only now, goes into the state as sensitive.
			{
				Config: created,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(ciKey, "name", regexp.MustCompile(`^organizations/acme/serviceAccounts/sa-[0-9]+/keys/key-[0-9]+$`)),
					checkFullName(ciKey),
					resource.TestCheckResourceAttr(ciKey, "algorithm", "ES256"),
					resource.TestCheckResourceAttr(ciKey, "state", "ACTIVE"),
					resource.TestCheckResourceAttr(ciKey, "creator", "//iam.origincloudapis.eu/users/u-me"),
					resource.TestCheckResourceAttr(ciKey, "create_time", "2026-11-01T12:00:00Z"),
					resource.TestCheckNoResourceAttr(ciKey, "delete_time"),
					resource.TestCheckNoResourceAttr(ciKey, "last_use_time"),
					checkCredentialsFile(fake),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectSensitiveValue(ciKey, tfjsonpath.New("credentials_file")),
				},
			},
			// Refreshed, the file stays though no read answers with it.
			{
				RefreshState: true,
				Check:        checkCredentialsFile(fake),
			},
			{
				Config: created,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
				Check: checkCredentialsFile(fake),
			},
			{
				Config: created + `
output "file" {
  value = origincloud_iam_service_account_key.ci.credentials_file
}
`,
				ExpectError: regexp.MustCompile(`Output refers to sensitive values`),
			},
			// Imported, a key has no credentials file.
			{
				Config:                  created,
				ResourceName:            ciKey,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"credentials_file"},
			},
			{
				Config:       created,
				ResourceName: ciKey,
				ImportState:  true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if file := states[0].Attributes["credentials_file"]; file != "" {
						return fmt.Errorf("an imported key has the credentials file %q", file)
					}
					return nil
				},
			},
			// A key deleted outside Terraform is DELETED, which is gone:
			// the next apply creates another.
			{
				PreConfig: func() {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					for name := range fake.keys {
						fake.keys[name].State = iamv1beta1.ServiceAccountKey_DELETED
					}
				},
				Config: created,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(ciKey, plancheck.ResourceActionCreate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(ciKey, "name", regexp.MustCompile(`/keys/key-[0-9]+$`)),
					resource.TestCheckResourceAttr(ciKey, "state", "ACTIVE"),
					checkCredentialsFile(fake),
				),
			},
			// A new service account replaces the key: another account, and a
			// key on it.
			{
				Config: keyConfig(fake, "CI pipeline", true, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(ciServiceAccount, plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction(ciKey, plancheck.ResourceActionReplace),
				}},
				Check: checkCredentialsFile(fake),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for name, k := range fake.keys {
				if k.GetState() != iamv1beta1.ServiceAccountKey_DELETED {
					return fmt.Errorf("%s is %s", name, k.GetState())
				}
			}
			if len(fake.keys) != 3 {
				return fmt.Errorf("%d keys, not 3", len(fake.keys))
			}
			return nil
		},
	})
}

// A key create whose answer is lost is sent again with the same request
// id; the private key went out with the lost answer only, so the API
// refuses the repeat with CONFLICT, naming the key. The key goes into the
// state tainted, and the next apply replaces it with one that has a file.
func TestAServiceAccountKeyCreateWhoseAnswerIsLostIsReplacedByTheNextApply(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	fake.loseKeyAnswer = true
	config := keyConfig(fake, "CI", true, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)CONFLICT:\s+the\s+key\s+organizations/acme/serviceAccounts/sa-1/keys/key-2\s+was\s+created\s+by\s+an\s+earlier\s+request.*tainted:\s+the\s+next\s+apply\s+replaces\s+it`),
			},
			{
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPreRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(ciKey, plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(ciKey, tfjsonpath.New("name"), knownvalue.StringExact("organizations/acme/serviceAccounts/sa-1/keys/key-3")),
				},
				Check: checkCredentialsFile(fake),
			},
		},
	})
}

func TestAKeyOfAWorkloadServiceAccountIsRefusedAndTheKeysAreListed(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	lists := `
resource "origincloud_iam_service_account_key" "second" {
  organization    = "acme"
  service_account = basename(origincloud_iam_service_account.ci.name)
  depends_on      = [origincloud_iam_service_account_key.ci]
}

data "origincloud_iam_service_account_keys" "ci" {
  organization    = "acme"
  service_account = basename(origincloud_iam_service_account.ci.name)
  depends_on      = [origincloud_iam_service_account_key.second]
}
`
	unlisted := `
data "origincloud_iam_service_account_keys" "ci" {
  organization    = "acme"
  service_account = basename(origincloud_iam_service_account.ci.name)
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: iamProvider(fake) + `
resource "origincloud_iam_service_account" "deploy" {
  organization = "acme"
  display_name = "Deploy"
  type         = "WORKLOAD"
}

resource "origincloud_iam_service_account_key" "deploy" {
  organization    = "acme"
  service_account = basename(origincloud_iam_service_account.deploy.name)
}
`,
				ExpectError: regexp.MustCompile(`a WORKLOAD one has no\s+key`),
			},
			// The fake answers one key per page: both are read, without a
			// credentials file.
			{
				Config: keyConfig(fake, "CI", true, lists),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(ciKeys, "service_account_keys.#", "2"),
					resource.TestCheckResourceAttrPair(ciKeys, "service_account_keys.0.name", ciKey, "name"),
					resource.TestCheckResourceAttrPair(ciKeys, "service_account_keys.0.id", ciKey, "id"),
					resource.TestCheckResourceAttrPair(ciKeys, "service_account_keys.1.name", "origincloud_iam_service_account_key.second", "name"),
					resource.TestCheckResourceAttr(ciKeys, "service_account_keys.0.algorithm", "ES256"),
					resource.TestCheckResourceAttr(ciKeys, "service_account_keys.0.state", "ACTIVE"),
					resource.TestCheckNoResourceAttr(ciKeys, "service_account_keys.0.credentials_file"),
				),
			},
			// A deleted key is left out of the list, once it is read again.
			{
				Config: keyConfig(fake, "CI", true, unlisted),
			},
			{
				Config: keyConfig(fake, "CI", true, unlisted),
				Check:  resource.TestCheckResourceAttr(ciKeys, "service_account_keys.#", "1"),
			},
		},
	})
}
