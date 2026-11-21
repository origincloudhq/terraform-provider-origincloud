package main

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A launch is a resource that is created from a recipe as well as from
// its own arguments, such as an instance launched from a launch template:
// the method that launches it takes the recipe's name, and the arguments
// that override its fields. The resource's field that says which recipe it
// was launched from (output only on the resource) becomes an argument
// that does: set, the create is a launch, and the arguments the
// configuration sets override the recipe's.
//
// So that the recipe can supply them, the arguments the API requires
// (requiredWithout) are required only without one, and the ones the API
// chooses when they are left out, or that the recipe supplies (chosen),
// keep what the API answers.
//
// Nothing in the API marks such a method, so each is declared here, by the
// API's names, and checked against the API when generating: a declaration
// whose resource, method or fields are missing fails it. A declaration of
// a package the API doesn't have yet is left until it does, as a secret
// is.
type launch struct {
	// resource is the message, such as origincloud.compute.v1beta1.Instance.
	resource protoreflect.FullName
	// method is the launching method, in the resource's service, such as
	// LaunchInstance.
	method protoreflect.Name
	// template is the resource's field that names the recipe, and the
	// launch request's; data the request's field that holds the overrides.
	template, data protoreflect.Name
	// requiredWithout are the fields that the recipe may supply, so they
	// are required only without one; chosen those that keep what the API
	// chose when the configuration leaves them out.
	requiredWithout, chosen []protoreflect.Name
	// overrides is the function, of the resource's package, that returns
	// the recipe's overrides a resource's arguments describe.
	overrides string
}

var launches = []launch{
	{
		resource:        "origincloud.compute.v1beta1.Instance",
		method:          "LaunchInstance",
		template:        "launch_template",
		data:            "launch_template_data",
		requiredWithout: []protoreflect.Name{"instance_type", "key_pair", "network_interfaces"},
		chosen:          []protoreflect.Name{"tags", "service_account"},
		overrides:       "instanceLaunchOverrides",
	},
}

// launchesFound are the launches whose resource was generated.
var launchesFound = map[protoreflect.FullName]bool{}

// A resolved launch has its method.
type resolvedLaunch struct {
	launch
	method *protogen.Method
}

// applyLaunch finds t's declared launch, if it has one, and checks it
// against the API.
func applyLaunch(t *resourceType) error {
	i := slices.IndexFunc(launches, func(l launch) bool { return l.resource == t.message.Desc.FullName() })
	if i < 0 {
		return nil
	}
	l := launches[i]
	var method *protogen.Method
	for _, m := range t.service.Methods {
		if m.Desc.Name() == l.method {
			method = m
		}
	}
	if method == nil {
		return fmt.Errorf("launched: the service has no %s", l.method)
	}
	templateField := fieldNamed(t.message, string(l.template))
	switch {
	case templateField == nil || templateField.Desc.Kind() != protoreflect.StringKind || !slices.Contains(fieldBehaviors(templateField), annotations.FieldBehavior_OUTPUT_ONLY):
		return fmt.Errorf("launched: %s isn't an output-only string", l.template)
	case fieldNamed(method.Input, string(l.template)) == nil || fieldNamed(method.Input, string(l.data)) == nil || fieldNamed(method.Input, "parent") == nil:
		return fmt.Errorf("launched: %s has no parent, %s or %s", l.method, l.template, l.data)
	case operationResult(method) != t.message.Desc.FullName():
		return fmt.Errorf("launched: %s doesn't answer with an operation whose result is the %s", l.method, t.message.Desc.Name())
	case t.create == nil || len(t.variables) < 2:
		return fmt.Errorf("launched: the %s has no create or no parent", t.message.Desc.Name())
	}
	for _, name := range slices.Concat(l.requiredWithout, l.chosen) {
		if fieldNamed(t.message, string(name)) == nil {
			return fmt.Errorf("launched: no field %s", name)
		}
	}
	launchesFound[l.resource] = true
	methodsCalled[method.Desc.FullName()] = true
	t.launch = &resolvedLaunch{launch: l, method: method}
	return nil
}

// applyLaunchAttribute makes a, a top-level attribute of t's resource, an
// argument of the launch when it is one of its fields.
func applyLaunchAttribute(t resourceType, a *attribute, forResource bool) error {
	if t.launch == nil || !forResource || a.field == nil {
		return nil
	}
	name := a.field.Desc.Name()
	switch {
	case name == t.launch.template:
		if !a.computedOnly() {
			return fmt.Errorf("the launch's template isn't output only")
		}
		// Written as `.../web`, `.../web@default` or `.../web@latest`, it
		// is answered with the version it resolved to.
		a.optional, a.keep, a.replace, a.versioned = true, true, true, true
		a.spelling = &spelling{framework: "TemplateSpelling", replacer: "StringReplaceTemplate", doc: "Changing it replaces the " + words(t.singular) + ", unless it names the version it was launched from."}
		a.doc = strings.TrimSpace(a.doc + " Set it to launch the " + words(t.singular) + " from a template: `//compute.origincloudapis.eu/projects/{project}/launchTemplates/{launch_template}` for its default version, or with `@{version}`, `@default` or `@latest` after it for that version. The arguments set with it override the version's for this launch only. The state keeps what was configured while the template is the one launched from.")
	case slices.Contains(t.launch.requiredWithout, name):
		if !a.required {
			return fmt.Errorf("%s isn't required", name)
		}
		a.required, a.optional, a.computed, a.keep = false, true, true, true
		a.validators = append(a.validators, func(c conv) string {
			return c.framework("RequiredUnless") + "{Template: " + fmt.Sprintf("%q", t.launch.template) + "}"
		})
		a.doc = strings.TrimSpace(a.doc + " Required unless `" + string(t.launch.template) + "` is set; left out, it is the template's.")
	case slices.Contains(t.launch.chosen, name):
		if !a.optional || a.computed {
			return fmt.Errorf("%s isn't optional", name)
		}
		a.computed, a.keep = true, true
		a.doc = strings.TrimSpace(a.doc + " Left out, it keeps its current value, the template's when launched from one; set it empty to clear it.")
	}
	return nil
}

// checkLaunchesFound fails on a launch declared for a resource the API
// doesn't have, or no longer has, in one of the API's packages, those of
// files.
func checkLaunchesFound(files []*protogen.File) error {
	for _, l := range launches {
		published := slices.ContainsFunc(files, func(f *protogen.File) bool {
			return strings.HasPrefix(string(l.resource), string(f.Desc.Package())+".")
		})
		if published && !launchesFound[l.resource] {
			return fmt.Errorf("launched: no resource type %s", l.resource)
		}
	}
	return nil
}
