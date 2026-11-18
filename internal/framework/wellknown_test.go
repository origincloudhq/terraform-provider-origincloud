package framework

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/genproto/googleapis/type/date"
)

func TestADateIsAStringInItsISOForm(t *testing.T) {
	if got := Date(&date.Date{Year: 2026, Month: 11, Day: 1}, types.StringNull()); got.ValueString() != "2026-11-01" {
		t.Errorf("Date = %v", got)
	}
	// A date without a year, month or day, or none, is null.
	for _, d := range []*date.Date{nil, {Month: 11, Day: 1}, {Year: 2026}} {
		if got := Date(d, types.StringNull()); !got.IsNull() {
			t.Errorf("Date(%v) = %v, want null", d, got)
		}
	}
	d := DateToProto(types.StringValue("2026-11-01"))
	if d.GetYear() != 2026 || d.GetMonth() != 11 || d.GetDay() != 1 {
		t.Errorf("DateToProto = %v", d)
	}
	for _, v := range []types.String{types.StringNull(), types.StringUnknown(), types.StringValue("2026-13-01"), types.StringValue("1 Nov 2026")} {
		if DateToProto(v) != nil {
			t.Errorf("DateToProto(%v) isn't nil", v)
		}
	}
}
