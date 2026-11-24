// Package provider is the Origin Cloud provider: its configuration, and
// the resources and data sources generated from the APIs.
package provider

import (
	"context"
	"net/http"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"go.origincloud.eu/sdk/core"
)

// New returns the provider's constructor; version is the provider's own.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &originCloud{version: version} }
}

type originCloud struct {
	version string
	// httpClient sends the calls, when set; tests set it to see the
	// hosts called.
	httpClient *http.Client
}

type config struct {
	Region          types.String `tfsdk:"region"`
	Endpoint        types.String `tfsdk:"endpoint"`
	CredentialsFile types.String `tfsdk:"credentials_file"`
	// ImpersonateServiceAccount is the full name of the service account the
	// provider acts as, with the token of the credentials it found.
	ImpersonateServiceAccount types.String `tfsdk:"impersonate_service_account"`
}

// The environment variables the provider reads itself, as the Go SDK does:
// the service account to impersonate, and the security token service the
// impersonation is exchanged at.
const (
	envImpersonateServiceAccount = "ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT"
	envSTSEndpoint               = "ORIGINCLOUD_STS_ENDPOINT"
)

func (p *originCloud) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "origincloud"
	resp.Version = p.version
}

func (p *originCloud) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages Origin Cloud resources.\n\n" +
			"Credentials come from `credentials_file` when it is set, and otherwise from the environment, the first of: " +
			"`ORIGINCLOUD_ACCESS_TOKEN`, an access token; " +
			"`ORIGINCLOUD_CREDENTIALS_FILE`, the path of a service account key's credentials file; " +
			"a CI job's OIDC token, exchanged at the security token service for a `WORKLOAD` service account's access tokens " +
			"(`ORIGINCLOUD_OIDC_TOKEN_FILE`, `ORIGINCLOUD_OIDC_TOKEN`, an HCP Terraform run's `TFC_WORKLOAD_IDENTITY_TOKEN`, " +
			"or a GitHub Actions job's token); " +
			"the session the `origin` CLI stored at its last `origin id login`; " +
			"on an instance that runs as a service account, the instance's metadata service.\n\n" +
			"With `impersonate_service_account`, or `ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT`, the provider acts as that service account instead, " +
			"exchanging the token of those credentials, a person's, at the security token service for the account's.",
		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "The region whose front door the provider calls, such as `eu-central1`. " +
					"Every front door serves every resource; a region only picks the nearest. " +
					"A global service, such as location, is always called at its regionless host. " +
					"Defaults to `ORIGINCLOUD_REGION`, and to the regionless endpoint when that is unset.",
				Optional: true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "The URL every call goes to instead, such as a test server's.",
				Optional:            true,
			},
			"credentials_file": schema.StringAttribute{
				MarkdownDescription: "The path of a service account key's credentials file, the JSON file `origin iam service-accounts keys create` " +
					"or the console saved: the provider calls the API as that service account, exchanging an assertion signed with the key " +
					"at the security token service for access tokens, and a new one before each expires. " +
					"Set, it comes before every credential of the environment; left out, `ORIGINCLOUD_CREDENTIALS_FILE` names the file. " +
					"The provider reads the file, and the state never holds the key: no resource or data source creates or returns one.",
				Optional: true,
			},
			"impersonate_service_account": schema.StringAttribute{
				MarkdownDescription: "The full name of a service account to act as, " +
					"`//iam.origincloudapis.eu/organizations/{organization}/serviceAccounts/{service_account}`: " +
					"the provider exchanges the token of the credentials it found, a person's such as the `origin id login` session, " +
					"at the security token service for the account's access tokens, and a new one before each expires, " +
					"so that the plan and apply have the account's roles only. " +
					"The person needs `iam.service_account.get_access_token` on the account, the role `roles/iam.service-account-token-creator`. " +
					"The audit log names the service account as the actor and the person as the acting party. " +
					"A refused exchange fails the plan: the provider never falls back to the person's own token. " +
					"Left out, `ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT` names the account; set to `\"\"`, the provider impersonates no one, even with the variable set.",
				Optional: true,
			},
		},
	}
}

