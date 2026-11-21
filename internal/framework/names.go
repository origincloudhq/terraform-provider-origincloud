package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"go.origincloud.eu/sdk/core"
)

// Every resource is identified by its full resource name (AIP-122),
// //{service}.origincloudapis.eu/{relative name}, which is its id and what
// every reference to it carries. Its name attribute stays the relative
// name the API answers with.

// StateVersion is the version of every resource's state schema. Version 0
// held the relative name as id, and a urn; version 1 held a child's parent
// attribute as the parent's bare id. The upgrades from them stay, as the
// readers of the old forms.
const StateVersion = 2

// FullName returns the full resource name of the resource of service, a
// short code such as "pvs", whose relative name the API answered with. A
// name that is full already is returned as it is.
func FullName(service, name string) (types.String, error) {
	full, err := fullName(service, name)
	if err != nil {
		return types.StringNull(), err
	}
	return types.StringValue(full), nil
}

func fullName(service, name string) (string, error) {
	// A resource read at one of its revisions is named with it, which its
	// own name hasn't.
	name = WithoutRevision(name)
	if strings.HasPrefix(name, "//") {
		parsed, err := core.ParseFullName(name)
		if err != nil {
			return "", err
		}
		return parsed.String(), nil
	}
	if !slices.Contains(core.ResourceServices(), service) {
		// A service the SDK doesn't know, such as the plugin's test API.
		return "//" + service + "." + core.Domain + "/" + name, nil
	}
	parsed, err := core.FullNameFromRelative(service, name)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

// RelativeName returns the relative name of a name of the resources of
// service, which may be given in either form, as an import does. A full
// name of another service is refused.
func RelativeName(service, name string) (string, error) {
	if !strings.HasPrefix(name, "//") {
		return name, nil
	}
	host := service + "." + core.Domain
	relative, ok := strings.CutPrefix(name, "//"+host+"/")
	if !ok {
		return "", fmt.Errorf("%q isn't a full name on %s, where a resource of this type is named", name, host)
	}
	return relative, nil
}

// UpgradeRawState upgrades the JSON of a state written by version 0 of a
// resource's schema, whose id was its relative name and which had a urn:
// the id becomes the full name of a resource of service, the urn is
// dropped, and each of references, an attribute that holds another
// resource's name and the service that resource is on, is written as a full
// name. A value that isn't a relative name is left as it is.
func UpgradeRawState(service string, references map[string]string, raw []byte) ([]byte, error) {
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("the state isn't a JSON object: %w", err)
	}
	delete(state, "urn")
	upgrade := func(attribute, service string) error {
		var value *string
		if err := json.Unmarshal(state[attribute], &value); err != nil || value == nil || *value == "" {
			return nil // absent, null, empty or not a string
		}
		full, err := fullName(service, *value)
		if err != nil {
			return nil
		}
		encoded, err := json.Marshal(full)
		if err != nil {
			return err
		}
		state[attribute] = encoded
		return nil
	}
	if err := upgrade("id", service); err != nil {
		return nil, err
	}
	for attribute, referred := range references {
		if err := upgrade(attribute, referred); err != nil {
			return nil, err
		}
	}
	return json.Marshal(state)
}

// UpgradeRawParent upgrades the JSON of a state whose parent attribute,
// the one that names the resource the state's resource is a child of, held
// the parent's bare id: it becomes the parent's full name, which the
// resource's own name, the state's, gives. A parent that is a full name
// already, and a state without a name, are left as they are.
func UpgradeRawParent(service, parent string, raw []byte) ([]byte, error) {
	if parent == "" {
		return raw, nil
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("the state isn't a JSON object: %w", err)
	}
	var name, held *string
	if err := json.Unmarshal(state["name"], &name); err != nil || name == nil {
		return raw, nil
	}
	if err := json.Unmarshal(state[parent], &held); err != nil || held == nil || strings.HasPrefix(*held, "//") {
		return raw, nil
	}
	full := ParentOf(service, RelativeResourceName(service, *name))
	if full.IsNull() {
		return raw, nil
	}
	encoded, err := json.Marshal(full.ValueString())
	if err != nil {
		return nil, err
	}
	state[parent] = encoded
	return json.Marshal(state)
}

