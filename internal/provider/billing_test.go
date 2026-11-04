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
)

const (
	researchAccount = "origincloud_billing_billing_account.research"
	researchLimit   = "origincloud_billing_spend_limit.research"
	prodBillingInfo = "origincloud_billing_project_billing_info.prod"
	// The name the fake gives the first account created.
	researchName = "organizations/acme/billingAccounts/ba-2"
)

// billingConfig returns a configuration of the provider calling fake, with
// the project acme-prod paid for by payer and, unless account is empty, a
// billing account whose body is account, with a spend limit whose body is
// limit unless that is empty, and more after them.
func billingConfig(fake *fakeBilling, payer, account, limit, more string) string {
	config := fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
}

resource "origincloud_billing_project_billing_info" "prod" {
  project         = "acme-prod"
  billing_account = %s
}
`, fake.URL, payer)
	if account != "" {
		config += fmt.Sprintf(`
resource "origincloud_billing_billing_account" "research" {
  organization   = "acme"
  invoice_email  = "invoices@acme.example"
  contact_person = "Ada Lovelace"
  %s
}
`, account)
	}
	if limit != "" {
		config += fmt.Sprintf(`
resource "origincloud_billing_spend_limit" "research" {
  organization    = "acme"
  billing_account = element(split("/", origincloud_billing_billing_account.research.name), 3)
  %s
}
`, limit)
	}
	return config + more
}

func TestBillingAccountsSpendLimitsAndProjectBillingAreManaged(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	fake := newFakeBilling(t)
	account := func(name, country, more string) string {
		return fmt.Sprintf(`display_name   = %q
  billing_address = {
    street      = "Hauptstraße 1"
    city        = "Berlin"
    postal_code = "10115"
    region_code = %q
  }
  %s`, name, country, more)
	}
	limit := `limit                    = "500.00"
  alert_thresholds_percent = [80, 100]
  notification_emails      = ["finance@acme.example"]`
	lists := `
data "origincloud_billing_billing_accounts" "acme" {
  organization = "acme"
  depends_on   = [origincloud_billing_billing_account.research]
}

data "origincloud_billing_billing_countries" "all" {}

