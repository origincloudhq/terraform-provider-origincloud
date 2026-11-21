package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const (
	launchTemplate = "origincloud_compute_launch_template.web"
	templateType2  = "//compute.origincloudapis.eu/instanceTypes/gp1.core.2"
	templateType4  = "//compute.origincloudapis.eu/instanceTypes/gp1.core.4"
)

// templateConfig returns a configuration with a key pair and a launch
// template of the instance type, its arguments as extra and its recipe's
// tags, then more.
func templateConfig(fake *fakeCompute, instanceType, extra, more string) string {
	return templateConfigTagged(fake, instanceType, "", extra, more)
}

func templateConfigTagged(fake *fakeCompute, instanceType, recipeTags, extra, more string) string {
	return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_compute_key_pair" "admin" {
  project = "p-1"
}

resource "origincloud_compute_launch_template" "web" {
  project = "p-1"
  launch_template_data = {
    instance_type = %q
    image_family  = "//compute.origincloudapis.eu/imageFamilies/ubuntu-24-04-minimal"
    key_pair      = origincloud_compute_key_pair.admin.id
    %s
  }
  %s
}
%s`, fake.URL, instanceType, recipeTags, extra, more)
}

// templateCalls returns the template's mutations the fake received.
func templateCalls(fake *fakeCompute) []string {
	var calls []string
	for _, call := range fake.mutations() {
		if strings.Contains(call, "launchTemplates") {
			calls = append(calls, call)
		}
	}
	return calls
}

func versionCount(fake *fakeCompute) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	n := 0
	for _, t := range fake.templates {
		n += len(t.versions)
	}
	return n
}

// A template's versions never change: a change to its recipe adds one, and
// the template's default version follows it unless the configuration sets
// it.
func TestALaunchTemplateAddsAVersionWhenItsRecipeChanges(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeCompute(t)
	name := regexp.MustCompile(`^projects/p-1/launchTemplates/lt-[0-9]+$`)
	var calls []string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created: version 1, which is the default.
			{
				Config: templateConfig(fake, templateType2, `tags = { env = "test" }`, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(launchTemplate, "name", name),
					checkFullName(launchTemplate),
					resource.TestCheckResourceAttr(launchTemplate, "default_version", "1"),
					resource.TestCheckResourceAttr(launchTemplate, "latest_version", "1"),
					resource.TestCheckResourceAttr(launchTemplate, "version", "1"),
					resource.TestCheckResourceAttr(launchTemplate, "creator", "user:test"),
					resource.TestCheckResourceAttr(launchTemplate, "create_time", "2026-11-01T12:00:00Z"),
					resource.TestCheckResourceAttr(launchTemplate, "version_create_time", "2026-11-01T12:01:00Z"),
					resource.TestCheckResourceAttr(launchTemplate, "tags.env", "test"),
					resource.TestCheckResourceAttr(launchTemplate, "launch_template_data.instance_type", templateType2),
					resource.TestCheckResourceAttrPair(launchTemplate, "launch_template_data.key_pair", "origincloud_compute_key_pair.admin", "id"),
				),
			},
			// Read again, it plans nothing.
			{
				Config:             templateConfig(fake, templateType2, `tags = { env = "test" }`, ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			// A changed recipe is a new version, in place, and the default
			// follows it.
			{
				PreConfig: func() { calls = templateCalls(fake) },
				Config:    templateConfig(fake, templateType4, `tags = { env = "test" }`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(launchTemplate, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(launchTemplate, "launch_template_data.instance_type", templateType4),
					resource.TestCheckResourceAttr(launchTemplate, "default_version", "2"),
					resource.TestCheckResourceAttr(launchTemplate, "latest_version", "2"),
					resource.TestCheckResourceAttr(launchTemplate, "version", "2"),
					resource.TestCheckResourceAttr(launchTemplate, "version_create_time", "2026-11-01T12:02:00Z"),
					resource.TestMatchResourceAttr(launchTemplate, "name", name),
					func(*terraform.State) error {
						// The name stays the template's, without the version
						// the API names it with after createVersion.
						added := templateCalls(fake)[len(calls):]
						if len(added) != 2 || !strings.HasSuffix(added[0], ":createVersion") || !strings.HasPrefix(added[1], "PATCH ") {
							return fmt.Errorf("calls %v", added)
						}
						if n := versionCount(fake); n != 2 {
							return fmt.Errorf("%d versions", n)
						}
						return nil
					},
				),
			},
			// The tags change the template, not a version.
			{
				Config: templateConfig(fake, templateType4, `tags = { env = "prod" }`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(launchTemplate, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(launchTemplate, "tags.env", "prod"),
					resource.TestCheckResourceAttr(launchTemplate, "latest_version", "2"),
					func(*terraform.State) error {
						if n := versionCount(fake); n != 2 {
							return fmt.Errorf("%d versions", n)
						}
						return nil
					},
				),
			},
			// Pinned to version 1, the default stays there while a third
			// version is added.
			{
				Config: templateConfig(fake, templateType2, `default_version = 1
  tags            = { env = "prod" }`, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(launchTemplate, "default_version", "1"),
					resource.TestCheckResourceAttr(launchTemplate, "latest_version", "3"),
					resource.TestCheckResourceAttr(launchTemplate, "launch_template_data.instance_type", templateType2),
				),
			},
			// Left out again, the default keeps its value until the recipe
			// changes.
			{
				Config:   templateConfig(fake, templateType2, `tags = { env = "prod" }`, ""),
				PlanOnly: true,
			},
			{
				Config: templateConfig(fake, templateType4, `tags = { env = "prod" }`, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(launchTemplate, "default_version", "4"),
					resource.TestCheckResourceAttr(launchTemplate, "latest_version", "4"),
				),
			},
			// A default that isn't a version the template has is refused.
			{
				Config:      templateConfig(fake, templateType4, `default_version = 9`, ""),
				ExpectError: regexp.MustCompile(`has no\s+version 9`),
			},
			// Adopted by its full name: it is read at its newest version.
			{
				Config:            templateConfig(fake, templateType4, `tags = { env = "prod" }`, ""),
				ResourceName:      launchTemplate,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if n := versionCount(fake); n != 0 {
				return fmt.Errorf("%d versions left", n)
			}
			return nil
		},
	})
}

func TestALaunchTemplateWithoutAnImageIsRefused(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeCompute(t)
	config := strings.Replace(templateConfig(fake, templateType2, "", ""),
		`image_family  = "//compute.origincloudapis.eu/imageFamilies/ubuntu-24-04-minimal"`, "", 1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps:                    []resource.TestStep{{Config: config, ExpectError: regexp.MustCompile(`set exactly one of image and\s+image_family`)}},
	})
}

// templateInstances returns a configuration with a template of the instance
// type and instances launched from it: one whose body is launch, and one
// that overrides the version's instance type and tags.
func templateInstances(fake *fakeCompute, instanceType, launch string) string {
	return templateConfigTagged(fake, instanceType, `tags = { env = "template" }`, "", fmt.Sprintf(`
