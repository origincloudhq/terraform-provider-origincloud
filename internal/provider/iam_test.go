package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
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

func TestServiceAccountsAndRoleBindingsAreCreatedImportedReplacedAndDestroyed(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	created := iamConfig(fake, "CI", "//iam.origincloudapis.eu/users/u-ana")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created; the plan after it is empty, which the test framework
			// checks after every step. The user's address stays as
			// configured.
			{
				Config: created,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(ciServiceAccount, "name", regexp.MustCompile(`^organizations/acme/serviceAccounts/sa-[0-9]+$`)),
					checkFullName(ciServiceAccount),
					resource.TestMatchResourceAttr(ciServiceAccount, "member", regexp.MustCompile(`^//iam\.origincloudapis\.eu/organizations/acme/serviceAccounts/sa-[0-9]+$`)),
					resource.TestCheckResourceAttr(ciServiceAccount, "state", "ACTIVE"),
					resource.TestCheckResourceAttr(ciServiceAccount, "creator", "//iam.origincloudapis.eu/users/u-me"),
					resource.TestCheckNoResourceAttr(ciServiceAccount, "delete_time"),
					resource.TestCheckNoResourceAttr(ciServiceAccount, "client_id"),
					resource.TestCheckNoResourceAttr(ciServiceAccount, "client_secret"),
					resource.TestMatchResourceAttr(ciBinding, "name", regexp.MustCompile(`^organizations/acme/roleBindings/rb-[0-9]+$`)),
					resource.TestCheckResourceAttrPair(ciBinding, "member", ciServiceAccount, "member"),
					resource.TestCheckResourceAttr(ciBinding, "project", "//resourcemanager.origincloudapis.eu/projects/acme-prod"),
					resource.TestCheckResourceAttr(anaBinding, "member", "//iam.origincloudapis.eu/users/u-ana"),
					resource.TestCheckResourceAttr(anaBinding, "project", ""),
					resource.TestCheckResourceAttr(anaBinding, "creator", "//iam.origincloudapis.eu/users/u-me"),
					resource.TestCheckResourceAttr(anaBinding, "create_time", "2026-11-01T12:00:00Z"),
				),
			},
			// Imported, a service account is as created, and a binding has
			// its member as the API answers it.
			{
				Config:            created,
				ResourceName:      ciServiceAccount,
				ImportState:       true,
				ImportStateVerify: true,
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
			// the binding of its member.
			{
				Config: iamConfig(fake, "CI pipeline", "//iam.origincloudapis.eu/users/u-ana"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(ciServiceAccount, plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction(ciBinding, plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionNoop),
				}},
				Check: resource.TestCheckResourceAttr(ciServiceAccount, "display_name", "CI pipeline"),
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
// same request id, which creates nothing and answers with the account the
// first one created: the apply succeeds with that one account.
func TestAServiceAccountCreateWhoseAnswerIsLostIsSentAgain(t *testing.T) {
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
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(ciServiceAccount, tfjsonpath.New("name"), knownvalue.StringExact("organizations/acme/serviceAccounts/sa-1")),
				},
			},
		},
	})

	calls, ids := fake.mutations(), fake.mutationRequestIDs()
	want := []string{
		"POST organizations/acme/serviceAccounts", "POST organizations/acme/serviceAccounts",
		"DELETE organizations/acme/serviceAccounts/sa-1",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("mutations %v, want %v", calls, want)
	}
	if !requestID.MatchString(ids[0]) || ids[1] != ids[0] {
		t.Errorf("request ids %q, want the same new one twice", ids)
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
  service_account = origincloud_iam_service_account.deploy.id
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
					resource.TestCheckNoResourceAttr(deployAccount, "client_id"),
					resource.TestMatchResourceAttr(githubCredential, "name", regexp.MustCompile(`^organizations/acme/serviceAccounts/sa-[0-9]+/federatedCredentials/fc-[0-9]+$`)),
					checkFullName(githubCredential),
					resource.TestCheckResourceAttr(githubCredential, "organization", "acme"),
					resource.TestMatchResourceAttr(githubCredential, "service_account", regexp.MustCompile(`^//iam\.origincloudapis\.eu/organizations/acme/serviceAccounts/sa-[0-9]+$`)),
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
  service_account = origincloud_iam_service_account.ci.id
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
	ciKey  = "data.origincloud_iam_service_account_key.ci"
	ciKeys = "data.origincloud_iam_service_account_keys.ci"
)

