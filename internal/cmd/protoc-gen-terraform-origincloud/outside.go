package main

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A type created outside Terraform is a resource type the API creates but
// never deletes, such as a support case, which is closed instead: as a
// resource, Terraform could create one but never destroy it. Its create is
// ignored, so it is read only, by the data sources any read-only type gets.
//
// Nothing in the API marks such a type, and a create without a delete
// fails generation unless its type is declared here, by the API's name,
// with the reason. Each declaration is checked against the API when
// generating: one whose resource type is missing, or that the API doesn't
// create, or deletes, fails generation. A declaration of a package the API
// doesn't have yet is left until it does, as a secret is.
type outside struct {
	// resource is the message, such as origincloud.support.v1beta1.Case.
	resource protoreflect.FullName
	// reason says why Terraform doesn't create it.
	reason string
}

var createdOutside = []outside{
	{"origincloud.support.v1beta1.Case", "a case is closed, never deleted"},
	{"origincloud.support.v1beta1.CaseMessage", "a message is never changed or removed once added"},
}

// outsideFound are the types created outside Terraform that were
// generated.
var outsideFound = map[protoreflect.FullName]bool{}

// applyOutside ignores t's create when its type is declared created
// outside Terraform.
func applyOutside(t *resourceType) error {
	name := t.message.Desc.FullName()
	if !slices.ContainsFunc(createdOutside, func(o outside) bool { return o.resource == name }) {
		return nil
	}
	switch {
	case t.del != nil:
		return fmt.Errorf("declared created outside Terraform, but Delete%s deletes it, so it can be a resource", t.message.Desc.Name())
	case t.create == nil:
		return fmt.Errorf("declared created outside Terraform, but there is no Create%s", t.message.Desc.Name())
	}
	outsideFound[name] = true
	methodsCalled[t.create.Desc.FullName()] = true // its reason is declared here
	t.create = nil
	return nil
}

// checkOutsideFound fails on a type declared created outside Terraform
// that the API doesn't have, or no longer has, in one of the API's
// packages, those of files.
func checkOutsideFound(files []*protogen.File) error {
	for _, o := range createdOutside {
		published := slices.ContainsFunc(files, func(f *protogen.File) bool {
			return strings.HasPrefix(string(o.resource), string(f.Desc.Package())+".")
		})
		if published && !outsideFound[o.resource] {
			return fmt.Errorf("created outside Terraform: no resource type %s", o.resource)
		}
	}
	return nil
}
