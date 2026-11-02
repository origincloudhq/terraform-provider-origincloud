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
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const (
	acmeOrganization = "origincloud_resourcemanager_organization.acme"
	prodProject      = "origincloud_resourcemanager_project.prod"
)

// resourceManagerConfig returns a configuration of the provider calling fake,
// with two organizations, the first named acme, and a project whose body
// is body, or none when body is empty.
func resourceManagerConfig(fake *fakeResourceManager, acme, body string) string {
	config := fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_resourcemanager_organization" "acme" {
  display_name = %q
}

resource "origincloud_resourcemanager_organization" "other" {
  display_name = "Other"
}
`, fake.URL, acme)
	if body != "" {
		config += fmt.Sprintf(`
resource "origincloud_resourcemanager_project" "prod" {
  %s
}
`, body)
	}
	return config
}

func TestProjectsAreCreatedWithTheirIdUpdatedImportedAndDeleted(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeResourceManager(t)
	prod := func(organization, id, name string) string {
		return fmt.Sprintf(`organization = origincloud_resourcemanager_organization.%s.name
  project_id   = %q
  display_name = %q`, organization, id, name)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created under the id chosen, which names it.
			{
				Config: resourceManagerConfig(fake, "Acme", prod("acme", "acme-prod", "Production")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(acmeOrganization, "name", regexp.MustCompile(`^organizations/org-[0-9]+$`)),
					resource.TestCheckResourceAttr(acmeOrganization, "state", "ACTIVE"),
					resource.TestCheckResourceAttr(acmeOrganization, "create_time", "2026-11-01T12:00:00Z"),
					resource.TestCheckResourceAttr(prodProject, "name", "projects/acme-prod"),
					resource.TestCheckResourceAttr(prodProject, "id", "projects/acme-prod"),
					resource.TestCheckResourceAttr(prodProject, "project_id", "acme-prod"),
					resource.TestCheckResourceAttrPair(prodProject, "organization", acmeOrganization, "name"),
					resource.TestCheckResourceAttr(prodProject, "creator", "user:test"),
				),
			},
			// Renamed in place, the organization too.
			{
				Config: resourceManagerConfig(fake, "Acme GmbH", prod("acme", "acme-prod", "Production EU")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(acmeOrganization, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(prodProject, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(acmeOrganization, "display_name", "Acme GmbH"),
					resource.TestCheckResourceAttr(prodProject, "display_name", "Production EU"),
				),
			},
			// Adopted by name, the id coming from it.
			{
				ResourceName:      prodProject,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      acmeOrganization,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:  prodProject,
				ImportState:   true,
				ImportStateId: "acme-prod",
				ExpectError:   regexp.MustCompile(`"acme-prod" isn't a name of the form\s+projects/\{project\}`),
			},
			// Moving it would replace it under the same id, which a
			// deleted project never gives up: refused at the plan.
			{
				Config:      resourceManagerConfig(fake, "Acme GmbH", prod("other", "acme-prod", "Production EU")),
				ExpectError: regexp.MustCompile(`choose a new\s+project_id`),
			},
			// With a new id it is replaced.
			{
				Config: resourceManagerConfig(fake, "Acme GmbH", prod("other", "acme-prod-2", "Production EU")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(prodProject, plancheck.ResourceActionReplace),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(prodProject, "name", "projects/acme-prod-2"),
					resource.TestCheckResourceAttrPair(prodProject, "organization", "origincloud_resourcemanager_organization.other", "name"),
				),
			},
			// The deleted project's id is refused by the API.
			{
				Config: resourceManagerConfig(fake, "Acme GmbH", prod("other", "acme-prod-2", "Production EU")) + `
resource "origincloud_resourcemanager_project" "again" {
  organization = origincloud_resourcemanager_organization.acme.name
  project_id   = "acme-prod"
  display_name = "Production again"
}
`,
				ExpectError: regexp.MustCompile(`CONFLICT: the project id acme-prod is used already`),
			},
			// A project that still holds resources isn't deleted: the
			// refusal, naming them, reaches the user, and it stays.
			{
				PreConfig:   func() { fake.hold("projects/acme-prod-2", "instances", "volumes") },
				Config:      resourceManagerConfig(fake, "Acme GmbH", ""),
				ExpectError: regexp.MustCompile(`still has resources; delete them\s+first, in\s+this\s+order:\s+instances,\s+volumes`),
			},
			// Once they are gone, it is deleted.
			{
				PreConfig: func() { fake.hold("projects/acme-prod-2") },
				Config:    resourceManagerConfig(fake, "Acme GmbH", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(prodProject, plancheck.ResourceActionDestroy),
				}},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			var deletes []string
			for _, call := range fake.mutations() {
				if strings.HasPrefix(call, "DELETE projects/") {
					deletes = append(deletes, call)
				}
			}
			want := []string{"DELETE projects/acme-prod", "DELETE projects/acme-prod-2", "DELETE projects/acme-prod-2"}
			if !slices.Equal(deletes, want) {
				return fmt.Errorf("the projects' deletes were %v, not %v", deletes, want)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.projects) > 0 || len(fake.organizations) > 0 {
				return fmt.Errorf("left: %v, %v", fake.projects, fake.organizations)
			}
			return nil
		},
	})
}

