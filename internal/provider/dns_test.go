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
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const (
	publicZone  = "origincloud_dns_hosted_zone.public"
	privateZone = "origincloud_dns_hosted_zone.private"
	wwwRecords  = "origincloud_dns_record_set.www"
	apexRecords = "origincloud_dns_record_set.apex"
	spfRecords  = "origincloud_dns_record_set.spf"
	corpVPC     = "origincloud_dns_vpc_attachment.corp"
	// The VPC the private zone is attached to.
	vpc1 = "projects/p-1/regions/eu-central1/vpcs/vpc-1"
)

// dnsConfig returns a configuration of the provider calling fake, with a
// public zone whose body is zone, a record set www in it whose body is www,
// a record set at its apex whose body is apex, a private zone attached to
// the VPC attached, and more after them.
func dnsConfig(fake *fakeDNS, zone, www, apex, attached, more string) string {
	return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_dns_hosted_zone" "public" {
  project    = "p-1"
  visibility = "PUBLIC"
  %s
}

resource "origincloud_dns_record_set" "www" {
  project     = "p-1"
  hosted_zone = basename(origincloud_dns_hosted_zone.public.name)
  %s
}

resource "origincloud_dns_record_set" "apex" {
  project     = "p-1"
  hosted_zone = basename(origincloud_dns_hosted_zone.public.name)
  dns_name    = origincloud_dns_hosted_zone.public.dns_name
  %s
}

resource "origincloud_dns_hosted_zone" "private" {
  project    = "p-1"
  dns_name   = "corp.example"
  visibility = "PRIVATE"
}

resource "origincloud_dns_vpc_attachment" "corp" {
  project     = "p-1"
  hosted_zone = basename(origincloud_dns_hosted_zone.private.name)
  vpc         = %q
}
%s`, fake.URL, zone, www, apex, attached, more)
}

func TestHostedZonesRecordSetsAndVPCAttachmentsAreManaged(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeDNS(t)
	zone := `dns_name = "Example.com"
  tags     = { env = "test" }`
	www := func(dnsName, ttl, values string) string {
		return fmt.Sprintf(`dns_name = %q
  type     = "A"
  ttl      = %q
  basic    = { values = [%s] }`, dnsName, ttl, values)
	}
	fip := `alias = { floating_ip = "projects/p-1/zones/eu-central1-az1/floatingIps/fip-1" }`
	apex := func(kind string) string {
		return `type = "A"
  ttl  = "300s"
  ` + kind
	}
	lists := `
data "origincloud_dns_hosted_zones" "p1" {
  project    = "p-1"
  depends_on = [origincloud_dns_hosted_zone.public, origincloud_dns_hosted_zone.private]
}

data "origincloud_dns_record_sets" "public" {
  project     = "p-1"
  hosted_zone = basename(origincloud_dns_hosted_zone.public.name)
  depends_on  = [origincloud_dns_record_set.www, origincloud_dns_record_set.apex]
}

