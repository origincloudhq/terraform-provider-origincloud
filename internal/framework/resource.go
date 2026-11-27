// Package framework runs the provider's resources and data sources: the
// Terraform side of every resource is the same over its API's standard
// methods (AIP-131 to AIP-135), and the generated code only says how one
// resource type maps onto them.
package framework

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
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
	// Gone says that a resource the API still returns is gone, in a
	// terminal state such as a volume's DELETED or an instance's
	// TERMINATED. Nil when the API stops returning deleted ones.
	Gone func(P) bool

	// Create answers with an operation to wait on; CreateSync, for an API
	// whose create answers with the resource, instead of Create.
	Create     func(ctx context.Context, client *core.Client, plan *M) (*core.Operation[P], error)
	CreateSync func(ctx context.Context, client *core.Client, plan *M) (P, error)
	Get        func(ctx context.Context, client *core.Client, name string) (P, error)
	// Update is nil for a resource that has no update method: each of its
	// attributes then replaces it.
	Update func(ctx context.Context, client *core.Client, resource P, mask []string) (P, error)
	Delete func(ctx context.Context, client *core.Client, name string) error
	// DeleteState is Delete for a resource whose delete takes options from
	// its state, such as a secret's force_delete, instead of Delete.
	DeleteState func(ctx context.Context, client *core.Client, state *M) error
	// FreesIDOnDelete says that deleting the resource, as its state sets
	// the delete's options, frees the id the client chose, so that a
	// replacement may keep it. Nil when a deleted resource's id is never
	// free.
	FreesIDOnDelete func(state *M) bool

	// Toggles are the resource's boolean attributes that methods of their
	// own set and clear, rather than an update.
	Toggles []Toggle[M, P]

	// Service is the short code of the service the resource is on, such as
	// "pvs": its full name is //{service}.origincloudapis.eu/{name}.
	Service string
	// References are the attributes of the state that hold another
	// resource's name, and the short code of the service that resource is
	// on, for the upgrade of a state that held them relative.
	References map[string]string
	// Parent is the attribute that holds the resource's parent's full name,
	// for a child of another resource such as a security group's rule; the
	// state of version 1 held the parent's bare id.
	Parent string
	// Oneofs are the members of each oneof among the top-level
	// attributes; the state of version 2 held a member that isn't the one
	// set as its zero value when its fields weren't a oneof yet.
	Oneofs [][]string

	// Pattern is the pattern of the resource's names, which an imported
	// name must have, such as projects/{project}/zones/{zone}/volumes/{volume}.
	Pattern string

	// ClientID returns the id the client chooses (AIP-133), for a
	// resource that has one, such as a project's project_id; IDAttribute
	// is its attribute, and Singular names the resource in messages.
	ClientID    func(*M) types.String
	IDAttribute string
	Singular    string
	// Replaced returns the attributes that replace the resource, of those
	// at the top level, from the planned and prior state and the
	// configuration. The framework doesn't tell a resource's ModifyPlan
	// which its attributes' plan modifiers replace it for.
	Replaced func(plan, state, config *M) []string

	// A singleton, which exists with its parent, is created by updating
	// it; Terraform owns the fields it set, which it records. Configured
	// returns the fields a configuration sets; Release plans null those of
	// owned that the configuration no longer sets, which unsets them; Clear
	// unsets fields, when the singleton is destroyed. Nil for any other
	// resource.
	Configured func(config *M) []string
	Release    func(plan, config *M, owned []string)
	Clear      func(ctx context.Context, client *core.Client, name string, fields []string) error

	// Spelled are the attributes the API answers in a form of its own,
	// such as a role binding's member (Spelling).
	Spelled []Spelled[M]

	// RefusesReplays says that the API refuses a create repeated with its
	// request id with CONFLICT, naming in the error's metadata the
	// resource the first one created, as for a resource with a secret,
	// which only the first answer carries.
	RefusesReplays bool

	// Revision is set for a resource that keeps a history of one field.
	Revision *Revision[P]

	// MovedFrom are the types this one replaces, as the API renamed them.
	MovedFrom []Move
}

// A Toggle is a boolean attribute that a pair of custom methods (AIP-136)
// sets and clears, such as a snapshot's protected (ProtectSnapshot and
// UnprotectSnapshot). Creates and updates call the method that brings the
// resource to the configured value, and wait on its operation; a value
// left out changes nothing.
type Toggle[M any, P proto.Message] struct {
	// Planned returns the attribute's value in a plan.
	Planned func(*M) types.Bool
	// IsSet and IsClear say that the resource is set or clear. It can be
	// neither, while a change is under way or after one failed.
	IsSet, IsClear func(P) bool
	Set, Clear     func(ctx context.Context, client *core.Client, name string) (*core.Operation[P], error)
	// ClearBeforeDelete clears a resource that isn't clear before deleting
	// it, as the API refuses to delete a protected snapshot.
	ClearBeforeDelete bool
}

// NewResource returns the constructor of the resource spec describes.
func NewResource[M any, P proto.Message](spec ResourceSpec[M, P]) func() resource.Resource {
	spec = reviseSpec(spec)
	return func() resource.Resource { return &Resource[M, P]{spec: spec} }
}

