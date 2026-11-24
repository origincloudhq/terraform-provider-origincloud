package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	resourcetest "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const (
	// impersonatedAccount is the service account a person impersonates.
	impersonatedAccount = "//iam.origincloudapis.eu/organizations/acme/serviceAccounts/sa-deploy"
	// person is the one who logged in with `origin id login`, and
	// personToken the access token of that session.
	person      = "//iam.origincloudapis.eu/users/" + iamCaller
	personToken = "person-token"
)

// fakeImpersonationSTS is the security token service's token endpoint for
// impersonation: it exchanges a person's access token for the token of a
// service account the person holds iam.service_account.get_access_token
// on, as the service does, and refuses every other exchange with
// invalid_grant. The tokens it hands out name the account as their
// principal and the person as the acting party, which iam accepts.
type fakeImpersonationSTS struct {
	*httptest.Server
	iam *fakeIAM

	mu sync.Mutex
	// bindings are the service accounts the person may impersonate.
	bindings map[string]bool
	// exchanges are the forms of the exchanges asked for.
	exchanges []map[string][]string
	granted   int
}

func newFakeImpersonationSTS(t *testing.T, iam *fakeIAM, bindings ...string) *fakeImpersonationSTS {
	f := &fakeImpersonationSTS{iam: iam, bindings: map[string]bool{}}
	for _, b := range bindings {
		f.bindings[b] = true
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	iam.mu.Lock()
	// The person's own token is accepted too, so that a call made with it
	// instead shows in the audit log as the person's.
	iam.tokens = map[string]fakeToken{personToken: {principal: person, expiry: time.Now().Add(time.Hour)}}
	iam.mu.Unlock()
	return f
}

func (f *fakeImpersonationSTS) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	refuse := func(code, reason string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": reason})
	}
	if r.URL.Path != "/v1/token" || r.Method != http.MethodPost || r.ParseForm() != nil {
		refuse("invalid_request", r.Method+" "+r.URL.Path+" isn't the token endpoint")
		return
	}
	f.exchanges = append(f.exchanges, r.PostForm)
	switch {
	case r.PostFormValue("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange",
		r.PostFormValue("subject_token_type") != "urn:ietf:params:oauth:token-type:access_token",
		r.PostForm.Has("audience"):
		refuse("invalid_request", "not an impersonation exchange")
		return
	case r.PostFormValue("subject_token") != personToken:
		refuse("invalid_grant", "the subject token isn't a person's")
		return
	case !f.bindings[r.PostFormValue("service_account")]:
		refuse("invalid_grant", "the caller may not impersonate the service account")
		return
	}
	f.granted++
	token := fmt.Sprintf("impersonated-token-%d", f.granted)
	f.iam.mu.Lock()
	f.iam.tokens[token] = fakeToken{principal: r.PostFormValue("service_account"), actingParty: person, expiry: time.Now().Add(5 * time.Minute)}
	f.iam.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": token, "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
		"token_type": "Bearer", "expires_in": 300,
	})
}

func (f *fakeImpersonationSTS) counts() (exchanges []map[string][]string, granted int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string][]string(nil), f.exchanges...), f.granted
}

// loginWithTheCLI stores the session `origin id login` would have, the
// person's, valid for an hour, where the SDK finds it.
func loginWithTheCLI(t *testing.T) {
	t.Helper()
	session, err := json.Marshal(map[string]any{
		"access_token": personToken, "expires": time.Now().Add(time.Hour),
		"refresh_token": "person-refresh-token", "refresh_expires": time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(os.Getenv("XDG_DATA_HOME"), "origincloud")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), session, 0o600); err != nil {
		t.Fatal(err)
	}
}

// noTokenInState fails when the state holds any of tokens.
func noTokenInState(tokens ...string) resourcetest.TestCheckFunc {
	return func(s *terraform.State) error {
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		for _, token := range tokens {
			if strings.Contains(string(raw), token) {
				return fmt.Errorf("the state holds the token %q", token)
			}
		}
		return nil
	}
}

// With only a person's `origin id login` session and
// impersonate_service_account, or ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT,
// the provider acts as the service account: the audit log names it as the
// actor and the person as the acting party, and no token is in the state.
func TestImpersonationWithOnlyACLISessionActsAsTheServiceAccount(t *testing.T) {
	for _, tc := range []struct {
		name string
		// configure returns the provider's arguments.
		configure func(t *testing.T) string
	}{
		{"impersonate_service_account", func(*testing.T) string {
			return fmt.Sprintf("impersonate_service_account = %q", impersonatedAccount)
		}},
		{"ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT", func(t *testing.T) string {
			t.Setenv("ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT", impersonatedAccount)
			return ""
		}},
		{"the attribute over the variable", func(t *testing.T) string {
			t.Setenv("ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT", "//iam.origincloudapis.eu/organizations/acme/serviceAccounts/sa-other")
			return fmt.Sprintf("impersonate_service_account = %q", impersonatedAccount)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withoutCredentials(t)
			loginWithTheCLI(t)
			iam := newFakeIAM(t)
			sts := newFakeImpersonationSTS(t, iam, impersonatedAccount)
			t.Setenv("ORIGINCLOUD_STS_ENDPOINT", sts.URL)
			config := keyConfig(iam, tc.configure(t))
			entries := "data.origincloud_iam_audit_log_entries.acme"

			resourcetest.UnitTest(t, resourcetest.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resourcetest.TestStep{
					{
						Config: config,
						Check: resourcetest.ComposeAggregateTestCheckFunc(
							resourcetest.TestCheckResourceAttr(entries, "audit_log_entries.#", "2"),
							resourcetest.TestCheckResourceAttr(entries, "audit_log_entries.0.actor", impersonatedAccount),
							resourcetest.TestCheckResourceAttr(entries, "audit_log_entries.0.acting_party", person),
							resourcetest.TestCheckResourceAttr(entries, "audit_log_entries.1.actor", impersonatedAccount),
							resourcetest.TestCheckResourceAttr(entries, "audit_log_entries.1.acting_party", person),
							resourcetest.TestCheckResourceAttr(entries, "audit_log_entries.1.action", "iam.service_account.create"),
							noTokenInState(personToken, "person-refresh-token", "impersonated-token-"),
						),
					},
					// A plan of what was applied reads it as the service
					// account too, and changes nothing.
					{Config: config, PlanOnly: true},
				},
			})
			exchanges, granted := sts.counts()
			iam.mu.Lock()
			refused := iam.refused
			iam.mu.Unlock()
			if granted == 0 || granted != len(exchanges) || refused > 0 {
				t.Fatalf("%d exchanges, %d granted, %d calls refused", len(exchanges), granted, refused)
			}
			for _, form := range exchanges {
				if got := form["service_account"]; len(got) != 1 || got[0] != impersonatedAccount {
					t.Errorf("the exchange impersonated %q, not %s", got, impersonatedAccount)
				}
			}
		})
	}
}

