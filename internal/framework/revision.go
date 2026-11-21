package framework

import (
	"context"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"go.origincloud.eu/sdk/core"
	"google.golang.org/protobuf/proto"
)

// A Revision is how a resource keeps its history (AIP-162): one of its
// fields, such as a launch template's recipe, is never changed in place; a
// change adds a revision of it, numbered by the API, and the resource shows
// the newest.
//
// The API names a resource read at a revision with the revision's number,
// `projects/p/launchTemplates/web@3`, where the resource's own name has
// none: the state holds the name without it.
type Revision[P proto.Message] struct {
	// Field is the revised attribute: a change to it adds a revision.
	Field string
	// Latest is appended to the resource's name to read its newest
	// revision, such as "@latest".
	Latest string

	// Name returns the resource's name in the API's message.
	Name func(P) string
	// Unversioned sets a message's name to the resource's own.
	Unversioned func(P)
	// Revise adds a revision holding resource's revised field, and returns
	// the resource at it.
	Revise func(ctx context.Context, client *core.Client, resource P) (P, error)
	// Follow sets, in resource, which an update is about to send, the
	// attribute that points at a revision, such as a template's default
	// version, to the new one when the configuration leaves it out.
	Follow func(resource, revised P)
}

// WithoutRevision returns a resource name without the revision it may be
// read at: `projects/p/launchTemplates/web@3` is `projects/p/launchTemplates/web`.
func WithoutRevision(name string) string {
	name, _, _ = strings.Cut(name, "@")
	return name
}

// reviseSpec makes the spec's calls read and write the resource through
// its revisions: a read is of the newest, and a change of the revised field
// adds one.
func reviseSpec[M any, P proto.Message](spec ResourceSpec[M, P]) ResourceSpec[M, P] {
	rev := spec.Revision
	if rev == nil {
		return spec
	}
	get := spec.Get
	spec.Get = func(ctx context.Context, client *core.Client, name string) (P, error) {
		got, err := get(ctx, client, WithoutRevision(name)+rev.Latest)
		if err == nil {
			rev.Unversioned(got)
		}
		return got, err
	}
	if create := spec.CreateSync; create != nil {
		spec.CreateSync = func(ctx context.Context, client *core.Client, plan *M) (P, error) {
			created, err := create(ctx, client, plan)
			if err == nil {
				rev.Unversioned(created)
			}
			return created, err
		}
	}
	if update := spec.Update; update != nil {
		reread := spec.Get
		spec.Update = func(ctx context.Context, client *core.Client, resource P, mask []string) (P, error) {
			if i := slices.Index(mask, rev.Field); i >= 0 {
				mask = slices.Delete(slices.Clone(mask), i, i+1)
				revised, err := rev.Revise(ctx, client, resource)
				if err != nil {
					return revised, err
				}
				rev.Follow(resource, revised)
			}
			if len(mask) > 0 {
				if _, err := update(ctx, client, resource, mask); err != nil {
					return resource, err
				}
			}
			// What the API answers with is the default revision's.
			return reread(ctx, client, rev.Name(resource))
		}
	}
	return spec
}

// Int64FollowsRevision is the plan modifier of an attribute that points at
// one of a resource's revisions, such as a template's default version,
// optional and computed: set, it is what the configuration says; left out,
// it is the API's choice, which is a new revision whenever field changes,
// and what it was before otherwise.
func Int64FollowsRevision(field string) planmodifier.Int64 { return followsRevision{field: field} }

type followsRevision struct{ field string }

func (m followsRevision) Description(context.Context) string {
	return "Left out, it is the new version whenever `" + m.field + "` changes, and what it was otherwise."
}

func (m followsRevision) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (m followsRevision) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if !req.ConfigValue.IsNull() {
		return
	}
	if req.State.Raw.IsNull() {
		resp.PlanValue = types.Int64Unknown()
		return
	}
	var plan, state types.Object
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(m.field), &plan)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(m.field), &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Equal(state) {
		resp.PlanValue = req.StateValue
	} else {
		resp.PlanValue = types.Int64Unknown()
	}
}