data "origincloud_dns_vpc_attachments" "private" {
  project     = "p-1"
  hosted_zone = basename(origincloud_dns_hosted_zone.private.name)
  depends_on  = [origincloud_dns_vpc_attachment.corp]
}
`
	zoneName := regexp.MustCompile(`^projects/p-1/hostedZones/hz-[0-9]+$`)
	recordSetName := regexp.MustCompile(`^projects/p-1/hostedZones/hz-[0-9]+/recordSets/rrset-[0-9]+$`)
	var publicName, wwwName string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created: the public zone is pending, with the challenge to
			// publish and the name servers to delegate to; the private
			// zone is active, without either, and attached. The zone's
			// name stays as configured though the API answers it in
			// lowercase. The lists read every page.
			{
				Config: dnsConfig(fake, zone, www("www.example.com", "60s", `"192.0.2.10", "192.0.2.11"`), apex(fip), vpc1, lists),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(publicZone, "name", zoneName),
					resource.TestCheckResourceAttrPair(publicZone, "id", publicZone, "name"),
					resource.TestCheckResourceAttr(publicZone, "dns_name", "Example.com"),
					resource.TestCheckResourceAttr(publicZone, "state", "PENDING_VERIFICATION"),
					resource.TestCheckResourceAttr(publicZone, "name_servers.#", "2"),
					resource.TestCheckResourceAttr(publicZone, "name_servers.0", "ns1.origincloud.zone"),
					resource.TestCheckResourceAttr(publicZone, "challenge.dns_name", "_origin-challenge.example.com"),
					resource.TestMatchResourceAttr(publicZone, "challenge.token", regexp.MustCompile(`^origin-verify=`)),
					resource.TestCheckResourceAttr(publicZone, "challenge.expire_time", "2026-11-08T12:00:00Z"),
					resource.TestCheckNoResourceAttr(publicZone, "challenge.verify_time"),
					resource.TestCheckResourceAttr(publicZone, "creator", "user:test"),
					resource.TestCheckResourceAttr(publicZone, "tags.env", "test"),
					resource.TestCheckResourceAttr(privateZone, "state", "ACTIVE"),
					resource.TestCheckResourceAttr(privateZone, "name_servers.#", "0"),
					resource.TestCheckNoResourceAttr(privateZone, "challenge.token"),
					resource.TestMatchResourceAttr(wwwRecords, "name", recordSetName),
					resource.TestCheckResourceAttr(wwwRecords, "ttl", "60s"),
					resource.TestCheckTypeSetElemAttr(wwwRecords, "basic.values.*", "192.0.2.11"),
					resource.TestCheckNoResourceAttr(wwwRecords, "alias.floating_ip"),
					resource.TestCheckResourceAttr(apexRecords, "dns_name", "Example.com"),
					resource.TestCheckResourceAttr(apexRecords, "alias.floating_ip", "projects/p-1/zones/eu-central1-az1/floatingIps/fip-1"),
					resource.TestCheckNoResourceAttr(apexRecords, "basic.values.#"),
					resource.TestCheckResourceAttr(corpVPC, "vpc", vpc1),
					resource.TestMatchResourceAttr(corpVPC, "name", regexp.MustCompile(`^projects/p-1/hostedZones/hz-[0-9]+/vpcAttachments/vpc-1$`)),
					resource.TestCheckResourceAttr("data.origincloud_dns_hosted_zones.p1", "hosted_zones.#", "2"),
					resource.TestCheckTypeSetElemNestedAttrs("data.origincloud_dns_hosted_zones.p1", "hosted_zones.*", map[string]string{
						"dns_name": "example.com", "visibility": "PUBLIC", "state": "PENDING_VERIFICATION", "challenge.dns_name": "_origin-challenge.example.com",
					}),
					resource.TestCheckTypeSetElemNestedAttrs("data.origincloud_dns_hosted_zones.p1", "hosted_zones.*", map[string]string{
						"dns_name": "corp.example", "visibility": "PRIVATE", "state": "ACTIVE",
					}),
					resource.TestCheckResourceAttr("data.origincloud_dns_record_sets.public", "record_sets.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_dns_vpc_attachments.private", "vpc_attachments.0.vpc", vpc1),
					func(s *terraform.State) error {
						publicName = s.RootModule().Resources[publicZone].Primary.Attributes["name"]
						wwwName = s.RootModule().Resources[wwwRecords].Primary.Attributes["name"]
						return nil
					},
				),
			},
			// The zone verified: a refresh finds it active. The names
			// written in another case change nothing; the TTL, the values
			// and the tags are updated in place, and the apex switches from
			// an alias to values of its own.
			{
				PreConfig: func() { fake.verify(publicName) },
				Config: dnsConfig(fake, `dns_name = "example.com"
  tags     = { env = "prod" }`, www("WWW.Example.com", "120s", `"192.0.2.12"`), apex(`basic = { values = ["192.0.2.20"] }`), vpc1, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(publicZone, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(wwwRecords, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(apexRecords, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(corpVPC, plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(publicZone, "state", "ACTIVE"),
					resource.TestCheckResourceAttr(publicZone, "challenge.verify_time", "2026-11-02T09:00:00Z"),
					resource.TestCheckNoResourceAttr(publicZone, "challenge.expire_time"),
					resource.TestCheckResourceAttr(publicZone, "tags.env", "prod"),
					resource.TestCheckResourceAttr(wwwRecords, "dns_name", "WWW.Example.com"),
					resource.TestCheckResourceAttr(wwwRecords, "ttl", "120s"),
					resource.TestCheckResourceAttr(wwwRecords, "basic.values.#", "1"),
					resource.TestCheckResourceAttr(wwwRecords, "update_time", "2026-11-02T09:00:00Z"),
					resource.TestCheckResourceAttr(apexRecords, "basic.values.#", "1"),
					resource.TestCheckNoResourceAttr(apexRecords, "alias.floating_ip"),
					func(s *terraform.State) error {
						if name := s.RootModule().Resources[wwwRecords].Primary.Attributes["name"]; name != wwwName {
							return fmt.Errorf("www was replaced: %s, not %s", name, wwwName)
						}
						return nil
					},
				),
			},
			// Another name or type is another record set: it replaces it.
			{
				Config: dnsConfig(fake, `dns_name = "example.com"
  tags     = { env = "prod" }`, `dns_name = "www.example.com"
  type     = "AAAA"
  ttl      = "120s"
  basic    = { values = ["2001:db8::10"] }`, apex(`basic = { values = ["192.0.2.20"] }`), vpc1, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(wwwRecords, plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction(apexRecords, plancheck.ResourceActionNoop),
				}},
				Check: resource.TestCheckResourceAttr(wwwRecords, "type", "AAAA"),
			},
			// Values and an alias at once, or neither, are refused before
			// anything is sent.
			{
				Config:      dnsConfig(fake, `dns_name = "example.com"`, www("www.example.com", "60s", `"192.0.2.10"`)+"\n  "+fip, apex(`basic = { values = ["192.0.2.20"] }`), vpc1, ""),
				ExpectError: regexp.MustCompile(`exactly one of this and alias must be set: alias is set too`),
			},
			{
				Config: dnsConfig(fake, `dns_name = "example.com"`, `dns_name = "www.example.com"
  type     = "A"`, apex(`basic = { values = ["192.0.2.20"] }`), vpc1, ""),
				ExpectError: regexp.MustCompile(`exactly one of this and alias must be set`),
			},
			// What the API refuses reaches the user: an alias to another
			// project's floating IP, and a VPC of another project.
			{
				Config:      dnsConfig(fake, `dns_name = "example.com"`, www("www.example.com", "60s", `"192.0.2.10"`), apex(`alias = { floating_ip = "projects/p-2/zones/eu-central1-az1/floatingIps/fip-9" }`), vpc1, ""),
				ExpectError: regexp.MustCompile(`isn't a floating IP of\s+the zone's project`),
			},
			{
				Config:      dnsConfig(fake, `dns_name = "example.com"`, www("www.example.com", "60s", `"192.0.2.10"`), apex(`basic = { values = ["192.0.2.20"] }`), "projects/p-2/regions/eu-central1/vpcs/vpc-9", ""),
				ExpectError: regexp.MustCompile(`isn't a VPC of the zone's\s+project`),
			},
			// Back to a configuration the API accepts.
			{
				Config: dnsConfig(fake, `dns_name = "example.com"`, www("www.example.com", "60s", `"192.0.2.10"`), apex(`basic = { values = ["192.0.2.20"] }`), vpc1, ""),
			},
			// A record set left without a TTL takes the API's default, and
			// the plan after it changes nothing.
			{
				Config: dnsConfig(fake, `dns_name = "example.com"`, www("www.example.com", "60s", `"192.0.2.10"`), apex(`basic = { values = ["192.0.2.20"] }`), vpc1, `
