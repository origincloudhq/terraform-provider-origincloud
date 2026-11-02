package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
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
)

const (
	networkRegion = "projects/p-1/regions/eu-central1"
	networkZone   = "projects/p-1/zones/eu-central1-az1"
)

// networkProvider configures the provider calling fake, and the project,
// region and zone the network's resources are in.
func networkProvider(fake *fakeNetwork) string {
	return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

locals {
  project = "p-1"
  region  = "eu-central1"
  zone    = "eu-central1-az1"
}

resource "origincloud_network_vpc" "main" {
  project = local.project
  region  = local.region
  tags    = { env = "test" }
}

resource "origincloud_network_subnet" "web" {
  project = local.project
  zone    = local.zone
  vpc     = origincloud_network_vpc.main.name
}
`, fake.URL)
}

// topologyConfig returns a network calling fake: a VPC whose tags are
// vpcTags, with a subnet, an internet gateway and a route table that sends
// the subnet's traffic to the internet through it, a security group with
// an SSH rule from sshFrom, and an ACL with an HTTPS rule for the subnet.
func topologyConfig(fake *fakeNetwork, vpcTags, sshFrom string) string {
	return strings.Replace(networkProvider(fake), `tags    = { env = "test" }`, "tags    = "+vpcTags, 1) + fmt.Sprintf(`
resource "origincloud_network_internet_gateway" "main" {
  project = local.project
  region  = local.region
  vpc     = origincloud_network_vpc.main.name
}

resource "origincloud_network_route_table" "public" {
  project = local.project
  region  = local.region
  vpc     = origincloud_network_vpc.main.name
}

resource "origincloud_network_route" "default" {
  project          = local.project
  region           = local.region
  route_table      = basename(origincloud_network_route_table.public.name)
  destination      = "0.0.0.0/0"
  internet_gateway = origincloud_network_internet_gateway.main.name
}

resource "origincloud_network_route_table_association" "web" {
  project     = local.project
  zone        = local.zone
  subnet      = basename(origincloud_network_subnet.web.name)
  route_table = origincloud_network_route_table.public.name
}

resource "origincloud_network_security_group" "web" {
  project = local.project
  region  = local.region
  vpc     = origincloud_network_vpc.main.name
}

resource "origincloud_network_security_group_rule" "ssh" {
  project        = local.project
  region         = local.region
  security_group = basename(origincloud_network_security_group.web.name)
  direction      = "INGRESS"
  protocol       = "TCP"
  first_port     = 22
  cidr_block     = %q
  description    = "SSH from the office"
}

resource "origincloud_network_acl" "web" {
  project = local.project
  region  = local.region
  vpc     = origincloud_network_vpc.main.name
}

resource "origincloud_network_acl_rule" "https" {
  project    = local.project
  region     = local.region
  acl        = basename(origincloud_network_acl.web.name)
  direction  = "INGRESS"
  priority   = 100
  protocol   = "TCP"
  first_port = 443
  cidr_block = "0.0.0.0/0"
  action     = "ACCEPT"
}

resource "origincloud_network_acl_association" "web" {
  project = local.project
  zone    = local.zone
  subnet  = basename(origincloud_network_subnet.web.name)
  acl     = origincloud_network_acl.web.name
}
`, sshFrom)
}

// checkNetworkAssociationName checks that the association at address is named
// in the resource at parent, after the id of the resource at named.
func checkNetworkAssociationName(address, parent, collection, named string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		name := func(address string) string {
			if r := s.RootModule().Resources[address]; r != nil {
				return r.Primary.Attributes["name"]
			}
			return ""
		}
		if want := name(parent) + "/" + collection + "/" + path.Base(name(named)); name(address) != want {
			return fmt.Errorf("%s is named %q, not %q", address, name(address), want)
		}
		return nil
	}
}

// checkNetworkDestroyed checks that fake holds nothing, and refused no
// delete on the way.
func checkNetworkDestroyed(fake *fakeNetwork) resource.TestCheckFunc {
	return func(*terraform.State) error {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for name := range fake.resources {
			return fmt.Errorf("%s wasn't deleted", name)
		}
		if len(fake.refused) > 0 {
			return fmt.Errorf("deletes of resources in use were refused: %v", fake.refused)
		}
		return nil
	}
}

func TestANetworkIsCreatedUpdatedReplacedAndDestroyedInDependencyOrder(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeNetwork(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created in the order the references give; the plan after it
			// is empty, which the test framework checks after every step.
			{
				Config: topologyConfig(fake, `{ env = "test" }`, "198.51.100.0/24"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("origincloud_network_vpc.main", "name", regexp.MustCompile("^"+networkRegion+"/vpcs/vpc-[0-9]+$")),
					resource.TestCheckResourceAttrPair("origincloud_network_vpc.main", "id", "origincloud_network_vpc.main", "name"),
					// Left out, the range is the one the API chose.
					resource.TestCheckResourceAttr("origincloud_network_vpc.main", "ipv4_cidr_block", "172.16.0.0/16"),
					resource.TestCheckResourceAttr("origincloud_network_vpc.main", "subnet_ipv4_prefix_length", "20"),
					resource.TestCheckResourceAttr("origincloud_network_vpc.main", "create_time", "2026-11-01T12:00:00Z"),
					resource.TestMatchResourceAttr("origincloud_network_subnet.web", "name", regexp.MustCompile("^"+networkZone+"/subnets/subnet-[0-9]+$")),
					resource.TestCheckResourceAttrPair("origincloud_network_subnet.web", "vpc", "origincloud_network_vpc.main", "name"),
					resource.TestCheckResourceAttr("origincloud_network_subnet.web", "ipv4_cidr_block", "172.16.0.0/20"),
					resource.TestCheckResourceAttr("origincloud_network_subnet.web", "ipv4_gateway", "172.16.0.1"),
					resource.TestCheckResourceAttr("origincloud_network_subnet.web", "ipv4_dns_servers.0", "172.16.0.1"),
					resource.TestCheckResourceAttr("origincloud_network_route_table.public", "local_routes.0", "172.16.0.0/16"),
					resource.TestCheckResourceAttrPair("origincloud_network_route.default", "internet_gateway", "origincloud_network_internet_gateway.main", "name"),
					resource.TestCheckResourceAttr("origincloud_network_route.default", "nat_gateway", ""),
					checkNetworkAssociationName("origincloud_network_route_table_association.web", "origincloud_network_subnet.web", "routeTableAssociations", "origincloud_network_route_table.public"),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.ssh", "cidr_block", "198.51.100.0/24"),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.ssh", "last_port", "0"),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.ssh", "peer_security_group", ""),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.ssh", "same_group", "false"),
					resource.TestCheckResourceAttr("origincloud_network_acl_rule.https", "action", "ACCEPT"),
					checkNetworkAssociationName("origincloud_network_acl_association.web", "origincloud_network_subnet.web", "aclAssociations", "origincloud_network_acl.web"),
				),
			},
			// What the associations and the gateway set on the VPC and the
			// subnet, created before them, is read at the next refresh.
			{
				RefreshState: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("origincloud_network_vpc.main", "internet_gateway", "origincloud_network_internet_gateway.main", "name"),
					resource.TestCheckResourceAttr("origincloud_network_vpc.main", "available_subnet_count", "15"),
					resource.TestCheckResourceAttrPair("origincloud_network_subnet.web", "route_table", "origincloud_network_route_table.public", "name"),
					resource.TestCheckResourceAttrPair("origincloud_network_subnet.web", "acl", "origincloud_network_acl.web", "name"),
				),
			},
			// A tag is updated in place, and nothing else changes.
			{
				Config: topologyConfig(fake, `{ env = "prod" }`, "198.51.100.0/24"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_network_vpc.main", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("origincloud_network_subnet.web", plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction("origincloud_network_security_group_rule.ssh", plancheck.ResourceActionNoop),
				}},
				Check: resource.TestCheckResourceAttr("origincloud_network_vpc.main", "tags.env", "prod"),
			},
			// An immutable attribute replaces the rule alone.
			{
				Config: topologyConfig(fake, `{ env = "prod" }`, "203.0.113.0/24"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_network_security_group_rule.ssh", plancheck.ResourceActionDestroyBeforeCreate),
					plancheck.ExpectResourceAction("origincloud_network_security_group.web", plancheck.ResourceActionNoop),
				}},
				Check: resource.TestCheckResourceAttr("origincloud_network_security_group_rule.ssh", "cidr_block", "203.0.113.0/24"),
			},
			// Adopted by name: regional, zonal and nested resources.
			{
				ResourceName:      "origincloud_network_vpc.main",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "origincloud_network_security_group_rule.ssh",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "origincloud_network_acl_association.web",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:  "origincloud_network_route.default",
				ImportState:   true,
				ImportStateId: networkRegion + "/routes/route-1",
				ExpectError:   regexp.MustCompile(`isn't a name of the form\s+projects/\{project\}/regions/\{region\}/routeTables/\{route_table\}/routes/\{route\}`),
			},
		},
		// The API refuses to delete a resource another is in or refers to,
		// such as a VPC that isn't empty: the order Terraform derives from
		// the references deletes the VPC last.
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			checkNetworkDestroyed(fake),
			func(*terraform.State) error {
				calls := fake.mutations()
				if last := calls[len(calls)-1]; !strings.HasPrefix(last, "DELETE "+networkRegion+"/vpcs/") {
					return fmt.Errorf("the last mutation was %q, not the VPC's delete", last)
				}
				return nil
			},
		),
	})
}

