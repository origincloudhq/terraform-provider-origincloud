package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

// ownedKey is the private state's key of the fields of a singleton that
// Terraform set.
const ownedKey = "owned"

// privateState is the private state of a resource, as a request or a
// response holds it.
type privateState interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

type privateWriter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// owned returns the fields of a singleton that Terraform set.
func owned(ctx context.Context, private privateState) ([]string, diag.Diagnostics) {
	data, diags := private.GetKey(ctx, ownedKey)
	if diags.HasError() || len(data) == 0 {
		return nil, diags
	}
	var fields []string
	if err := json.Unmarshal(data, &fields); err != nil {
		diags.AddError("Unreadable private state", err.Error())
	}
	return fields, diags
}

// own records the fields of a singleton the configuration sets, which
// Terraform owns from now on.
func (r *Resource[M, P]) own(ctx context.Context, config tfsdk.Config, private privateWriter) diag.Diagnostics {
	if r.spec.Configured == nil {
		return nil
	}
	var m M
	diags := config.Get(ctx, &m)
	if diags.HasError() {
		return diags
	}
	fields := r.spec.Configured(&m)
	if fields == nil {
		fields = []string{}
	}
	data, _ := json.Marshal(fields)
	return append(diags, private.SetKey(ctx, ownedKey, data)...)
}

// ModifyPlan plans null the fields of a singleton that Terraform set and
// the configuration no longer does, which unsets them; and refuses to
// replace a resource with the id the client chose, which a deleted one
// keeps: the create would fail, and the resource be gone. A replacement
// forced from the command line (-replace) isn't planned by the provider,
// and isn't refused.
func (r *Resource[M, P]) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return // a create or a destroy
	}
	var plan, state, config M
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.spec.Release != nil {
		fields, diags := owned(ctx, req.Private)
		if resp.Diagnostics.Append(diags...); resp.Diagnostics.HasError() {
			return
		}
		if len(fields) > 0 {
			r.spec.Release(&plan, &config, fields)
			resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		}
	}
	if r.spec.ClientID != nil && r.spec.Replaced != nil {
		id := r.spec.ClientID(&plan)
		// An optional id left out is the API's choice again.
		if changed := r.spec.Replaced(&plan, &state, &config); len(changed) > 0 &&
			!r.spec.ClientID(&config).IsNull() && !id.IsUnknown() && id.Equal(r.spec.ClientID(&state)) {
			resp.Diagnostics.AddAttributeError(path.Root(changed[0]), "Can't replace with the same id", fmt.Sprintf(
				"Changing %s replaces the %s, and a deleted %[2]s's id can't be used again: choose a new %[3]s.",
				strings.Join(changed, ", "), r.spec.Singular, r.spec.IDAttribute))
		}
	}
}