resource "origincloud_dns_record_set" "spf" {
  project     = "p-1"
  hosted_zone = basename(origincloud_dns_hosted_zone.public.name)
  dns_name    = "example.com"
  type        = "TXT"
  basic       = { values = ["v=spf1 -all"] }
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(spfRecords, plancheck.ResourceActionCreate),
						plancheck.ExpectUnknownValue(spfRecords, tfjsonpath.New("ttl")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr(spfRecords, "ttl", "300s"),
			},
			// Adopted by name.
			{
				ResourceName:      publicZone,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      wwwRecords,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      corpVPC,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:  wwwRecords,
				ImportState:   true,
				ImportStateId: "rrset-1",
				ExpectError:   regexp.MustCompile(`"rrset-1" isn't a name of the form\s+projects/\{project\}/hostedZones/\{hosted_zone\}/recordSets/\{record_set\}`),
			},
		},
		// The record sets and the attachment go before their zones, which
		// the API refuses to delete while they hold any.
		CheckDestroy: func(*terraform.State) error {
			calls := fake.mutations()
			for _, want := range []string{
				"PATCH " + publicName + " tags",
				"PATCH " + wwwName + " ttl,basic",
				"DELETE " + wwwName,
			} {
				if !slices.Contains(calls, want) {
					return fmt.Errorf("no call %q in %q", want, calls)
				}
			}
			if !slices.ContainsFunc(calls, func(c string) bool {
				return strings.HasPrefix(c, "PATCH "+publicName+"/recordSets/") && strings.HasSuffix(c, " basic,alias")
			}) {
				return fmt.Errorf("no switch from the alias to values in %q", calls)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.zones)+len(fake.recordSets)+len(fake.attachments) != 0 {
				return fmt.Errorf("left: %v %v %v", fake.zones, fake.recordSets, fake.attachments)
			}
			return nil
		},
	})
}

