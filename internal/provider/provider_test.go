package provider

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	pvsv1beta1 "go.origincloud.eu/sdk/pvs/v1beta1"
)

const zone = "projects/p-1/zones/eu-central1-az1"

func factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"origincloud": providerserver.NewProtocol6WithError(New("test")()),
	}
}

// pvsConfig returns a configuration of the provider calling fake, with a
// volume and a snapshot of it whose bodies are volume and snapshot.
func pvsConfig(fake *fakePVS, volume, snapshot string) string {
	return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

data "origincloud_pvs_storage_class" "throughput" {
  name = "storageClasses/st1"
}

resource "origincloud_pvs_volume" "data" {
  project = "p-1"
  zone    = "eu-central1-az1"
  %s
}

resource "origincloud_pvs_snapshot" "data" {
  project       = "p-1"
  zone          = "eu-central1-az1"
  source_volume = origincloud_pvs_volume.data.name
  %s
}
`, fake.URL, volume, snapshot)
}

func TestVolumesAndSnapshotsAreCreatedUpdatedReplacedAndImported(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakePVS(t)
	volumeName := regexp.MustCompile("^" + zone + "/volumes/vol-[0-9]+$")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created; the plan after it is empty, which the test
			// framework checks after every step.
			{
				Config: pvsConfig(fake, `size_gib = 1
  tags = { env = "test" }`, `tags = { kept = "yes" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("origincloud_pvs_volume.data", "name", volumeName),
					resource.TestCheckResourceAttrPair("origincloud_pvs_volume.data", "id", "origincloud_pvs_volume.data", "name"),
					resource.TestCheckResourceAttr("origincloud_pvs_volume.data", "storage_class", "storageClasses/gp1"),
					resource.TestCheckResourceAttr("origincloud_pvs_volume.data", "state", "AVAILABLE"),
					resource.TestCheckResourceAttr("origincloud_pvs_volume.data", "tags.env", "test"),
					resource.TestCheckResourceAttrPair("origincloud_pvs_snapshot.data", "source_volume", "origincloud_pvs_volume.data", "name"),
					resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "state", "COMPLETED"),
					resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "source_size_gib", "1"),
					resource.TestCheckResourceAttr("data.origincloud_pvs_storage_class.throughput", "media", "HDD"),
				),
			},
			// Updated in place: the volume grows and loses its tags, the
			// snapshot's tags change.
			{
				Config: pvsConfig(fake, `size_gib = 2`, `tags = { kept = "no" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_pvs_volume.data", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("origincloud_pvs_snapshot.data", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("origincloud_pvs_volume.data", "size_gib", "2"),
					resource.TestCheckNoResourceAttr("origincloud_pvs_volume.data", "tags.%"),
					resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "tags.kept", "no"),
				),
			},
			// An immutable field replaces the volume, and with it the
			// snapshot of it.
			{
				Config: pvsConfig(fake, `size_gib = 2
  storage_class = data.origincloud_pvs_storage_class.throughput.name`, `tags = { kept = "no" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_pvs_volume.data", plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction("origincloud_pvs_snapshot.data", plancheck.ResourceActionReplace),
				}},
				Check: resource.TestCheckResourceAttr("origincloud_pvs_volume.data", "storage_class", "storageClasses/st1"),
			},
			// Adopted by name.
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
			{
				ResourceName:  "origincloud_pvs_volume.data",
				ImportState:   true,
				ImportStateId: "vol-1",
				ExpectError:   regexp.MustCompile(`name must have the form projects/\*/zones/\*/volumes/\*`),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for name, v := range fake.volumes {
				if v.GetState() != pvsv1beta1.Volume_DELETED {
					return fmt.Errorf("%s is %s", name, v.GetState())
				}
			}
			for name, s := range fake.snapshots {
				if s.GetState() != pvsv1beta1.Snapshot_DELETED {
					return fmt.Errorf("%s is %s", name, s.GetState())
				}
			}
			return nil
		},
	})
}

