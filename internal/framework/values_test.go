package framework

import (
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
