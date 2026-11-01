package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestLocationDataSourcesReadRegionsAndZones(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeLocation(t)
	config := fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

data "origincloud_location_region" "central" {
  name = "regions/eu-central1"
}

data "origincloud_location_zone" "az1" {
  name = "zones/eu-central1-az1"
}

data "origincloud_location_zone" "west" {
  name = "zones/eu-west1-az1"
}

data "origincloud_location_regions" "all" {}

data "origincloud_location_zones" "all" {}

output "pvs_zones" {
  value = [for z in data.origincloud_location_zones.all.zones : z.name if contains(z.services, "pvs")]
}
`, fake.URL)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Read; the plan after it is empty, which the test framework
			// checks after every step, so an empty list doesn't churn.
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.origincloud_location_region.central", "display_name", "EU Central 1"),
					resource.TestCheckResourceAttr("data.origincloud_location_region.central", "zones.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_location_region.central", "zones.0", "zones/eu-central1-az1"),
					resource.TestCheckResourceAttr("data.origincloud_location_region.central", "zones.1", "zones/eu-central1-az2"),
					resource.TestCheckResourceAttr("data.origincloud_location_zone.az1", "region", "regions/eu-central1"),
					resource.TestCheckResourceAttr("data.origincloud_location_zone.az1", "services.#", "1"),
					resource.TestCheckResourceAttr("data.origincloud_location_zone.az1", "services.0", "pvs"),
					resource.TestCheckResourceAttr("data.origincloud_location_regions.all", "regions.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_location_regions.all", "regions.1.zones.0", "zones/eu-west1-az1"),
					resource.TestCheckResourceAttr("data.origincloud_location_zones.all", "zones.#", "3"),
					resource.TestCheckResourceAttr("data.origincloud_location_zones.all", "zones.1.display_name", "EU Central 1, zone 2"),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					// A zone that offers no service: an empty list, not null.
					statecheck.ExpectKnownValue("data.origincloud_location_zone.west", tfjsonpath.New("services"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownValue("data.origincloud_location_zones.all", tfjsonpath.New("zones").AtSliceIndex(2).AtMapKey("services"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownOutputValue("pvs_zones", knownvalue.ListExact([]knownvalue.Check{
						knownvalue.StringExact("zones/eu-central1-az1"),
						knownvalue.StringExact("zones/eu-central1-az2"),
					})),
				},
			},
			{
				Config: fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

data "origincloud_location_zone" "nowhere" {
  name = "zones/nowhere"
}
`, fake.URL),
				ExpectError: regexp.MustCompile(`zones/nowhere doesn't exist`),
			},
		},
	})
}

func TestARegionalProviderCallsLocationAtItsGlobalHost(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	t.Setenv("ORIGINCLOUD_REGION", "")
	// Only these hosts answer: a location call to the region's front door
	// would fail.
	h := &hosts{servers: map[string]*httptest.Server{
		"location.origincloudapis.eu":        newFakeLocation(t),
		"pvs.eu-central1.origincloudapis.eu": newFakePVS(t).Server,
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

data "origincloud_location_zone" "az1" {
  name = "zones/eu-central1-az1"
}

data "origincloud_location_zones" "all" {}

data "origincloud_pvs_storage_class" "general" {
  name = "storageClasses/gp1"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.origincloud_location_zone.az1", "services.0", "pvs"),
					resource.TestCheckResourceAttr("data.origincloud_location_zones.all", "zones.#", "3"),
					resource.TestCheckResourceAttr("data.origincloud_pvs_storage_class.general", "media", "SSD"),
					func(*terraform.State) error {
						for _, want := range []string{
							"location.origincloudapis.eu /v1beta1/zones/eu-central1-az1",
							"location.origincloudapis.eu /v1beta1/zones",
							"pvs.eu-central1.origincloudapis.eu /v1beta1/storageClasses/gp1",
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
