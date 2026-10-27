// Package framework runs the provider's resources and data sources: the
// Terraform side of every resource is the same over its API's standard
// methods (AIP-131 to AIP-135), and the generated code only says how one
// resource type maps onto them.
package framework

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"go.origincloud.eu/sdk/core"
	"google.golang.org/protobuf/proto"
)

// providerTypeName prefixes every resource's and data source's type name,
// for messages. Terraform passes it to Metadata only, and the framework
// makes a new resource for each call.
const providerTypeName = "origincloud"

// A ResourceSpec maps one API resource type, whose message is P, onto a
// Terraform resource whose state is M. The generated code fills it in.
type ResourceSpec[M any, P proto.Message] struct {
	// TypeName follows the provider's, such as "_pvs_volume".
	TypeName string
	Schema   schema.Schema

	// Name returns the resource name the state holds.
	Name func(*M) string
	// ToProto returns the resource as the plan describes it.
	ToProto func(*M) P
	// FromProto sets the state from the resource the API returned.
	FromProto func(P, *M) error
	// UpdateMask returns the fields that differ between the plan and the
	// state and that an update can change.
	UpdateMask func(plan, state *M) []string
	// Gone says that a resource the API still returns is deleted, such as
	// a volume in DELETED. Nil when the API stops returning deleted ones.
	Gone func(P) bool

	Create func(ctx context.Context, client *core.Client, plan *M) (*core.Operation[P], error)
	Get    func(ctx context.Context, client *core.Client, name string) (P, error)
	// Update is nil for a resource that has no update method: each of its
	// attributes then replaces it.
	Update func(ctx context.Context, client *core.Client, resource P, mask []string) (P, error)
	Delete func(ctx context.Context, client *core.Client, name string) error
}

// NewResource returns the constructor of the resource spec describes.
func NewResource[M any, P proto.Message](spec ResourceSpec[M, P]) func() resource.Resource {
	return func() resource.Resource { return &Resource[M, P]{spec: spec} }
}

// A Resource is a Terraform resource over one API resource type.
type Resource[M any, P proto.Message] struct {
	spec   ResourceSpec[M, P]
	client *core.Client
}

var (
	_ resource.ResourceWithConfigure   = (*Resource[struct{}, proto.Message])(nil)
	_ resource.ResourceWithImportState = (*Resource[struct{}, proto.Message])(nil)
)

func (r *Resource[M, P]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.spec.TypeName
}

func (r *Resource[M, P]) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = r.spec.Schema
}

func (r *Resource[M, P]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return // validating, before the provider is configured
	}
	client, ok := req.ProviderData.(*core.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("%T isn't a *core.Client", req.ProviderData))
		return
	}
	r.client = client
}

func (r *Resource[M, P]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m M
	if resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	op, err := r.spec.Create(ctx, r.client, &m)
	if err != nil {
		resp.Diagnostics.AddError(r.summary("create"), err.Error())
		return
	}
	created, err := op.Wait(ctx)
	if err != nil {
		resp.Diagnostics.AddError(r.summary("create"), err.Error())
		// A create that fails after the resource exists, such as a volume
		// whose storage couldn't be created, leaves it to delete: it goes
		// into the state, where Terraform marks it tainted and replaces it.
		if target := OperationTarget(op.Proto()); target != "" {
			if created, err = r.spec.Get(ctx, r.client, target); err == nil && r.spec.FromProto(created, &m) == nil {
				resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			}
		}
		return
	}
	if err := r.spec.FromProto(created, &m); err != nil {
		resp.Diagnostics.AddError(r.summary("read the created"), err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *Resource[M, P]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m M
	if resp.Diagnostics.Append(req.State.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	got, err := r.spec.Get(ctx, r.client, r.spec.Name(&m))
	if NotFound(err) || err == nil && r.spec.Gone != nil && r.spec.Gone(got) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(r.summary("read"), err.Error())
		return
	}
	if err := r.spec.FromProto(got, &m); err != nil {
		resp.Diagnostics.AddError(r.summary("read"), err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *Resource[M, P]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state M
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var (
		got P
		err error
	)
	// A plan whose only differences are computed attributes changes
	// nothing in the API: the resource is read again instead.
	if mask := r.spec.UpdateMask(&plan, &state); len(mask) > 0 && r.spec.Update != nil {
		got, err = r.spec.Update(ctx, r.client, r.spec.ToProto(&plan), mask)
	} else {
		got, err = r.spec.Get(ctx, r.client, r.spec.Name(&state))
	}
	if err != nil {
		resp.Diagnostics.AddError(r.summary("update"), err.Error())
		return
	}
	if err := r.spec.FromProto(got, &plan); err != nil {
		resp.Diagnostics.AddError(r.summary("read the updated"), err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *Resource[M, P]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m M
	if resp.Diagnostics.Append(req.State.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	if err := r.spec.Delete(ctx, r.client, r.spec.Name(&m)); err != nil && !NotFound(err) {
		resp.Diagnostics.AddError(r.summary("delete"), err.Error())
	}
}

// ImportState adopts a resource by its resource name, such as
// projects/my-project/zones/eu-central1-az1/volumes/vol-1: Terraform
// reads the rest after.
func (r *Resource[M, P]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

func (r *Resource[M, P]) summary(verb string) string {
	return fmt.Sprintf("Couldn't %s %s%s", verb, providerTypeName, r.spec.TypeName)
}

// NotFound says that err is the API answering that a resource doesn't
// exist.
func NotFound(err error) bool {
	var apiErr *core.Error
	return errors.As(err, &apiErr) && apiErr.Status == "NOT_FOUND"
}
