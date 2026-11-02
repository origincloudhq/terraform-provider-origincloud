package provider

import (
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	computev1beta1 "go.origincloud.eu/sdk/compute/v1beta1"
)

const (
	computeInstance = "origincloud_compute_instance.web"
	// computeNetworkInterface is an interface the network service created, which
	// the fake compute service takes as it is.
	computeNetworkInterface = zone + "/networkInterfaces/eni-1"
)

// computeConfig returns a configuration of the provider calling fake,
// with a generated key pair and an instance whose body is instance, and
// then more.
func computeConfig(fake *fakeCompute, instance, more string) string {
	return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_compute_key_pair" "admin" {
  project = "p-1"
}

resource "origincloud_compute_instance" "web" {
  project            = "p-1"
  zone               = "eu-central1-az1"
  key_pair           = origincloud_compute_key_pair.admin.name
  network_interfaces = [%q]
  %s
}
%s`, fake.URL, computeNetworkInterface, instance, more)
}

func TestInstancesAreCreatedUpdatedReplacedAndImported(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeCompute(t)
	instanceName := regexp.MustCompile("^" + zone + "/instances/i-[0-9]+$")
	var first string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Launched from a family: the API resolves the image, the
			// login user and the boot volume, which the next plan keeps.
			{
				Config: computeConfig(fake, `instance_type = "instanceTypes/gp1.core.2"
  image_family  = "imageFamilies/ubuntu-24-04-minimal"
  tags          = { env = "test" }`, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(computeInstance, "name", instanceName),
					resource.TestCheckResourceAttrPair(computeInstance, "id", computeInstance, "name"),
					resource.TestCheckResourceAttr(computeInstance, "image", "zones/eu-central1-az1/images/ubuntu-24-04-v2"),
					resource.TestCheckResourceAttr(computeInstance, "login_user", "ubuntu"),
					resource.TestCheckResourceAttr(computeInstance, "boot_volume.size_gib", "10"),
					resource.TestCheckResourceAttr(computeInstance, "boot_volume.storage_class", "storageClasses/gp1"),
					resource.TestMatchResourceAttr(computeInstance, "boot_volume.volume", regexp.MustCompile("^"+zone+"/volumes/vol-boot-")),
					resource.TestCheckResourceAttr(computeInstance, "state", "RUNNING"),
					resource.TestCheckResourceAttr(computeInstance, "create_time", "2026-11-01T12:00:00Z"),
					resource.TestCheckResourceAttr(computeInstance, "health.availability", "AVAILABLE"),
					resource.TestCheckResourceAttr(computeInstance, "health.checks.0.change_time", "2026-11-01T12:00:00Z"),
					resource.TestCheckResourceAttr(computeInstance, "health.checks.1.outcome", "INSUFFICIENT_DATA"),
					resource.TestCheckNoResourceAttr(computeInstance, "health.checks.1.change_time"),
					resource.TestCheckResourceAttr(computeInstance, "interface_addresses.0.network_interface", computeNetworkInterface),
					resource.TestMatchResourceAttr(computeInstance, "interface_addresses.0.ipv4_address", regexp.MustCompile(`^10\.0\.0\.`)),
					resource.TestCheckResourceAttr(computeInstance, "tags.env", "test"),
					func(s *terraform.State) error {
						first = s.RootModule().Resources[computeInstance].Primary.ID
						return nil
					},
				),
			},
			// Updated in place: the tags change.
			{
				Config: computeConfig(fake, `instance_type = "instanceTypes/gp1.core.2"
  image_family  = "imageFamilies/ubuntu-24-04-minimal"
  tags          = { env = "prod" }`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(computeInstance, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(computeInstance, "tags.env", "prod"),
			},
			// A new type needs the instance stopped, which Terraform
			// doesn't do: the API's refusal reaches the user.
			{
				Config: computeConfig(fake, `instance_type = "instanceTypes/gp1.core.4"
  image_family  = "imageFamilies/ubuntu-24-04-minimal"
  tags          = { env = "prod" }`, ""),
				ExpectError: regexp.MustCompile(`a new instance_type needs the instance\s+STOPPED`),
			},
			// Stopped outside Terraform, it changes type in place.
			{
				PreConfig: func() { fake.stop(first) },
				Config: computeConfig(fake, `instance_type = "instanceTypes/gp1.core.4"
  image_family  = "imageFamilies/ubuntu-24-04-minimal"
  tags          = { env = "prod" }`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(computeInstance, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(computeInstance, "instance_type", "instanceTypes/gp1.core.4"),
					resource.TestCheckResourceAttr(computeInstance, "state", "STOPPED"),
					resource.TestCheckResourceAttrPtr(computeInstance, "id", &first),
				),
			},
			// An immutable field replaces it.
			{
				Config: computeConfig(fake, `instance_type = "instanceTypes/gp1.core.4"
  image_family  = "imageFamilies/ubuntu-24-04-minimal"
  login_user    = "admin"
  tags          = { env = "prod" }`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(computeInstance, plancheck.ResourceActionReplace),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(computeInstance, "login_user", "admin"),
					resource.TestCheckResourceAttr(computeInstance, "state", "RUNNING"),
					func(s *terraform.State) error {
						if id := s.RootModule().Resources[computeInstance].Primary.ID; id == first {
							return fmt.Errorf("%s wasn't replaced", id)
						}
						return nil
					},
				),
			},
			// Adopted by name.
			{
				ResourceName:      computeInstance,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:  computeInstance,
				ImportState:   true,
				ImportStateId: "i-1",
				ExpectError:   regexp.MustCompile(`"i-1" isn't a name of the form\s+projects/\{project\}/zones/\{zone\}/instances/\{instance\}`),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for name, i := range fake.instances {
				if i.GetState() != computev1beta1.Instance_TERMINATED {
					return fmt.Errorf("%s is %s", name, i.GetState())
				}
			}
			if len(fake.keyPairs) > 0 {
				return fmt.Errorf("key pairs left: %v", fake.keyPairs)
			}
			return nil
		},
	})
}

