package framework

import (
	"context"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A resource that can be launched from a recipe, such as an instance from a
// launch template, takes the recipe's fields from it: an attribute the
// configuration leaves out is the template's, which the API answers with.
// Without a template, the attributes the API requires are required.

// templateSet says whether the configuration's launch template attribute
// is set: ok is false while it isn't known yet.
func templateSet(ctx context.Context, config tfsdk.Config, attribute string) (set, ok bool) {
	var template types.String
	if diags := config.GetAttribute(ctx, path.Root(attribute), &template); diags.HasError() || template.IsUnknown() {
		return false, false
	}
	return !template.IsNull() && template.ValueString() != "", true
}

// RequiredUnless validates an attribute that is required unless the
// attribute named Template is set, as a launch template supplies it.
type RequiredUnless struct{ Template string }

var (
	_ validator.String = RequiredUnless{}
	_ validator.List   = RequiredUnless{}
)

func (v RequiredUnless) Description(context.Context) string {
	return "required unless `" + v.Template + "` is set"
}

func (v RequiredUnless) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v RequiredUnless) validate(ctx context.Context, config tfsdk.Config, p path.Path, value attr.Value, summary *validator.StringResponse, list *validator.ListResponse) {
	if !value.IsNull() {
		return
	}
	if set, ok := templateSet(ctx, config, v.Template); !ok || set {
		return
	}
	detail := "Missing required argument: " + p.String() + " is required unless " + v.Template + " is set."
	if summary != nil {
		summary.Diagnostics.AddAttributeError(p, "Missing required argument", detail)
	} else {
		list.Diagnostics.AddAttributeError(p, "Missing required argument", detail)
	}
}

func (v RequiredUnless) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	v.validate(ctx, req.Config, req.Path, req.ConfigValue, resp, nil)
}

func (v RequiredUnless) ValidateList(ctx context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	v.validate(ctx, req.Config, req.Path, req.ConfigValue, nil, resp)
}

// TemplateSpelling is a launch template's name as a launch is asked for it
// and as the instance answers with it: `.../launchTemplates/web`,
// `.../web@default` and `.../web@latest` are answered with the version the
// launch resolved them to, `.../web@3`. Two names are the same unless they
// are of different templates, or of different version numbers: which
// version a bare name, `@default` or `@latest` is, is only known at the
// launch. It is never renamed.
var TemplateSpelling = Spelling{
	Equal: func(a, b string) bool {
		if a == b {
			return true
		}
		nameA, versionA, _ := strings.Cut(a, "@")
		nameB, versionB, _ := strings.Cut(b, "@")
		return nameA == nameB && !(numbered(versionA) && numbered(versionB))
	},
	Renamed: func(string, string) string { return "" },
}

// numbered says that a version selector is a number.
func numbered(version string) bool {
	_, err := strconv.Atoi(version)
	return err == nil
}

// StringReplaceTemplate is the plan modifier of an instance's launch
// template: a change replaces the instance unless it names the template
// and version the instance was launched from. The version a bare name,
// `@default` or `@latest` resolved to is what the API last answered with,
// which the private state keeps.
func StringReplaceTemplate(s Spelling) planmodifier.String {
	const description = "Changing it to another template or version replaces the resource."
	return stringplanmodifier.RequiresReplaceIf(func(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		if req.PlanValue.IsUnknown() || !IsSet(req.StateValue) {
			resp.RequiresReplace = true
			return
		}
		planned := req.PlanValue.ValueString()
		if planned == req.StateValue.ValueString() {
			return
		}
		var answer *string
		if req.Private != nil {
			var diags diag.Diagnostics
			answer, diags = answered(ctx, req.Private, req.Path.String())
			resp.Diagnostics.Append(diags...)
		}
		if answer == nil {
			resp.RequiresReplace = !s.Equal(req.StateValue.ValueString(), planned)
			return
		}
		resp.RequiresReplace = !s.Equal(*answer, planned)
	}, description, description)
}