func TestARefusedDeleteOfAVPCThatIsntEmptyReachesTheUser(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeNetwork(t)
	// The subnet names the VPC rather than referring to it, so nothing
	// orders the VPC's delete after the subnet's but depends_on.
	subnet := func(dependsOn string) string {
		return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_network_subnet" "web" {
  project = "p-1"
  zone    = "eu-central1-az1"
  vpc     = "%s/vpcs/vpc-1"
  %s
}
`, fake.URL, networkRegion, dependsOn)
	}
	both := subnet(`depends_on = [origincloud_network_vpc.main]`) + `
resource "origincloud_network_vpc" "main" {
  project = "p-1"
  region  = "eu-central1"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: both},
			{
				Config: subnet(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_network_vpc.main", plancheck.ResourceActionDestroy),
				}},
				ExpectError: regexp.MustCompile(`vpcs/vpc-1\s+isn't empty:\s+projects/p-1/zones/eu-central1-az1/subnets/subnet-2\s+is in it`),
			},
			// The VPC stays in the state, and is deleted after the subnet.
			{Config: both},
		},
		CheckDestroy: func(*terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.resources) > 0 || !slices.Equal(fake.refused, []string{networkRegion + "/vpcs/vpc-1"}) {
				return fmt.Errorf("left %d resources; refused %v", len(fake.resources), fake.refused)
			}
			return nil
		},
	})
}