// A Resource is a Terraform resource over one API resource type.
type Resource[M any, P proto.Message] struct {
	spec   ResourceSpec[M, P]
	client *core.Client
}

var (
	_ resource.ResourceWithConfigure    = (*Resource[struct{}, proto.Message])(nil)
	_ resource.ResourceWithImportState  = (*Resource[struct{}, proto.Message])(nil)
	_ resource.ResourceWithModifyPlan   = (*Resource[struct{}, proto.Message])(nil)
	_ resource.ResourceWithUpgradeState = (*Resource[struct{}, proto.Message])(nil)
	_ resource.ResourceWithMoveState    = (*Resource[struct{}, proto.Message])(nil)
)

func (r *Resource[M, P]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.spec.TypeName
}

func (r *Resource[M, P]) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = r.spec.Schema
	resp.Schema.Version = StateVersion
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
	var created P
	if r.spec.CreateSync != nil {
		var err error
		if created, err = r.spec.CreateSync(ctx, r.client, &m); err != nil {
			// A create the API refuses as a repeat of one that created the
			// resource, as when the SDK sent it again after losing the
			// answer, names the resource; it goes into the state, tainted.
			detail := err.Error()
			if r.keepCreated(ctx, r.replayedCreate(err), m, resp) {
				detail += "\n\nThe state holds that resource, tainted: the next apply replaces it."
			}
			resp.Diagnostics.AddError(r.summary("create"), detail)
			return
		}
		r.created(ctx, m, created, resp)
		r.ownIfDone(ctx, req.Config, resp.Private, &resp.Diagnostics)
		return
	}
	op, err := r.spec.Create(ctx, r.client, &m)
	if err != nil {
		resp.Diagnostics.AddError(r.summary("create"), err.Error())
		return
	}
	created, err = op.Wait(ctx)
	if err != nil {
		resp.Diagnostics.AddError(r.summary("create"), err.Error())
		// A create that fails after the resource exists, such as a volume
		// whose storage couldn't be created, leaves it to delete.
		r.keepCreated(ctx, OperationTarget(op.Proto()), m, resp)
		return
	}
	r.created(ctx, m, created, resp)
	r.ownIfDone(ctx, req.Config, resp.Private, &resp.Diagnostics)
}

