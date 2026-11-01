package testapi

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"origincloud": providerserver.NewProtocol6WithError(testProvider{}),
	}
}

const firewall = "origincloud_firewalls_firewall.fw"

// firewallConfig returns a configuration of the test provider calling
// fake, with a firewall whose body is body.
func firewallConfig(fake *fakeFirewalls, body string) string {
	return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_firewalls_firewall" "fw" {
  project = "p-1"
  %s
}
`, fake.URL, body)
}

// The firewall the tests start from. Its sets are configured out of the
// order the API answers with. It leaves optional fields out at every
// level, such as a rule's ports and sources, a port range's last and an
// address's primary, and sets some of them empty: a rule's sources and an
// interface's addresses.
const (
	rules = `rules = [
    { protocol = "tcp", ports = { first = 443 }, sources = ["10.0.0.0/8"] },
    { protocol = "udp", ports = { first = 53, last = 54 } },
    { protocol = "icmp", sources = [] },
  ]`
	reordered = `rules = [
    { protocol = "udp", ports = { first = 53, last = 54 } },
    { protocol = "tcp", ports = { first = 443 }, sources = ["10.0.0.0/8"] },
    { protocol = "icmp", sources = [] },
  ]`
	spec = `spec = {
    network = "net-1"
    interfaces = [
      { subnet = "sub-b", addresses = [{ ip = "10.0.1.5", primary = true }, { ip = "10.0.1.6" }] },
      { subnet = "sub-a", addresses = [] },
    ]
  }`
	others = `tags         = ["web", "edge", "api"]
  priorities   = [3, 1, 2]
  modes        = ["CLOSED", "OPEN"]
  default_mode = "OPEN"
  labels       = { team = { value = "net" } }`
)

// mask checks that the last update named want, and nothing else.
func mask(fake *fakeFirewalls, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got, _ := fake.lastMask(); got != want {
			return fmt.Errorf("the last update mask was %q, not %q", got, want)
		}
		return nil
	}
}

func TestNestedAndRepeatedFields(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	interfaceB := tfjsonpath.New("spec").AtMapKey("interfaces")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created. The API answers with the sets in another order and
			// with zero values for what was left out; the plan after the
			// apply, which the test framework checks, is empty.
			{
				Config: firewallConfig(fake, rules+"\n  "+spec+"\n  "+others),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(firewall, "rules.#", "3"),
					resource.TestCheckResourceAttr(firewall, "rules.2.id", "rule-3"),
					resource.TestCheckResourceAttr(firewall, "rules.0.protocol", "tcp"),
					resource.TestCheckResourceAttr(firewall, "rules.0.id", "rule-1"),
					resource.TestCheckResourceAttr(firewall, "rules.1.ports.last", "54"),
					resource.TestCheckResourceAttr(firewall, "tags.#", "3"),
					resource.TestCheckTypeSetElemAttr(firewall, "tags.*", "edge"),
					resource.TestCheckResourceAttr(firewall, "priorities.0", "3"),
					resource.TestCheckTypeSetElemAttr(firewall, "modes.*", "CLOSED"),
					resource.TestCheckResourceAttr(firewall, "labels.team.value", "net"),
					resource.TestCheckResourceAttr(firewall, "status.state", "OPEN"),
					resource.TestCheckResourceAttr(firewall, "status.rule_count", "3"),
					resource.TestCheckTypeSetElemNestedAttrs(firewall, "spec.interfaces.*", map[string]string{
						"subnet": "sub-b", "addresses.#": "2", "addresses.0.ip": "10.0.1.5", "addresses.0.primary": "true", "addresses.1.ip": "10.0.1.6",
					}),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					// Left out at every level, null.
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("rules").AtSliceIndex(0).AtMapKey("ports").AtMapKey("last"), knownvalue.Null()),
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("rules").AtSliceIndex(1).AtMapKey("sources"), knownvalue.Null()),
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("rules").AtSliceIndex(2).AtMapKey("ports"), knownvalue.Null()),
					// Set empty, at every level, empty.
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("rules").AtSliceIndex(2).AtMapKey("sources"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("labels").AtMapKey("team").AtMapKey("note"), knownvalue.Null()),
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("spec").AtMapKey("description"), knownvalue.Null()),
					// Computed and empty, an empty list.
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("status").AtMapKey("messages"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownValue(firewall, interfaceB, knownvalue.SetPartial([]knownvalue.Check{
						knownvalue.ObjectExact(map[string]knownvalue.Check{
							"subnet":    knownvalue.StringExact("sub-a"),
							"addresses": knownvalue.ListSizeExact(0),
						}),
						knownvalue.ObjectExact(map[string]knownvalue.Check{
							"subnet": knownvalue.StringExact("sub-b"),
							"addresses": knownvalue.ListExact([]knownvalue.Check{
								knownvalue.ObjectExact(map[string]knownvalue.Check{"ip": knownvalue.StringExact("10.0.1.5"), "primary": knownvalue.Bool(true)}),
								knownvalue.ObjectExact(map[string]knownvalue.Check{"ip": knownvalue.StringExact("10.0.1.6"), "primary": knownvalue.Null()}),
							}),
						}),
					})),
				},
			},
			// One field two levels down changes: the update names the
			// top-level field holding it, and sends it whole.
			{
				Config: firewallConfig(fake, rules+"\n  "+`spec = {
    network = "net-1"
    interfaces = [
      { subnet = "sub-b", addresses = [{ ip = "10.0.1.5", primary = true }, { ip = "10.0.1.7" }] },
      { subnet = "sub-a", addresses = [] },
    ]
  }`+"\n  "+others),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(firewall, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					mask(fake, "spec"),
					func(*terraform.State) error {
						_, sent := fake.lastMask()
						if n := len(sent.GetSpec().GetInterfaces()); n != 2 || sent.GetSpec().GetNetwork() != "net-1" {
							return fmt.Errorf("the update sent %v, not the whole spec", sent.GetSpec())
						}
						return nil
					},
					resource.TestCheckTypeSetElemNestedAttrs(firewall, "spec.interfaces.*", map[string]string{"subnet": "sub-b", "addresses.1.ip": "10.0.1.7"}),
				),
			},
			// The rules' order is theirs: reordered, they are updated.
			{
				Config: firewallConfig(fake, reordered+"\n  "+spec+"\n  "+others),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(firewall, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					// spec went back to its first addresses too.
					mask(fake, "rules,spec"),
					resource.TestCheckResourceAttr(firewall, "rules.0.protocol", "udp"),
					resource.TestCheckResourceAttr(firewall, "rules.1.sources.0", "10.0.0.0/8"),
				),
			},
			// Reordered outside Terraform, the rules differ from the
			// configuration.
			{
				PreConfig:          func() { fake.reverseRules("projects/p-1/firewalls/fw-1") },
				Config:             firewallConfig(fake, reordered+"\n  "+spec+"\n  "+others),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			// Applied, they are put back in order.
			{
				Config: firewallConfig(fake, rules+"\n  "+spec+"\n  "+others),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(firewall, "rules.0.protocol", "tcp"),
				),
			},
			// Empty and left out stay apart: [] stays [], and what is left
			// out is null, though the API answers with nothing for both.
			{
				Config: firewallConfig(fake, spec+`
  tags       = []
  priorities = []`),
				Check: mask(fake, "rules,tags,priorities,modes,labels,defaultMode"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("tags"), knownvalue.SetSizeExact(0)),
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("priorities"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("rules"), knownvalue.Null()),
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("modes"), knownvalue.Null()),
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("labels"), knownvalue.Null()),
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("default_mode"), knownvalue.Null()),
				},
			},
			// From [] to left out: null.
			{
				Config: firewallConfig(fake, spec+`
  priorities = []`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(firewall, tfjsonpath.New("tags"), knownvalue.Null()),
				},
			},
			// A nested immutable field replaces the firewall.
			{
				Config: firewallConfig(fake, `spec = {
    network    = "net-2"
    interfaces = [{ subnet = "sub-a" }]
  }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(firewall, plancheck.ResourceActionReplace),
				}},
				Check: resource.TestCheckResourceAttr(firewall, "name", "projects/p-1/firewalls/fw-2"),
			},
			// Adopted by name.
			{
				ResourceName:      firewall,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// The list data source reads the nested fields too.
			{
				Config: firewallConfig(fake, `spec = {
    network    = "net-2"
    interfaces = [{ subnet = "sub-a" }]
  }`) + `
data "origincloud_firewalls_firewalls" "all" {
  project    = "p-1"
  depends_on = [origincloud_firewalls_firewall.fw]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.origincloud_firewalls_firewalls.all", "firewalls.#", "1"),
					resource.TestCheckResourceAttr("data.origincloud_firewalls_firewalls.all", "firewalls.0.spec.network", "net-2"),
					resource.TestCheckResourceAttr("data.origincloud_firewalls_firewalls.all", "firewalls.0.spec.interfaces.0.subnet", "sub-a"),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.origincloud_firewalls_firewalls.all", tfjsonpath.New("firewalls").AtSliceIndex(0).AtMapKey("rules"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownValue("data.origincloud_firewalls_firewalls.all", tfjsonpath.New("firewalls").AtSliceIndex(0).AtMapKey("tags"), knownvalue.SetSizeExact(0)),
				},
			},
		},
	})
}

func TestNullElementsAreRefused(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config:      firewallConfig(fake, `tags = ["web", null]`),
				ExpectError: regexp.MustCompile(`Null element`),
			},
			{
				Config:      firewallConfig(fake, `rules = [null]`),
				ExpectError: regexp.MustCompile(`Null element`),
			},
			{
				Config:      firewallConfig(fake, `modes = ["OPEN", "AJAR"]`),
				ExpectError: regexp.MustCompile(`must be one of OPEN, CLOSED`),
			},
			{
				Config:      firewallConfig(fake, `default_mode = "AJAR"`),
				ExpectError: regexp.MustCompile(`value must be one of OPEN, CLOSED`),
			},
			{
				Config:      firewallConfig(fake, `annotations = { a = null }`),
				ExpectError: regexp.MustCompile(`Null element`),
			},
			// Optional and computed, an int32 is checked too.
			{
				Config:      firewallConfig(fake, `options = { slots = 3000000000 }`),
				ExpectError: regexp.MustCompile(`value must be between`),
			},
		},
	})
	if n := fake.updateCount(); n != 0 || len(fake.firewalls) != 0 {
		t.Errorf("a refused configuration reached the API")
	}
}

// Immutable attributes holding nested messages: an object and a list the
// API chooses when they are left out, both with output-only fields, and
// immutable fields the API chooses in a mutable object.
func TestImmutableNestedFields(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeFirewalls(t)
	step := func(body string, action plancheck.ResourceActionType, check ...resource.TestCheckFunc) resource.TestStep {
		return resource.TestStep{
			Config: firewallConfig(fake, body),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(firewall, action),
			}},
			Check: resource.ComposeAggregateTestCheckFunc(check...),
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Left out, the placement is the API's; the plan after the
			// apply is empty.
			step(`tags = ["a"]
  peers = [{ address = "p1" }]`, plancheck.ResourceActionCreate,
				resource.TestCheckResourceAttr(firewall, "placement.group", "auto"),
				resource.TestCheckResourceAttr(firewall, "placement.host", "host-auto"),
				resource.TestCheckResourceAttr(firewall, "peers.0.latency", "2"),
			),
			// Another attribute changes: the immutable ones, whose
			// output-only fields the plan doesn't know, don't replace it.
			step(`tags = ["b"]
  peers = [{ address = "p1" }]`, plancheck.ResourceActionUpdate),
			// Configured differently, the placement replaces it.
			step(`tags = ["b"]
  peers = [{ address = "p1" }]
  placement = { group = "g" }`, plancheck.ResourceActionReplace,
				resource.TestCheckResourceAttr(firewall, "placement.host", "host-g"),
			),
			step(`tags = ["c"]
  peers = [{ address = "p1" }]
  placement = { group = "g" }`, plancheck.ResourceActionUpdate),
			// Left out again, it stays as it is.
			step(`tags = ["c"]
  peers = [{ address = "p1" }]`, plancheck.ResourceActionNoop),
			// A new object whose immutable fields the API chooses: they
			// are unknown until it answers, not the state's null.
			step(`tags = ["c"]
  peers = [{ address = "p1" }]
  options = { note = "x" }`, plancheck.ResourceActionUpdate,
				resource.TestCheckResourceAttr(firewall, "options.tier", "standard"),
				resource.TestCheckResourceAttr(firewall, "options.slots", "4"),
			),
			// Its mutable field changes; the chosen ones stay.
			step(`tags = ["c"]
  peers = [{ address = "p1" }]
  options = { note = "y" }`, plancheck.ResourceActionUpdate,
				resource.TestCheckResourceAttr(firewall, "options.tier", "standard"),
				func(*terraform.State) error {
					if _, sent := fake.lastMask(); sent.GetOptions().GetTier() != "standard" {
						return fmt.Errorf("the update sent the options %v, without their tier", sent.GetOptions())
					}
					return nil
				},
			),
			// Configured differently, an immutable field replaces it.
			step(`tags = ["c"]
  peers = [{ address = "p1" }]
  options = { note = "y", tier = "premium" }`, plancheck.ResourceActionReplace,
				resource.TestCheckResourceAttr(firewall, "options.tier", "premium"),
			),
			// So does the immutable list.
			step(`tags = ["c"]
  peers = [{ address = "p22" }]
  options = { note = "y", tier = "premium" }`, plancheck.ResourceActionReplace,
				resource.TestCheckResourceAttr(firewall, "peers.0.latency", "3"),
			),
		},
	})
}
