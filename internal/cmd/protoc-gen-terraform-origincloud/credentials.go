package main

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A credential type is a resource type whose create answers with a
// credential to the API, such as a service account key's private key: as a
// resource, Terraform would keep the credential in the state, where anyone
// who can read the state could act as the account. So Terraform never
// creates, changes or deletes one; it is read only, by the data sources any
// read-only type gets, which leave the credential's field out. A key is
// created with the CLI or the console and handed to the system that needs
// it.
//
// Nothing in the API marks such a type, so each is declared here by the
// API's names, with the field that carries the credential and the reason.
// Each declaration is checked against the API when generating: one whose
// type is missing, that the API doesn't create, or whose field isn't an
// output-only string, fails generation. A declaration of a package the API
// doesn't have yet is left until it does, as a secret is.
type credential struct {
	// resource is the message, such as
	// origincloud.iam.v1beta1.ServiceAccountKey.
	resource protoreflect.FullName
	// field carries the credential in the create's answer.
	field protoreflect.Name
	// reason says why Terraform doesn't create it.
	reason string
}

var credentials = []credential{
	{"origincloud.iam.v1beta1.ServiceAccountKey", "credentials_file", "its create answers with the key's private key, which would land in the state; create a key with the CLI or the console"},
}

// credentialsFound are the credential types that were generated.
var credentialsFound = map[protoreflect.FullName]bool{}

// credentialOf returns t's declaration as a credential type, if it is one.
func credentialOf(t resourceType) (credential, bool) {
	i := slices.IndexFunc(credentials, func(c credential) bool { return c.resource == t.message.Desc.FullName() })
	if i < 0 {
		return credential{}, false
	}
	return credentials[i], true
}

// applyCredential makes t read only when it is declared a credential type:
// its create, update and delete are ignored, their reason declared here.
func applyCredential(t *resourceType) error {
	c, ok := credentialOf(*t)
	if !ok {
		return nil
	}
	f := fieldNamed(t.message, string(c.field))
	switch {
	case t.create == nil:
		return fmt.Errorf("declared a credential type, but there is no Create%s", t.message.Desc.Name())
	case f == nil:
		return fmt.Errorf("declared a credential type, but there is no field %s", c.field)
	case f.Desc.Kind() != protoreflect.StringKind || f.Desc.IsList() || !slices.Contains(fieldBehaviors(f), annotations.FieldBehavior_OUTPUT_ONLY):
		return fmt.Errorf("declared a credential type, but its field %s isn't an output-only string", c.field)
	}
	credentialsFound[c.resource] = true
	methodsCalled[t.create.Desc.FullName()] = true // its reason is declared here
	t.create = nil
	// Terraform has none to change or delete.
	for _, m := range []**protogen.Method{&t.update, &t.del} {
		if *m != nil {
			methodsCalled[(*m).Desc.FullName()] = true
			*m = nil
		}
	}
	return nil
}

// isCredentialField says that f is the field carrying t's credential, left
// out of every schema.
func isCredentialField(t resourceType, f *protogen.Field) bool {
	c, ok := credentialOf(t)
	return ok && f.Desc.Name() == c.field
}

// checkCredentialsFound fails on a credential type the API doesn't have,
// or no longer has, in one of the API's packages, those of files.
func checkCredentialsFound(files []*protogen.File) error {
	for _, c := range credentials {
		published := slices.ContainsFunc(files, func(f *protogen.File) bool {
			return strings.HasPrefix(string(c.resource), string(f.Desc.Package())+".")
		})
		if published && !credentialsFound[c.resource] {
			return fmt.Errorf("credential type: no resource type %s", c.resource)
		}
	}
	return nil
}
