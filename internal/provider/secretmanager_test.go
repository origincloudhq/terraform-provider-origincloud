package provider

import (
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	secretmanagerv1beta1 "go.origincloud.eu/sdk/secretmanager/v1beta1"
)

const (
	secretDB     = "origincloud_secret_manager_secret.db"
	secretDBName = secretRegion + "/secrets/billing-db"
)

// secretManagerConfig returns a configuration of the provider calling fake,
// with the secret billing-db whose body is body, or none when body is empty,
// and more after it.
func secretManagerConfig(fake *fakeSecretManager, body, more string) string {
	config := fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}
`, fake.URL)
	if body != "" {
		config += fmt.Sprintf(`
resource "origincloud_secret_manager_secret" "db" {
  project   = "p-1"
  region    = "eu-central1"
  secret_id = "billing-db"
  %s
}
`, body)
	}
	return config + more
}

func TestSecretsAreCreatedUpdatedInPlaceImportedAndDeletedWithTheirIdFreed(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeSecretManager(t)
	const key1 = "//kms.origincloudapis.eu/projects/p-1/regions/eu-central1/keys/key-1"
	const key2 = "//kms.origincloudapis.eu/projects/p-1/regions/eu-central1/keys/key-2"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created under the id chosen, which names it, with no version.
			{
				Config: secretManagerConfig(fake, `tags = { team = "billing" }`, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(secretDB, "name", secretDBName),
					checkFullName(secretDB),
					resource.TestCheckResourceAttr(secretDB, "secret_id", "billing-db"),
					resource.TestCheckResourceAttr(secretDB, "state", "ACTIVE"),
					resource.TestCheckResourceAttr(secretDB, "create_time", "2026-11-01T12:00:00Z"),
					resource.TestCheckNoResourceAttr(secretDB, "kms_key"),
					resource.TestCheckNoResourceAttr(secretDB, "delete_time"),
					resource.TestCheckNoResourceAttr(secretDB, "force_delete"),
					resource.TestCheckResourceAttr(secretDB, "tags.team", "billing"),
				),
			},
			// Tags are changed in place, naming them alone.
			{
				Config: secretManagerConfig(fake, `tags = { team = "payments" }`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(secretDB, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(secretDB, "tags.team", "payments"),
					resource.TestCheckResourceAttr(secretDB, "update_time", "2026-11-02T09:00:00Z"),
					func(*terraform.State) error {
						want := []string{"POST " + secretRegion + "/secrets", "PATCH " + secretDBName + " tags"}
						if got := fake.mutations(); !slices.Equal(got, want) {
							return fmt.Errorf("the mutations were %v, not %v", got, want)
						}
						return nil
					},
				),
			},
			// force_delete only decides how the secret is destroyed: setting
			// it changes nothing in the API.
			{
				Config: secretManagerConfig(fake, `tags = { team = "payments" }
  force_delete = true`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(secretDB, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(secretDB, "force_delete", "true"),
					func(*terraform.State) error {
						if got := fake.mutations(); len(got) != 2 {
							return fmt.Errorf("setting force_delete called the API: %v", got)
						}
						return nil
					},
				),
			},
			// Adopted by name; the API never returns force_delete.
			{
				Config: secretManagerConfig(fake, `tags = { team = "payments" }
  force_delete = true`, ""),
				ResourceName:            secretDB,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"force_delete"},
			},
			// A new KMS key replaces the secret under the same id, which its
			// state's force_delete frees.
			{
				Config: secretManagerConfig(fake, `kms_key = "`+key1+`"
  force_delete = true`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(secretDB, plancheck.ResourceActionReplace),
				}},
				Check: resource.TestCheckResourceAttr(secretDB, "kms_key", key1),
			},
			// Left out again, force_delete is cleared in place.
			{
				Config: secretManagerConfig(fake, `kms_key = "`+key1+`"`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(secretDB, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckNoResourceAttr(secretDB, "force_delete"),
			},
			// Without it the replacement would find its id taken: refused at
			// the plan.
			{
				Config:      secretManagerConfig(fake, `kms_key = "`+key2+`"`, ""),
				ExpectError: regexp.MustCompile(`choose a new\s+secret_id`),
			},
			// Destroyed without force_delete, the secret is deleted, within
			// its recovery window.
			{
				Config: secretManagerConfig(fake, "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(secretDB, plancheck.ResourceActionDestroy),
				}},
				Check: func(*terraform.State) error {
					if got := fake.state(secretDBName); got != secretmanagerv1beta1.Secret_DELETED {
						return fmt.Errorf("the secret is %s, not DELETED", got)
					}
					return nil
				},
			},
			// Its id stays taken: the API's refusal, which names
			// RestoreSecret, reaches the user.
			{
				Config:      secretManagerConfig(fake, `tags = { team = "billing" }`, ""),
				ExpectError: regexp.MustCompile(`CONFLICT: projects/p-1/regions/eu-central1/secrets/billing-db\s+is\s+deleted,\s+within\s+its\s+recovery\s+window`),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			want := []string{
				"POST " + secretRegion + "/secrets",
				"PATCH " + secretDBName + " tags",
				// The replacement: destroyed with force, created again.
				"DELETE " + secretDBName + "?force=true",
				"POST " + secretRegion + "/secrets",
				// Destroyed without force.
				"DELETE " + secretDBName,
				// The refused create.
				"POST " + secretRegion + "/secrets",
			}
			if got := fake.mutations(); !slices.Equal(got, want) {
				return fmt.Errorf("the mutations were %v, not %v", got, want)
			}
			return nil
		},
	})
}

func TestASecretDeletedWithForceDeleteGivesUpItsIdAtOnce(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeSecretManager(t)
	forced := `force_delete = true`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: secretManagerConfig(fake, forced, ""),
				Check:  resource.TestCheckResourceAttr(secretDB, "force_delete", "true"),
			},
			// Destroyed: no recovery window, so nothing is left of it.
			{
				Config: secretManagerConfig(fake, "", ""),
				Check: func(*terraform.State) error {
					if fake.has(secretDBName) {
						return fmt.Errorf("%s is left, though it was deleted with force", secretDBName)
					}
					return nil
				},
			},
			// The id is free again.
			{
				Config: secretManagerConfig(fake, forced+"\n  tags = { again = \"yes\" }", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(secretDB, "name", secretDBName),
					resource.TestCheckResourceAttr(secretDB, "tags.again", "yes"),
				),
			},
			// A secret deleted outside Terraform leaves the state at the
			// refresh, and is created again, but its id isn't free.
			{
				PreConfig:   func() { fake.softDelete(secretDBName) },
				Config:      secretManagerConfig(fake, forced, ""),
				ExpectError: regexp.MustCompile(`CONFLICT`),
			},
		},
	})
}

func TestSecretsAreListed(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeSecretManager(t)
	fake.seedSecret("api-key", map[string]string{"team": "api"})
	fake.seedSecret("old-key", nil)
	fake.softDelete(secretRegion + "/secrets/old-key")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: secretManagerConfig(fake, "", `
data "origincloud_secret_manager_secrets" "all" {
  project = "p-1"
  region  = "eu-central1"
}
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.origincloud_secret_manager_secrets.all", "secrets.#", "1"),
				resource.TestCheckResourceAttr("data.origincloud_secret_manager_secrets.all", "secrets.0.name", secretRegion+"/secrets/api-key"),
				resource.TestCheckResourceAttr("data.origincloud_secret_manager_secrets.all", "secrets.0.tags.team", "api"),
			),
		}},
	})
}