// peersConfig returns a configuration of the provider calling fake, with a
// VPC, a subnet, two security groups, web and lb, rules of web admitting
// lb, whose peer is fromLB, and its own members, and the rules in extra.
func peersConfig(fake *fakeNetwork, fromLB, extra string) string {
	return networkProvider(fake) + fmt.Sprintf(`
resource "origincloud_network_security_group" "web" {
  project = local.project
  region  = local.region
  vpc     = origincloud_network_vpc.main.name
}

resource "origincloud_network_security_group" "lb" {
  project = local.project
  region  = local.region
  vpc     = origincloud_network_vpc.main.name
}

resource "origincloud_network_security_group_rule" "from_lb" {
  project        = local.project
  region         = local.region
  security_group = basename(origincloud_network_security_group.web.name)
  direction      = "INGRESS"
  protocol       = "TCP"
  first_port     = 8080
  %s
}

resource "origincloud_network_security_group_rule" "within" {
  project        = local.project
  region         = local.region
  security_group = basename(origincloud_network_security_group.web.name)
  direction      = "INGRESS"
  protocol       = "ALL"
  same_group     = true
}
%s`, fromLB, extra)
}

// extraSecurityGroupRule returns a rule of the web group admitting peer.
func extraSecurityGroupRule(peer string) string {
	return fmt.Sprintf(`
resource "origincloud_network_security_group_rule" "extra" {
  project        = local.project
  region         = local.region
  security_group = basename(origincloud_network_security_group.web.name)
  direction      = "INGRESS"
  protocol       = "TCP"
  first_port     = 443
  %s
}
`, peer)
}