func TestKeyPairsAreImportedOrGenerated(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeCompute(t)
	const publicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIImported laptop"
	config := func(tags string) string {
		return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_compute_key_pair" "imported" {
  project    = "p-1"
  public_key = %q
}

resource "origincloud_compute_key_pair" "generated" {
  project = "p-1"
  tags    = %s
}
`, fake.URL, publicKey, tags)
	}
	generated := "origincloud_compute_key_pair.generated"
	privateKey := func(s *terraform.State) error {
		name := s.RootModule().Resources[generated].Primary.ID
		fake.mu.Lock()
		want := fake.privateKeys[name]
		fake.mu.Unlock()
		return resource.TestCheckResourceAttr(generated, "private_key", want)(s)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// The imported key is kept as it is, without a private key;
			// the generated one's private key, which the API answers with
			// only now, goes into the state as sensitive.
			{
				Config: config(`{ "origin:name" = "ci" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("origincloud_compute_key_pair.imported", "name", regexp.MustCompile(`^projects/p-1/keyPairs/kp-[0-9]+$`)),
					resource.TestCheckResourceAttr("origincloud_compute_key_pair.imported", "public_key", publicKey),
					resource.TestCheckNoResourceAttr("origincloud_compute_key_pair.imported", "private_key"),
					resource.TestMatchResourceAttr(generated, "public_key", regexp.MustCompile(`^ssh-ed25519 AAAA\S+ ci$`)),
					resource.TestMatchResourceAttr(generated, "private_key", regexp.MustCompile(`^-----BEGIN OPENSSH PRIVATE KEY-----\n`)),
					privateKey,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectSensitiveValue(generated, tfjsonpath.New("private_key")),
				},
			},
			// Refreshed and updated, the private key stays, though no
			// read answers with it.
			{
				Config: config(`{ "origin:name" = "ci", env = "test" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(generated, plancheck.ResourceActionUpdate),
					plancheck.ExpectKnownValue(generated, tfjsonpath.New("private_key"), knownvalue.NotNull()),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(generated, "tags.env", "test"),
					privateKey,
				),
			},
			// A plan that would output it unmarked is refused.
			{
				Config: config(`{ "origin:name" = "ci", env = "test" }`) + `
output "key" {
  value = origincloud_compute_key_pair.generated.private_key
}
`,
				ExpectError: regexp.MustCompile(`Output refers to sensitive values`),
			},
			// Imported, a generated key pair has no private key.
			{
				Config:                  config(`{ "origin:name" = "ci", env = "test" }`),
				ResourceName:            generated,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"private_key"},
			},
			{
				Config:       config(`{ "origin:name" = "ci", env = "test" }`),
				ResourceName: generated,
				ImportState:  true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if key := states[0].Attributes["private_key"]; key != "" {
						return fmt.Errorf("an imported key pair has the private key %q", key)
					}
					return nil
				},
			},
			// A new public key replaces the key pair.
			{
				Config: fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_compute_key_pair" "imported" {
  project    = "p-1"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOther laptop"
}
`, fake.URL),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("origincloud_compute_key_pair.imported", plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction(generated, plancheck.ResourceActionDestroy),
				}},
			},
		},
	})
}

