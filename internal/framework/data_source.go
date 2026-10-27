package framework

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"go.origincloud.eu/sdk/core"
	"google.golang.org/protobuf/proto"
)

// A DataSourceSpec maps one API resource type, whose message is P, onto a
// Terraform data source that reads one by its resource name into M. The
// generated code fills it in.
type DataSourceSpec[M any, P proto.Message] struct {
	// TypeName follows the provider's, such as "_pvs_storage_class".
	TypeName string
	Schema   schema.Schema

	// Name returns the resource name the configuration asks for.
	Name      func(*M) string
	FromProto func(P, *M) error
	Get       func(ctx context.Context, client *core.Client, name string) (P, error)
}

// NewDataSource returns the constructor of the data source spec describes.
func NewDataSource[M any, P proto.Message](spec DataSourceSpec[M, P]) func() datasource.DataSource {
	return func() datasource.DataSource { return &DataSource[M, P]{spec: spec} }
}

// A DataSource reads one resource of an API resource type.
type DataSource[M any, P proto.Message] struct {
	spec   DataSourceSpec[M, P]
	client *core.Client
}

var _ datasource.DataSourceWithConfigure = (*DataSource[struct{}, proto.Message])(nil)

func (d *DataSource[M, P]) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + d.spec.TypeName
}

func (d *DataSource[M, P]) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = d.spec.Schema
}

func (d *DataSource[M, P]) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return // validating, before the provider is configured
	}
	client, ok := req.ProviderData.(*core.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("%T isn't a *core.Client", req.ProviderData))
		return
	}
	d.client = client
}

func (d *DataSource[M, P]) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m M
	if resp.Diagnostics.Append(req.Config.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	got, err := d.spec.Get(ctx, d.client, d.spec.Name(&m))
	if err == nil {
		err = d.spec.FromProto(got, &m)
	}
	if err != nil {
		resp.Diagnostics.AddError(fmt.Sprintf("Couldn't read %s%s", providerTypeName, d.spec.TypeName), err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
