package main

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A delete option is a boolean argument of the resource that only its
// destruction uses: it sets a field of the delete's request, such as a
// secret's `force`, which skips the recovery window. It isn't a field of the
// resource, so the API never answers with it: the state keeps the
// configured value, and the delete reads it from the state, so a change to
// it must be applied before the apply that destroys the resource.
//
// Nothing in the API says which request fields are such options, so each is
// declared here by the API's names, and checked against the API when
// generating: an option whose resource is missing, or whose delete request
// has no such boolean field, fails generation.
type deleteOption struct {
	// resource is the message, such as origincloud.secretmanager.v1beta1.Secret.
	resource protoreflect.FullName
	// attribute is the Terraform attribute, such as "force_delete".
	attribute string
	// field is the boolean field of the delete's request, such as "force".
	field protoreflect.Name
	// frees says that deleting with the option frees the resource's id, which
	// a delete without it keeps taken (AIP-135 soft deletion), so that a
	// plan replacing a resource whose state sets the option may keep the id.
	frees bool
	doc   string
}

var deleteOptions = []deleteOption{
	{
		resource:  "origincloud.secretmanager.v1beta1.Secret",
		attribute: "force_delete",
		field:     "force",
		frees:     true,
		doc: "Whether destroying the secret deletes it and all of its versions at once, with no recovery window, " +
			"which frees its `secret_id` for a new secret. Left out or `false`, destroying it only schedules the deletion: " +
			"the id stays taken, and the API refuses a new secret with it until the recovery window has passed, " +
			"or the secret is restored outside Terraform. Destroying reads this from the state, so apply a change to it " +
			"before the apply that destroys the secret. It needs the permission to force-delete the secret. " +
			"The API never returns it: an imported secret has none.",
	},
}

// A delete option resolved against the API.
type resolvedDeleteOption struct {
	deleteOption
	field *protogen.Field
}

// deleteOptionsOf returns the delete options declared for t's resource.
func deleteOptionsOf(t resourceType) ([]*resolvedDeleteOption, error) {
	var resolved []*resolvedDeleteOption
	for _, d := range deleteOptions {
		if d.resource != t.message.Desc.FullName() {
			continue
		}
		if t.del == nil {
			return nil, fmt.Errorf("delete option %s: %s has no delete method", d.attribute, t.message.Desc.Name())
		}
		field := fieldNamed(t.del.Input, string(d.field))
		if field == nil || field.Desc.Kind() != protoreflect.BoolKind || field.Desc.IsList() || field.Desc.IsMap() {
			return nil, fmt.Errorf("delete option %s: %s has no boolean field %s", d.attribute, t.del.Input.Desc.Name(), d.field)
		}
		resolved = append(resolved, &resolvedDeleteOption{deleteOption: d, field: field})
		deleteOptionsFound[d.resource] = true
	}
	return resolved, nil
}

// deleteOptionsFound are the resources whose delete options were generated.
var deleteOptionsFound = map[protoreflect.FullName]bool{}

// checkDeleteOptionsFound fails on a delete option declared for a resource
// the API doesn't have, or no longer has, in one of the API's packages,
// those of files: one of a package the API doesn't have yet is left until it
// does, as a secret is.
func checkDeleteOptionsFound(files []*protogen.File) error {
	for _, d := range deleteOptions {
		published := slices.ContainsFunc(files, func(f *protogen.File) bool {
			return strings.HasPrefix(string(d.resource), string(f.Desc.Package())+".")
		})
		if published && !deleteOptionsFound[d.resource] {
			return fmt.Errorf("delete option %s: no resource %s", d.attribute, d.resource)
		}
	}
	return nil
}
