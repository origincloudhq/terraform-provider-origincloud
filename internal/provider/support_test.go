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

// Cases are opened and answered outside Terraform: they are only read.
func TestSupportDataSourcesReadCasesAndTheirMessages(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeSupport(t)
	config := fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

data "origincloud_support_case" "unreachable" {
  name = "organizations/acme/cases/c-1"
}

data "origincloud_support_case_message" "answer" {
  name = "organizations/acme/cases/c-1/messages/m-2"
}

data "origincloud_support_cases" "acme" {
  organization = "acme"
}

data "origincloud_support_case_messages" "unreachable" {
  organization = "acme"
  case         = "c-1"
}

data "origincloud_support_case_messages" "invoice" {
  organization = "acme"
  case         = "c-2"
}

output "open" {
  value = [for c in data.origincloud_support_cases.acme.cases : c.subject if c.state != "CLOSED"]
}
`, fake.URL)
	unreachable := "data.origincloud_support_case.unreachable"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(unreachable, "project", "projects/acme-prod"),
					resource.TestCheckResourceAttr(unreachable, "state", "IN_PROGRESS"),
					resource.TestCheckResourceAttr(unreachable, "contact_email", "ops@acme.example"),
					resource.TestCheckResourceAttr(unreachable, "update_time", "2026-11-01T13:30:00Z"),
					resource.TestCheckResourceAttr("data.origincloud_support_case_message.answer", "author", "user:support-1"),
					resource.TestCheckResourceAttr("data.origincloud_support_case_message.answer", "body", "Its security group allows no SSH."),
					// Every page, newest first, and only acme's.
					resource.TestCheckResourceAttr("data.origincloud_support_cases.acme", "cases.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_support_cases.acme", "cases.0.subject", "Invoice address"),
					resource.TestCheckResourceAttr("data.origincloud_support_cases.acme", "cases.1.name", "organizations/acme/cases/c-1"),
					resource.TestCheckResourceAttr("data.origincloud_support_case_messages.unreachable", "case_messages.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_support_case_messages.unreachable", "case_messages.0.create_time", "2026-11-01T12:00:00Z"),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.origincloud_support_case_messages.invoice", tfjsonpath.New("case_messages"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownOutputValue("open", knownvalue.ListExact([]knownvalue.Check{
						knownvalue.StringExact("Invoice address"),
						knownvalue.StringExact("Instance unreachable"),
					})),
				},
			},
			{
				Config: fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

data "origincloud_support_case" "gone" {
  name = "organizations/acme/cases/c-9"
}
`, fake.URL),
				ExpectError: regexp.MustCompile(`organizations/acme/cases/c-9 doesn't exist`),
			},
		},
	})
}

func TestARegionalProviderCallsSupportAtItsGlobalHost(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	t.Setenv("ORIGINCLOUD_REGION", "")
	// Only this host answers: a support call to the region's front door
	// would fail.
	h := &hosts{servers: map[string]*httptest.Server{
		"support.origincloudapis.eu": newFakeSupport(t),
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

data "origincloud_support_cases" "acme" {
  organization = "acme"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.origincloud_support_cases.acme", "cases.#", "2"),
					func(*terraform.State) error {
						if want := "support.origincloudapis.eu /v1beta1/organizations/acme/cases"; !slices.Contains(h.calls(), want) {
							return fmt.Errorf("no call %q in %v", want, h.calls())
						}
						return nil
					},
				),
			},
		},
	})
}
