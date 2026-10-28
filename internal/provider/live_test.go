package provider

import (
	"fmt"
	"os"
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
