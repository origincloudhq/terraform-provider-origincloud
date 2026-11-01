package testapi

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// keyStep is a step configuring the key with body, whose plan for it is
// action.
func keyStep(fake *fakeFirewalls, body string, action plancheck.ResourceActionType, check ...resource.TestCheckFunc) resource.TestStep {
	return resource.TestStep{
		Config: keyConfig(fake, body),
		ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction(key, action),
		}},
		Check: resource.ComposeAggregateTestCheckFunc(check...),
	}
}

func keyTest(t *testing.T, steps func(*fakeFirewalls) []resource.TestStep) {
	t.Helper()
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories(), Steps: steps(fake)})
}

// An immutable timestamp written as the same instant in another time zone,
// as after an import, whose state has the API's UTC, doesn't replace it.
func TestAnImmutableTimestampRespelledIsntReplaced(t *testing.T) {
	keyTest(t, func(fake *fakeFirewalls) []resource.TestStep {
		return []resource.TestStep{
			keyStep(fake, `not_before = "2027-01-01T00:00:00Z"`, plancheck.ResourceActionCreate),
			keyStep(fake, `not_before = "2027-01-01T01:00:00+01:00"`, plancheck.ResourceActionUpdate,
				resource.TestCheckResourceAttr(key, "not_before", "2027-01-01T01:00:00+01:00")),
			keyStep(fake, `not_before = "2027-01-02T00:00:00Z"`, plancheck.ResourceActionReplace),
		}
	})
}

// A set's elements with a field the API chooses: matched by what they
// send, whatever the order, also while another attribute changes; in an
// immutable set too.
func TestPresenceInSetElements(t *testing.T) {
	keyTest(t, func(fake *fakeFirewalls) []resource.TestStep {
		return []resource.TestStep{
			keyStep(fake, `pools = [{ name = "aa" }]
  frozen = [{ name = "aa" }]
  rotation_period = "10s"`, plancheck.ResourceActionCreate,
				resource.TestCheckTypeSetElemNestedAttrs(key, "pools.*", map[string]string{"name": "aa", "weight": "20"})),
			keyStep(fake, `pools = [{ name = "aa" }]
  frozen = [{ name = "aa" }]
  rotation_period = "20s"`, plancheck.ResourceActionUpdate, keyMask(fake, "rotationPeriod")),
			keyStep(fake, `pools = [{ name = "bbbb" }, { name = "aa" }]
  frozen = [{ name = "aa" }]
  rotation_period = "20s"`, plancheck.ResourceActionUpdate,
				keyMask(fake, "pools"),
				resource.TestCheckTypeSetElemNestedAttrs(key, "pools.*", map[string]string{"name": "aa", "weight": "20"}),
				resource.TestCheckTypeSetElemNestedAttrs(key, "pools.*", map[string]string{"name": "bbbb", "weight": "40"})),
			keyStep(fake, `pools = [{ name = "bbbb" }, { name = "aa" }]
  frozen = [{ name = "aa" }]
  rotation_period = "20s"`, plancheck.ResourceActionNoop),
		}
	})
}

// A oneof whose members differ in IMMUTABLE: switching away from the
// immutable member, or to it, replaces the resource, as the immutable
// member can't change after the create; left out while nothing else is
// set, it is kept; switching between mutable members is an update.
func TestAOneofSwitchesFromAnImmutableMember(t *testing.T) {
	keyTest(t, func(fake *fakeFirewalls) []resource.TestStep {
		return []resource.TestStep{
			keyStep(fake, `zone = "z1"`, plancheck.ResourceActionCreate),
			keyStep(fake, ``, plancheck.ResourceActionNoop,
				resource.TestCheckResourceAttr(key, "zone", "z1")),
			keyStep(fake, `region = "r1"`, plancheck.ResourceActionReplace,
				resource.TestCheckResourceAttr(key, "region", "r1"),
				resource.TestCheckNoResourceAttr(key, "zone")),
			keyStep(fake, `zone = "z2"`, plancheck.ResourceActionReplace,
				resource.TestCheckResourceAttr(key, "zone", "z2"),
				resource.TestCheckNoResourceAttr(key, "region")),
		}
	})
}

