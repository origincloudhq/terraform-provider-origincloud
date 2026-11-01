package framework

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The plan modifiers of an immutable attribute holding nested messages,
// which replace the resource when what the attribute sends changes, as
// same compares it: an output-only field within it, which the plan
// doesn't know, doesn't count. ifConfigured replaces it only when the
// configuration sets the attribute, as for an immutable attribute whose
// value the API chooses when it is left out.

const replaceDescription = "Changing what it sends replaces the resource."

func replaces(ifConfigured, configNull, same bool) bool {
	return !(ifConfigured && configNull) && !same
}

func ObjectReplaceIfSent(same func(plan, state types.Object) bool, ifConfigured bool) planmodifier.Object {
	return objectplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = replaces(ifConfigured, req.ConfigValue.IsNull(), same(req.PlanValue, req.StateValue))
	}, replaceDescription, replaceDescription)
}

func ListReplaceIfSent(same func(plan, state types.List) bool, ifConfigured bool) planmodifier.List {
	return listplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.ListRequest, resp *listplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = replaces(ifConfigured, req.ConfigValue.IsNull(), same(req.PlanValue, req.StateValue))
	}, replaceDescription, replaceDescription)
}

func SetReplaceIfSent(same func(plan, state types.Set) bool, ifConfigured bool) planmodifier.Set {
	return setplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.SetRequest, resp *setplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = replaces(ifConfigured, req.ConfigValue.IsNull(), same(req.PlanValue, req.StateValue))
	}, replaceDescription, replaceDescription)
}

func MapReplaceIfSent(same func(plan, state types.Map) bool, ifConfigured bool) planmodifier.Map {
	return mapplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.MapRequest, resp *mapplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = replaces(ifConfigured, req.ConfigValue.IsNull(), same(req.PlanValue, req.StateValue))
	}, replaceDescription, replaceDescription)
}
