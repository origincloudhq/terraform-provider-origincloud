package framework

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// private is a resource's private state, in memory.
type private map[string][]byte

func (p private) GetKey(_ context.Context, key string) ([]byte, diag.Diagnostics) {
	return p[key], nil
}

func (p private) SetKey(_ context.Context, key string, value []byte) diag.Diagnostics {
	p[key] = value
	return nil
}

func TestMembersAreTheSameInAnyCaseOfTheirAddress(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"user:Mail@Example.com", "user:mail@example.com", true},
		{"user:mail@example.com", "user:mail@example.com", true},
		{"serviceAccount:sa-1", "serviceAccount:sa-1", true},
		{"serviceAccount:SA-1", "serviceAccount:sa-1", false},
		{"user:ana@example.com", "user:bo@example.com", false},
		{"user:u-1", "userId:u-1", false},
		{"user:ana@example.com", "deleted:user:u-1", false},
	} {
		if got := MemberSpelling.Equal(tc.a, tc.b); got != tc.same {
			t.Errorf("Equal(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

// A binding created for a user still grants the role to their account
// under a new address, or without a verified one; not once it is gone, or
// for another kind of member.
func TestAMemberIsRenamedOnlyWhileItsAccountExists(t *testing.T) {
	for _, tc := range []struct {
		prior, answer string
		renamed       bool
	}{
		{"user:ana@example.com", "user:ana@new.example", true},
		{"user:ana@example.com", "userId:u-1", true},
		{"user:ana@example.com", "deleted:user:u-1", false},
		{"userId:u-1", "user:ana@example.com", false},
		{"serviceAccount:sa-1", "serviceAccount:sa-2", false},
		{"deleted:user:u-1", "user:ana@example.com", false},
	} {
		if got := MemberSpelling.Renamed(tc.prior, tc.answer) != ""; got != tc.renamed {
			t.Errorf("Renamed(%q, %q): %v", tc.prior, tc.answer, got)
		}
	}
}

type bindingModel struct{ Member types.String }

func spelledResource() *Resource[bindingModel, *wrapperspb.StringValue] {
	return &Resource[bindingModel, *wrapperspb.StringValue]{spec: ResourceSpec[bindingModel, *wrapperspb.StringValue]{
		FromProto: func(p *wrapperspb.StringValue, m *bindingModel) error {
			m.Member = types.StringValue(p.GetValue())
			return nil
		},
		Spelled: []Spelled[bindingModel]{{
			Attribute: "member",
			Spelling:  MemberSpelling,
			Value:     func(m *bindingModel) *types.String { return &m.Member },
		}},
	}}
}

func TestASpelledAttributeKeepsItsValueWhileTheAnswerNamesTheSameThing(t *testing.T) {
	for _, tc := range []struct {
		name          string
		prior, answer types.String
		want          string
		warned        bool
	}{
		{"another case", types.StringValue("user:Ana@Example.com"), types.StringValue("user:ana@example.com"), "user:Ana@Example.com", false},
		{"a new address", types.StringValue("user:ana@example.com"), types.StringValue("user:ana@new.example"), "user:ana@example.com", true},
		{"no verified address", types.StringValue("user:ana@example.com"), types.StringValue("userId:u-1"), "user:ana@example.com", true},
		{"a deleted account", types.StringValue("user:ana@example.com"), types.StringValue("deleted:user:u-1"), "deleted:user:u-1", false},
		{"imported", types.StringNull(), types.StringValue("user:ana@example.com"), "user:ana@example.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := bindingModel{Member: tc.prior}
			m := prior
			p := private{}
			var diags diag.Diagnostics
			if err := spelledResource().fromProto(context.Background(), wrapperspb.String(tc.answer.ValueString()), &prior, &m, p, &diags); err != nil {
				t.Fatal(err)
			}
			if m.Member.ValueString() != tc.want {
				t.Errorf("the state holds %v, want %q", m.Member, tc.want)
			}
			if diags.HasError() || (diags.WarningsCount() > 0) != tc.warned {
				t.Errorf("diagnostics %v, want a warning: %v", diags, tc.warned)
			}
			answer, _ := answered(context.Background(), p, "member")
			if answer == nil || *answer != tc.answer.ValueString() {
				t.Errorf("the private state records %v, not %q", answer, tc.answer.ValueString())
			}
		})
	}
}