func TestAVolumeIsAttachedToAnInstance(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeCompute(t)
	instance := `instance_type = "instanceTypes/gp1.core.2"
  image         = "zones/eu-central1-az1/images/ubuntu-24-04-v1"`
	attachment := func(device string) string {
		return fmt.Sprintf(`
resource "origincloud_compute_volume_attachment" "data" {
  project  = "p-1"
  zone     = "eu-central1-az1"
  instance = basename(origincloud_compute_instance.web.name)
  volume   = %q
  %s
}

data "origincloud_compute_volume_attachments" "web" {
  project    = "p-1"
  zone       = "eu-central1-az1"
  instance   = basename(origincloud_compute_instance.web.name)
  depends_on = [origincloud_compute_volume_attachment.data]
}

data "origincloud_compute_volume_attachments" "zone" {
  project    = "p-1"
  zone       = "eu-central1-az1"
  instance   = "-"
  depends_on = [origincloud_compute_volume_attachment.data]
}
`, zone+"/volumes/vol-data", device)
	}
	data := "origincloud_compute_volume_attachment.data"
	var detached string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Attached on the first free device; listed after the boot
			// volume, for the instance and for the zone.
			{
				Config: computeConfig(fake, instance, attachment("")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(data, "name", regexp.MustCompile("^"+zone+"/instances/i-[0-9]+/volumeAttachments/att-[0-9]+$")),
					resource.TestCheckResourceAttr(data, "device", "vdb"),
					resource.TestCheckResourceAttr(data, "boot", "false"),
					resource.TestCheckResourceAttr(data, "state", "ATTACHED"),
					resource.TestCheckResourceAttr(computeInstance, "image_family", ""),
					resource.TestCheckResourceAttr("data.origincloud_compute_volume_attachments.web", "volume_attachments.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_compute_volume_attachments.web", "volume_attachments.0.boot", "true"),
					resource.TestCheckResourceAttrPair("data.origincloud_compute_volume_attachments.web", "volume_attachments.0.volume", computeInstance, "boot_volume.volume"),
					resource.TestCheckResourceAttrPair("data.origincloud_compute_volume_attachments.web", "volume_attachments.1.name", data, "name"),
					resource.TestCheckResourceAttr("data.origincloud_compute_volume_attachments.zone", "volume_attachments.#", "2"),
					func(s *terraform.State) error {
						detached = s.RootModule().Resources[data].Primary.ID
						return nil
					},
				),
			},
			// Another device replaces the attachment, not the instance.
			{
				Config: computeConfig(fake, instance, attachment(`device = "vdd"`)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(data, plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction(computeInstance, plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(data, "device", "vdd"),
					func(*terraform.State) error {
						if !slices.Contains(fake.mutations(), "DELETE "+detached) {
							return fmt.Errorf("%s wasn't detached: %v", detached, fake.mutations())
						}
						return nil
					},
				),
			},
			{
				ResourceName:      data,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Detached, the instance stays.
			{
				Config: computeConfig(fake, instance, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(data, plancheck.ResourceActionDestroy),
					plancheck.ExpectResourceAction(computeInstance, plancheck.ResourceActionNoop),
				}},
			},
		},
	})
}