func TestQuotasAndResourceManagerListsAreRead(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeResourceManager(t)
	config := func(quota, more string) string {
		return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_resourcemanager_organization" "acme" {
  display_name = "Acme"
}

# Created after the first, so listed after it.
resource "origincloud_resourcemanager_organization" "empty" {
  display_name = "Empty"
  depends_on   = [origincloud_resourcemanager_organization.acme]
}

resource "origincloud_resourcemanager_project" "prod" {
  organization = origincloud_resourcemanager_organization.acme.name
  project_id   = "acme-prod"
  display_name = "Production"
}

resource "origincloud_resourcemanager_project" "dev" {
  organization = origincloud_resourcemanager_organization.acme.name
  project_id   = "acme-dev"
  display_name = "Development"
  depends_on   = [origincloud_resourcemanager_project.prod]
}

resource "origincloud_resourcemanager_project_quota" "prod" {
  project = origincloud_resourcemanager_project.prod.project_id
  %s
}
%s`, fake.URL, quota, more)
	}
	lists := `
data "origincloud_resourcemanager_organization_quota" "acme" {
  name       = "${origincloud_resourcemanager_organization.acme.name}/quota"
  depends_on = [origincloud_resourcemanager_project.dev]
}

data "origincloud_resourcemanager_organizations" "all" {
  depends_on = [origincloud_resourcemanager_organization.empty]
}

data "origincloud_resourcemanager_projects" "acme" {
  organization = origincloud_resourcemanager_organization.acme.name
  depends_on   = [origincloud_resourcemanager_project.dev]
}

data "origincloud_resourcemanager_projects" "empty" {
  organization = origincloud_resourcemanager_organization.empty.name
}
`
	quota := "origincloud_resourcemanager_project_quota.prod"
	orgQuota := "data.origincloud_resourcemanager_organization_quota.acme"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// The project's quota exists with it: creating it sets the
			// budget configured, and the usage is read.
			{
				Config: config(`max_vcpu = 8`, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(quota, "name", "projects/acme-prod/quota"),
					resource.TestCheckResourceAttr(quota, "max_vcpu", "8"),
					resource.TestCheckNoResourceAttr(quota, "max_instances"),
					resource.TestCheckResourceAttr(quota, "usage.vcpu", "2"),
				),
			},
			// The organization's quota sums its projects' usage; the lists
			// read one item per page, every page.
			{
				Config: config(`max_vcpu = 8`, lists),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(orgQuota, "max_vcpu", "64"),
					resource.TestCheckResourceAttr(orgQuota, "max_public_ips", "4"),
					resource.TestCheckResourceAttr(orgQuota, "usage.instances", "2"),
					resource.TestCheckResourceAttr(orgQuota, "usage.vcpu", "4"),
					resource.TestCheckResourceAttr(orgQuota, "usage.vpcs", "0"),
					resource.TestCheckResourceAttr("data.origincloud_resourcemanager_organizations.all", "organizations.#", "2"),
					resource.TestCheckResourceAttrPair("data.origincloud_resourcemanager_organizations.all", "organizations.1.name", "origincloud_resourcemanager_organization.empty", "name"),
					resource.TestCheckResourceAttr("data.origincloud_resourcemanager_organizations.all", "organizations.0.state", "ACTIVE"),
					resource.TestCheckResourceAttr("data.origincloud_resourcemanager_projects.acme", "projects.#", "2"),
					// Sorted by name in the fake: acme-dev, then acme-prod.
					resource.TestCheckResourceAttr("data.origincloud_resourcemanager_projects.acme", "projects.0.display_name", "Development"),
					resource.TestCheckResourceAttrPair("data.origincloud_resourcemanager_projects.acme", "projects.1.organization", acmeOrganization, "name"),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.origincloud_resourcemanager_projects.empty", tfjsonpath.New("projects"), knownvalue.ListSizeExact(0)),
				},
			},
			// Changed and added in place, then one is removed, which unsets it.
			{
				Config: config(`max_vcpu = 16
  max_instances = 10`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(quota, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(quota, "max_instances", "10"),
			},
			{
				Config: config(`max_instances = 10`, ""),
				Check:  resource.TestCheckNoResourceAttr(quota, "max_vcpu"),
			},
		},
		// Destroyed, the quota unsets what Terraform set, before the
		// project goes.
		CheckDestroy: func(*terraform.State) error {
			var got []string
			for _, call := range fake.mutations() {
				if strings.Contains(call, "projects/acme-prod") {
					got = append(got, call)
				}
			}
			want := []string{
				"PATCH projects/acme-prod/quota maxVcpu",
				"PATCH projects/acme-prod/quota maxInstances,maxVcpu",
				"PATCH projects/acme-prod/quota maxVcpu",
				"PATCH projects/acme-prod/quota maxInstances",
				"DELETE projects/acme-prod",
			}
			if !slices.Equal(got, want) {
				return fmt.Errorf("the project's mutations were %q, not %q", got, want)
			}
			return nil
		},
	})
}

func TestARegionalProviderCallsResourceManagerAtItsGlobalHost(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	t.Setenv("ORIGINCLOUD_REGION", "")
	// Only these hosts answer: a resource manager call to the region's
	// front door would fail.
	h := &hosts{servers: map[string]*httptest.Server{
		"resourcemanager.origincloudapis.eu":     newFakeResourceManager(t).Server,
		"compute.eu-central1.origincloudapis.eu": newFakeCompute(t).Server,
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

resource "origincloud_resourcemanager_organization" "acme" {
  display_name = "Acme"
}

resource "origincloud_resourcemanager_project" "prod" {
  organization = origincloud_resourcemanager_organization.acme.name
  project_id   = "acme-prod"
  display_name = "Production"
}

data "origincloud_compute_instance_type" "core2" {
  name = "instanceTypes/gp1.core.2"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(prodProject, "name", "projects/acme-prod"),
					resource.TestCheckResourceAttr("data.origincloud_compute_instance_type.core2", "vcpu", "2"),
				),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			for _, want := range []string{
				"resourcemanager.origincloudapis.eu /v1beta1/organizations",
				"resourcemanager.origincloudapis.eu /v1beta1/projects",
				"resourcemanager.origincloudapis.eu /v1beta1/projects/acme-prod",
				"compute.eu-central1.origincloudapis.eu /v1beta1/instanceTypes/gp1.core.2",
			} {
				if !slices.Contains(h.calls(), want) {
					return fmt.Errorf("no call %q in %v", want, h.calls())
				}
			}
			for _, call := range h.calls() {
				if strings.HasPrefix(call, "resourcemanager.eu-central1.") {
					return fmt.Errorf("the resource manager was called at the region's host: %s", call)
				}
			}
			return nil
		},
	})
}
