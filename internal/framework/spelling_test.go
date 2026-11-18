package framework

import (
	"context"
	"fmt"
	"strings"
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

func TestDomainsAreTheSameInAnyCase(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"WWW.Example.com", "www.example.com", true},
		{"www.example.com", "www.example.com", true},
		{"www.example.com", "example.com", false},
		{"www.example.com", "www.example.com.", false},
	} {
		if got := DomainSpelling.Equal(tc.a, tc.b); got != tc.same {
			t.Errorf("Equal(%q, %q) = %v", tc.a, tc.b, got)
		}
		if why := DomainSpelling.Renamed(tc.a, tc.b); why != "" {
			t.Errorf("Renamed(%q, %q) = %q", tc.a, tc.b, why)
		}
	}
}

// memberSpelling is a spelling that can be renamed, as a role binding's
// member once was: `user:{email}` is the same in any case, and answered with
// the account's new address, or as `userId:{user}`, still names it.
var memberSpelling = Spelling{Equal: sameMember, Renamed: renamedMember}

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

type bindingModel struct{ Member types.String }

func spelledResource() *Resource[bindingModel, *wrapperspb.StringValue] {
	return &Resource[bindingModel, *wrapperspb.StringValue]{spec: ResourceSpec[bindingModel, *wrapperspb.StringValue]{
		FromProto: func(p *wrapperspb.StringValue, m *bindingModel) error {
			m.Member = types.StringValue(p.GetValue())
			return nil
		},
		Spelled: []Spelled[bindingModel]{{
			Attribute: "member",
			Spelling:  memberSpelling,
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