// RelativeResourceName returns name, which may be full, as a relative name;
// one that isn't of service's is returned as it is.
func RelativeResourceName(service, name string) string {
	if relative, err := RelativeName(service, name); err == nil {
		return relative
	}
	return name
}

// UpgradeState upgrades the states of versions 0 and 1, which Terraform
// hands over as JSON of the old schema: what it holds that the schema no
// longer has is dropped, and what the schema has that it doesn't is null.
func (r *Resource[M, P]) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: r.upgradeFromVersion0},
		1: {StateUpgrader: r.upgradeFromVersion1},
	}
}

func (r *Resource[M, P]) upgradeFromVersion0(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	r.upgrade(ctx, req, resp, func(raw []byte) ([]byte, error) {
		upgraded, err := UpgradeRawState(r.spec.Service, r.spec.References, raw)
		if err != nil {
			return nil, err
		}
		return UpgradeRawParent(r.spec.Service, r.spec.Parent, upgraded)
	})
}

func (r *Resource[M, P]) upgradeFromVersion1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	r.upgrade(ctx, req, resp, func(raw []byte) ([]byte, error) {
		return UpgradeRawParent(r.spec.Service, r.spec.Parent, raw)
	})
}

func (r *Resource[M, P]) upgrade(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse, rewrite func([]byte) ([]byte, error)) {
	if req.RawState == nil || req.RawState.JSON == nil {
		resp.Diagnostics.AddError(r.summary("upgrade the state of"), "the state has no JSON form")
		return
	}
	upgraded, err := rewrite(req.RawState.JSON)
	if err != nil {
		resp.Diagnostics.AddError(r.summary("upgrade the state of"), err.Error())
		return
	}
	typ := r.spec.Schema.Type().TerraformType(ctx)
	value, err := (&tfprotov6.RawState{JSON: upgraded}).UnmarshalWithOpts(typ, tfprotov6.UnmarshalOpts{
		ValueFromJSONOpts: tftypes.ValueFromJSONOpts{IgnoreUndefinedAttributes: true},
	})
	if err != nil {
		resp.Diagnostics.AddError(r.summary("upgrade the state of"), err.Error())
		return
	}
	dynamic, err := tfprotov6.NewDynamicValue(typ, value)
	if err != nil {
		resp.Diagnostics.AddError(r.summary("upgrade the state of"), err.Error())
		return
	}
	resp.DynamicValue = &dynamic
}

// VersionedFullNameReference is FullNameReference for a reference that may
// name a revision of the resource it refers to, `{name}@3`, `{name}@default`
// or `{name}@latest`.
func VersionedFullNameReference() validator.String { return fullNameValidator{versioned: true} }

// FullNameReference validates that a string attribute that refers to
// another resource holds its full resource name: a reference to another
// service's resource, or another project's, is written as the `id` of the
// resource it refers to, never as its `name`, which is relative.
func FullNameReference() validator.String { return fullNameValidator{} }

type fullNameValidator struct{ versioned bool }

func (fullNameValidator) Description(context.Context) string {
	return "value must be a full resource name, //{service}.origincloudapis.eu/{name}; use the `id` of the resource it refers to"
}

func (v fullNameValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v fullNameValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || req.ConfigValue.ValueString() == "" {
		return
	}
	name := req.ConfigValue.ValueString()
	if v.versioned {
		name = WithoutRevision(name)
	}
	if _, err := core.ParseFullName(name); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Not a full resource name", fmt.Sprintf("%s, got %q: %s", v.Description(ctx), req.ConfigValue.ValueString(), err))
	}
}

