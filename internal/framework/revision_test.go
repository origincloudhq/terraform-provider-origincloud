package framework

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"go.origincloud.eu/sdk/core"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestARevisionIsNotPartOfAResourcesName(t *testing.T) {
	for name, want := range map[string]string{
		"projects/p/launchTemplates/web":          "projects/p/launchTemplates/web",
		"projects/p/launchTemplates/web@3":        "projects/p/launchTemplates/web",
		"projects/p/launchTemplates/web@latest":   "projects/p/launchTemplates/web",
		"//compute.origincloudapis.eu/projects/p": "//compute.origincloudapis.eu/projects/p",
	} {
		if got := WithoutRevision(name); got != want {
			t.Errorf("WithoutRevision(%q) = %q, want %q", name, got, want)
		}
	}
	// So that the id of a template read at a version is the template's.
	full, err := FullName("compute", "projects/p/launchTemplates/web@3")
	if err != nil || full.ValueString() != "//compute.origincloudapis.eu/projects/p/launchTemplates/web" {
		t.Errorf("full name %v, %v", full, err)
	}
}

// A revised resource, whose message is the name, is read at its newest, a
// change of the revised field adds a revision before the rest of an update,
// and what the update answers is read again.
func TestARevisedResourceIsReadAtItsNewestAndRevisedByAChange(t *testing.T) {
	var calls []string
	spec := ResourceSpec[struct{}, *wrapperspb.StringValue]{
		Get: func(_ context.Context, _ *core.Client, name string) (*wrapperspb.StringValue, error) {
			calls = append(calls, "get "+name)
			return wrapperspb.String(name + "@2"), nil
		},
		CreateSync: func(context.Context, *core.Client, *struct{}) (*wrapperspb.StringValue, error) {
			return wrapperspb.String("t@1"), nil
		},
		Update: func(_ context.Context, _ *core.Client, resource *wrapperspb.StringValue, mask []string) (*wrapperspb.StringValue, error) {
			calls = append(calls, "update "+strings.Join(mask, ","))
			return resource, nil
		},
		Revision: &Revision[*wrapperspb.StringValue]{
			Field:       "data",
			Latest:      "@latest",
			Name:        func(p *wrapperspb.StringValue) string { return WithoutRevision(p.GetValue()) },
			Unversioned: func(p *wrapperspb.StringValue) { p.Value = WithoutRevision(p.GetValue()) },
			Revise: func(_ context.Context, _ *core.Client, resource *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
				calls = append(calls, "revise "+resource.GetValue())
				return wrapperspb.String("t@3"), nil
			},
			Follow: func(resource, revised *wrapperspb.StringValue) { calls = append(calls, "follow "+revised.GetValue()) },
		},
	}
	spec = reviseSpec(spec)

	got, err := spec.Get(context.Background(), nil, "t@1")
	if err != nil || got.GetValue() != "t" || !slices.Equal(calls, []string{"get t@latest"}) {
		t.Errorf("read %q, %v after %v", got.GetValue(), err, calls)
	}
	if created, _ := spec.CreateSync(context.Background(), nil, nil); created.GetValue() != "t" {
		t.Errorf("created %q", created.GetValue())
	}

	calls = nil
	got, err = spec.Update(context.Background(), nil, wrapperspb.String("t"), []string{"tags", "data"})
	want := []string{"revise t", "follow t@3", "update tags", "get t@latest"}
	if err != nil || got.GetValue() != "t" || !slices.Equal(calls, want) {
		t.Errorf("updated to %q, %v after %v, want %v", got.GetValue(), err, calls, want)
	}

	// Nothing left to update after the revision.
	calls = nil
	if _, err = spec.Update(context.Background(), nil, wrapperspb.String("t"), []string{"data"}); err != nil || strings.Contains(strings.Join(calls, ";"), "update") {
		t.Errorf("after %v, %v", calls, err)
	}

	// A refused revision stops the update.
	spec.Revision.Revise = func(context.Context, *core.Client, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return nil, errors.New("refused")
	}
	spec = reviseSpec(ResourceSpec[struct{}, *wrapperspb.StringValue]{Update: spec.Update, Get: spec.Get, Revision: spec.Revision})
	calls = nil
	if _, err = spec.Update(context.Background(), nil, wrapperspb.String("t"), []string{"data", "tags"}); err == nil || len(calls) != 0 {
		t.Errorf("refused revision: %v after %v", err, calls)
	}
}

func TestATemplateIsTheSameWrittenAnotherWay(t *testing.T) {
	const web = "//compute.origincloudapis.eu/projects/p/launchTemplates/web"
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{web, web, true},
		{web, web + "@3", true},
		{web + "@default", web + "@3", true},
		{web + "@latest", web + "@3", true},
		{web + "@3", web + "@3", true},
		{web + "@3", web + "@4", false},
		{web, web + "-2@3", false},
		{web + "@3", web + "-2", false},
	} {
		if got := TemplateSpelling.Equal(tc.a, tc.b); got != tc.same {
			t.Errorf("Equal(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}