data "origincloud_billing_billing_country" "at" {
  name = "billingCountries/AT"
}
`
	research := researchAccount + ".name"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Created with a whole profile; the spend limit, which exists
			// with the account, is set; the project is linked to the
			// account. The lists read every page.
			{
				Config: billingConfig(fake, research, account("Research", "DE", ""), limit, lists),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(researchAccount, "name", researchName),
					resource.TestCheckResourceAttr(researchAccount, "organization", "acme"),
					resource.TestCheckResourceAttr(researchAccount, "default_account", "false"),
					resource.TestCheckResourceAttr(researchAccount, "payment_method.state", "MISSING"),
					resource.TestCheckResourceAttr(researchAccount, "billing_address.region_code", "DE"),
					resource.TestCheckResourceAttr(researchAccount, "creator", "user:test"),
					resource.TestCheckNoResourceAttr(researchAccount, "vat_id"),
					resource.TestCheckResourceAttr(researchLimit, "name", researchName+"/spendLimit"),
					resource.TestCheckResourceAttr(researchLimit, "limit", "500.00"),
					resource.TestCheckResourceAttr(researchLimit, "alert_thresholds_percent.1", "100"),
					resource.TestCheckResourceAttr(researchLimit, "update_time", "2026-11-02T09:00:00Z"),
					resource.TestCheckResourceAttr(prodBillingInfo, "name", "projects/acme-prod/billingInfo"),
					resource.TestCheckResourceAttr(prodBillingInfo, "billing_account", researchName),
					resource.TestCheckResourceAttr("data.origincloud_billing_billing_accounts.acme", "billing_accounts.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_billing_billing_accounts.acme", "billing_accounts.0.default_account", "true"),
					resource.TestCheckResourceAttr("data.origincloud_billing_billing_accounts.acme", "billing_accounts.0.payment_method.state", "NOT_REQUIRED"),
					resource.TestCheckResourceAttr("data.origincloud_billing_billing_accounts.acme", "billing_accounts.1.display_name", "Research"),
					resource.TestCheckResourceAttr("data.origincloud_billing_billing_countries.all", "billing_countries.#", "2"),
					resource.TestCheckResourceAttr("data.origincloud_billing_billing_countries.all", "billing_countries.0.region_code", "DE"),
					resource.TestCheckResourceAttr("data.origincloud_billing_billing_country.at", "vat_id_required", "true"),
				),
			},
			// An address outside Germany without a VAT id is refused, and
			// the account is left as it was.
			{
				Config:      billingConfig(fake, research, account("Research", "AT", ""), limit, ""),
				ExpectError: regexp.MustCompile(`a billing address outside Germany needs a\s+vat_id`),
			},
			// With one it is updated in place, renamed too.
			{
				Config: billingConfig(fake, research, account("Research GmbH", "AT", `vat_id = "ATU12345678"`), limit, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(researchAccount, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(researchLimit, plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(researchAccount, "display_name", "Research GmbH"),
					resource.TestCheckResourceAttr(researchAccount, "vat_id", "ATU12345678"),
					resource.TestCheckResourceAttr(researchAccount, "billing_address.region_code", "AT"),
				),
			},
			{
				ResourceName:      researchAccount,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Destroying the project billing info leaves the project paid
			// for by the account, which therefore isn't deleted: the
			// refusal reaches the user. The spend limit, destroyed first,
			// is unset.
			{
				Config:      billingConfig(fake, fmt.Sprintf("%q", researchName), "", "", ""),
				ExpectError: regexp.MustCompile(`pays\s+for\s+projects/acme-prod:\s+link\s+it\s+to\s+another\s+account\s+first`),
			},
			// Linked back to the default account, the project lets the
			// account go.
			{
				Config: billingConfig(fake, fmt.Sprintf("%q", defaultAccount), account("Research GmbH", "AT", `vat_id = "ATU12345678"`), "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(prodBillingInfo, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(prodBillingInfo, "billing_account", defaultAccount),
			},
			{
				Config: billingConfig(fake, fmt.Sprintf("%q", defaultAccount), "", "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(researchAccount, plancheck.ResourceActionDestroy),
				}},
			},
		},
		// Each resource's mutations, in order: the project's billing info
		// isn't touched by its destroy.
		CheckDestroy: func(*terraform.State) error {
			want := map[string][]string{
				researchName + " ": {
					"PATCH " + researchName + " billingAddress",
					"PATCH " + researchName + " displayName,billingAddress,vatId",
					"DELETE " + researchName,
					"DELETE " + researchName,
				},
				researchName + "/spendLimit": {
					"PATCH " + researchName + "/spendLimit limit,alertThresholdsPercent,notificationEmails",
					"PATCH " + researchName + "/spendLimit limit,alertThresholdsPercent,notificationEmails",
				},
				billingProject: {
					"PATCH " + billingProject + " billingAccount",
					"PATCH " + billingProject + " billingAccount",
				},
			}
			calls := fake.mutations()
			for of, want := range want {
				var got []string
				for _, call := range calls {
					if strings.Contains(call+" ", of) {
						got = append(got, call)
					}
				}
				if !slices.Equal(got, want) {
					return fmt.Errorf("the mutations of %s were %q, not %q", of, got, want)
				}
			}
			if !slices.Contains(calls, "POST organizations/acme/billingAccounts") {
				return fmt.Errorf("no create in %q", calls)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.accounts) != 1 {
				return fmt.Errorf("left: %v", fake.accounts)
			}
			return nil
		},
	})
}

func TestARegionalProviderCallsBillingAtItsGlobalHost(t *testing.T) {
	t.Setenv("ORIGINCLOUD_ACCESS_TOKEN", "test-token")
	t.Setenv("ORIGINCLOUD_REGION", "")
	// Only this host answers: a billing call to the region's front door
	// would fail.
	h := &hosts{servers: map[string]*httptest.Server{
		"billing.origincloudapis.eu": newFakeBilling(t).Server,
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

resource "origincloud_billing_project_billing_info" "prod" {
  project         = "acme-prod"
  billing_account = "organizations/acme/billingAccounts/ba-1"
}

data "origincloud_billing_billing_country" "de" {
  name = "billingCountries/DE"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(prodBillingInfo, "billing_account", defaultAccount),
					resource.TestCheckResourceAttr("data.origincloud_billing_billing_country.de", "vat_id_required", "false"),
					func(*terraform.State) error {
						for _, want := range []string{
							"billing.origincloudapis.eu /v1beta1/projects/acme-prod/billingInfo",
							"billing.origincloudapis.eu /v1beta1/billingCountries/DE",
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
