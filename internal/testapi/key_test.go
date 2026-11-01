package testapi

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/origincloudhq/terraform-provider-origincloud/internal/testapi/firewallsv1"
)

const key = "origincloud_keys_key.k"

func keyConfig(fake *fakeFirewalls, body string) string {
	return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_keys_key" "k" {
  project = "p-1"
  sign    = true
  %s
}
`, fake.URL, body)
}

// keyMask checks that the last key update named want.
func keyMask(fake *fakeFirewalls, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := fake.lastKeyMask(); got != want {
			return fmt.Errorf("the last update mask was %q, not %q", got, want)
		}
		return nil
	}
}

func TestKeyLifecycle(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	step := func(body string, action plancheck.ResourceActionType, check ...resource.TestCheckFunc) resource.TestStep {
		return resource.TestStep{
			Config: keyConfig(fake, body),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(key, action),
			}},
			Check: resource.ComposeAggregateTestCheckFunc(check...),
		}
	}
	// The expiry is written in another time zone than the API answers in.
	const base = `expire_time     = "2027-01-01T01:00:00+01:00"
  seed            = "abc"`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created synchronously. The secret, which only the create
			// answers with, is kept; the values the API chose for what was
			// left out are kept; the expiry stays as written. The plan after
			// the apply is empty.
			step(base+`
  rotation_period = "90s"`, plancheck.ResourceActionCreate,
				resource.TestCheckResourceAttr(key, "secret", "secret-1"),
				resource.TestCheckResourceAttr(key, "create_time", "2026-11-01T12:00:00Z"),
				resource.TestCheckResourceAttr(key, "expire_time", "2027-01-01T01:00:00+01:00"),
				resource.TestCheckResourceAttr(key, "rotation_period", "90s"),
				resource.TestCheckResourceAttr(key, "algorithm", "ed25519"),
				resource.TestCheckResourceAttr(key, "bits", "256"),
				resource.TestCheckResourceAttr(key, "state", "ENABLED"),
				resource.TestCheckResourceAttr(key, "enabled", "true"),
			),
			// A duration changes: the update names it, and the secret stays
			// though the API doesn't answer with it.
			step(base+`
  rotation_period = "120s"`, plancheck.ResourceActionUpdate,
				keyMask(fake, "rotationPeriod"),
				resource.TestCheckResourceAttr(key, "rotation_period", "120s"),
				resource.TestCheckResourceAttr(key, "secret", "secret-1"),
			),
			// The same instant, written in UTC: the state takes the new
			// spelling, and nothing is sent.
			step(`expire_time = "2027-01-01T00:00:00Z"
  seed        = "abc"
  rotation_period = "120s"`, plancheck.ResourceActionUpdate,
				keyMask(fake, "rotationPeriod"),
				resource.TestCheckResourceAttr(key, "expire_time", "2027-01-01T00:00:00Z"),
			),
			// Another member of the oneof: both are named, the one left
			// null, and the nested member's timestamp is read.
			step(`expire_time = "2027-01-01T00:00:00Z"
  imported    = { fingerprint = "ff" }
  rotation_period = "120s"`, plancheck.ResourceActionUpdate,
				keyMask(fake, "seed,imported"),
				resource.TestCheckResourceAttr(key, "imported.fingerprint", "ff"),
				resource.TestCheckResourceAttr(key, "imported.import_time", "2026-11-01T12:00:00Z"),
				resource.TestCheckNoResourceAttr(key, "seed"),
			),
			// A field with presence set, then left out: kept as the API has
			// it.
			step(`expire_time = "2027-01-01T00:00:00Z"
  imported    = { fingerprint = "ff" }
  rotation_period = "120s"
  algorithm   = "rsa"`, plancheck.ResourceActionUpdate,
				keyMask(fake, "algorithm"),
				resource.TestCheckResourceAttr(key, "algorithm", "rsa"),
			),
			step(`expire_time = "2027-01-01T00:00:00Z"
  imported    = { fingerprint = "ff" }
  rotation_period = "120s"`, plancheck.ResourceActionNoop,
				resource.TestCheckResourceAttr(key, "algorithm", "rsa"),
			),
			// The toggle's methods answer with operations.
			step(`expire_time = "2027-01-01T00:00:00Z"
  imported    = { fingerprint = "ff" }
  rotation_period = "120s"
  enabled     = false`, plancheck.ResourceActionUpdate,
				resource.TestCheckResourceAttr(key, "state", "DISABLED"),
			),
			// Terminated outside Terraform, it is gone: the next plan
			// creates it again.
			{
				PreConfig: func() { fake.terminate("projects/p-1/keys/k-1") },
				Config: keyConfig(fake, `expire_time = "2027-01-01T00:00:00Z"
  imported    = { fingerprint = "ff" }
  rotation_period = "120s"
  enabled     = false`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			step(`expire_time = "2027-01-01T00:00:00Z"
  imported    = { fingerprint = "ff" }
  rotation_period = "120s"
  enabled     = false`, plancheck.ResourceActionCreate,
				resource.TestCheckResourceAttr(key, "name", "projects/p-1/keys/k-2"),
				resource.TestCheckResourceAttr(key, "secret", "secret-2"),
				resource.TestCheckResourceAttr(key, "state", "DISABLED"),
			),
		},
		CheckDestroy: func(*terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for name, k := range fake.keys {
				if k.GetState() != firewallsv1.Key_TERMINATED {
					return fmt.Errorf("%s is %s", name, k.GetState())
				}
			}
			return nil
		},
	})
}

func TestKeyRefusals(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: keyConfig(fake, `seed = "abc"
  imported = { fingerprint = "ff" }`),
				ExpectError: regexp.MustCompile(`Conflicting oneof members`),
			},
			{
				Config: fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_keys_key" "k" {
  project = "p-1"
}
`, fake.URL),
				ExpectError: regexp.MustCompile(`Missing oneof member`),
			},
			{
				Config:      keyConfig(fake, `expire_time = "tomorrow"`),
				ExpectError: regexp.MustCompile(`RFC 3339 timestamp`),
			},
			{
				Config:      keyConfig(fake, `rotation_period = "1m30s"`),
				ExpectError: regexp.MustCompile(`duration in seconds`),
			},
		},
	})
	if len(fake.keys) != 0 {
		t.Errorf("a refused configuration reached the API")
	}
}

// A key imported has no secret: the API never answers with it again.
func TestAnImportedKeyHasNoSecret(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: keyConfig(fake, `seed = "abc"`)},
			{
				ResourceName:            key,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if s, ok := states[0].Attributes["secret"]; ok && s != "" {
						return fmt.Errorf("the imported key has the secret %q", s)
					}
					return nil
				},
			},
		},
	})
}