func TestAVolumeWhoseCreateFailedIsReplacedByTheNextApply(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakePVS(t)
	config := func(tags string) string {
		return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_pvs_volume" "data" {
  project  = "p-1"
  zone     = "eu-central1-az1"
  size_gib = 1
  tags     = %s
}
`, fake.URL, tags)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config:      config(`{ fail = "create" }`),
				ExpectError: regexp.MustCompile(`the storage couldn't be created`),
			},
			{
				Config: config(`{}`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_pvs_volume.data", plancheck.ResourceActionReplace),
				}},
				Check: func(*terraform.State) error {
					if !slices.Contains(fake.mutations(), "DELETE "+zone+"/volumes/vol-1") {
						return fmt.Errorf("the failed volume wasn't deleted: %v", fake.mutations())
					}
					return nil
				},
			},
		},
	})
}

// protectionConfig returns a configuration of the provider calling fake,
// with a volume, a snapshot of it whose body is snapshot, and, unless copy
// is false, a volume created from the snapshot.
func protectionConfig(fake *fakePVS, snapshot string, copy bool) string {
	config := fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_pvs_volume" "data" {
  project  = "p-1"
  zone     = "eu-central1-az1"
  size_gib = 1
}

resource "origincloud_pvs_snapshot" "data" {
  project       = "p-1"
  zone          = "eu-central1-az1"
  source_volume = origincloud_pvs_volume.data.name
  %s
}
`, fake.URL, snapshot)
	if copy {
		config += `
resource "origincloud_pvs_volume" "copy" {
  project         = "p-1"
  zone            = "eu-central1-az1"
  size_gib        = 1
  source_snapshot = origincloud_pvs_snapshot.data.name
}
`
	}
	return config
}

func TestASnapshotIsProtectedUnprotectedAndUnprotectedBeforeItIsDeleted(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakePVS(t)
	snapshot := zone + "/snapshots/snap-2"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Protected as it is created, so a volume can be created from
			// it in the same apply.
			{
				Config: protectionConfig(fake, `protected = true`, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "protected", "true"),
					resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "protection_state", "PROTECTED"),
					resource.TestCheckResourceAttrPair("origincloud_pvs_volume.copy", "source_snapshot", "origincloud_pvs_snapshot.data", "name"),
				),
			},
			// Another change leaves a protected snapshot as it is, without
			// protecting it again.
			{
				Config: protectionConfig(fake, `protected = true
  tags = { env = "test" }`, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_pvs_snapshot.data", plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "tags.env", "test"),
			},
			// Left out, the protection stays as it is.
			{
				Config: protectionConfig(fake, `tags = { env = "test" }`, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
				Check: resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "protected", "true"),
			},
			// Unprotected in place.
			{
				Config: protectionConfig(fake, `protected = false
  tags = { env = "test" }`, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_pvs_snapshot.data", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("origincloud_pvs_volume.copy", plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "protected", "false"),
					resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "protection_state", "UNPROTECTED"),
				),
			},
			// Protected again, then destroyed with the rest.
			{
				Config: protectionConfig(fake, `protected = true
  tags = { env = "test" }`, true),
				Check: resource.TestCheckResourceAttr("origincloud_pvs_snapshot.data", "protection_state", "PROTECTED"),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			want := []string{
				"POST " + snapshot + ":protect",
				"PATCH " + snapshot,
				"POST " + snapshot + ":unprotect",
				"POST " + snapshot + ":protect",
				"POST " + snapshot + ":unprotect",
				"DELETE " + snapshot,
			}
			var got []string
			for _, call := range fake.mutations() {
				if strings.Contains(call, "/snapshots/") {
					got = append(got, call)
				}
			}
			if !slices.Equal(got, want) {
				return fmt.Errorf("the snapshot's mutations were %v, not %v", got, want)
			}
			return nil
		},
	})
}

func TestARefusedUnprotectReachesTheUser(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakePVS(t)
	refused := protectionConfig(fake, `protected = true
  tags = { fail = "unprotect" }`, false)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: refused},
			{
				Config:      refused,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`an image is being built from`),
			},
			// Once the API allows it, the snapshot is destroyed.
			{Config: protectionConfig(fake, `protected = true`, false)},
		},
	})
}