// A public zone that doesn't verify within its window is deleted with its
// record sets, outside Terraform: they leave the state at the next refresh,
// and the next apply creates them again, with a new challenge.
func TestAnExpiredPublicZoneIsCreatedAgain(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeDNS(t)
	config := fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_dns_hosted_zone" "public" {
  project    = "p-1"
  dns_name   = "example.com"
  visibility = "PUBLIC"
}

resource "origincloud_dns_record_set" "www" {
  project     = "p-1"
  hosted_zone = basename(origincloud_dns_hosted_zone.public.name)
  dns_name    = "www.example.com"
  type        = "TXT"
  ttl         = "300s"
  basic       = { values = ["hello"] }
}
`, fake.URL)
	var first, token string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					first = s.RootModule().Resources[publicZone].Primary.Attributes["name"]
					token = s.RootModule().Resources[publicZone].Primary.Attributes["challenge.token"]
					return nil
				},
			},
			{
				PreConfig: func() {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					for name := range fake.recordSets {
						delete(fake.recordSets, name)
					}
					delete(fake.zones, first)
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(publicZone, plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction(wwwRecords, plancheck.ResourceActionCreate),
				}},
				Check: func(s *terraform.State) error {
					zone := s.RootModule().Resources[publicZone].Primary.Attributes
					if zone["name"] == first || zone["challenge.token"] == token {
						return fmt.Errorf("the zone wasn't created again: %s, %s", zone["name"], zone["challenge.token"])
					}
					return nil
				},
			},
		},
	})
}

func TestARegionalProviderCallsDNSAtItsGlobalHost(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	t.Setenv("ORIGINCLOUD_REGION", "")
	// Only this host answers: a DNS call to the region's front door would
	// fail.
	h := &hosts{servers: map[string]*httptest.Server{
		"dns.origincloudapis.eu": newFakeDNS(t).Server,
	}}
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

resource "origincloud_dns_hosted_zone" "public" {
  project    = "p-1"
  dns_name   = "example.com"
  visibility = "PUBLIC"
}

data "origincloud_dns_hosted_zones" "p1" {
  project    = "p-1"
  depends_on = [origincloud_dns_hosted_zone.public]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(publicZone, "state", "PENDING_VERIFICATION"),
					resource.TestCheckResourceAttr("data.origincloud_dns_hosted_zones.p1", "hosted_zones.#", "1"),
					func(*terraform.State) error {
						for _, want := range []string{
							"dns.origincloudapis.eu /v1beta1/projects/p-1/hostedZones",
						} {
							if !slices.Contains(h.calls(), want) {
								return fmt.Errorf("no call %q in %v", want, h.calls())
							}
						}
						return nil
					},
				),
			},
		},
	})
}
