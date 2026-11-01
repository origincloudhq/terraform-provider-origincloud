package framework

import (
	"context"
	"maps"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The plan modifiers of an immutable attribute holding nested messages,
// which replace the resource when what the attribute sends changes, as
// same compares it: an output-only field within it, which the plan
// doesn't know, doesn't count. ifConfigured replaces it only when the
// configuration sets the attribute, as for an immutable attribute whose
// value the API chooses when it is left out.

const replaceDescription = "Changing what it sends replaces the resource."

// known returns plan with each unknown value the configuration leaves out
// replaced by the state's at the same place, as it is planned after: the
// plan modifiers of the attributes nested in one run after the attribute's
// own, so a kept value is still unknown here. A set's elements, which have
// no place, are left as they are.
func known(plan, state, config attr.Value) attr.Value {
	if plan.IsUnknown() && (config == nil || config.IsNull()) {
		return state
	}
	if plan.IsNull() || plan.IsUnknown() || state.IsNull() || state.IsUnknown() {
		return plan
	}
	switch p := plan.(type) {
	case types.Object:
		s, ok := state.(types.Object)
		if !ok {
			return plan
		}
		c, _ := config.(types.Object)
		attrs := make(map[string]attr.Value, len(p.Attributes()))
		for name, v := range p.Attributes() {
			if sv, ok := s.Attributes()[name]; ok {
				v = known(v, sv, c.Attributes()[name])
			}
			attrs[name] = v
		}
		return types.ObjectValueMust(p.AttributeTypes(context.Background()), attrs)
	case types.List:
		s, ok := state.(types.List)
		if !ok {
			return plan
		}
		var c []attr.Value
		if cl, ok := config.(types.List); ok {
			c = cl.Elements()
		}
		elements := slices.Clone(p.Elements())
		for i := range elements {
			if i < len(s.Elements()) {
				var ce attr.Value
				if i < len(c) {
					ce = c[i]
				}
				elements[i] = known(elements[i], s.Elements()[i], ce)
			}
		}
		return types.ListValueMust(p.ElementType(context.Background()), elements)
	case types.Map:
		s, ok := state.(types.Map)
		if !ok {
			return plan
		}
		c, _ := config.(types.Map)
		elements := maps.Clone(p.Elements())
		for k, v := range elements {
			if sv, ok := s.Elements()[k]; ok {
				elements[k] = known(v, sv, c.Elements()[k])
			}
		}
		return types.MapValueMust(p.ElementType(context.Background()), elements)
	}
	return plan
}

func replaces(ifConfigured, configNull, same bool) bool {
	return !(ifConfigured && configNull) && !same
}

// StringReplaceIfSent is for a timestamp or a duration, whose spelling
// can differ while it sends the same instant or length.
func StringReplaceIfSent(same func(plan, state types.String) bool, ifConfigured bool) planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = replaces(ifConfigured, req.ConfigValue.IsNull(), same(known(req.PlanValue, req.StateValue, req.ConfigValue).(types.String), req.StateValue))
	}, replaceDescription, replaceDescription)
}

func ObjectReplaceIfSent(same func(plan, state types.Object) bool, ifConfigured bool) planmodifier.Object {
	return objectplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = replaces(ifConfigured, req.ConfigValue.IsNull(), same(known(req.PlanValue, req.StateValue, req.ConfigValue).(types.Object), req.StateValue))
	}, replaceDescription, replaceDescription)
}

func ListReplaceIfSent(same func(plan, state types.List) bool, ifConfigured bool) planmodifier.List {
	return listplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.ListRequest, resp *listplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = replaces(ifConfigured, req.ConfigValue.IsNull(), same(known(req.PlanValue, req.StateValue, req.ConfigValue).(types.List), req.StateValue))
	}, replaceDescription, replaceDescription)
}

func SetReplaceIfSent(same func(plan, state types.Set) bool, ifConfigured bool) planmodifier.Set {
	return setplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.SetRequest, resp *setplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = replaces(ifConfigured, req.ConfigValue.IsNull(), same(known(req.PlanValue, req.StateValue, req.ConfigValue).(types.Set), req.StateValue))
	}, replaceDescription, replaceDescription)
}

func MapReplaceIfSent(same func(plan, state types.Map) bool, ifConfigured bool) planmodifier.Map {
	return mapplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.MapRequest, resp *mapplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = replaces(ifConfigured, req.ConfigValue.IsNull(), same(known(req.PlanValue, req.StateValue, req.ConfigValue).(types.Map), req.StateValue))
	}, replaceDescription, replaceDescription)
}
