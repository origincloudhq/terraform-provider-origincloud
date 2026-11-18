package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

var pvsPath = regexp.MustCompile(`/(volumes|snapshots|storageClasses)(/|$)`)

// servicesFake serves PVS and compute at one endpoint, by the resources a
// request's path names.
func servicesFake(t *testing.T) (url string, pvs *fakePVS, compute *fakeCompute) {
	t.Helper()
	pvs, compute = newFakePVS(t), newFakeCompute(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pvsPath.MatchString(r.URL.Path) {
			pvs.serve(w, r)
			return
		}
		compute.serve(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL, pvs, compute
}

// A reference to another service's resource is written as its id, its full
// name. The platform refuses one to another project's resource.
func TestAReferenceToAnotherServicesResourceTakesItsId(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	url, _, _ := servicesFake(t)
	config := func(volume string) string {
		return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_pvs_volume" "data" {
  project  = "p-1"
  zone     = "eu-central1-az1"
  size_gib = 5
}

resource "origincloud_pvs_volume" "other" {
  project  = "p-2"
  zone     = "eu-central1-az1"
  size_gib = 5
}

resource "origincloud_compute_key_pair" "admin" {
  project = "p-1"
}

resource "origincloud_compute_instance" "web" {
  project            = "p-1"
  zone               = "eu-central1-az1"
  key_pair           = origincloud_compute_key_pair.admin.id
  network_interfaces = [%q]
  instance_type      = "//compute.origincloudapis.eu/instanceTypes/gp1.core.2"
  image              = "//compute.origincloudapis.eu/zones/eu-central1-az1/images/ubuntu-24-04-v1"
}

resource "origincloud_compute_volume_attachment" "data" {
  project  = "p-1"
  zone     = "eu-central1-az1"
  instance = basename(origincloud_compute_instance.web.name)
  volume   = %s
}
`, url, computeNetworkInterface, volume)
	}
	attachment := "origincloud_compute_volume_attachment.data"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// The id of a PVS volume is what a compute attachment takes.
			{
				Config: config("origincloud_pvs_volume.data.id"),
				Check: resource.ComposeAggregateTestCheckFunc(
					checkFullName("origincloud_pvs_volume.data"),
					resource.TestCheckResourceAttrPair(attachment, "volume", "origincloud_pvs_volume.data", "id"),
					resource.TestMatchResourceAttr(attachment, "volume", regexp.MustCompile(`^//pvs\.origincloudapis\.eu/projects/p-1/zones/eu-central1-az1/volumes/vol-[0-9]+$`)),
				),
			},
			// Another project's volume is refused by the platform, by its id too.
			{
				Config:      config("origincloud_pvs_volume.other.id"),
				ExpectError: regexp.MustCompile(`(?s)INVALID_ARGUMENT.*isn't\s+a\s+volume\s+of\s+the\s+instance's\s+project`),
			},
			// A name, which is relative, is refused before anything is sent.
			{
				Config:      config("origincloud_pvs_volume.data.name"),
				ExpectError: regexp.MustCompile(`(?s)Not a full resource name.*use\s+the\s+` + "`id`"),
			},
		},
	})
}

// An import takes a full or a relative name, and the same state results.
func TestAnImportTakesAFullOrARelativeName(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakePVS(t)
	config := pvsConfig(fake, `size_gib = 1`, ``)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config:            config,
				ResourceName:      "origincloud_pvs_volume.data",
				ImportState:       true,
				ImportStateId:     full("pvs", zone+"/volumes/vol-1"),
				ImportStateVerify: true,
			},
			{
				Config:            config,
				ResourceName:      "origincloud_pvs_volume.data",
				ImportState:       true,
				ImportStateId:     zone + "/volumes/vol-1",
				ImportStateVerify: true,
			},
			{
				Config:        config,
				ResourceName:  "origincloud_pvs_volume.data",
				ImportState:   true,
				ImportStateId: full("compute", zone+"/volumes/vol-1"),
				ExpectError:   regexp.MustCompile(`(?s)Couldn't import.*isn't\s+a\s+full\s+name\s+on\s+pvs\.origincloudapis\.eu`),
			},
		},
	})
}
