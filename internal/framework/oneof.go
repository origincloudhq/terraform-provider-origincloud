package framework

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A Oneof validates an attribute that is a member of a oneof: no other
// member, Others, may be set with it. Required, on the first member only,
// also checks that one is set.
type Oneof struct {
	Others   []string
	Required bool
}

var (
	_ validator.String = Oneof{}
	_ validator.Int64  = Oneof{}
	_ validator.Bool   = Oneof{}
	_ validator.Object = Oneof{}
	_ validator.List   = Oneof{}
	_ validator.Set    = Oneof{}
	_ validator.Map    = Oneof{}
)

func (v Oneof) Description(context.Context) string {
	if v.Required {
		return "exactly one of this and " + strings.Join(v.Others, ", ") + " must be set"
	}
	return "at most one of this and " + strings.Join(v.Others, ", ") + " may be set"
}

func (v Oneof) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v Oneof) validate(ctx context.Context, config tfsdk.Config, p path.Path, value attr.Value, diags *diag.Diagnostics) {
	if value.IsUnknown() {
		return
	}
	var set []string
	for _, other := range v.Others {
		var o attr.Value
		if d := config.GetAttribute(ctx, p.ParentPath().AtName(other), &o); d.HasError() || o == nil {
			continue
		}
		if o.IsUnknown() {
			return // can't tell yet
		}
		if !o.IsNull() {
			set = append(set, other)
		}
	}
	switch {
	case !value.IsNull() && len(set) > 0:
		diags.AddAttributeError(p, "Conflicting oneof members", fmt.Sprintf("%s: %s is set too", v.Description(ctx), strings.Join(set, ", ")))
	case v.Required && value.IsNull() && len(set) == 0:
		diags.AddAttributeError(p, "Missing oneof member", v.Description(ctx))
	}
}

func (v Oneof) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	v.validate(ctx, req.Config, req.Path, req.ConfigValue, &resp.Diagnostics)
}

func (v Oneof) ValidateInt64(ctx context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	v.validate(ctx, req.Config, req.Path, req.ConfigValue, &resp.Diagnostics)
}

func (v Oneof) ValidateBool(ctx context.Context, req validator.BoolRequest, resp *validator.BoolResponse) {
	v.validate(ctx, req.Config, req.Path, req.ConfigValue, &resp.Diagnostics)
}

func (v Oneof) ValidateObject(ctx context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	v.validate(ctx, req.Config, req.Path, req.ConfigValue, &resp.Diagnostics)
}

func (v Oneof) ValidateList(ctx context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	v.validate(ctx, req.Config, req.Path, req.ConfigValue, &resp.Diagnostics)
}

func (v Oneof) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	v.validate(ctx, req.Config, req.Path, req.ConfigValue, &resp.Diagnostics)
}

func (v Oneof) ValidateMap(ctx context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	v.validate(ctx, req.Config, req.Path, req.ConfigValue, &resp.Diagnostics)
}

// OneofOthers plans a oneof member that the state keeps, such as an
// immutable one the API chose, as null when the configuration sets
// another member, Others: the request would otherwise send the kept one.
type OneofOthers []string

var (
	_ planmodifier.String = OneofOthers{}
	_ planmodifier.Int64  = OneofOthers{}
	_ planmodifier.Bool   = OneofOthers{}
	_ planmodifier.Object = OneofOthers{}
)

func (o OneofOthers) Description(context.Context) string {
	return "null while another of " + strings.Join(o, ", ") + " is set"
}

func (o OneofOthers) MarkdownDescription(ctx context.Context) string { return o.Description(ctx) }

// otherSet says that the configuration sets another member than the one
// at p, whose configured value is config.
func (o OneofOthers) otherSet(ctx context.Context, cfg tfsdk.Config, p path.Path, config attr.Value) bool {
	if !config.IsNull() {
		return false
	}
	for _, other := range o {
		var v attr.Value
		if d := cfg.GetAttribute(ctx, p.ParentPath().AtName(other), &v); !d.HasError() && v != nil && !v.IsNull() {
			return true
		}
	}
	return false
}

func (o OneofOthers) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if o.otherSet(ctx, req.Config, req.Path, req.ConfigValue) {
		resp.PlanValue = types.StringNull()
	}
}

func (o OneofOthers) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if o.otherSet(ctx, req.Config, req.Path, req.ConfigValue) {
		resp.PlanValue = types.Int64Null()
	}
}

func (o OneofOthers) PlanModifyBool(ctx context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if o.otherSet(ctx, req.Config, req.Path, req.ConfigValue) {
		resp.PlanValue = types.BoolNull()
	}
}

func (o OneofOthers) PlanModifyObject(ctx context.Context, req planmodifier.ObjectRequest, resp *planmodifier.ObjectResponse) {
	if o.otherSet(ctx, req.Config, req.Path, req.ConfigValue) {
		resp.PlanValue = types.ObjectNull(req.PlanValue.AttributeTypes(ctx))
	}
}
