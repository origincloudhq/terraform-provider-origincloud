package provider

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/origincloudhq/terraform-provider-origincloud/internal/framework"
)

// TestLivePVS runs against the live API, in the zone
// ORIGINCLOUD_LIVE_ZONE names (projects/{project}/zones/{zone}), with the
// credentials the provider finds in the environment. It creates a 1 GiB
// volume and a protected snapshot of it with a volume created from that,
// then grows the first volume while deleting the second and unprotecting
// the snapshot, plans a replacement, imports both, and destroys them at
// the end.
func TestLivePVS(t *testing.T) {
	zone := os.Getenv("ORIGINCLOUD_LIVE_ZONE")
	if zone == "" {
		t.Skip("ORIGINCLOUD_LIVE_ZONE isn't set")
	}
	values, err := framework.NameValues("projects/{project}/zones/{zone}", zone)
	if err != nil {
		t.Fatal(err)
	}
	config := func(volume, protected string, restored bool) string {
		config := fmt.Sprintf(`
data "origincloud_pvs_storage_class" "throughput" {
  name = "storageClasses/st1"
}

resource "origincloud_pvs_volume" "data" {
  project = %[1]q
  zone    = %[2]q
  tags    = { test = "terraform-provider" }
  %[3]s
}

resource "origincloud_pvs_snapshot" "data" {
  project       = %[1]q
  zone          = %[2]q
  source_volume = origincloud_pvs_volume.data.name
  protected     = %[4]s
}
`, values[0], values[1], volume, protected)
		if restored {
			config += fmt.Sprintf(`
resource "origincloud_pvs_volume" "restored" {
  project         = %[1]q
  zone            = %[2]q
  size_gib        = 1
  source_snapshot = origincloud_pvs_snapshot.data.name
  tags            = { test = "terraform-provider" }
}
`, values[0], values[1])
		}
		return config
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: config(`size_gib = 1`, "true", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("origincloud_pvs_volume.data", "state", "AVAILABLE"),
					resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "state", "COMPLETED"),
					resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "protection_state", "PROTECTED"),
					resource.TestCheckResourceAttr("origincloud_pvs_volume.restored", "state", "AVAILABLE"),
				),
			},
			{
				// The snapshot can be unprotected only once no volume
				// created from it exists.
				Config: config(`size_gib = 2`, "false", false),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_pvs_volume.data", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("origincloud_pvs_volume.restored", plancheck.ResourceActionDestroy),
					plancheck.ExpectResourceAction("origincloud_pvs_snapshot.data", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("origincloud_pvs_volume.data", "size_gib", "2"),
					resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "protection_state", "UNPROTECTED"),
				),
			},
			// Planned only: the replacement would need a second class
			// on sale.
			{
				Config: config(`size_gib = 2
  storage_class = data.origincloud_pvs_storage_class.throughput.name`, "false", false),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPreRefresh: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_pvs_volume.data", plancheck.ResourceActionReplace),
				}},
			},
			{
				ResourceName:      "origincloud_pvs_volume.data",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "origincloud_pvs_snapshot.data",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestLiveIAM reads IAM against the live API, with the credentials the
// provider finds in the environment, when ORIGINCLOUD_LIVE_ZONE is set as
// for TestLivePVS: it lists the predefined roles and the caller's own role
// bindings (organizations/-), which it needs no permission for. It changes
// nothing.
func TestLiveIAM(t *testing.T) {
	if os.Getenv("ORIGINCLOUD_LIVE_ZONE") == "" {
		t.Skip("ORIGINCLOUD_LIVE_ZONE isn't set")
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: `
data "origincloud_iam_roles" "all" {}

data "origincloud_iam_role_bindings" "mine" {
  organization = "-"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckTypeSetElemNestedAttrs("data.origincloud_iam_roles.all", "roles.*", map[string]string{"name": "roles/owner"}),
					// The caller holds a role somewhere, or couldn't run
					// TestLivePVS.
					resource.TestMatchResourceAttr("data.origincloud_iam_role_bindings.mine", "role_bindings.0.name", regexp.MustCompile(`^organizations/[^/]+/roleBindings/[^/]+$`)),
					resource.TestMatchResourceAttr("data.origincloud_iam_role_bindings.mine", "role_bindings.0.role", regexp.MustCompile(`^roles/`)),
				),
			},
		},
	})
}

// TestLiveResourceManager reads, in the live API, the organizations the
// caller belongs to and the projects of the first of them, with the
// credentials TestLivePVS uses. It changes nothing.
func TestLiveResourceManager(t *testing.T) {
	if os.Getenv("ORIGINCLOUD_LIVE_ZONE") == "" {
		t.Skip("ORIGINCLOUD_LIVE_ZONE isn't set")
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: `
data "origincloud_resourcemanager_organizations" "mine" {}

data "origincloud_resourcemanager_projects" "first" {
  organization = data.origincloud_resourcemanager_organizations.mine.organizations[0].name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("data.origincloud_resourcemanager_organizations.mine", "organizations.0.name", regexp.MustCompile(`^organizations/[^/]+$`)),
					resource.TestMatchResourceAttr("data.origincloud_resourcemanager_projects.first", "projects.0.name", regexp.MustCompile(`^projects/[^/]+$`)),
				),
			},
		},
	})
}

