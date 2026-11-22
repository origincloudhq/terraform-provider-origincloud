package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	resourcetest "github.com/hashicorp/terraform-plugin-testing/helper/resource"

	datasourceschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	resourceschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

const (
	keyAccount = "//iam.origincloudapis.eu/organizations/acme/serviceAccounts/sa-deployer"
	keyName    = keyAccount + "/keys/key-1"
)

// fakeSTS is the security token service's token endpoint for one service
// account key: it checks each assertion as the service does, its ES256
// signature with the key's public half, its key, account, audience and
// lifetime, and hands out access tokens that live lifetime, which iam
// accepts until then.
type fakeSTS struct {
	*httptest.Server
	public   *ecdsa.PublicKey
	lifetime time.Duration
	iam      *fakeIAM

	mu        sync.Mutex
	exchanges int
	// refusals are why the assertions refused were.
	refusals []string
}

func newFakeSTS(t *testing.T, iam *fakeIAM, public *ecdsa.PublicKey, lifetime time.Duration) *fakeSTS {
	f := &fakeSTS{public: public, lifetime: lifetime, iam: iam}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	iam.mu.Lock()
	iam.tokens = map[string]fakeToken{}
	iam.mu.Unlock()
	return f
}

func (f *fakeSTS) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	refuse := func(reason string) {
		f.refusals = append(f.refusals, reason)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": reason})
	}
	if r.URL.Path != "/v1/token" || r.Method != http.MethodPost {
		refuse(r.Method + " " + r.URL.Path + " isn't the token endpoint")
		return
	}
	if grant := r.PostFormValue("grant_type"); grant != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		refuse("grant_type " + grant)
		return
	}
	if reason := f.check(r.PostFormValue("assertion")); reason != "" {
		refuse(reason)
		return
	}
	f.exchanges++
	token := fmt.Sprintf("sts-token-%d", f.exchanges)
	f.iam.mu.Lock()
	f.iam.tokens[token] = fakeToken{principal: keyAccount, expiry: time.Now().Add(f.lifetime)}
	f.iam.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": token, "token_type": "Bearer", "expires_in": int(f.lifetime.Seconds()),
	})
}

// check returns why assertion is refused, or "" when it is accepted.
func (f *fakeSTS) check(assertion string) string {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return "the assertion isn't a JWS"
	}
	var header struct{ Alg, Kid string }
	var claims struct {
		Iss, Sub, Aud, Jti string
		Iat, Exp           int64
	}
	for i, v := range []any{&header, &claims} {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil || json.Unmarshal(raw, v) != nil {
			return "the assertion's header or claims aren't JSON"
		}
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	now := time.Now().Unix()
	switch {
	case err != nil || len(signature) != 64:
		return "the signature isn't ES256's"
	case !ecdsa.Verify(f.public, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])):
		return "the signature isn't the key's"
	case header.Alg != "ES256" || header.Kid != keyName:
		return fmt.Sprintf("alg %q, kid %q", header.Alg, header.Kid)
	case claims.Iss != keyAccount || claims.Sub != keyAccount:
		return fmt.Sprintf("iss %q, sub %q", claims.Iss, claims.Sub)
	case claims.Aud != f.URL:
		return fmt.Sprintf("aud %q, not %q", claims.Aud, f.URL)
	case claims.Jti == "" || claims.Iat > now+5 || claims.Exp <= now || claims.Exp-claims.Iat > 3600:
		return "the assertion's lifetime or jti is wrong"
	}
	return ""
}

func (f *fakeSTS) counts() (exchanges int, refusals []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchanges, append([]string(nil), f.refusals...)
}