func TestASecurityGroupRuleHasExactlyOnePeer(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeNetwork(t)
	fromGroup := `peer_security_group = origincloud_network_security_group.lb.name`
	fromRange := `cidr_block = "10.0.0.0/8"`
	exactlyOne := regexp.MustCompile(`set exactly one of cidr_block,\s+peer_security_group and same_group`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// The peers left out read back empty, and plan no change.
			{
				Config: peersConfig(fake, fromGroup, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("origincloud_network_security_group_rule.from_lb", "peer_security_group", "origincloud_network_security_group.lb", "name"),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.from_lb", "cidr_block", ""),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.from_lb", "same_group", "false"),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.from_lb", "description", ""),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.within", "same_group", "true"),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.within", "cidr_block", ""),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.within", "peer_security_group", ""),
				),
			},
			// The provider doesn't check the peers: the API's refusals
			// reach the user.
			{
				Config:      peersConfig(fake, fromGroup, extraSecurityGroupRule(fromGroup+"\n  "+fromRange)),
				ExpectError: exactlyOne,
			},
			{
				Config:      peersConfig(fake, fromGroup, extraSecurityGroupRule("")),
				ExpectError: exactlyOne,
			},
			{
				Config:      peersConfig(fake, fromGroup, extraSecurityGroupRule(`peer_security_group = origincloud_network_security_group.web.name`)),
				ExpectError: regexp.MustCompile(`peer_security_group is the rule's own group:\s+set same_group`),
			},
			// Switching to another peer replaces the rule, and the new one
			// is created with that peer alone.
			{
				Config: peersConfig(fake, fromRange, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_network_security_group_rule.from_lb", plancheck.ResourceActionDestroyBeforeCreate),
					plancheck.ExpectResourceAction("origincloud_network_security_group_rule.within", plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.from_lb", "cidr_block", "10.0.0.0/8"),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.from_lb", "peer_security_group", ""),
				),
			},
			{
				Config: peersConfig(fake, `same_group = true`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_network_security_group_rule.from_lb", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.from_lb", "same_group", "true"),
					resource.TestCheckResourceAttr("origincloud_network_security_group_rule.from_lb", "cidr_block", ""),
				),
			},
			// Back to the other group, which is deleted after the rule
			// referring to it.
			{
				Config: peersConfig(fake, fromGroup, ""),
				Check:  resource.TestCheckResourceAttrPair("origincloud_network_security_group_rule.from_lb", "peer_security_group", "origincloud_network_security_group.lb", "name"),
			},
		},
		CheckDestroy: checkNetworkDestroyed(fake),
	})
}

// floatingIPConfig returns a configuration of the provider calling fake,
// with a VPC, a subnet, a network interface whose body is iface, and a
// floating IP, associated with the interface unless associated is false.
func floatingIPConfig(fake *fakeNetwork, iface string, associated bool) string {
	config := networkProvider(fake) + fmt.Sprintf(`
resource "origincloud_network_network_interface" "web" {
  project = local.project
  zone    = local.zone
  subnet  = origincloud_network_subnet.web.name
  %s
}

resource "origincloud_network_floating_ip" "web" {
  project = local.project
  zone    = local.zone
  tags    = { "origin:name" = "web" }
}
`, iface)
	if associated {
		config += `
resource "origincloud_network_floating_ip_association" "web" {
  project           = local.project
  zone              = local.zone
  floating_ip       = basename(origincloud_network_floating_ip.web.name)
  network_interface = origincloud_network_network_interface.web.name
}
`
	}
	return config
}