resource "origincloud_compute_instance" "web" {
  project            = "p-1"
  zone               = "eu-central1-az1"
  network_interfaces = [%q]
  %s
}

resource "origincloud_compute_instance" "override" {
  project            = "p-1"
  zone               = "eu-central1-az1"
  launch_template    = origincloud_compute_launch_template.web.id
  network_interfaces = [%q]
  instance_type      = %q
  tags               = { env = "override" }
  depends_on         = [origincloud_compute_instance.web]
}
`, computeNetworkInterface, launch, full("network", zone+"/networkInterfaces/eni-2"), templateType2))
}

// An instance is launched from a version of a template, named or as its
// default, with the arguments it sets overriding the version's.
func TestAnInstanceIsLaunchedFromALaunchTemplate(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeCompute(t)
	const override = "origincloud_compute_instance.override"
	var first, second string
	launchedFrom := func(instance, version string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			name := s.RootModule().Resources[instance].Primary.Attributes["name"]
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if got := fake.instances[name].GetLaunchTemplate(); !strings.HasSuffix(got, "@"+version) {
				return fmt.Errorf("%s was launched from %s, not version %s", name, got, version)
			}
			return nil
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// From version 1 by name: the instance type, the image and the
			// tags, which the configuration doesn't set, are the version's.
			{
				Config: templateInstances(fake, templateType2, `launch_template = "${origincloud_compute_launch_template.web.id}@1"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					launchedFrom(computeInstance, "1"),
					resource.TestCheckResourceAttr(computeInstance, "state", "RUNNING"),
					resource.TestCheckResourceAttr(computeInstance, "instance_type", templateType2),
					resource.TestCheckResourceAttr(computeInstance, "image", full("compute", "zones/eu-central1-az1/images/ubuntu-24-04-v2")),
					resource.TestCheckResourceAttr(computeInstance, "login_user", "ubuntu"),
					resource.TestCheckResourceAttrPair(computeInstance, "key_pair", "origincloud_compute_key_pair.admin", "id"),
					resource.TestCheckResourceAttr(computeInstance, "tags.env", "template"),
					// What was configured is kept: the version's number is
					// the one named.
					resource.TestMatchResourceAttr(computeInstance, "launch_template", regexp.MustCompile(`^//compute\.origincloudapis\.eu/projects/p-1/launchTemplates/lt-[0-9]+@1$`)),
					func(s *terraform.State) error {
						first = s.RootModule().Resources[computeInstance].Primary.Attributes["name"]
						return nil
					},
					// From the default, with its own arguments over the
					// version's.
					launchedFrom(override, "1"),
					resource.TestCheckResourceAttr(override, "instance_type", templateType2),
					resource.TestCheckResourceAttr(override, "tags.env", "override"),
					resource.TestCheckResourceAttrPair(override, "launch_template", launchTemplate, "id"),
					resource.TestCheckResourceAttrPair(override, "key_pair", "origincloud_compute_key_pair.admin", "id"),
				),
			},
			{
				Config:   templateInstances(fake, templateType2, `launch_template = "${origincloud_compute_launch_template.web.id}@1"`),
				PlanOnly: true,
			},
			// The template gains a version, which changes no instance
			// launched from it before.
			{
				Config: templateInstances(fake, templateType4, `launch_template = "${origincloud_compute_launch_template.web.id}@1"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(launchTemplate, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(computeInstance, plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction(override, plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(computeInstance, "name", &first),
					resource.TestCheckResourceAttr(computeInstance, "instance_type", templateType2),
					launchedFrom(override, "1"),
				),
			},
			// Another version number replaces the instance, which is
			// launched from it: the template's own type.
			{
				Config: templateInstances(fake, templateType4, `launch_template = "${origincloud_compute_launch_template.web.id}@2"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(computeInstance, plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction(override, plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					launchedFrom(computeInstance, "2"),
					resource.TestCheckResourceAttr(computeInstance, "instance_type", templateType4),
					resource.TestCheckResourceAttr(override, "instance_type", templateType2),
					func(s *terraform.State) error {
						second = s.RootModule().Resources[computeInstance].Primary.Attributes["name"]
						if second == first {
							return fmt.Errorf("%s wasn't replaced", second)
						}
						return nil
					},
				),
			},
			// The default's name is the version's, written another way: the
			// default is version 2 now, so the instance isn't replaced, and
			// the state takes what was written.
			{
				Config: templateInstances(fake, templateType4, `launch_template = origincloud_compute_launch_template.web.id`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(computeInstance, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(computeInstance, "name", &second),
					resource.TestCheckResourceAttrPair(computeInstance, "launch_template", launchTemplate, "id"),
					launchedFrom(computeInstance, "2"),
				),
			},
			{
				Config:   templateInstances(fake, templateType4, `launch_template = origincloud_compute_launch_template.web.id`),
				PlanOnly: true,
			},
			// Adopted: the API answers with the version.
			{
				ResourceName:            override,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"launch_template"},
			},
			{
				Config:      templateInstances(fake, templateType4, `launch_template = "${origincloud_compute_launch_template.web.id}@9"`),
				ExpectError: regexp.MustCompile(`has no\s+version 9`),
			},
		},
	})
}