func TestComputeListAndCatalogDataSourcesReadEveryPage(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeCompute(t)
	resources := func(more string) string {
		return computeConfig(fake, `instance_type = "instanceTypes/gp1.core.2"
  image_family  = "imageFamilies/ubuntu-24-04-minimal"`, `
resource "origincloud_compute_image" "golden" {
  project     = "p-1"
  zone        = "eu-central1-az1"
  description = "Golden image"
  root_device = "vda"
  block_devices = [
    { device = "vda", size_gib = 20, source_snapshot = "`+zone+`/snapshots/snap-1" },
    { device = "vdb", size_gib = 5 },
  ]
}

# Launched from the project's own image, after the first.
resource "origincloud_compute_instance" "golden" {
  project            = "p-1"
  zone               = "eu-central1-az1"
  instance_type      = "instanceTypes/gp1.core.4"
  image              = origincloud_compute_image.golden.name
  key_pair           = origincloud_compute_key_pair.admin.name
  network_interfaces = ["`+zone+`/networkInterfaces/eni-2"]
  depends_on         = [origincloud_compute_instance.web]
}
`+more)
	}
	terminated := `
resource "origincloud_compute_instance" "gone" {
  project            = "p-1"
  zone               = "eu-central1-az1"
  instance_type      = "instanceTypes/gp1.core.2"
  image_family       = "imageFamilies/ubuntu-24-04-minimal"
  key_pair           = origincloud_compute_key_pair.admin.name
  network_interfaces = ["` + zone + `/networkInterfaces/eni-3"]
}
`
	lists := resources(`
data "origincloud_compute_instances" "az1" {
  project = "p-1"
  zone    = "eu-central1-az1"
}

data "origincloud_compute_instances" "az2" {
  project = "p-1"
  zone    = "eu-central1-az2"
}

data "origincloud_compute_key_pairs" "p1" {
  project = "p-1"
}

data "origincloud_compute_images" "az1" {
  project = "p-1"
  zone    = "eu-central1-az1"
}

data "origincloud_compute_public_images" "az1" {
  zone = "eu-central1-az1"
}

data "origincloud_compute_public_image" "v2" {
  name = "zones/eu-central1-az1/images/ubuntu-24-04-v2"
}

data "origincloud_compute_instance_types" "all" {}

data "origincloud_compute_instance_type" "core4" {
  name = "instanceTypes/gp1.core.4"
}

data "origincloud_compute_image_families" "all" {}

data "origincloud_compute_image_family" "noble" {
  name = "imageFamilies/ubuntu-24-04-minimal"
}
`)
	instances := "data.origincloud_compute_instances.az1"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: resources(terminated),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("origincloud_compute_image.golden", "default_user", "cloud"),
					resource.TestCheckResourceAttr("origincloud_compute_image.golden", "architecture", "X86_64"),
					resource.TestCheckResourceAttr("origincloud_compute_instance.golden", "login_user", "cloud"),
					resource.TestCheckResourceAttr("origincloud_compute_instance.golden", "boot_volume.size_gib", "20"),
				),
			},
			// The third instance is terminated, which the API still lists
			// and the data source leaves out.
			{Config: resources("")},
			// The fake answers one item per page: every page is read.
			{
				Config: lists,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(instances, "instances.#", "2"),
					resource.TestCheckResourceAttrPair(instances, "instances.0.name", computeInstance, "name"),
					resource.TestCheckResourceAttrPair(instances, "instances.1.name", "origincloud_compute_instance.golden", "name"),
					resource.TestCheckResourceAttrPair(instances, "instances.1.image", "origincloud_compute_image.golden", "name"),
					resource.TestCheckResourceAttr(instances, "instances.0.boot_volume.size_gib", "10"),
					resource.TestCheckResourceAttr(instances, "instances.0.health.checks.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_compute_key_pairs.p1", "key_pairs.#", "1"),
					resource.TestCheckResourceAttrPair("data.origincloud_compute_key_pairs.p1", "key_pairs.0.public_key", "origincloud_compute_key_pair.admin", "public_key"),
					resource.TestCheckResourceAttr("data.origincloud_compute_images.az1", "images.#", "1"),
					resource.TestCheckResourceAttr("data.origincloud_compute_images.az1", "images.0.block_devices.0.source_snapshot", zone+"/snapshots/snap-1"),
					resource.TestCheckResourceAttr("data.origincloud_compute_images.az1", "images.0.block_devices.1.size_gib", "5"),
					resource.TestCheckResourceAttr("data.origincloud_compute_public_images.az1", "public_images.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_compute_public_images.az1", "public_images.0.family_current", "false"),
					resource.TestCheckResourceAttr("data.origincloud_compute_public_images.az1", "public_images.1.name", "zones/eu-central1-az1/images/ubuntu-24-04-v2"),
					resource.TestCheckResourceAttr("data.origincloud_compute_public_image.v2", "family_current", "true"),
					resource.TestCheckResourceAttr("data.origincloud_compute_public_image.v2", "default_user", "ubuntu"),
					resource.TestCheckResourceAttr("data.origincloud_compute_public_image.v2", "block_devices.0.size_gib", "10"),
					resource.TestCheckResourceAttr("data.origincloud_compute_instance_types.all", "instance_types.#", "3"),
					resource.TestCheckResourceAttr("data.origincloud_compute_instance_types.all", "instance_types.2.available", "false"),
					resource.TestCheckResourceAttr("data.origincloud_compute_instance_type.core4", "vcpu", "4"),
					resource.TestCheckResourceAttr("data.origincloud_compute_instance_type.core4", "memory_mib", "16384"),
					resource.TestCheckResourceAttr("data.origincloud_compute_instance_type.core4", "storage_burst_bandwidth_mbit", "4000"),
					resource.TestCheckResourceAttr("data.origincloud_compute_image_families.all", "image_families.#", "2"),
					resource.TestCheckNoResourceAttr("data.origincloud_compute_image_families.all", "image_families.1.support_end_time"),
					resource.TestCheckResourceAttr("data.origincloud_compute_image_family.noble", "support_end_time", "2029-05-31T00:00:00Z"),
					resource.TestCheckResourceAttr("data.origincloud_compute_image_family.noble", "vendor", "Canonical"),
				),
				// A zone without instances lists none: an empty list, not null.
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.origincloud_compute_instances.az2", tfjsonpath.New("instances"), knownvalue.ListSizeExact(0)),
				},
			},
		},
	})
}
