package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// A Move says that the resource type was named differently in an earlier
// release, as the API renamed it, so that a `moved` block from the old type
// to this one carries the state over.
type Move struct {
	// TypeName follows the provider's, as ResourceSpec.TypeName does, of
	// the type this one replaces, such as "_network_dns_policy".
	TypeName string
	// Attributes are the attributes the old type had under another name,
	// by the old name.
	Attributes map[string]string
	// Segments are the collection names of the old type's resource names
	// that the API renamed, by the old one: a name in the state, and a
	// reference to the resource, are rewritten to the new.
	Segments map[string]string
}

// MoveRawState returns the JSON of a state of the type m names, written for
// the type that replaces it: attributes and names are renamed, and
// everything else stays. What the new schema doesn't have is dropped when
// the state is read, and what it has that the old one hadn't is null.
func MoveRawState(m Move, raw []byte) ([]byte, error) {
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("the state isn't a JSON object: %w", err)
	}
	moved := make(map[string]json.RawMessage, len(state))
	for attribute, value := range state {
		var text *string
		if json.Unmarshal(value, &text) == nil && text != nil {
			for old, current := range m.Segments {
				*text = strings.ReplaceAll(*text, "/"+old+"/", "/"+current+"/")
			}
			encoded, err := json.Marshal(*text)
			if err != nil {
				return nil, err
			}
			value = encoded
		}
		if renamed, ok := m.Attributes[attribute]; ok {
			attribute = renamed
		}
		moved[attribute] = value
	}
	return json.Marshal(moved)
}

// MoveState carries the state of the types a resource replaces over to it,
// when a `moved` block says so.
func (r *Resource[M, P]) MoveState(context.Context) []resource.StateMover {
	var movers []resource.StateMover
	for _, m := range r.spec.MovedFrom {
		movers = append(movers, resource.StateMover{StateMover: func(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
			if req.SourceTypeName != providerTypeName+m.TypeName || !strings.HasSuffix(req.SourceProviderAddress, "/"+providerTypeName) {
				return // not this mover's source; another may take it
			}
			if req.SourceRawState == nil || req.SourceRawState.JSON == nil {
				resp.Diagnostics.AddError(r.summary("move the state of"), "the state has no JSON form")
				return
			}
			moved, err := MoveRawState(m, req.SourceRawState.JSON)
			if err == nil {
				// The type that was moved from held its parent as a bare id.
				moved, err = UpgradeRawParent(r.spec.Service, r.spec.Parent, moved)
			}
			if err != nil {
				resp.Diagnostics.AddError(r.summary("move the state of"), err.Error())
				return
			}
			typ := r.spec.Schema.Type().TerraformType(ctx)
			value, err := (&tfprotov6.RawState{JSON: moved}).UnmarshalWithOpts(typ, tfprotov6.UnmarshalOpts{
				ValueFromJSONOpts: tftypes.ValueFromJSONOpts{IgnoreUndefinedAttributes: true},
			})
			if err != nil {
				resp.Diagnostics.AddError(r.summary("move the state of"), err.Error())
				return
			}
			resp.TargetState.Raw = value
		}})
	}
	return movers
}