// ParentOf returns the full name of the parent of the resource of service
// whose relative name is name: the name without its last two segments, a
// collection and an id, such as the security group of one of its rules.
// Null for a name that has no parent.
func ParentOf(service, name string) types.String {
	i := strings.LastIndex(name, "/")
	if i < 0 {
		return types.StringNull()
	}
	i = strings.LastIndex(name[:i], "/")
	if i < 0 {
		return types.StringNull()
	}
	full, err := FullName(service, name[:i])
	if err != nil {
		return types.StringNull()
	}
	return full
}

// ParentPath returns the relative name of the parent a child resource's
// parent attribute holds, as the requests that create or list the child
// take it. A value that isn't a full name, which the plan refused, is
// returned as it is.
func ParentPath(parent types.String) string {
	name := parent.ValueString()
	if parsed, err := core.ParseFullName(name); err == nil {
		return parsed.Relative()
	}
	// A service the SDK doesn't know, such as the plugin's test API.
	if rest, ok := strings.CutPrefix(name, "//"); ok {
		if _, relative, ok := strings.Cut(rest, "/"); ok {
			return relative
		}
	}
	return name
}

// ParentReference validates that a child resource's parent attribute holds
// the full resource name of its parent, which is what the parent's `id` is:
// of the service that has the parent, and of the parent's pattern, such as
// projects/{project}/regions/{region}/securityGroups/{security_group}. The
// parent's own place, its project and region here, are the child's
// attributes of the same names: when the configuration sets them, they
// must be the parent's. parent names the parent in the message.
func ParentReference(service, pattern, parent string) validator.String {
	return parentValidator{service: service, pattern: pattern, parent: parent}
}

type parentValidator struct{ service, pattern, parent string }

func (v parentValidator) Description(context.Context) string {
	return fmt.Sprintf("value must be the full resource name of the %s, //%s.%s/%s: use the `id` of the %[1]s, not its `name`", v.parent, v.service, core.Domain, v.pattern)
}

func (v parentValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v parentValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || req.ConfigValue.ValueString() == "" {
		return
	}
	value := req.ConfigValue.ValueString()
	fail := func(summary, detail string) {
		resp.Diagnostics.AddAttributeError(req.Path, summary, fmt.Sprintf("%s, got %q: %s", v.Description(ctx), value, detail))
	}
	relative, ok := "", false
	if slices.Contains(core.ResourceServices(), v.service) {
		parsed, err := core.ParseFullName(value)
		if err != nil {
			fail("Not a full resource name", err.Error())
			return
		}
		if parsed.Service() != v.service {
			fail("Not the full name of a "+v.parent, fmt.Sprintf("it is a name on %s", parsed.Host()))
			return
		}
		relative, ok = parsed.Relative(), true
	} else {
		// A service the SDK doesn't know, such as the plugin's test API.
		relative, ok = strings.CutPrefix(value, "//"+v.service+"."+core.Domain+"/")
		if !ok {
			fail("Not a full resource name", "it isn't a name on the service's host")
			return
		}
	}
	values, err := NameValues(v.pattern, relative)
	if err != nil {
		fail("Not the full name of a "+v.parent, err.Error())
		return
	}
	// The place the parent is in: its variables but its own, each an
	// attribute of the child.
	variables := patternVariables(v.pattern)
	for i, variable := range variables[:len(variables)-1] {
		var set types.String
		if resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(variable), &set)...); resp.Diagnostics.HasError() {
			return
		}
		if set.IsNull() || set.IsUnknown() || set.ValueString() == values[i] {
			continue
		}
		resp.Diagnostics.AddAttributeError(req.Path, "The "+v.parent+" is elsewhere", fmt.Sprintf("the %s is in %s %q, but %s is %q", v.parent, strings.ReplaceAll(variable, "_", " "), values[i], variable, set.ValueString()))
	}
}

// patternVariables returns the variables of a name's pattern: project and
// volume for projects/{project}/volumes/{volume}.
func patternVariables(pattern string) []string {
	var variables []string
	for _, segment := range strings.Split(pattern, "/") {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			variables = append(variables, segment[1:len(segment)-1])
		}
	}
	return variables
}
