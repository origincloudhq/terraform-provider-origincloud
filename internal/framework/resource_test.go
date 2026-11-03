package framework

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"go.origincloud.eu/sdk/core"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// A resource in a terminal state leaves nothing to delete.
func TestAGoneResourceIsntDeleted(t *testing.T) {
	for _, state := range []string{"TERMINATED", "RUNNING"} {
		r := &Resource[struct{}, *wrapperspb.StringValue]{spec: ResourceSpec[struct{}, *wrapperspb.StringValue]{
			Get: func(context.Context, *core.Client, string) (*wrapperspb.StringValue, error) {
				return wrapperspb.String(state), nil
			},
			Gone: func(p *wrapperspb.StringValue) bool { return p.GetValue() == "TERMINATED" },
		}}
		gone, err := r.clearBeforeDelete(context.Background(), "things/t-1")
		if err != nil || gone != (state == "TERMINATED") {
			t.Errorf("%s: gone %v, %v", state, gone, err)
		}
	}
}

// Only a CONFLICT naming exactly one resource of the created type, which
// refuses replays and whose name the API chose, is a create's replay; a
// resource whose id the client chooses may be someone else's.
func TestAReplayedCreateIsACONFLICTNamingOneResourceOfTheType(t *testing.T) {
	const name = "organizations/acme/things/t-1"
	conflict := func(metadata map[string]string) error {
		return &core.Error{HTTPStatus: 409, Reason: "CONFLICT", Metadata: metadata}
	}
	spec := ResourceSpec[struct{}, *wrapperspb.StringValue]{Pattern: "organizations/{organization}/things/{thing}", RefusesReplays: true}
	chosen := spec
	chosen.ClientID = func(*struct{}) types.String { return types.StringValue("t-1") }
	answered := spec
	answered.RefusesReplays = false
	for _, c := range []struct {
		what string
		spec ResourceSpec[struct{}, *wrapperspb.StringValue]
		err  error
		want string
	}{
		{"named in the metadata", spec, conflict(map[string]string{"thing": name}), name},
		{"wrapped", spec, fmt.Errorf("create: %w", conflict(map[string]string{"thing": name})), name},
		{"another type", spec, conflict(map[string]string{"organization": "organizations/acme"}), ""},
		{"two of the type", spec, conflict(map[string]string{"thing": name, "other": name + "2"}), ""},
		{"no metadata", spec, conflict(map[string]string{}), ""},
		{"another reason", spec, &core.Error{HTTPStatus: 400, Reason: "BAD_REQUEST", Metadata: map[string]string{"thing": name}}, ""},
		{"not the API's", spec, errors.New(name), ""},
		{"an id the client chose", chosen, conflict(map[string]string{"thing": name}), ""},
		{"replays answered", answered, conflict(map[string]string{"thing": name}), ""},
	} {
		r := &Resource[struct{}, *wrapperspb.StringValue]{spec: c.spec}
		if got := r.replayedCreate(c.err); got != c.want {
			t.Errorf("%s: %q, want %q", c.what, got, c.want)
		}
	}
}

func TestKeptValuesStay(t *testing.T) {
	if got := Kept(types.StringUnknown(), "s"); got.ValueString() != "s" {
		t.Errorf("the create's answer: %v", got)
	}
	if got := Kept(types.StringValue("s"), ""); got.ValueString() != "s" {
		t.Errorf("a later answer without it: %v", got)
	}
	if got := Kept(types.StringUnknown(), ""); !got.IsNull() {
		t.Errorf("a create that answers without one: %v", got)
	}
	if got := Kept(types.StringNull(), ""); !got.IsNull() {
		t.Errorf("imported: %v", got)
	}
}

func TestTimestampsKeepTheirSpelling(t *testing.T) {
	ts := TimestampToProto(types.StringValue("2027-01-01T00:00:00Z"))
	if got := Timestamp(ts, types.StringValue("2027-01-01T01:00:00+01:00")); got.ValueString() != "2027-01-01T01:00:00+01:00" {
		t.Errorf("the same instant: %v", got)
	}
	if got := Timestamp(ts, types.StringValue("2027-01-02T00:00:00Z")); got.ValueString() != "2027-01-01T00:00:00Z" {
		t.Errorf("another instant: %v", got)
	}
	if got := Timestamp(nil, types.StringValue("2027-01-01T00:00:00Z")); !got.IsNull() {
		t.Errorf("unset: %v", got)
	}
	d := DurationToProto(types.StringValue("1.5s"))
	if got := Duration(d, types.StringNull()); got.ValueString() != "1.500s" {
		t.Errorf("a duration in the API's form: %v", got)
	}
	if DurationToProto(types.StringValue("1m30s")) != nil {
		t.Error("a duration not in seconds parsed")
	}
}