// The provider creates no service account key: its create answers with the
// private key, which would land in the state. A key made with the CLI or
// the console is read, by its public metadata only.
func TestServiceAccountKeysAreReadButNeverCreated(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	account := iamProvider(fake) + `
resource "origincloud_iam_service_account" "ci" {
  organization = "acme"
  display_name = "CI"
}
`
	var first, second string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: account + `
resource "origincloud_iam_service_account_key" "ci" {
  organization    = "acme"
  service_account = origincloud_iam_service_account.ci.id
}
`,
				ExpectError: regexp.MustCompile(`(?s)Invalid resource type.*origincloud_iam_service_account_key`),
			},
			{Config: account},
			// The fake answers one key per page: both are read, and neither
			// has a credentials file.
			{
				PreConfig: func() {
					first = fake.addKey("organizations/acme/serviceAccounts/sa-1")
					second = fake.addKey("organizations/acme/serviceAccounts/sa-1")
				},
				Config: account + `
data "origincloud_iam_service_account_key" "ci" {
  name = "//iam.origincloudapis.eu/organizations/acme/serviceAccounts/sa-1/keys/key-2"
}

data "origincloud_iam_service_account_keys" "ci" {
  organization    = "acme"
  service_account = origincloud_iam_service_account.ci.id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					func(*terraform.State) error {
						if first != "organizations/acme/serviceAccounts/sa-1/keys/key-2" {
							return fmt.Errorf("the first key is %s", first)
						}
						return nil
					},
					resource.TestCheckResourceAttr(ciKey, "name", "organizations/acme/serviceAccounts/sa-1/keys/key-2"),
					resource.TestCheckResourceAttr(ciKey, "algorithm", "ES256"),
					resource.TestCheckResourceAttr(ciKey, "state", "ACTIVE"),
					resource.TestCheckNoResourceAttr(ciKey, "credentials_file"),
					resource.TestCheckResourceAttr(ciKeys, "service_account_keys.#", "2"),
					resource.TestCheckResourceAttrPair(ciKeys, "service_account_keys.0.id", ciKey, "id"),
					func(s *terraform.State) error {
						return resource.TestCheckResourceAttr(ciKeys, "service_account_keys.1.name", second)(s)
					},
					resource.TestCheckNoResourceAttr(ciKeys, "service_account_keys.0.credentials_file"),
				),
			},
		},
	})
	for _, call := range fake.mutations() {
		if strings.Contains(call, "/keys") {
			t.Errorf("the provider called %s", call)
		}
	}
}

// roleBindingResourcesConfig returns a configuration of the provider
// calling fake with a binding of role to a user in a project, limited to
// resources, a list of HCL expressions, or on the whole project when
// resources is nil.
func roleBindingResourcesConfig(fake *fakeIAM, role string, resources ...string) string {
	limit := ""
	if resources != nil {
		limit = "\n  resources    = [" + strings.Join(resources, ", ") + "]"
	}
	return iamProvider(fake) + fmt.Sprintf(`
resource "origincloud_iam_role_binding" "ana" {
  organization = "acme"
  member       = "//iam.origincloudapis.eu/users/u-ana"
  role         = "//iam.origincloudapis.eu/roles/%s"
  project      = "//resourcemanager.origincloudapis.eu/projects/acme-prod"%s
}
`, role, limit)
}

// A binding's resources are read back and imported, and changing them
// updates the binding in place through UpdateRoleBinding, also to none,
// which widens it to the whole project; changing its role still replaces
// it.
func TestARoleBindingsResourcesChangeInPlace(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeIAM(t)
	const (
		billingDB     = `"//pvs.origincloudapis.eu/projects/acme-prod/zones/eu-central1-az1/volumes/billing-db"`
		billingStripe = `"//pvs.origincloudapis.eu/projects/acme-prod/zones/eu-central1-az1/volumes/billing-stripe"`
		webDB         = `"//pvs.origincloudapis.eu/projects/acme-prod/zones/eu-central1-az1/volumes/web-db"`
	)
	listed := func(resources ...string) resource.TestCheckFunc {
		checks := []resource.TestCheckFunc{resource.TestCheckResourceAttr(anaBinding, "resources.#", strconv.Itoa(len(resources)))}
		for _, r := range resources {
			checks = append(checks, resource.TestCheckTypeSetElemAttr(anaBinding, "resources.*", strings.Trim(r, `"`)))
		}
		return resource.ComposeAggregateTestCheckFunc(checks...)
	}
	var name string
	sameBinding := func(s *terraform.State) error {
		if got := s.RootModule().Resources[anaBinding].Primary.Attributes["name"]; got != name {
			return fmt.Errorf("the binding is %s, not %s", got, name)
		}
		return nil
	}
	created := roleBindingResourcesConfig(fake, "viewer", billingDB, billingStripe)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: created,
				Check: resource.ComposeAggregateTestCheckFunc(
					listed(billingDB, billingStripe),
					func(s *terraform.State) error {
						name = s.RootModule().Resources[anaBinding].Primary.Attributes["name"]
						return nil
					},
				),
			},
			// Imported, the binding has the list it was created with.
			{
				Config:            created,
				ResourceName:      anaBinding,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// An entry swapped for another updates the binding in place.
			{
				Config: roleBindingResourcesConfig(fake, "viewer", billingDB, webDB),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(listed(billingDB, webDB), sameBinding),
			},
			// No list widens it to the whole project, in place too.
			{
				Config: roleBindingResourcesConfig(fake, "viewer"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckNoResourceAttr(anaBinding, "resources.#"), sameBinding),
			},
			{
				Config: roleBindingResourcesConfig(fake, "viewer", billingStripe),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(listed(billingStripe), sameBinding),
			},
			// Another role still replaces the binding.
			{
				Config: roleBindingResourcesConfig(fake, "editor", billingStripe),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(anaBinding, plancheck.ResourceActionReplace),
				}},
				Check: listed(billingStripe),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			var updates []string
			for _, call := range fake.calls {
				if strings.HasPrefix(call, "PATCH ") {
					updates = append(updates, call)
				}
			}
			if want := []string{"PATCH " + name, "PATCH " + name, "PATCH " + name}; !slices.Equal(updates, want) {
				return fmt.Errorf("the updates sent are %v, not %v", updates, want)
			}
			return nil
		},
	})
}
