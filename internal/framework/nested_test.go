package framework

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// The conversions of a nested message, as the generator writes them, for
// a message with one optional string field, value: wrapperspb.StringValue.
var valueTypes = map[string]attr.Type{"value": types.StringType}

func valueFromProto(p *wrapperspb.StringValue, prior types.Object) types.Object {
	return types.ObjectValueMust(valueTypes, map[string]attr.Value{
		"value": Optional(Attr(prior, "value", types.StringNull()), types.StringValue(p.GetValue()), p.GetValue() == ""),
	})
}

func valueToProto(o types.Object) *wrapperspb.StringValue {
	if o.IsNull() || o.IsUnknown() {
		return nil
	}
	return wrapperspb.String(Attr(o, "value", types.StringNull()).ValueString())
}

func object(value attr.Value) types.Object {
	return types.ObjectValueMust(valueTypes, map[string]attr.Value{"value": value})
}

func valueObjects(values ...attr.Value) []attr.Value {
	out := make([]attr.Value, len(values))
	for i, v := range values {
		out[i] = object(v)
	}
	return out
}

var objectType = types.ObjectType{AttrTypes: valueTypes}

func TestScalarCollectionsAreEmptyNotNull(t *testing.T) {
	if s := StringSet(nil); s.IsNull() || len(s.Elements()) != 0 {
		t.Errorf("StringSet(nil) = %v, want an empty set", s)
	}
	l := ListOf(types.Int64Type, []int32{3, 1}, func(v int32) types.Int64 { return types.Int64Value(int64(v)) })
	if want := types.ListValueMust(types.Int64Type, []attr.Value{types.Int64Value(3), types.Int64Value(1)}); !l.Equal(want) {
		t.Errorf("ListOf = %v, want %v in order", l, want)
	}
	if s := SetOf(types.BoolType, []bool(nil), types.BoolValue); s.IsNull() || len(s.Elements()) != 0 {
		t.Errorf("SetOf(nil) = %v, want an empty set", s)
	}
}

func TestElementsOfNullAreNone(t *testing.T) {
	toString := func(e attr.Value) string { return e.(types.String).ValueString() }
	if got := Elements(types.ListNull(types.StringType), toString); got != nil {
		t.Errorf("null list: %v", got)
	}
	if got := Elements(types.SetUnknown(types.StringType), toString); got != nil {
		t.Errorf("unknown set: %v", got)
	}
	if got := Elements(StringList([]string{"a", "b"}), toString); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("list: %v", got)
	}
	m := types.MapValueMust(objectType, map[string]attr.Value{"k": object(types.StringValue("v"))})
	if got := MapElements(m, func(e attr.Value) *wrapperspb.StringValue { return valueToProto(e.(types.Object)) }); got["k"].GetValue() != "v" {
		t.Errorf("map: %v", got)
	}
}

