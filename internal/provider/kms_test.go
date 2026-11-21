package provider

import (
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	kmsv1beta1 "go.origincloud.eu/sdk/kms/v1beta1"
)

const (
	kmsKey      = "origincloud_kms_key.main"
	kmsKeyName1 = "projects/p-1/regions/eu-central1/keys/key-1"
)

var kmsKeyPattern = regexp.MustCompile(`^projects/p-1/regions/eu-central1/keys/key-[0-9]+$`)

// kmsConfig returns a configuration of the provider calling fake, with a
// key whose body is key, and more after it.
func kmsConfig(fake *fakeKMS, key, more string) string {
	return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_kms_key" "main" {
  project = "p-1"
  region  = "eu-central1"
  %s
}
`, fake.URL, key) + more
}

const kmsGranted = `
resource "origincloud_kms_grant" "host" {
  project = "p-1"
  region  = "eu-central1"
  key     = origincloud_kms_key.main.id
  %s
}
`

func grantBody(context, ttl string) string {
	return fmt.Sprintf(`operation          = "DECRYPT"
  grantee            = "spiffe://origincloud.internal/zones/eu-central1-az1/hosts/h-1"
  encryption_context = %s
  ttl                = %q`, context, ttl)
}

func TestKeysAreCreatedUpdatedReplacedImportedAndDeleted(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeKMS(t)
	encrypt := func(more string) string {
		return kmsConfig(fake, `purpose = "ENCRYPT_DECRYPT"`+"\n  "+more, "")
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created ENABLED with its first version, which is the
			// primary; the plan after it is empty.
			{
				Config: encrypt(`tags = { "origin:name" = "volumes" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(kmsKey, "name", kmsKeyPattern),
					checkFullName(kmsKey),
					resource.TestCheckResourceAttr(kmsKey, "purpose", "ENCRYPT_DECRYPT"),
					resource.TestCheckResourceAttr(kmsKey, "versions.#", "1"),
					resource.TestCheckResourceAttr(kmsKey, "primary_version", full("kms", kmsKeyName1+"/versions/1")),
					resource.TestCheckResourceAttr(kmsKey, "versions.0.key_version", full("kms", kmsKeyName1+"/versions/1")),
					resource.TestCheckResourceAttr(kmsKey, "create_time", "2026-11-01T12:00:00Z"),
					resource.TestCheckResourceAttr(kmsKey, "platform_managed", "false"),
					resource.TestCheckNoResourceAttr(kmsKey, "purge_time"),
					resource.TestCheckResourceAttr(kmsKey, "tags.origin:name", "volumes"),
				),
			},
			// Tags are changed in place, naming them alone.
			{
				Config: encrypt(`tags = { "origin:name" = "payments" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(kmsKey, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(kmsKey, "tags.origin:name", "payments"),
			},
			// A key is disabled by its state, and enabled again.
			{
				Config: encrypt(`tags = { "origin:name" = "payments" }
  state = "DISABLED"`),
				Check: resource.TestCheckResourceAttr(kmsKey, "state", "DISABLED"),
			},
			{
				Config: encrypt(`tags = { "origin:name" = "payments" }
  state = "ENABLED"`),
				Check: resource.TestCheckResourceAttr(kmsKey, "state", "ENABLED"),
			},
			// A rotation outside Terraform shows at the next refresh.
			{
				PreConfig: func() { fake.rotate(kmsKeyName1) },
				Config: encrypt(`tags = { "origin:name" = "payments" }
  state = "ENABLED"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(kmsKey, "versions.#", "2"),
					resource.TestCheckResourceAttr(kmsKey, "primary_version", full("kms", kmsKeyName1+"/versions/2")),
				),
			},
			{
				Config: encrypt(`tags = { "origin:name" = "payments" }
  state = "ENABLED"`),
				ResourceName:      kmsKey,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: encrypt(`tags = { "origin:name" = "payments" }
  state = "ENABLED"`),
				ResourceName:  kmsKey,
				ImportState:   true,
				ImportStateId: "key-1",
				ExpectError:   regexp.MustCompile(`"key-1" isn't a name of the form\s+projects/\{project\}/regions/\{region\}/keys/\{key\}`),
			},
			// The purpose can't change: the key is replaced.
			{
				Config: kmsConfig(fake, `purpose = "SIGN_ED25519"`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(kmsKey, plancheck.ResourceActionReplace),
				}},
				Check: resource.TestCheckResourceAttr(kmsKey, "purpose", "SIGN_ED25519"),
			},
			// A key deleted outside Terraform is gone: the next apply
			// creates it again.
			{
				PreConfig: func() {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					for _, k := range fake.keys {
						k.State = kmsv1beta1.Key_DELETED
					}
				},
				Config: kmsConfig(fake, `purpose = "SIGN_ED25519"`, ""),
				Check:  resource.TestMatchResourceAttr(kmsKey, "name", regexp.MustCompile(`/keys/key-3$`)),
			},
		},
		// Destroying deletes the key, which is then DELETED, its material
		// kept until its purge time.
		CheckDestroy: func(*terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for name, k := range fake.keys {
				if k.GetState() != kmsv1beta1.Key_DELETED {
					return fmt.Errorf("%s is %s", name, k.GetState())
				}
			}
			if len(fake.keys) != 3 {
				return fmt.Errorf("%d keys, not 3", len(fake.keys))
			}
			return nil
		},
	})
	if calls := fake.mutations(); !slices.Contains(calls, "PATCH "+kmsKeyName1+" tags") || !slices.Contains(calls, "PATCH "+kmsKeyName1+" state") {
		t.Errorf("mutations %v", calls)
	}
}

