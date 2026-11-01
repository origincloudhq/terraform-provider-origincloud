package framework

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
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
