package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

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
// held the relative name as id, and a urn; the upgrade from it stays, as the
// one reader of the old identifier forms.
const StateVersion = 1

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

// UpgradeState upgrades the states of version 0, which Terraform hands over
// as JSON of the old schema: what it holds that the schema no longer has is
// dropped, and what the schema has that it doesn't is null.
func (r *Resource[M, P]) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: r.upgradeFromVersion0},
	}
}

func (r *Resource[M, P]) upgradeFromVersion0(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.RawState == nil || req.RawState.JSON == nil {
		resp.Diagnostics.AddError(r.summary("upgrade the state of"), "the state has no JSON form")
		return
	}
	upgraded, err := UpgradeRawState(r.spec.Service, r.spec.References, req.RawState.JSON)
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

// FullNameReference validates that a string attribute that refers to
// another resource holds its full resource name: a reference to another
// service's resource, or another project's, is written as the `id` of the
// resource it refers to, never as its `name`, which is relative.
func FullNameReference() validator.String { return fullNameValidator{} }

type fullNameValidator struct{}

func (fullNameValidator) Description(context.Context) string {
	return "value must be a full resource name, //{service}.origincloudapis.eu/{name}; use the `id` of the resource it refers to"
}

func (v fullNameValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v fullNameValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || req.ConfigValue.ValueString() == "" {
		return
	}
	if _, err := core.ParseFullName(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Not a full resource name", fmt.Sprintf("%s, got %q: %s", v.Description(ctx), req.ConfigValue.ValueString(), err))
	}
}