// TestLiveCompute reads, in the live API, the instance type catalog and the
// key pairs of the project ORIGINCLOUD_LIVE_ZONE names. It changes nothing.
func TestLiveCompute(t *testing.T) {
	zone := os.Getenv("ORIGINCLOUD_LIVE_ZONE")
	if zone == "" {
		t.Skip("ORIGINCLOUD_LIVE_ZONE isn't set")
	}
	values, err := framework.NameValues("projects/{project}/zones/{zone}", zone)
	if err != nil {
		t.Fatal(err)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
data "origincloud_compute_instance_types" "all" {}

data "origincloud_compute_key_pairs" "project" {
  project = %q
}
`, values[0]),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("data.origincloud_compute_instance_types.all", "instance_types.0.name", regexp.MustCompile(`^instanceTypes/`)),
					resource.TestCheckResourceAttrSet("data.origincloud_compute_key_pairs.project", "key_pairs.#"),
				),
			},
		},
	})
}

// TestLiveNetwork reads, in the live API, the VPCs of the project
// ORIGINCLOUD_LIVE_ZONE names, in its zone's region: among them the
// project's default VPC. It changes nothing.
func TestLiveNetwork(t *testing.T) {
	zone := os.Getenv("ORIGINCLOUD_LIVE_ZONE")
	if zone == "" {
		t.Skip("ORIGINCLOUD_LIVE_ZONE isn't set")
	}
	values, err := framework.NameValues("projects/{project}/zones/{zone}", zone)
	if err != nil {
		t.Fatal(err)
	}
	region := regexp.MustCompile(`-az[0-9]+$`).ReplaceAllString(values[1], "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
data "origincloud_network_vpcs" "project" {
  project = %q
  region  = %q
}
`, values[0], region),
				Check: resource.TestCheckTypeSetElemNestedAttrs("data.origincloud_network_vpcs.project", "vpcs.*", map[string]string{"project_default": "true"}),
			},
		},
	})
}