func TestAFloatingIPIsAssociatedWithANetworkInterface(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeNetwork(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: floatingIPConfig(fake, "", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("origincloud_network_floating_ip.web", "address", regexp.MustCompile(`^203\.0\.113\.[0-9]+$`)),
					resource.TestCheckResourceAttr("origincloud_network_floating_ip.web", "network_interface", ""),
					resource.TestCheckResourceAttr("origincloud_network_network_interface.web", "ipv4_address", "172.16.0.4"),
					resource.TestCheckResourceAttr("origincloud_network_network_interface.web", "state", "AVAILABLE"),
					// Left out, the API chooses it.
					resource.TestCheckResourceAttr("origincloud_network_network_interface.web", "source_address_check", "true"),
					resource.TestCheckResourceAttrPair("origincloud_network_floating_ip_association.web", "private_address", "origincloud_network_network_interface.web", "ipv4_address"),
					resource.TestCheckResourceAttrPair("origincloud_network_floating_ip_association.web", "network_interface", "origincloud_network_network_interface.web", "name"),
					checkNetworkAssociationName("origincloud_network_floating_ip_association.web", "origincloud_network_floating_ip.web", "associations", "origincloud_network_network_interface.web"),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("origincloud_network_network_interface.web", tfjsonpath.New("security_groups"), knownvalue.SetSizeExact(0)),
				},
			},
			// The floating IP reads what its association set at the next
			// refresh.
			{
				RefreshState: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("origincloud_network_floating_ip.web", "network_interface", "origincloud_network_network_interface.web", "name"),
					resource.TestCheckResourceAttr("origincloud_network_floating_ip.web", "private_address", "172.16.0.4"),
					resource.TestCheckResourceAttr("origincloud_network_subnet.web", "available_ipv4_address_count", "4090"),
				),
			},
			{
				ResourceName:      "origincloud_network_floating_ip_association.web",
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Removing the association leaves the address and the interface,
			// which is updated in place.
			{
				Config: floatingIPConfig(fake, "source_address_check = false", false),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_network_floating_ip_association.web", plancheck.ResourceActionDestroy),
					plancheck.ExpectResourceAction("origincloud_network_network_interface.web", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("origincloud_network_floating_ip.web", plancheck.ResourceActionNoop),
				}},
				Check: resource.TestCheckResourceAttr("origincloud_network_network_interface.web", "source_address_check", "false"),
			},
			{
				RefreshState: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("origincloud_network_floating_ip.web", "network_interface", ""),
					resource.TestCheckResourceAttr("origincloud_network_floating_ip.web", "private_address", ""),
				),
			},
			// Associated again, then destroyed: the association before the
			// interface it refers to.
			{Config: floatingIPConfig(fake, "source_address_check = false", true)},
		},
		CheckDestroy: checkNetworkDestroyed(fake),
	})
}