// writeCredentialsFile writes the credentials file creating the key
// keyName would have returned, for the security token service at sts, and
// returns its path and the key's public half.
func writeCredentialsFile(t *testing.T, sts string) (string, *ecdsa.PublicKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	file, err := json.Marshal(map[string]string{
		"type":            "service_account_key",
		"name":            keyName,
		"service_account": keyAccount,
		"algorithm":       "ES256",
		"private_key":     string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"sts_endpoint":    sts,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "deployer.json")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, &key.PublicKey
}

// withoutCredentials clears every credential of the environment, so that
// the provider finds only what a test configures: no access token, no
// credentials file, no CI job's token, no CLI session and no instance.
func withoutCredentials(t *testing.T) {
	for _, v := range []string{
		"ORIGINCLOUD_ACCESS_TOKEN", "ORIGINCLOUD_CREDENTIALS_FILE", "ORIGINCLOUD_OIDC_TOKEN_FILE", "ORIGINCLOUD_OIDC_TOKEN",
		"TFC_WORKLOAD_IDENTITY_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN",
	} {
		t.Setenv(v, "")
	}
	t.Setenv("ORIGINCLOUD_OIDC_DISABLED", "true")
	t.Setenv("ORIGINCLOUD_METADATA_DISABLED", "true")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// keyConfig returns a configuration of the provider calling fake with the
// provider's arguments args, a service account and a binding of it, and
// the organization's audit log, read after them.
func keyConfig(fake *fakeIAM, args string) string {
	return fmt.Sprintf(`
provider "origincloud" {
  endpoint = %q
  %s
}

resource "origincloud_iam_service_account" "ci" {
  organization = "acme"
  display_name = "CI"
}

resource "origincloud_iam_role_binding" "ci" {
  organization = "acme"
  member       = origincloud_iam_service_account.ci.member
  role         = "//iam.origincloudapis.eu/roles/viewer"
}

data "origincloud_iam_audit_log_entries" "acme" {
  organization = "acme"
  depends_on   = [origincloud_iam_role_binding.ci]
}
`, fake.URL, args)
}

// With only a credentials file, in credentials_file or in
// ORIGINCLOUD_CREDENTIALS_FILE, the provider calls the API as the key's
// service account, which the audit log names as the actor of what it did.
func TestWithOnlyACredentialsFileTheProviderActsAsItsServiceAccount(t *testing.T) {
	for _, tc := range []struct {
		name string
		// configure returns the provider's arguments for the file at path.
		configure func(t *testing.T, path string) string
	}{
		{"credentials_file", func(_ *testing.T, path string) string { return fmt.Sprintf("credentials_file = %q", path) }},
		{"ORIGINCLOUD_CREDENTIALS_FILE", func(t *testing.T, path string) string {
			t.Setenv("ORIGINCLOUD_CREDENTIALS_FILE", path)
			return ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withoutCredentials(t)
			iam := newFakeIAM(t)
			sts := newFakeSTS(t, iam, nil, time.Hour)
			path, public := writeCredentialsFile(t, sts.URL)
			sts.public = public
			config := keyConfig(iam, tc.configure(t, path))

			resourcetest.UnitTest(t, resourcetest.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resourcetest.TestStep{{
					Config: config,
					Check: resourcetest.ComposeAggregateTestCheckFunc(
						resourcetest.TestCheckResourceAttr("data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.#", "2"),
						resourcetest.TestCheckResourceAttr("data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.0.actor", keyAccount),
						resourcetest.TestCheckResourceAttr("data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.1.actor", keyAccount),
						resourcetest.TestCheckResourceAttr("data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.1.action", "iam.service_account.create"),
					),
				}},
			})
			exchanges, refusals := sts.counts()
			iam.mu.Lock()
			refused := iam.refused
			iam.mu.Unlock()
			if len(refusals) > 0 || refused > 0 {
				t.Errorf("the security token service refused %q, the API %d calls", refusals, refused)
			}
			if exchanges == 0 {
				t.Error("no assertion was exchanged")
			}
		})
	}
}

// A token about to expire is replaced before a call is sent with it, so a
// long apply makes no call the API refuses. The tokens here live a
// minute, the margin the SDK renews within, so that every call needs a new
// one, as a call late in a long apply does.
func TestATokenAboutToExpireIsRenewedWithoutAFailedCall(t *testing.T) {
	withoutCredentials(t)
	iam := newFakeIAM(t)
	sts := newFakeSTS(t, iam, nil, time.Minute)
	path, public := writeCredentialsFile(t, sts.URL)
	sts.public = public

	resourcetest.UnitTest(t, resourcetest.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resourcetest.TestStep{{
			Config: keyConfig(iam, fmt.Sprintf("credentials_file = %q", path)),
			Check:  resourcetest.TestCheckResourceAttr("data.origincloud_iam_audit_log_entries.acme", "audit_log_entries.0.actor", keyAccount),
		}},
	})
	exchanges, refusals := sts.counts()
	iam.mu.Lock()
	refused := iam.refused
	iam.mu.Unlock()
	if len(refusals) > 0 || refused > 0 {
		t.Errorf("the security token service refused %q, the API %d calls", refusals, refused)
	}
	if exchanges < 4 {
		t.Errorf("%d exchanges: tokens about to expire weren't renewed", exchanges)
	}
}