// keepCreated sets the state from the resource named target, which a
// failed create left behind, m being the planned state: Terraform marks
// it tainted and replaces it. It says whether it did; it doesn't when
// target is empty or the resource is gone.
func (r *Resource[M, P]) keepCreated(ctx context.Context, target string, m M, resp *resource.CreateResponse) bool {
	if target == "" {
		return false
	}
	created, err := r.spec.Get(ctx, r.client, target)
	if err != nil || (r.spec.Gone != nil && r.spec.Gone(created)) {
		return false
	}
	plan := m
	if r.fromProto(ctx, created, &plan, &m, resp.Private, &resp.Diagnostics) != nil {
		return false
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	return true
}

// replayedCreate returns the resource a create's CONFLICT names in its
// metadata, a resource of this type, which an earlier request with the
// same request id created, for a resource whose API refuses replays; ""
// for any other error. Only a resource whose name the API chooses counts:
// a CONFLICT naming one whose id the client chooses may be about someone
// else's.
func (r *Resource[M, P]) replayedCreate(err error) string {
	var apiErr *core.Error
	if !r.spec.RefusesReplays || r.spec.ClientID != nil || r.spec.Pattern == "" ||
		!errors.As(err, &apiErr) || apiErr.Reason != "CONFLICT" {
		return ""
	}
	var named []string
	for _, v := range apiErr.Metadata {
		if _, err := NameValues(r.spec.Pattern, v); err == nil {
			named = append(named, v)
		}
	}
	if len(named) != 1 {
		return ""
	}
	return named[0]
}

// created sets the state from the resource a create answered with, plan
// being the planned state, after setting its toggles.
func (r *Resource[M, P]) created(ctx context.Context, m M, created P, resp *resource.CreateResponse) {
	plan := m
	if err := r.fromProto(ctx, created, &plan, &m, resp.Private, &resp.Diagnostics); err != nil {
		resp.Diagnostics.AddError(r.summary("read the created"), err.Error())
		return
	}
	toggled, err := r.toggle(ctx, &plan, r.spec.Name(&m), created)
	if err != nil {
		// The resource exists: it goes into the state as it is, tainted.
		resp.Diagnostics.AddError(r.summary("create"), err.Error())
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	if err := r.fromProto(ctx, toggled, &plan, &m, resp.Private, &resp.Diagnostics); err != nil {
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
	prior := m
	if err := r.fromProto(ctx, got, &prior, &m, resp.Private, &resp.Diagnostics); err != nil {
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
	toggled, toggleErr := r.toggle(ctx, &plan, r.spec.Name(&state), got)
	planned := plan
	if err := r.fromProto(ctx, toggled, &planned, &plan, resp.Private, &resp.Diagnostics); err != nil {
		resp.Diagnostics.AddError(r.summary("read the updated"), err.Error())
		return
	}
	if toggleErr != nil {
		resp.Diagnostics.AddError(r.summary("update"), toggleErr.Error())
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	r.ownIfDone(ctx, req.Config, resp.Private, &resp.Diagnostics)
}

func (r *Resource[M, P]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m M
	if resp.Diagnostics.Append(req.State.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	name := r.spec.Name(&m)
	if r.spec.Clear != nil {
		// A singleton stays: what Terraform set is unset.
		owned, diags := owned(ctx, req.Private)
		if resp.Diagnostics.Append(diags...); resp.Diagnostics.HasError() || len(owned) == 0 {
			return
		}
		if err := r.spec.Clear(ctx, r.client, name, owned); err != nil && !NotFound(err) {
			resp.Diagnostics.AddError(r.summary("unset"), err.Error())
		}
		return
	}
	gone, err := r.clearBeforeDelete(ctx, name)
	if err != nil {
		if !NotFound(err) {
			resp.Diagnostics.AddError(r.summary("delete"), err.Error())
		}
		return
	}
	if gone {
		return
	}
	if err := r.delete(ctx, name, &m); err != nil && !NotFound(err) {
		// Gone meanwhile, such as an instance terminated while its delete
		// was refused, it is deleted all the same.
		if r.spec.Gone != nil {
			if got, getErr := r.spec.Get(ctx, r.client, name); NotFound(getErr) || getErr == nil && r.spec.Gone(got) {
				return
			}
		}
		resp.Diagnostics.AddError(r.summary("delete"), err.Error())
	}
}

// delete calls the spec's delete, with the options of state when it takes
// them.
func (r *Resource[M, P]) delete(ctx context.Context, name string, state *M) error {
	if r.spec.DeleteState != nil {
		return r.spec.DeleteState(ctx, r.client, state)
	}
	return r.spec.Delete(ctx, r.client, name)
}

// fromProto sets m from got, the resource the API answered with, over
// prior, the plan or the state m held before: the attributes the API
// spells in a form of its own keep their values in prior while got names
// the same thing, and the private state records got's.
func (r *Resource[M, P]) fromProto(ctx context.Context, got P, prior, m *M, private privateWriter, diags *diag.Diagnostics) error {
	if err := r.spec.FromProto(got, m); err != nil {
		return err
	}
	r.respell(ctx, prior, m, private, diags)
	return nil
}

// toggle brings the resource named name, which is got, to the toggles'
// values in plan, and returns it after. On an error it returns the
// resource as the last change left it.
func (r *Resource[M, P]) toggle(ctx context.Context, plan *M, name string, got P) (P, error) {
	for _, t := range r.spec.Toggles {
		want := t.Planned(plan)
		if want.IsNull() || want.IsUnknown() {
			continue
		}
		change := t.Set
		switch {
		case want.ValueBool() && !t.IsSet(got):
		case !want.ValueBool() && !t.IsClear(got):
			change = t.Clear
		default:
			continue
		}
		op, err := change(ctx, r.client, name)
		if err != nil {
			return got, err
		}
		changed, err := op.Wait(ctx)
		if err != nil {
			return got, err
		}
		got = changed
	}
	return got, nil
}

// clearBeforeDelete clears the toggles that must be clear before the
// resource is deleted.
// It also says whether the resource is gone already, in a terminal state,
// which leaves nothing to delete.
func (r *Resource[M, P]) clearBeforeDelete(ctx context.Context, name string) (bool, error) {
	clear := slices.ContainsFunc(r.spec.Toggles, func(t Toggle[M, P]) bool { return t.ClearBeforeDelete })
	if !clear && r.spec.Gone == nil {
		return false, nil
	}
	got, err := r.spec.Get(ctx, r.client, name)
	if err != nil {
		return false, err
	}
	if r.spec.Gone != nil && r.spec.Gone(got) {
		return true, nil
	}
	for _, t := range r.spec.Toggles {
		if !t.ClearBeforeDelete || t.IsClear(got) {
			continue
		}
		op, err := t.Clear(ctx, r.client, name)
		if err != nil {
			return false, err
		}
		if got, err = op.Wait(ctx); err != nil {
			return false, err
		}
	}
	return false, nil
}

// ImportState adopts a resource by its full resource name, such as
// //pvs.origincloudapis.eu/projects/my-project/zones/eu-central1-az1/volumes/vol-1,
// or by its relative one, without the host: Terraform reads the rest after.
func (r *Resource[M, P]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	name, err := r.importName(req.ID)
	if err != nil {
		resp.Diagnostics.AddError(r.summary("import"), err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

// importName returns the relative name of the resource an import names, in
// either form; it must have the resource's pattern.
func (r *Resource[M, P]) importName(id string) (string, error) {
	name := id
	if r.spec.Service != "" {
		var err error
		if name, err = RelativeName(r.spec.Service, id); err != nil {
			return "", err
		}
	}
	if r.spec.Pattern != "" {
		if _, err := NameValues(r.spec.Pattern, name); err != nil {
			return "", err
		}
	}
	return name, nil
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
