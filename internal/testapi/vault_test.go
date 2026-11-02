package testapi

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
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
// sending only the budgets configured, and owns those. A budget removed
// from the configuration is unset; destroying the resource unsets the
// budgets it owns and leaves the others; with nothing configured, nothing
// is sent.
func TestAVaultsQuotaIsASingleton(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	const quota = "origincloud_vaults_vault_quota.q"
	theVault := `
resource "origincloud_vaults_vault" "v" {
  vault_id     = "v1"
  owner        = "o1"
  display_name = "A"
}
`
	config := func(budgets string) string {
		return theVault + `
resource "origincloud_vaults_vault_quota" "q" {
  vault = origincloud_vaults_vault.v.vault_id
  ` + budgets + `
}
`
	}
	masks := func(want ...string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if _, got := fake.quota("vaults/v1/quota"); !slices.Equal(got, want) {
				return fmt.Errorf("the quota's update masks were %v, not %v", got, want)
			}
			return nil
		}
	}
	budgets := func(secrets, versions *int64) resource.TestCheckFunc {
		return func(*terraform.State) error {
			q, _ := fake.quota("vaults/v1/quota")
			if fmt.Sprint(q.MaxSecrets) != fmt.Sprint(secrets) && (q.MaxSecrets == nil || secrets == nil || *q.MaxSecrets != *secrets) ||
				fmt.Sprint(q.MaxVersions) != fmt.Sprint(versions) && (q.MaxVersions == nil || versions == nil || *q.MaxVersions != *versions) {
				return fmt.Errorf("the quota is %v", q)
			}
			return nil
		}
	}
	n := func(v int64) *int64 { return &v }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Nothing configured: read, not updated, as the API refuses
			// an empty mask.
			vaultStep(fake, config(``), quota, plancheck.ResourceActionCreate,
				masks(),
				resource.TestCheckResourceAttr(quota, "name", "vaults/v1/quota"),
				resource.TestCheckNoResourceAttr(quota, "max_secrets"),
				resource.TestCheckResourceAttr(quota, "max_versions", "10"),
			),
			vaultStep(fake, config(`max_secrets = 50`), quota, plancheck.ResourceActionUpdate,
				masks("maxSecrets"),
				resource.TestCheckResourceAttr(quota, "max_secrets", "50"),
				resource.TestCheckResourceAttr(quota, "max_versions", "10"),
			),
			vaultStep(fake, config(`max_secrets = 50
  max_versions = 20`), quota, plancheck.ResourceActionUpdate,
				masks("maxSecrets", "maxVersions"),
				budgets(n(50), n(20)),
			),
			// A budget removed from the configuration is unset: no budget.
			vaultStep(fake, config(`max_versions = 20`), quota, plancheck.ResourceActionUpdate,
				masks("maxSecrets", "maxVersions", "maxSecrets"),
				resource.TestCheckNoResourceAttr(quota, "max_secrets"),
				budgets(nil, n(20)),
			),
			// One that falls back to a default when unset takes it.
			vaultStep(fake, config(``), quota, plancheck.ResourceActionUpdate,
				masks("maxSecrets", "maxVersions", "maxSecrets", "maxVersions"),
				resource.TestCheckResourceAttr(quota, "max_versions", "10"),
				budgets(nil, n(10)),
			),
			vaultStep(fake, config(``), quota, plancheck.ResourceActionNoop),
			vaultStep(fake, config(`max_secrets = 30`), quota, plancheck.ResourceActionUpdate,
				masks("maxSecrets", "maxVersions", "maxSecrets", "maxVersions", "maxSecrets"),
			),
			{ResourceName: quota, ImportState: true, ImportStateVerify: true, ImportStateId: "vaults/v1/quota"},
			{
				ResourceName:  quota,
				ImportState:   true,
				ImportStateId: "vaults/v1",
				ExpectError:   regexp.MustCompile(`"vaults/v1" isn't a name of the form vaults/\{vault\}/quota`),
			},
			// Destroyed alone, it unsets the budget it owns.
			{
				Config: vaultConfig(fake, theVault),
				Check: resource.ComposeAggregateTestCheckFunc(
					masks("maxSecrets", "maxVersions", "maxSecrets", "maxVersions", "maxSecrets", "maxSecrets"),
					budgets(nil, n(10)),
				),
			},
		},
	})
}