// A credentials file that can't be used fails the provider's
// configuration, naming credentials_file, and never quotes the file.
func TestACredentialsFileThatCantBeUsedFailsTheConfiguration(t *testing.T) {
	withoutCredentials(t)
	iam := newFakeIAM(t)
	dir := t.TempDir()
	notJSON := filepath.Join(dir, "not-json")
	if err := os.WriteFile(notJSON, []byte("-----BEGIN PRIVATE KEY----- secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	step := func(args, want string) resourcetest.TestStep {
		return resourcetest.TestStep{Config: keyConfig(iam, args), ExpectError: regexp.MustCompile(want)}
	}
	resourcetest.UnitTest(t, resourcetest.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resourcetest.TestStep{
			step(fmt.Sprintf("credentials_file = %q", filepath.Join(dir, "missing.json")), `(?s)credentials_file.*reading the credentials file`),
			step(fmt.Sprintf("credentials_file = %q", notJSON), `(?s)credentials_file.*the credentials file is not JSON`),
			step(`credentials_file = ""`, `credentials_file is empty`),
		},
	})
}

// A private key never lands in the state: no resource or data source has
// an attribute that holds a service account key's credentials file, nor
// any sensitive attribute but those listed here, each a value the API
// generates once that isn't a credential to the API.
func TestNoResourceOrDataSourceReturnsAPrivateKey(t *testing.T) {
	sensitive := map[string]bool{
		// An SSH key pair's generated private key: a login to the
		// instances launched with it, not to the API.
		"origincloud_compute_key_pair.private_key": true,
	}
	ctx := context.Background()
	p := New("test")()
	var found []string
	seen := map[string]bool{}
	visit := func(typeName string, walk func(func(path string, sensitive bool))) {
		walk(func(path string, isSensitive bool) {
			name := path[strings.LastIndex(path, ".")+1:]
			seen[typeName+"."+path] = seen[typeName+"."+path] || isSensitive
			switch {
			case name == "credentials_file" || name == "private_key" && !sensitive[typeName+"."+path]:
				found = append(found, typeName+"."+path)
			case isSensitive && !sensitive[typeName+"."+path]:
				found = append(found, typeName+"."+path+" (sensitive)")
			}
		})
	}
	for _, newResource := range p.Resources(ctx) {
		r := newResource()
		var meta resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "origincloud"}, &meta)
		if strings.Contains(meta.TypeName, "service_account_key") {
			found = append(found, meta.TypeName+" (a resource)")
		}
		var schema resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &schema)
		visit(meta.TypeName, func(f func(string, bool)) { walkResourceAttributes("", schema.Schema.Attributes, f) })
	}
	for _, newDataSource := range p.DataSources(ctx) {
		d := newDataSource()
		var meta datasource.MetadataResponse
		d.Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "origincloud"}, &meta)
		var schema datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &schema)
		visit("data."+meta.TypeName, func(f func(string, bool)) { walkDataSourceAttributes("", schema.Schema.Attributes, f) })
	}
	if len(found) > 0 {
		t.Errorf("attributes that may hold a private key:\n%s", strings.Join(found, "\n"))
	}
	for path := range sensitive {
		if !seen[path] {
			t.Errorf("%s isn't a sensitive attribute: the walk missed it, or the list is stale", path)
		}
	}
	// The provider's own credentials_file is a path, not a key.
	var schema provider.SchemaResponse
	p.Schema(ctx, provider.SchemaRequest{}, &schema)
	if a, ok := schema.Schema.Attributes["credentials_file"]; !ok || a.IsSensitive() {
		t.Error("the provider has no credentials_file argument, or one marked sensitive")
	}
}

// walkResourceAttributes calls f with the path of each of attrs, at any
// depth, and whether it is sensitive.
func walkResourceAttributes(prefix string, attrs map[string]resourceschema.Attribute, f func(string, bool)) {
	for name, a := range attrs {
		path := prefix + name
		f(path, a.IsSensitive())
		if nested, ok := a.(interface {
			GetNestedObject() resourceschema.NestedAttributeObject
		}); ok {
			walkResourceAttributes(path+".", nested.GetNestedObject().Attributes, f)
		}
	}
}

// walkDataSourceAttributes is walkResourceAttributes for a data source.
func walkDataSourceAttributes(prefix string, attrs map[string]datasourceschema.Attribute, f func(string, bool)) {
	for name, a := range attrs {
		path := prefix + name
		f(path, a.IsSensitive())
		if nested, ok := a.(interface {
			GetNestedObject() datasourceschema.NestedAttributeObject
		}); ok {
			walkDataSourceAttributes(path+".", nested.GetNestedObject().Attributes, f)
		}
	}
}