// Between mutable members, a switch is an update naming both.
func TestAOneofSwitchesBetweenMutableMembers(t *testing.T) {
	keyTest(t, func(fake *fakeFirewalls) []resource.TestStep {
		return []resource.TestStep{
			keyStep(fake, `seed = "a"`, plancheck.ResourceActionCreate),
			keyStep(fake, `imported = { fingerprint = "ff" }`, plancheck.ResourceActionUpdate,
				keyMask(fake, "seed,imported"),
				resource.TestCheckNoResourceAttr(key, "seed")),
		}
	})
}

// A field with presence set, then removed from the configuration, keeps
// the API's value: removing it doesn't clear it.
func TestRemovingAPresenceFieldKeepsItsValue(t *testing.T) {
	keyTest(t, func(fake *fakeFirewalls) []resource.TestStep {
		return []resource.TestStep{
			keyStep(fake, `algorithm = "rsa"`, plancheck.ResourceActionCreate),
			keyStep(fake, ``, plancheck.ResourceActionNoop, func(s *terraform.State) error {
				if got := fake.key(s.RootModule().Resources[key].Primary.ID).GetAlgorithm(); got != "rsa" {
					return fmt.Errorf("the API's algorithm is %q", got)
				}
				return nil
			}),
		}
	})
}

// A message field marked optional is an object the API may choose: left
// out, null or the API's; set, its fields the API chooses are kept; left
// out again, kept.
func TestAnOptionalMessageField(t *testing.T) {
	keyTest(t, func(fake *fakeFirewalls) []resource.TestStep {
		return []resource.TestStep{
			{
				Config:            keyConfig(fake, ``),
				ConfigStateChecks: []statecheck.StateCheck{statecheck.ExpectKnownValue(key, tfjsonpath.New("primary"), knownvalue.Null())},
			},
			keyStep(fake, `primary = { name = "p" }`, plancheck.ResourceActionUpdate,
				keyMask(fake, "primary"),
				resource.TestCheckResourceAttr(key, "primary.weight", "10")),
			keyStep(fake, ``, plancheck.ResourceActionNoop,
				resource.TestCheckResourceAttr(key, "primary.name", "p")),
		}
	})
}

// A key in the other pattern, a public one, is read by name through the
// data source; the list leaves out the terminated keys.
func TestPublicKeysAndTerminatedKeysInLists(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	config := func(body string) string {
		return keyConfig(fake, body) + `
data "origincloud_keys_key" "public" {
  name = "keys/pub-1"
}

data "origincloud_keys_keys" "all" {
  project    = "p-1"
  depends_on = [origincloud_keys_key.k]
}
`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: config(`seed = "a"`)},
			{
				Config: config(`seed = "a"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.origincloud_keys_key.public", "algorithm", "ed25519"),
					resource.TestCheckResourceAttr("data.origincloud_keys_keys.all", "keys.#", "1"),
					// A list's items have no secret: no read answers with it.
					resource.TestCheckNoResourceAttr("data.origincloud_keys_keys.all", "keys.0.secret"),
				),
			},
			// Terminated outside Terraform, it leaves the list; the key is
			// created again.
			{
				PreConfig: func() { fake.terminate("projects/p-1/keys/k-1") },
				Config:    config(`seed = "a"`),
				Check:     resource.TestCheckResourceAttr("data.origincloud_keys_keys.all", "keys.#", "1"),
			},
		},
	})
}

// A delete that fails after the key is terminated succeeds: it is gone.
func TestADeleteThatFailsOnceGoneSucceeds(t *testing.T) {
	keyTest(t, func(fake *fakeFirewalls) []resource.TestStep {
		return []resource.TestStep{
			keyStep(fake, `algorithm = "doomed"`, plancheck.ResourceActionCreate),
		}
	})
}

// A field with presence in a list's elements is refused by the generator
// (TestUnsupportedFieldsFailGeneration); here, the validators.
func TestAMixedOneofRefusesTwoMembers(t *testing.T) {
	keyTest(t, func(fake *fakeFirewalls) []resource.TestStep {
		return []resource.TestStep{{
			Config: keyConfig(fake, `zone = "z1"
  region = "r1"`),
			ExpectError: regexp.MustCompile(`Conflicting oneof members`),
		}}
	})
}