// A key's state is optional and computed: left out, it is the API's
// ENABLED, and the plan after the create is empty.
func TestAKeyWithoutAStateIsEnabledByTheAPI(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeKMS(t)
	config := kmsConfig(fake, `purpose = "ENCRYPT_DECRYPT"`, "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config:           config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check:            resource.TestCheckResourceAttr(kmsKey, "state", "ENABLED"),
			},
			// Disabling it is still a change, and leaving the state out
			// afterwards keeps what it is.
			{
				Config: kmsConfig(fake, `purpose = "ENCRYPT_DECRYPT"
  state   = "DISABLED"`, ""),
				Check: resource.TestCheckResourceAttr(kmsKey, "state", "DISABLED"),
			},
			{
				Config:           config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check:            resource.TestCheckResourceAttr(kmsKey, "state", "DISABLED"),
			},
		},
	})
}

// A grant's ttl is input only: no answer has it, so the state keeps the
// configured value, and expiry shows in expire_time. An imported grant has
// no ttl, as nothing in the API says what it was.
func TestAGrantKeepsItsTTLAndIsImportedWithoutIt(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeKMS(t)
	key := `purpose = "ENCRYPT_DECRYPT"`
	grant := "origincloud_kms_grant.host"
	grantName := regexp.MustCompile(`^projects/p-1/regions/eu-central1/keys/key-1/grants/grant-[0-9]+$`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config:           kmsConfig(fake, key, fmt.Sprintf(kmsGranted, grantBody(`{ purpose = "volumes" }`, "600s"))),
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(grant, "name", grantName),
					resource.TestCheckResourceAttr(grant, "ttl", "600s"),
					resource.TestCheckResourceAttr(grant, "expire_time", "2026-11-01T12:10:00Z"),
				),
			},
			// The ttl is kept across a refresh.
			{
				Config:           kmsConfig(fake, key, fmt.Sprintf(kmsGranted, grantBody(`{ purpose = "volumes" }`, "600s"))),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check:            resource.TestCheckResourceAttr(grant, "ttl", "600s"),
			},
			// Imported, it has everything the API answers with, and no ttl.
			{
				Config:                  kmsConfig(fake, key, fmt.Sprintf(kmsGranted, grantBody(`{ purpose = "volumes" }`, "600s"))),
				ResourceName:            grant,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"ttl"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("%d imported resources, not 1", len(states))
					}
					if ttl, ok := states[0].Attributes["ttl"]; ok && ttl != "" {
						return fmt.Errorf("the imported grant has ttl %q", ttl)
					}
					return nil
				},
			},
			// A different ttl is a different grant.
			{
				Config: kmsConfig(fake, key, fmt.Sprintf(kmsGranted, grantBody(`{ purpose = "volumes" }`, "900s"))),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(grant, plancheck.ResourceActionReplace),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(grant, "ttl", "900s"),
					resource.TestCheckResourceAttr(grant, "expire_time", "2026-11-01T12:15:00Z"),
				),
			},
		},
	})
}