// TestLiveBilling reads, in the live API, the billing accounts of the first
// organization the caller belongs to, among them its default account, and
// the billing countries, with the credentials TestLivePVS uses. It changes
// nothing.
func TestLiveBilling(t *testing.T) {
	if os.Getenv("ORIGINCLOUD_LIVE_ZONE") == "" {
		t.Skip("ORIGINCLOUD_LIVE_ZONE isn't set")
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: `
data "origincloud_resourcemanager_organizations" "mine" {}

data "origincloud_billing_billing_accounts" "first" {
  organization = element(split("/", data.origincloud_resourcemanager_organizations.mine.organizations[0].name), 1)
}

data "origincloud_billing_billing_countries" "all" {}

data "origincloud_billing_billing_country" "de" {
  name = "billingCountries/DE"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckTypeSetElemNestedAttrs("data.origincloud_billing_billing_accounts.first", "billing_accounts.*", map[string]string{"default_account": "true"}),
					resource.TestMatchResourceAttr("data.origincloud_billing_billing_accounts.first", "billing_accounts.0.name", regexp.MustCompile(`^organizations/[^/]+/billingAccounts/[^/]+$`)),
					resource.TestCheckTypeSetElemNestedAttrs("data.origincloud_billing_billing_countries.all", "billing_countries.*", map[string]string{"region_code": "DE"}),
					resource.TestCheckResourceAttr("data.origincloud_billing_billing_country.de", "vat_id_required", "false"),
				),
			},
		},
	})
}

// TestLiveSupport lists, in the live API, the support cases of the first
// organization the caller belongs to, which may have none, with the
// credentials TestLivePVS uses. It changes nothing.
func TestLiveSupport(t *testing.T) {
	if os.Getenv("ORIGINCLOUD_LIVE_ZONE") == "" {
		t.Skip("ORIGINCLOUD_LIVE_ZONE isn't set")
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: `
data "origincloud_resourcemanager_organizations" "mine" {}

data "origincloud_support_cases" "first" {
  organization = element(split("/", data.origincloud_resourcemanager_organizations.mine.organizations[0].name), 1)
}
`,
				Check: resource.TestCheckResourceAttrSet("data.origincloud_support_cases.first", "cases.#"),
			},
		},
	})
}

// TestLiveDNS runs against the live API, in the project
// ORIGINCLOUD_LIVE_ZONE names, with the credentials TestLivePVS uses. It
// creates a public zone of terraform-provider.example.com, which stays
// pending, as no one can publish its challenge, with two record sets in it,
// one left without a TTL, and a private zone attached to a VPC it creates
// in the zone's region; then changes the other record set's TTL and values,
// imports the zones' three resources, and destroys everything at the end.
func TestLiveDNS(t *testing.T) {
	zone := os.Getenv("ORIGINCLOUD_LIVE_ZONE")
	if zone == "" {
		t.Skip("ORIGINCLOUD_LIVE_ZONE isn't set")
	}
	values, err := framework.NameValues("projects/{project}/zones/{zone}", zone)
	if err != nil {
		t.Fatal(err)
	}
	region := regexp.MustCompile(`-az[0-9]+$`).ReplaceAllString(values[1], "")
	config := func(ttl, address string) string {
		return fmt.Sprintf(`
resource "origincloud_network_vpc" "dns" {
  project = %[1]q
  region  = %[2]q
  tags    = { test = "terraform-provider" }
}

resource "origincloud_dns_hosted_zone" "public" {
  project    = %[1]q
  dns_name   = "terraform-provider.example.com"
  visibility = "PUBLIC"
  tags       = { test = "terraform-provider" }
}

resource "origincloud_dns_record_set" "www" {
  project     = %[1]q
  hosted_zone = basename(origincloud_dns_hosted_zone.public.name)
  dns_name    = "www.terraform-provider.example.com"
  type        = "A"
  ttl         = %[3]q
  basic       = { values = [%[4]q] }
}

resource "origincloud_dns_record_set" "spf" {
  project     = %[1]q
  hosted_zone = basename(origincloud_dns_hosted_zone.public.name)
  dns_name    = "terraform-provider.example.com"
  type        = "TXT"
  basic       = { values = ["v=spf1 -all"] }
}

resource "origincloud_dns_hosted_zone" "private" {
  project    = %[1]q
  dns_name   = "terraform-provider.example"
  visibility = "PRIVATE"
}

resource "origincloud_dns_vpc_attachment" "dns" {
  project     = %[1]q
  hosted_zone = basename(origincloud_dns_hosted_zone.private.name)
  vpc         = origincloud_network_vpc.dns.name
}
`, values[0], region, ttl, address)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: config("60s", "192.0.2.10"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("origincloud_dns_hosted_zone.public", "state", "PENDING_VERIFICATION"),
					resource.TestCheckResourceAttr("origincloud_dns_hosted_zone.public", "challenge.dns_name", "_origin-challenge.terraform-provider.example.com"),
					resource.TestCheckResourceAttrSet("origincloud_dns_hosted_zone.public", "challenge.token"),
					resource.TestCheckResourceAttrSet("origincloud_dns_hosted_zone.public", "name_servers.0"),
					resource.TestCheckResourceAttr("origincloud_dns_record_set.www", "ttl", "60s"),
					resource.TestCheckResourceAttr("origincloud_dns_record_set.spf", "ttl", "300s"),
					resource.TestCheckResourceAttr("origincloud_dns_hosted_zone.private", "state", "ACTIVE"),
					resource.TestCheckResourceAttrPair("origincloud_dns_vpc_attachment.dns", "vpc", "origincloud_network_vpc.dns", "name"),
				),
			},
			{
				Config: config("120s", "192.0.2.11"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_dns_record_set.www", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("origincloud_dns_record_set.spf", plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("origincloud_dns_record_set.www", "ttl", "120s"),
					resource.TestCheckTypeSetElemAttr("origincloud_dns_record_set.www", "basic.values.*", "192.0.2.11"),
				),
			},
			{
				ResourceName:      "origincloud_dns_hosted_zone.public",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "origincloud_dns_record_set.www",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "origincloud_dns_vpc_attachment.dns",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}