func (p *originCloud) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg config
	if resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...); resp.Diagnostics.HasError() {
		return
	}
	opts := []core.Option{core.WithUserAgent("terraform-provider-origincloud/" + p.version)}
	if !cfg.Region.IsNull() {
		opts = append(opts, core.WithRegion(cfg.Region.ValueString()))
	}
	if !cfg.Endpoint.IsNull() {
		opts = append(opts, core.WithEndpoint(cfg.Endpoint.ValueString()))
	}
	if p.httpClient != nil {
		opts = append(opts, core.WithHTTPClient(p.httpClient))
	}
	var source core.Credentials
	switch {
	case cfg.CredentialsFile.IsUnknown():
		resp.Diagnostics.AddAttributeError(path.Root("credentials_file"), "Couldn't configure the Origin Cloud provider",
			"credentials_file is known only after apply: the provider needs the file's path to plan, so give it a value known at plan time, or set ORIGINCLOUD_CREDENTIALS_FILE.")
		return
	case cfg.CredentialsFile.IsNull():
	case cfg.CredentialsFile.ValueString() == "":
		resp.Diagnostics.AddAttributeError(path.Root("credentials_file"), "Couldn't configure the Origin Cloud provider",
			"credentials_file is empty: set it to the path of a service account key's credentials file, or leave it out.")
		return
	default:
		credentials, err := core.CredentialsFromFile(cfg.CredentialsFile.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("credentials_file"), "Couldn't configure the Origin Cloud provider", err.Error())
			return
		}
		credentials.HTTPClient = p.httpClient
		opts = append(opts, core.WithCredentials(credentials))
		source = credentials
	}
	impersonated, ok := p.impersonate(ctx, cfg, source, &resp.Diagnostics)
	if !ok {
		return
	}
	// The provider decides whom it impersonates, so the SDK is told to
	// impersonate no one itself: not again on top of the impersonated
	// credentials, and not the account ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT
	// names when impersonate_service_account is "".
	opts = append(opts, core.WithImpersonateServiceAccount(""))
	if impersonated != nil {
		opts = append(opts, core.WithCredentials(impersonated))
	}
	client, err := core.NewClient(ctx, opts...)
	if err != nil {
		resp.Diagnostics.AddError("Couldn't configure the Origin Cloud provider", err.Error())
		return
	}
	resp.ResourceData = client
	resp.DataSourceData = client
}

// impersonate returns the credentials acting as the service account
// impersonate_service_account, or ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT,
// names, with the token of source, or of the environment's credentials when
// source is nil; or nil when no account is named. It exchanges a token
// once here, so that a refused impersonation fails the plan with one
// diagnostic, even a plan that would read nothing, rather than every call
// later; the token is kept for the calls. ok is false when it added an
// error to diags.
func (p *originCloud) impersonate(ctx context.Context, cfg config, source core.Credentials, diags *diag.Diagnostics) (_ *core.ImpersonatedCredentials, ok bool) {
	attribute := path.Root("impersonate_service_account")
	serviceAccount, from := os.Getenv(envImpersonateServiceAccount), envImpersonateServiceAccount
	switch {
	case cfg.ImpersonateServiceAccount.IsUnknown():
		diags.AddAttributeError(attribute, "Couldn't configure the Origin Cloud provider",
			"impersonate_service_account is known only after apply: the provider needs the service account to plan, so give it a value known at plan time, or set "+envImpersonateServiceAccount+".")
		return nil, false
	case !cfg.ImpersonateServiceAccount.IsNull():
		serviceAccount, from = cfg.ImpersonateServiceAccount.ValueString(), "impersonate_service_account"
	}
	if serviceAccount == "" {
		return nil, true
	}
	fail := func(summary, detail string) {
		if from == envImpersonateServiceAccount {
			diags.AddError(summary, detail+" (the service account is the one "+envImpersonateServiceAccount+" names)")
		} else {
			diags.AddAttributeError(attribute, summary, detail)
		}
	}
	if source == nil {
		credentials, err := core.DefaultCredentials(ctx)
		if err != nil {
			fail("Couldn't configure the Origin Cloud provider", "impersonating "+serviceAccount+" needs credentials to impersonate it with: "+err.Error())
			return nil, false
		}
		source = credentials
	}
	impersonated, err := core.Impersonate(source, serviceAccount)
	if err != nil {
		fail("Couldn't configure the Origin Cloud provider", err.Error())
		return nil, false
	}
	impersonated.Endpoint = os.Getenv(envSTSEndpoint)
	impersonated.HTTPClient = p.httpClient
	if _, err := impersonated.Token(ctx); err != nil {
		fail("Couldn't impersonate the service account",
			err.Error()+". The provider makes no call with your own credentials instead.")
		return nil, false
	}
	return impersonated, true
}

func (p *originCloud) Resources(context.Context) []func() resource.Resource {
	return serviceResources()
}

func (p *originCloud) DataSources(context.Context) []func() datasource.DataSource {
	return serviceDataSources()
}
