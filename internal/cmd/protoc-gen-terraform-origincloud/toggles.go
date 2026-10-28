package main

import (
	"fmt"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A toggle is a boolean attribute that a pair of custom methods (AIP-136)
// sets and clears, where the API has no field to write, only a state the
// methods change: a snapshot is protected by ProtectSnapshot, not by an
// update.
//
// Nothing in the API says which methods pair up this way, so each toggle is
// declared here by the API's names, and checked against the API when
// generating: a toggle whose resource, methods or state value is missing,
// or whose methods don't take the resource's name and answer with an
// operation on it, fails generation.
type toggle struct {
	// resource is the message, such as origincloud.pvs.v1beta1.Snapshot.
	resource protoreflect.FullName
	// attribute is the Terraform attribute, such as "protected".
	attribute string
	// set and clear are the methods, of the service that gets the
	// resource.
	set, clear protoreflect.Name
	// state is the resource's enum field the methods change, on its value
	// when set and off its value when clear; any other value is neither.
	state   protoreflect.Name
	on, off protoreflect.Name
	// clearBeforeDelete clears the resource before deleting it, when the
	// API refuses to delete it set.
	clearBeforeDelete bool
	doc               string
}

var toggles = []toggle{
	{
		resource:          "origincloud.pvs.v1beta1.Snapshot",
		attribute:         "protected",
		set:               "ProtectSnapshot",
		clear:             "UnprotectSnapshot",
		state:             "protection_state",
		on:                "PROTECTED",
		off:               "UNPROTECTED",
		clearBeforeDelete: true,
		doc: "Whether the snapshot is protected: only a protected snapshot is the source of volumes and images, " +
			"and it can't be deleted. Setting it to `true` protects the snapshot and `false` unprotects it, each waiting " +
			"until done. Destroying a protected snapshot unprotects it first. Left out, the snapshot's protection is " +
			"left as it is.",
	},
}

// A toggle resolved against the API.
type resolvedToggle struct {
	toggle
	set, clear *protogen.Method
	state      *protogen.Field
	on, off    *protogen.EnumValue
}

// togglesOf returns the toggles declared for t's resource.
func togglesOf(t resourceType) ([]resolvedToggle, error) {
	var resolved []resolvedToggle
	for _, d := range toggles {
		if d.resource != t.message.Desc.FullName() {
			continue
		}
		r := resolvedToggle{toggle: d}
		for _, m := range t.service.Methods {
			switch m.Desc.Name() {
			case d.set:
				r.set = m
			case d.clear:
				r.clear = m
			}
		}
		for _, m := range []*protogen.Method{r.set, r.clear} {
			if m == nil {
				return nil, fmt.Errorf("toggle %s: %s has no method %s or %s", d.attribute, t.service.Desc.Name(), d.set, d.clear)
			}
			if fieldNamed(m.Input, "name") == nil || operationResult(m) != t.message.Desc.FullName() {
				return nil, fmt.Errorf("toggle %s: %s doesn't take a name and answer with an operation whose result is the %s", d.attribute, m.Desc.Name(), t.message.Desc.Name())
			}
		}
		r.state = fieldNamed(t.message, string(d.state))
		if r.state == nil || r.state.Enum == nil {
			return nil, fmt.Errorf("toggle %s: %s has no enum field %s", d.attribute, t.message.Desc.Name(), d.state)
		}
		for _, v := range r.state.Enum.Values {
			switch v.Desc.Name() {
			case d.on:
				r.on = v
			case d.off:
				r.off = v
			}
		}
		if r.on == nil || r.off == nil {
			return nil, fmt.Errorf("toggle %s: %s has no value %s or %s", d.attribute, r.state.Enum.Desc.Name(), d.on, d.off)
		}
		resolved = append(resolved, r)
		togglesFound[d.resource] = true
	}
	return resolved, nil
}

// togglesFound are the resources whose toggles were generated.
var togglesFound = map[protoreflect.FullName]bool{}

// checkTogglesFound fails on a toggle declared for a resource the API
// doesn't have, or no longer has.
func checkTogglesFound() error {
	for _, d := range toggles {
		if !togglesFound[d.resource] {
			return fmt.Errorf("toggle %s: no resource %s", d.attribute, d.resource)
		}
	}
	return nil
}
