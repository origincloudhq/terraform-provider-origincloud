package framework

import (
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestStringListIsEmptyNotNull(t *testing.T) {
	for _, l := range [][]string{nil, {}} {
		got := StringList(l)
		if got.IsNull() || got.IsUnknown() || len(got.Elements()) != 0 {
			t.Errorf("StringList(%#v) = %v, want an empty list", l, got)
		}
	}
	got := StringList([]string{"zones/b", "zones/a"})
	want := types.ListValueMust(types.StringType, []attr.Value{types.StringValue("zones/b"), types.StringValue("zones/a")})
	if !got.Equal(want) {
		t.Errorf("StringList kept %v, want %v in order", got, want)
	}
}

func TestStringListToProto(t *testing.T) {
	for _, tc := range []struct {
		in   types.List
		want []string
	}{
		{types.ListNull(types.StringType), nil},
		{types.ListUnknown(types.StringType), nil},
		{StringList(nil), []string{}},
		{StringList([]string{"a", "b"}), []string{"a", "b"}},
	} {
		if got := StringListToProto(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("StringListToProto(%v) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

// An optional list left out stays null when the API answers with none,
// and one set to [] stays [], so that neither plans a change.
func TestAnOptionalListKeepsNullAndEmptyApart(t *testing.T) {
	empty := StringList(nil)
	if got := Optional(types.ListNull(types.StringType), empty, true); !got.IsNull() {
		t.Errorf("left out, it became %v", got)
	}
	if got := Optional(empty, empty, true); got.IsNull() || len(got.Elements()) != 0 {
		t.Errorf("set to [], it became %v", got)
	}
	answered := StringList([]string{"a"})
	if got := Optional(types.ListNull(types.StringType), answered, false); !got.Equal(answered) {
		t.Errorf("left out, the API's %v became %v", answered, got)
	}
}