// A failed update leaves what Terraform owns as it was: the budget removed
// from the configuration is unset by the next apply that succeeds.
func TestAFailedQuotaUpdateKeepsWhatTerraformOwns(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	config := func(budgets string) string {
		return vaultConfig(fake, `
resource "origincloud_vaults_vault" "v" {
  vault_id     = "v1"
  owner        = "o1"
  display_name = "A"
}

resource "origincloud_vaults_vault_quota" "q" {
  vault = origincloud_vaults_vault.v.vault_id
  `+budgets+`
}
`)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: config(`max_secrets = 50`)},
			{
				PreConfig:   func() { fake.setFailQuota(true) },
				Config:      config(``),
				ExpectError: regexp.MustCompile(`the quota can't be updated now`),
			},
			{
				PreConfig: func() { fake.setFailQuota(false) },
				Config:    config(``),
				Check: func(*terraform.State) error {
					if q, _ := fake.quota("vaults/v1/quota"); q.MaxSecrets != nil {
						return fmt.Errorf("the budget is still %d", q.GetMaxSecrets())
					}
					return nil
				},
			},
		},
	})
}

// Terraform owns a field once it has sent it: imported, with a budget set
// to the value the API has, nothing is sent, the budget isn't owned, and
// destroying the resource leaves it.
func TestAFieldIsOwnedOnceSent(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	theVault := `
resource "origincloud_vaults_vault" "v" {
  vault_id     = "v1"
  owner        = "o1"
  display_name = "A"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: vaultConfig(fake, theVault)},
			{
				PreConfig:        func() { fake.setMaxSecrets("vaults/v1/quota", 50) },
				Config:           vaultConfig(fake, theVault+"\nimport {\n  to = origincloud_vaults_vault_quota.q\n  id = \"vaults/v1/quota\"\n}\n\nresource \"origincloud_vaults_vault_quota\" \"q\" {\n  vault       = \"v1\"\n  max_secrets = 50\n}\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("origincloud_vaults_vault_quota.q", plancheck.ResourceActionNoop)}},
			},
			{
				Config: vaultConfig(fake, theVault),
				Check: func(*terraform.State) error {
					if q, masks := fake.quota("vaults/v1/quota"); q.GetMaxSecrets() != 50 || len(masks) != 0 {
						return fmt.Errorf("the quota is %v after %v", q, masks)
					}
					return nil
				},
			},
		},
	})
}

// Replacing a vault with the id it has fails at the plan: a deleted
// vault's id can't be used again, so the create would fail with the vault
// already deleted.
func TestAVaultIsntReplacedWithItsOwnId(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	config := func(id, owner string) string {
		return fmt.Sprintf(`
resource "origincloud_vaults_vault" "v" {
  vault_id     = %q
  owner        = %q
  display_name = "A"
}
`, id, owner)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: vaultConfig(fake, config("v1", "o1"))},
			{
				Config:      vaultConfig(fake, config("v1", "o2")),
				ExpectError: regexp.MustCompile(`(?s)Can't replace with the same id.*Changing owner replaces the vault, and a\s+deleted\s+vault's\s+id\s+can't\s+be\s+used\s+again`),
			},
			// With a new id, it is replaced; the old id is a tombstone.
			vaultStep(fake, config("v2", "o2"), vault, plancheck.ResourceActionReplace,
				resource.TestCheckResourceAttr(vault, "name", "vaults/v2"),
			),
			{
				Config:      vaultConfig(fake, config("v2", "o2")+strings.ReplaceAll(config("v1", "o1"), `"v"`, `"again"`)),
				ExpectError: regexp.MustCompile(`vaults/v1 exists`),
			},
		},
	})
}