func TestAGrantThatTheAPIRefusesFailsTheApply(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeKMS(t)
	signer := `purpose = "SIGN_ECDSA_P256_SHA256"`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config:      kmsConfig(fake, signer, fmt.Sprintf(kmsGranted, grantBody(`{ purpose = "volumes" }`, "600s"))),
				ExpectError: regexp.MustCompile(`(?s)isn't an ENCRYPT_DECRYPT key`),
			},
			{
				Config:      kmsConfig(fake, `purpose = "ENCRYPT_DECRYPT"`, fmt.Sprintf(kmsGranted, grantBody(`{ purpose = "volumes" }`, "7200s"))),
				ExpectError: regexp.MustCompile(`the ttl must be 1 to 60\s+minutes`),
			},
			{
				Config:      kmsConfig(fake, `purpose = "ENCRYPT_DECRYPT"`, fmt.Sprintf(kmsGranted, grantBody(`{}`, "600s"))),
				ExpectError: regexp.MustCompile(`the encryption context is empty`),
			},
		},
	})
}

func TestKMSDataSourcesReadEveryPage(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeKMS(t)
	main := fake.seedKey(kmsv1beta1.Key_ENCRYPT_DECRYPT, kmsv1beta1.Key_ENABLED)
	signer := fake.seedKey(kmsv1beta1.Key_SIGN_ED25519, kmsv1beta1.Key_DISABLED)
	fake.seedKey(kmsv1beta1.Key_SIGN_ECDSA_P256_SHA256, kmsv1beta1.Key_DELETED)
	fake.seedGrant(main, "volumes")
	fake.seedGrant(main, "snapshots")
	fake.rotate(signer)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

data "origincloud_kms_keys" "all" {
  project = "p-1"
  region  = "eu-central1"
}

data "origincloud_kms_grants" "main" {
  project = "p-1"
  region  = "eu-central1"
  key     = "//kms.origincloudapis.eu/projects/p-1/regions/eu-central1/keys/key-1"
}

data "origincloud_kms_public_key" "signer" {
  name = "%s/versions/2/publicKey"
}
`, fake.URL, signer),
				Check: resource.ComposeAggregateTestCheckFunc(
					// The fake answers one key per page: all are read, the
					// deleted one left out.
					resource.TestCheckResourceAttr("data.origincloud_kms_keys.all", "keys.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_kms_keys.all", "keys.0.name", main),
					resource.TestCheckResourceAttr("data.origincloud_kms_keys.all", "keys.0.id", full("kms", main)),
					resource.TestCheckResourceAttr("data.origincloud_kms_keys.all", "keys.0.purpose", "ENCRYPT_DECRYPT"),
					resource.TestCheckResourceAttr("data.origincloud_kms_keys.all", "keys.0.state", "ENABLED"),
					resource.TestCheckResourceAttr("data.origincloud_kms_keys.all", "keys.0.versions.#", "1"),
					resource.TestCheckResourceAttr("data.origincloud_kms_keys.all", "keys.1.name", signer),
					resource.TestCheckResourceAttr("data.origincloud_kms_keys.all", "keys.1.state", "DISABLED"),
					resource.TestCheckResourceAttr("data.origincloud_kms_keys.all", "keys.1.versions.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_kms_keys.all", "keys.1.primary_version", full("kms", signer+"/versions/2")),
					resource.TestCheckResourceAttr("data.origincloud_kms_grants.main", "grants.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_kms_grants.main", "grants.0.operation", "DECRYPT"),
					resource.TestCheckResourceAttr("data.origincloud_kms_grants.main", "grants.0.zone", "eu-central1-az1"),
					resource.TestCheckResourceAttr("data.origincloud_kms_grants.main", "grants.0.encryption_context.purpose", "volumes"),
					resource.TestCheckResourceAttr("data.origincloud_kms_grants.main", "grants.1.encryption_context.purpose", "snapshots"),
					resource.TestCheckResourceAttr("data.origincloud_kms_grants.main", "grants.0.expire_time", "2026-11-01T12:10:00Z"),
					resource.TestCheckResourceAttr("data.origincloud_kms_public_key.signer", "algorithm", "SIGN_ED25519"),
					resource.TestMatchResourceAttr("data.origincloud_kms_public_key.signer", "pem", regexp.MustCompile(`^-----BEGIN PUBLIC KEY-----\n`)),
					resource.TestCheckResourceAttr("data.origincloud_kms_public_key.signer", "id", full("kms", signer+"/versions/2/publicKey")),
				),
			},
			// A public key of a version the key doesn't have is an error.
			{
				Config: fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

data "origincloud_kms_public_key" "missing" {
  name = "%s/versions/9/publicKey"
}
`, fake.URL, signer),
				ExpectError: regexp.MustCompile(`NOT_FOUND:\s+\S+\s+doesn't\s+exist`),
			},
		},
	})
}
