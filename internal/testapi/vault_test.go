package testapi

import (
	"fmt"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const vault = "origincloud_vaults_vault.v"

func vaultConfig(fake *fakeFirewalls, body string) string {
	return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

%s
`, fake.URL, body)
}

func vaultStep(fake *fakeFirewalls, body, address string, action plancheck.ResourceActionType, check ...resource.TestCheckFunc) resource.TestStep {
	return resource.TestStep{
		Config: vaultConfig(fake, body),
		ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction(address, action),
		}},
		Check: resource.ComposeAggregateTestCheckFunc(check...),
	}
}

// A vault's id is chosen by the client: sent in the create, read back from
// the name, derived from it on import, and replacing the vault when it
// changes. Vaults are listed by owner, a required filter.
func TestAVaultsIdIsTheClients(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	vaults := func(id string) string {
		return fmt.Sprintf(`
resource "origincloud_vaults_vault" "v" {
  vault_id     = %q
  owner        = "o1"
  display_name = "A"
}

resource "origincloud_vaults_vault" "other" {
  vault_id     = "other"
  owner        = "o2"
  display_name = "B"
}

data "origincloud_vaults_vaults" "o1" {
  owner      = "o1"
  depends_on = [origincloud_vaults_vault.v, origincloud_vaults_vault.other]
}
`, id)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			vaultStep(fake, vaults("v1"), vault, plancheck.ResourceActionCreate,
				resource.TestCheckResourceAttr(vault, "name", "vaults/v1"),
				resource.TestCheckResourceAttr(vault, "vault_id", "v1"),
				resource.TestCheckResourceAttr(vault, "create_time", "2026-11-01T12:00:00Z"),
			),
			{
				Config: vaultConfig(fake, vaults("v1")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.origincloud_vaults_vaults.o1", "vaults.#", "1"),
					resource.TestCheckResourceAttr("data.origincloud_vaults_vaults.o1", "vaults.0.name", "vaults/v1"),
				),
			},
			// Imported by name, the id comes from it.
			{ResourceName: vault, ImportState: true, ImportStateVerify: true, ImportStateId: "vaults/v1"},
			vaultStep(fake, vaults("v2"), vault, plancheck.ResourceActionReplace,
				resource.TestCheckResourceAttr(vault, "name", "vaults/v2"),
			),
		},
	})
}

// A vault's quota is a singleton: the resource updates it when created,
// sending only the budgets configured, keeps the API's for the others,
// is imported by name, and leaves the quota as it is when destroyed.
func TestAVaultsQuotaIsASingleton(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	const quota = "origincloud_vaults_vault_quota.q"
	config := func(budgets string) string {
		return `
resource "origincloud_vaults_vault" "v" {
  vault_id     = "v1"
  owner        = "o1"
  display_name = "A"
}

resource "origincloud_vaults_vault_quota" "q" {
  vault = origincloud_vaults_vault.v.vault_id
  ` + budgets + `
}
`
	}
	lastMask := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if _, masks := fake.quota("vaults/v1/quota"); len(masks) == 0 || masks[len(masks)-1] != want {
				return fmt.Errorf("the quota's update masks were %v, the last not %q", masks, want)
			}
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			vaultStep(fake, config(`max_secrets = 50`), quota, plancheck.ResourceActionCreate,
				lastMask("maxSecrets"),
				resource.TestCheckResourceAttr(quota, "name", "vaults/v1/quota"),
				resource.TestCheckResourceAttr(quota, "max_secrets", "50"),
				resource.TestCheckResourceAttr(quota, "max_versions", "10"),
			),
			vaultStep(fake, config(`max_secrets = 50
  max_versions = 20`), quota, plancheck.ResourceActionUpdate,
				lastMask("maxVersions"),
				resource.TestCheckResourceAttr(quota, "max_versions", "20"),
			),
			// Removed from the configuration, a budget keeps its value.
			vaultStep(fake, config(`max_versions = 20`), quota, plancheck.ResourceActionNoop,
				resource.TestCheckResourceAttr(quota, "max_secrets", "50"),
			),
			{ResourceName: quota, ImportState: true, ImportStateVerify: true, ImportStateId: "vaults/v1/quota"},
			// Destroyed alone, the quota stays as it is.
			{
				Config: vaultConfig(fake, `
resource "origincloud_vaults_vault" "v" {
  vault_id     = "v1"
  owner        = "o1"
  display_name = "A"
}
`),
				Check: func(*terraform.State) error {
					q, masks := fake.quota("vaults/v1/quota")
					if q.GetMaxSecrets() != 50 || q.GetMaxVersions() != 20 || !slices.Equal(masks, []string{"maxSecrets", "maxVersions"}) {
						return fmt.Errorf("the quota is %v after %v", q, masks)
					}
					return nil
				},
			},
		},
	})
}
