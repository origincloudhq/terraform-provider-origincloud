// Package provider is the Origin Cloud provider: its configuration, and
// the resources and data sources generated from the APIs.
package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
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
}

type config struct {
	Region   types.String `tfsdk:"region"`
	Endpoint types.String `tfsdk:"endpoint"`
}

func (p *originCloud) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "origincloud"
	resp.Version = p.version
}

func (p *originCloud) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages Origin Cloud resources.\n\n" +
			"Credentials come from the environment, the first of: `ORIGINCLOUD_ACCESS_TOKEN`, an access token; " +
			"`ORIGINCLOUD_CLIENT_ID` and `ORIGINCLOUD_CLIENT_SECRET`, a service account; " +
			"the session the `origin` CLI stored at its last `origin id login`.",
		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "The region whose front door the provider calls, such as `eu-central1`. " +
					"Every front door serves every resource; a region only picks the nearest. " +
					"Defaults to `ORIGINCLOUD_REGION`, and to the regionless endpoint when that is unset.",
				Optional: true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "The URL every call goes to instead, such as a test server's.",
				Optional:            true,
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
	client, err := core.NewClient(ctx, opts...)
	if err != nil {
		resp.Diagnostics.AddError("Couldn't configure the Origin Cloud provider", err.Error())
		return
	}
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *originCloud) Resources(context.Context) []func() resource.Resource {
	return serviceResources()
}

func (p *originCloud) DataSources(context.Context) []func() datasource.DataSource {
	return serviceDataSources()
}