func TestNetworkListDataSourcesReadEveryPage(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeNetwork(t)
	resources := networkProvider(fake) + `
resource "origincloud_network_security_group" "web" {
  project = local.project
  region  = local.region
  vpc     = origincloud_network_vpc.main.name
}

resource "origincloud_network_security_group_rule" "https" {
  project        = local.project
  region         = local.region
  security_group = basename(origincloud_network_security_group.web.name)
  direction      = "INGRESS"
  protocol       = "TCP"
  first_port     = 443
  cidr_block     = "0.0.0.0/0"
}

# Each created after the one before, so listed after it.
resource "origincloud_network_security_group_rule" "ephemeral" {
  project        = local.project
  region         = local.region
  security_group = basename(origincloud_network_security_group.web.name)
  direction      = "EGRESS"
  protocol       = "UDP"
  first_port     = 32768
  last_port      = 60999
  cidr_block     = "::/0"
  depends_on     = [origincloud_network_security_group_rule.https]
}

resource "origincloud_network_security_group_rule" "within" {
  project        = local.project
  region         = local.region
  security_group = basename(origincloud_network_security_group.web.name)
  direction      = "INGRESS"
  protocol       = "ALL"
  same_group     = true
  depends_on     = [origincloud_network_security_group_rule.ephemeral]
}
`
	lists := resources + `
data "origincloud_network_security_group_rules" "web" {
  project        = local.project
  region         = local.region
  security_group = basename(origincloud_network_security_group.web.name)
}

data "origincloud_network_vpcs" "central" {
  project = local.project
  region  = local.region
}

data "origincloud_network_subnets" "az1" {
  project = local.project
  zone    = local.zone
}

data "origincloud_network_subnets" "az2" {
  project = local.project
  zone    = "eu-central1-az2"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: resources},
			// The fake answers one resource per page: every one is read.
			{
				Config: lists,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.origincloud_network_security_group_rules.web", "security_group_rules.#", "3"),
					resource.TestCheckResourceAttrPair("data.origincloud_network_security_group_rules.web", "security_group_rules.0.name", "origincloud_network_security_group_rule.https", "name"),
					resource.TestCheckResourceAttrPair("data.origincloud_network_security_group_rules.web", "security_group_rules.1.name", "origincloud_network_security_group_rule.ephemeral", "name"),
					resource.TestCheckResourceAttrPair("data.origincloud_network_security_group_rules.web", "security_group_rules.2.name", "origincloud_network_security_group_rule.within", "name"),
					resource.TestCheckResourceAttr("data.origincloud_network_security_group_rules.web", "security_group_rules.1.direction", "EGRESS"),
					resource.TestCheckResourceAttr("data.origincloud_network_security_group_rules.web", "security_group_rules.1.last_port", "60999"),
					resource.TestCheckResourceAttr("data.origincloud_network_security_group_rules.web", "security_group_rules.2.same_group", "true"),
					resource.TestCheckResourceAttr("data.origincloud_network_vpcs.central", "vpcs.#", "1"),
					resource.TestCheckResourceAttr("data.origincloud_network_vpcs.central", "vpcs.0.tags.env", "test"),
					resource.TestCheckResourceAttr("data.origincloud_network_subnets.az1", "subnets.#", "1"),
					resource.TestCheckResourceAttrPair("data.origincloud_network_subnets.az1", "subnets.0.vpc", "origincloud_network_vpc.main", "name"),
				),
				// A zone without subnets lists none: an empty list, not null.
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.origincloud_network_subnets.az2", tfjsonpath.New("subnets"), knownvalue.ListSizeExact(0)),
				},
			},
		},
		CheckDestroy: checkNetworkDestroyed(fake),
	})
}

func TestARegionalProviderCallsNetworkAtItsRegionsHost(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	t.Setenv("ORIGINCLOUD_REGION", "")
	fake := newFakeNetwork(t)
	// Operations are waited on: their names, in a zone, reach the
	// region's host too.
	fake.pending = true
	const host = "network.eu-central1.origincloudapis.eu"
	h := &hosts{servers: map[string]*httptest.Server{host: fake.Server}}
	regional := map[string]func() (tfprotov6.ProviderServer, error){
		"origincloud": providerserver.NewProtocol6WithError(&originCloud{version: "test", httpClient: &http.Client{Transport: h}}),
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: regional,
		Steps: []resource.TestStep{
			{
				Config: strings.Replace(networkProvider(fake), fmt.Sprintf("endpoint = %q", fake.URL), `region = "eu-central1"`, 1) + `
data "origincloud_network_vpcs" "central" {
  project    = local.project
  region     = local.region
  depends_on = [origincloud_network_subnet.web]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("origincloud_network_subnet.web", "vpc", "origincloud_network_vpc.main", "name"),
					resource.TestCheckResourceAttr("data.origincloud_network_vpcs.central", "vpcs.#", "1"),
					func(*terraform.State) error {
						calls := h.calls()
						for _, want := range []string{
							host + " /v1beta1/" + networkRegion + "/vpcs",
							host + " /v1beta1/" + networkZone + "/subnets",
						} {
							if !slices.Contains(calls, want) {
								return fmt.Errorf("no call %q in %v", want, calls)
							}
						}
						waits := regexp.MustCompile("^" + regexp.QuoteMeta(host+" /v1beta1/"+networkZone) + "/operations/op-[0-9]+:wait$")
						if !slices.ContainsFunc(calls, waits.MatchString) {
							return fmt.Errorf("no operation waited on in %v", calls)
						}
						for _, call := range calls {
							if !strings.HasPrefix(call, host+" ") {
								return fmt.Errorf("a call went to another host: %q", call)
							}
						}
						return nil
					},
				),
			},
		},
	})
}
