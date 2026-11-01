package framework

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// OneOf validates that a string attribute an enum field is sent as names
// one of the enum's values.
func OneOf(values ...string) validator.String { return oneOfValidator{values} }

type oneOfValidator struct{ values []string }

func (v oneOfValidator) Description(context.Context) string {
	return "value must be one of " + strings.Join(v.values, ", ")
}

func (v oneOfValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v oneOfValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if s := req.ConfigValue.ValueString(); !slices.Contains(v.values, s) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", fmt.Sprintf("%s, got %q", v.Description(ctx), s))
	}
}

// An ElementCheck validates the elements of a list, set or map attribute:
// none may be null, as the repeated field it is sent as has no null
// element.
type ElementCheck struct {
	// OneOf, when set, are the values a string element may have: an enum's.
	OneOf []string
	// Int32 checks that a number element fits the int32 field it is sent as.
	Int32 bool
}

var (
	_ validator.List = ElementCheck{}
	_ validator.Set  = ElementCheck{}
	_ validator.Map  = ElementCheck{}
)

func (v ElementCheck) Description(context.Context) string {
	d := "elements must not be null"
	if len(v.OneOf) > 0 {
		d += " and must be one of " + strings.Join(v.OneOf, ", ")
	}
	if v.Int32 {
		d += fmt.Sprintf(" and must be between %d and %d", math.MinInt32, math.MaxInt32)
	}
	return d
}

func (v ElementCheck) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v ElementCheck) ValidateList(ctx context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for i, e := range req.ConfigValue.Elements() {
		v.check(ctx, req.Path.AtListIndex(i), e, &resp.Diagnostics)
	}
}

func (v ElementCheck) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, e := range req.ConfigValue.Elements() {
		v.check(ctx, req.Path.AtSetValue(e), e, &resp.Diagnostics)
	}
}

func (v ElementCheck) ValidateMap(ctx context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for k, e := range req.ConfigValue.Elements() {
		v.check(ctx, req.Path.AtMapKey(k), e, &resp.Diagnostics)
	}
}

func (v ElementCheck) check(ctx context.Context, p path.Path, e attr.Value, diags *diag.Diagnostics) {
	switch {
	case e.IsNull():
		diags.AddAttributeError(p, "Null element", v.Description(ctx))
	case e.IsUnknown():
	default:
		if s, ok := e.(types.String); ok && len(v.OneOf) > 0 && !slices.Contains(v.OneOf, s.ValueString()) {
			diags.AddAttributeError(p, "Invalid element", fmt.Sprintf("%s, got %q", v.Description(ctx), s.ValueString()))
		}
		if n, ok := e.(types.Int64); ok && v.Int32 && (n.ValueInt64() < math.MinInt32 || n.ValueInt64() > math.MaxInt32) {
			diags.AddAttributeError(p, "Element out of range", fmt.Sprintf("%s, got %d", v.Description(ctx), n.ValueInt64()))
		}
	}
}
