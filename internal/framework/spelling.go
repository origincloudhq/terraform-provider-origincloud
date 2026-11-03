package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A Spelling is how the API answers a string attribute that it writes in
// a form of its own, such as a role binding's member: configured as
// user:Mail@Example.com, it is answered as user:mail@example.com, and,
// once the user's address changes, with the new one.
//
// The state keeps the value configured while the API's answer names the
// same thing, so that neither plans a change; and the private state keeps
// the API's last answer, so that a configuration changed to it, as the
// API now writes it, doesn't replace the resource either.
type Spelling struct {
	// Equal says that a and b are the same value written two ways.
	Equal func(a, b string) bool
	// Renamed says why answer, the API's, names the same thing as prior,
	// the value the resource was created with or the state kept, though
	// it isn't Equal to it; empty when it names something else, which the
	// state then takes.
	Renamed func(prior, answer string) string
}

// Spelled is a resource's top-level string attribute that the API answers
// in a form of its own, as its Spelling says.
type Spelled[M any] struct {
	Attribute string
	Spelling  Spelling
	// Value returns the attribute's value in a model.
	Value func(*M) *types.String
}

// answerKey is the private state's key of the API's last answer for a
// spelled attribute.
func answerKey(attribute string) string { return "answer:" + attribute }

// answered returns the API's last answer for the spelled attribute, from
// the private state; nil when none is recorded.
func answered(ctx context.Context, private privateState, attribute string) (*string, diag.Diagnostics) {
	data, diags := private.GetKey(ctx, answerKey(attribute))
	if diags.HasError() || len(data) == 0 {
		return nil, diags
	}
	var answer string
	if err := json.Unmarshal(data, &answer); err != nil {
		diags.AddError("Unreadable private state", err.Error())
		return nil, diags
	}
	return &answer, diags
}

// respell sets m's spelled attributes, which hold the API's answers, back
// to their values in prior, the plan or the state m was read over, while
// the answers name the same thing, and records the answers in the private
// state.
func (r *Resource[M, P]) respell(ctx context.Context, prior, m *M, private privateWriter, diags *diag.Diagnostics) {
	for _, s := range r.spec.Spelled {
		answer, kept := *s.Value(m), *s.Value(prior)
		data, _ := json.Marshal(answer.ValueString())
		diags.Append(private.SetKey(ctx, answerKey(s.Attribute), data)...)
		// Imported, the state has no value to keep.
		if !IsSet(answer) || !IsSet(kept) || answer.Equal(kept) {
			continue
		}
		if s.Spelling.Equal(kept.ValueString(), answer.ValueString()) {
			*s.Value(m) = kept
			continue
		}
		if why := s.Spelling.Renamed(kept.ValueString(), answer.ValueString()); why != "" {
			*s.Value(m) = kept
			diags.AddAttributeWarning(path.Root(s.Attribute), fmt.Sprintf("%s answered as %s", s.Attribute, answer.ValueString()), why)
		}
	}
}

// StringReplaceSpelled is the plan modifier of an immutable attribute the
// API answers as s says: a change replaces the resource, unless the
// configuration writes another way the value the state holds, or the one
// the API last answered with.
func StringReplaceSpelled(s Spelling) planmodifier.String {
	const description = "Changing it to another value, not merely written another way, replaces the resource."
	return stringplanmodifier.RequiresReplaceIf(func(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		if req.PlanValue.IsUnknown() || !IsSet(req.StateValue) {
			resp.RequiresReplace = true
			return
		}
		planned := req.PlanValue.ValueString()
		if s.Equal(req.StateValue.ValueString(), planned) {
			return
		}
		var answer *string
		if req.Private != nil {
			var diags diag.Diagnostics
			answer, diags = answered(ctx, req.Private, req.Path.String())
			resp.Diagnostics.Append(diags...)
		}
		resp.RequiresReplace = answer == nil || !s.Equal(*answer, planned)
	}, description, description)
}

// MemberSpelling is a role binding's member: `user:{email}` or
// `serviceAccount:{service_account}`, and, as the API answers only,
// `userId:{user}` and `deleted:user:{user}`. An address is the same in any
// case. A binding holds on to a user's account, not their address, so
// answered with the account's new address, or as `userId:` while it has
// no verified one, it still grants the role to the user it was created
// for; answered as `deleted:user:`, it grants it to no one.
var MemberSpelling = Spelling{Equal: sameMember, Renamed: renamedMember}

func sameMember(a, b string) bool {
	if a == b {
		return true
	}
	ea, aUser := strings.CutPrefix(a, "user:")
	eb, bUser := strings.CutPrefix(b, "user:")
	return aUser && bUser && strings.EqualFold(ea, eb)
}

func renamedMember(prior, answer string) string {
	if !strings.HasPrefix(prior, "user:") {
		return ""
	}
	// A binding created from the configuration as it is written would
	// name whichever account has the address now, or be refused.
	switch {
	case strings.HasPrefix(answer, "user:"):
		return fmt.Sprintf("The binding still grants the role to the account %s named, whose verified address is now %s. "+
			"The state keeps %[1]s. Write the member as %[3]s, which doesn't replace the binding: "+
			"a binding created for %[1]s would be refused, or grant the role to another account.",
			prior, strings.TrimPrefix(answer, "user:"), answer)
	case strings.HasPrefix(answer, "userId:"):
		return fmt.Sprintf("The binding still grants the role to the account %s named, which has no verified address now. "+
			"The state keeps %[1]s until it has one again: meanwhile, a binding created for %[1]s would be refused, "+
			"or grant the role to another account.", prior)
	}
	return ""
}