func TestAnInstanceNeedsItsArgumentsUnlessATemplateSuppliesThem(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeCompute(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config:      computeConfig(fake, `image_family = "//compute.origincloudapis.eu/imageFamilies/ubuntu-24-04-minimal"`, ""),
			ExpectError: regexp.MustCompile(`instance_type is required unless\s+launch_template\s+is\s+set`),
		}},
	})
}

// The data sources read a template at any of its versions, and list its
// versions, newest first, and the project's templates by their default.
func TestLaunchTemplateDataSourcesReadVersions(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeCompute(t)
	more := `
data "origincloud_compute_launch_template" "default" {
  name = origincloud_compute_launch_template.web.name
}

data "origincloud_compute_launch_template" "first" {
  name = "${origincloud_compute_launch_template.web.name}@1"
}

data "origincloud_compute_launch_template" "latest" {
  name = "${origincloud_compute_launch_template.web.name}@latest"
}

data "origincloud_compute_launch_template_versions" "web" {
  launch_template = origincloud_compute_launch_template.web.id
}

data "origincloud_compute_launch_templates" "all" {
  project = "p-1"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: templateConfig(fake, templateType2, "", "")},
			{
				Config: templateConfig(fake, templateType4, `default_version = 1`, more),
				Check: resource.ComposeAggregateTestCheckFunc(
					// The default is version 1, the newest 2.
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_template.default", "version", "1"),
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_template.default", "launch_template_data.instance_type", templateType2),
					resource.TestCheckResourceAttrPair("data.origincloud_compute_launch_template.default", "id", launchTemplate, "id"),
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_template.first", "version", "1"),
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_template.latest", "version", "2"),
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_template.latest", "launch_template_data.instance_type", templateType4),
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_template.latest", "default_version", "1"),
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_template_versions.web", "versions.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_template_versions.web", "versions.0.version", "2"),
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_template_versions.web", "versions.1.version", "1"),
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_template_versions.web", "versions.0.launch_template_data.instance_type", templateType4),
					resource.TestCheckResourceAttrPair("data.origincloud_compute_launch_template_versions.web", "versions.0.id", launchTemplate, "id"),
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_templates.all", "launch_templates.#", "1"),
					resource.TestCheckResourceAttr("data.origincloud_compute_launch_templates.all", "launch_templates.0.version", "1"),
				),
			},
		},
	})
}
