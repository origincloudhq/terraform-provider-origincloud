package framework

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/proto"
)

// The conversions of repeated fields and nested messages, between the
// SDK's messages and the framework's values. The generated code writes
// one FromProto and one ToProto function per nested message, and these
// apply them to the fields holding it.
//
// A FromProto function takes the attribute's prior value, from the plan or
// the state, so that what the API answers with doesn't change a value
// that means the same: an optional attribute left out stays null when the
// API answers with nothing, and an element of a set keeps the prior
// element it matches.

// StringSet returns a repeated string field as a set attribute: an empty
// set, not null, when the field is empty.
func StringSet(l []string) types.Set {
	return SetOf(types.StringType, l, types.StringValue)
}

// ListOf returns a repeated scalar field as a list attribute of elem, each
// element converted by from: an empty list, not null, when the field is
// empty.
func ListOf[T any, V attr.Value](elem attr.Type, items []T, from func(T) V) types.List {
	return types.ListValueMust(elem, scalars(items, from))
}

// SetOf returns a repeated scalar field as a set attribute of elem, each
// element converted by from: an empty set, not null, when the field is
// empty.
func SetOf[T any, V attr.Value](elem attr.Type, items []T, from func(T) V) types.Set {
	return types.SetValueMust(elem, scalars(items, from))
}

func scalars[T any, V attr.Value](items []T, from func(T) V) []attr.Value {
	elements := make([]attr.Value, len(items))
	for i, item := range items {
		elements[i] = from(item)
	}
	return elements
}

// A collection is a list's or a set's value.
type collection interface {
	IsNull() bool
	IsUnknown() bool
	Elements() []attr.Value
}

// Elements returns a list or set attribute as a repeated field, each
// element converted by to; nil when it is null or unknown.
func Elements[T any](c collection, to func(attr.Value) T) []T {
	if c.IsNull() || c.IsUnknown() {
		return nil
	}
	out := make([]T, 0, len(c.Elements()))
	for _, e := range c.Elements() {
		out = append(out, to(e))
	}
	return out
}

// MapElements returns a map attribute as a map field, each value
// converted by to; nil when it is null or unknown.
func MapElements[T any](m types.Map, to func(attr.Value) T) map[string]T {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := make(map[string]T, len(m.Elements()))
	for k, e := range m.Elements() {
		out[k] = to(e)
	}
	return out
}

// Attr returns the attribute name of an object, or null when the object
// is null or unknown, or hasn't the attribute.
func Attr[T attr.Value](o types.Object, name string, null T) T {
	if v, ok := o.Attributes()[name].(T); ok {
		return v
	}
	return null
}

// Object returns a nested message field as an object attribute, converted
// by from with the prior value: null when the field is unset or empty and
// the prior value is null or unknown, so that an optional object left out
// stays null.
func Object[P proto.Message](p P, prior types.Object, attrTypes map[string]attr.Type, from func(P, types.Object) types.Object) types.Object {
	if (prior.IsNull() || prior.IsUnknown()) && proto.Size(p) == 0 {
		return types.ObjectNull(attrTypes)
	}
	return from(p, prior)
}

// MessageList returns a repeated message field as a list attribute, each
// element converted by from with the prior list's element at the same
// index: an empty list, not null, when the field is empty.
func MessageList[P proto.Message](attrTypes map[string]attr.Type, items []P, prior types.List, from func(P, types.Object) types.Object) types.List {
	priors := objects(prior)
	elements := make([]attr.Value, len(items))
	for i, item := range items {
		e := types.ObjectNull(attrTypes)
		if i < len(priors) {
			e = priors[i]
		}
		elements[i] = from(item, e)
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: attrTypes}, elements)
}

// MessageSet returns a repeated message field whose order the API doesn't
// keep as a set attribute: an empty set, not null, when the field is
// empty. Each element is converted by from with the prior set's element
// that sends the same, as to converts it, if any.
func MessageSet[P proto.Message](attrTypes map[string]attr.Type, items []P, prior types.Set, from func(P, types.Object) types.Object, to func(types.Object) P) types.Set {
	priors := objects(prior)
	priorSent := make([]P, len(priors))
	for j, p := range priors {
		priorSent[j] = to(p)
	}
	used := make([]bool, len(priors))
	elements := make([]attr.Value, len(items))
	for i, item := range items {
		e := types.ObjectNull(attrTypes)
		sent := to(from(item, e))
		for j, p := range priors {
			if !used[j] && proto.Equal(priorSent[j], sent) {
				e, used[j] = p, true
				break
			}
		}
		elements[i] = from(item, e)
	}
	return types.SetValueMust(types.ObjectType{AttrTypes: attrTypes}, elements)
}

// MessageMap returns a map field of messages as a map attribute, each
// value converted by from with the prior map's value under the same key:
// an empty map, not null, when the field is empty.
func MessageMap[P proto.Message](attrTypes map[string]attr.Type, items map[string]P, prior types.Map, from func(P, types.Object) types.Object) types.Map {
	var priors map[string]attr.Value
	if !prior.IsNull() && !prior.IsUnknown() {
		priors = prior.Elements()
	}
	elements := make(map[string]attr.Value, len(items))
	for k, item := range items {
		e := types.ObjectNull(attrTypes)
		if o, ok := priors[k].(types.Object); ok {
			e = o
		}
		elements[k] = from(item, e)
	}
	return types.MapValueMust(types.ObjectType{AttrTypes: attrTypes}, elements)
}

// objects returns a list's or set's elements, the objects of a nested
// attribute; none when it is null or unknown.
func objects(c collection) []types.Object {
	if c.IsNull() || c.IsUnknown() {
		return nil
	}
	var out []types.Object
	for _, e := range c.Elements() {
		if o, ok := e.(types.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

// SameMessages says that two repeated message fields hold the same
// messages in the same order.
func SameMessages[P proto.Message](a, b []P) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !proto.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// SameMessageSets says that two repeated message fields hold the same
// messages, in any order.
func SameMessageSets[P proto.Message](a, b []P) bool {
	if len(a) != len(b) {
		return false
	}
	used := make([]bool, len(b))
	for _, x := range a {
		found := false
		for j, y := range b {
			if !used[j] && proto.Equal(x, y) {
				used[j], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// SameMessageMaps says that two map fields of messages hold the same
// messages under the same keys.
func SameMessageMaps[P proto.Message](a, b map[string]P) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		y, ok := b[k]
		if !ok || !proto.Equal(x, y) {
			return false
		}
	}
	return true
}