// A refused impersonation fails the plan with one diagnostic naming the
// service account and the role the person needs, even a plan that would
// call nothing, and the provider sends no call with the person's own
// token instead.
func TestARefusedImpersonationFailsThePlan(t *testing.T) {
	const newOnly = `
provider "origincloud" {
  endpoint = %q
  %s
}

resource "origincloud_iam_service_account" "ci" {
  organization = "acme"
  display_name = "CI"
}
`
	for _, tc := range []struct {
		name      string
		configure func(t *testing.T) string
		want      string
	}{
		{"impersonate_service_account", func(*testing.T) string {
			return fmt.Sprintf("impersonate_service_account = %q", impersonatedAccount)
		}, `(?s)Couldn't impersonate the service account.*impersonate_service_account = .*` + regexp.QuoteMeta(impersonatedAccount) + `.*roles/iam.service-account-token-creator.*no\s+call\s+with\s+your\s+own\s+credentials`},
		{"ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT", func(t *testing.T) string {
			t.Setenv("ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT", impersonatedAccount)
			return ""
		}, `(?s)Couldn't impersonate the service account.*` + regexp.QuoteMeta(impersonatedAccount) + `.*roles/iam.service-account-token-creator.*ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT\s+names`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withoutCredentials(t)
			loginWithTheCLI(t)
			iam := newFakeIAM(t)
			// The person holds the role on no service account.
			sts := newFakeImpersonationSTS(t, iam)
			t.Setenv("ORIGINCLOUD_STS_ENDPOINT", sts.URL)

			resourcetest.UnitTest(t, resourcetest.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resourcetest.TestStep{{
					Config:      fmt.Sprintf(newOnly, iam.URL, tc.configure(t)),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
			exchanges, granted := sts.counts()
			if len(exchanges) == 0 || granted != 0 {
				t.Errorf("%d exchanges, %d granted: the impersonation wasn't asked for, or was granted", len(exchanges), granted)
			}
			iam.mu.Lock()
			defer iam.mu.Unlock()
			if len(iam.calls) > 0 || len(iam.auditLog) > 0 {
				t.Errorf("the API got calls after the refusal: %q", iam.calls)
			}
		})
	}
}

// A service account that isn't named by its full name fails the
// configuration before any exchange, naming the attribute; and an empty
// impersonate_service_account impersonates no one, even with the variable
// set, so the provider acts as the person.
func TestImpersonateServiceAccountsValues(t *testing.T) {
	withoutCredentials(t)
	loginWithTheCLI(t)
	iam := newFakeIAM(t)
	sts := newFakeImpersonationSTS(t, iam, impersonatedAccount)
	t.Setenv("ORIGINCLOUD_STS_ENDPOINT", sts.URL)
	t.Setenv("ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT", impersonatedAccount)

	resourcetest.UnitTest(t, resourcetest.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resourcetest.TestStep{
			{
				Config:      keyConfig(iam, `impersonate_service_account = "organizations/acme/serviceAccounts/sa-deploy"`),
				ExpectError: regexp.MustCompile(`(?s)impersonate_service_account = .*the\s+service\s+account\s+to\s+impersonate`),
			},
			{
				Config:      keyConfig(iam, `impersonate_service_account = "//iam.origincloudapis.eu/users/u-ana"`),
				ExpectError: regexp.MustCompile(`(?s)impersonate_service_account = .*must\s+be\s+named\s+//iam.origincloudapis.eu/organizations/\{organization\}/serviceAccounts/\{service_account\}`),
			},
			{
				Config: keyConfig(iam, `impersonate_service_account = ""`),
				Check: resourcetest.ComposeAggregateTestCheckFunc(
					resourcetest.TestCheckResourceAttr("data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.0.actor", person),
					resourcetest.TestCheckResourceAttr("data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.0.acting_party", ""),
				),
			},
		},
	})
	if exchanges, _ := sts.counts(); len(exchanges) > 0 {
		t.Errorf("%d exchanges: a malformed or empty name was exchanged", len(exchanges))
	}
}