func TestAnObjectLeftOutStaysNull(t *testing.T) {
	null := types.ObjectNull(valueTypes)
	for _, tc := range []struct {
		name  string
		p     *wrapperspb.StringValue
		prior types.Object
		null  bool
	}{
		{"left out, the API answers with none", nil, null, true},
		{"left out, the API answers with an empty one", wrapperspb.String(""), null, true},
		{"unknown, the API answers with none", nil, types.ObjectUnknown(valueTypes), true},
		{"left out, the API answers with one", wrapperspb.String("x"), null, false},
		// {} configured: the API's empty answer is it, not null.
		{"set to {}, the API answers with none", nil, object(types.StringNull()), false},
	} {
		got := Object(tc.p, tc.prior, valueTypes, valueFromProto)
		if got.IsNull() != tc.null {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

// A list's elements keep, in order, what each prior element left out.
func TestAMessageListConvertsEachElementWithItsPrior(t *testing.T) {
	prior := types.ListValueMust(objectType, valueObjects(types.StringNull(), types.StringValue("b")))
	got := MessageList(valueTypes, []*wrapperspb.StringValue{wrapperspb.String(""), wrapperspb.String("b")}, prior, valueFromProto)
	if !got.Equal(prior) {
		t.Errorf("got %v, want %v", got, prior)
	}
	// Reordered by the API: a difference.
	got = MessageList(valueTypes, []*wrapperspb.StringValue{wrapperspb.String("b"), wrapperspb.String("")}, prior, valueFromProto)
	if got.Equal(prior) {
		t.Errorf("a reordered list equals its prior: %v", got)
	}
	if empty := MessageList(valueTypes, nil, types.ListNull(objectType), valueFromProto); empty.IsNull() || len(empty.Elements()) != 0 {
		t.Errorf("an empty field is %v, want an empty list", empty)
	}
}

// A set's elements match the prior elements that send the same, whatever
// the order the API answers in.
func TestAMessageSetMatchesItsPriorElements(t *testing.T) {
	prior := types.SetValueMust(objectType, valueObjects(types.StringValue("b"), types.StringNull()))
	got := MessageSet(valueTypes, []*wrapperspb.StringValue{wrapperspb.String(""), wrapperspb.String("b")}, prior, valueFromProto, valueToProto)
	if !got.Equal(prior) {
		t.Errorf("got %v, want %v", got, prior)
	}
}

func TestAMessageMapConvertsEachValueWithItsPrior(t *testing.T) {
	prior := types.MapValueMust(objectType, map[string]attr.Value{"a": object(types.StringNull())})
	got := MessageMap(valueTypes, map[string]*wrapperspb.StringValue{"a": wrapperspb.String("")}, prior, valueFromProto)
	if !got.Equal(prior) {
		t.Errorf("got %v, want %v", got, prior)
	}
}

func TestSameMessages(t *testing.T) {
	a, b := wrapperspb.String("a"), wrapperspb.String("b")
	list := func(v ...*wrapperspb.StringValue) []*wrapperspb.StringValue { return v }
	if !SameMessages(list(a, b), list(wrapperspb.String("a"), wrapperspb.String("b"))) || SameMessages(list(a, b), list(b, a)) {
		t.Error("SameMessages doesn't compare in order")
	}
	if !SameMessageSets(list(a, b), list(b, a)) || SameMessageSets(list(a, a), list(a, b)) {
		t.Error("SameMessageSets doesn't compare in any order")
	}
	if !SameMessageMaps(map[string]*wrapperspb.StringValue{"k": a}, map[string]*wrapperspb.StringValue{"k": wrapperspb.String("a")}) ||
		SameMessageMaps(map[string]*wrapperspb.StringValue{"k": a}, map[string]*wrapperspb.StringValue{"j": a}) {
		t.Error("SameMessageMaps doesn't compare by key")
	}
}

func TestElementCheck(t *testing.T) {
	check := func(v ElementCheck, l types.List) string {
		var resp validator.ListResponse
		v.ValidateList(context.Background(), validator.ListRequest{Path: path.Root("l"), ConfigValue: l}, &resp)
		var errs []string
		for _, d := range resp.Diagnostics.Errors() {
			errs = append(errs, d.Summary())
		}
		return strings.Join(errs, ", ")
	}
	strs := func(v ...attr.Value) types.List { return types.ListValueMust(types.StringType, v) }
	ints := func(v ...attr.Value) types.List { return types.ListValueMust(types.Int64Type, v) }
	for _, tc := range []struct {
		name string
		v    ElementCheck
		l    types.List
		want string
	}{
		{"null element", ElementCheck{}, strs(types.StringValue("a"), types.StringNull()), "Null element"},
		{"unknown element", ElementCheck{}, strs(types.StringUnknown()), ""},
		{"enum value", ElementCheck{OneOf: []string{"OPEN"}}, strs(types.StringValue("OPEN")), ""},
		{"not an enum value", ElementCheck{OneOf: []string{"OPEN"}}, strs(types.StringValue("AJAR")), "Invalid element"},
		{"int32", ElementCheck{Int32: true}, ints(types.Int64Value(1 << 31)), "Element out of range"},
		{"null list", ElementCheck{}, types.ListNull(types.StringType), ""},
	} {
		if got := check(tc.v, tc.l); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	var resp validator.StringResponse
	OneOf("OPEN").ValidateString(context.Background(), validator.StringRequest{ConfigValue: types.StringValue("AJAR")}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("OneOf accepted a value not among its own")
	}
}
