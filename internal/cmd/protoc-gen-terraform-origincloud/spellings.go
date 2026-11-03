package main

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A spelling is a required, immutable string field that the API answers
// in a form of its own, rather than as it was written, such as a role
// binding's member: `user:Mail@Example.com` is answered as
// `user:mail@example.com`, and later with the user's new address. Its
// attribute keeps the value configured while the API's answer names the
// same thing, and is replaced only when the configuration names something
// else (the framework's Spelling).
//
// Nothing in the API marks such a field, so each is declared here by the
// API's names, with the framework's Spelling that says when two values
// name the same thing, and checked against the API when generating: a
// spelling whose resource or field is missing, or whose field isn't a
// required, immutable string, fails generation. A spelling of a package the
// API doesn't have yet is left until it does, as a secret is.
type spelling struct {
	// resource is the message, such as origincloud.iam.v1beta1.RoleBinding.
	resource protoreflect.FullName
	field    protoreflect.Name
	// framework names the framework's Spelling, such as MemberSpelling.
	framework string
	// doc replaces the attribute's "Changing it replaces" sentence.
	doc string
}

var spellings = []spelling{
	{
		resource:  "origincloud.iam.v1beta1.RoleBinding",
		field:     "member",
		framework: "MemberSpelling",
		doc: "Changing it replaces the role binding, unless it names the same member another way: " +
			"an address in another case, or the address the user's account has now. " +
			"While the API answers with the same account under another address, or as `userId:{user}`, " +
			"the state keeps the member as configured; answered as `deleted:user:{user}`, " +
			"once the account is gone, the binding is replaced.",
	},
}

// spellingsFound are the spellings whose fields were generated.
var spellingsFound = map[spelling]bool{}

// applySpelling makes a, a resource's top-level attribute, spelled when it
// is declared so.
func applySpelling(t resourceType, a *attribute, forResource bool) error {
	if a.field == nil || !forResource {
		return nil
	}
	i := slices.IndexFunc(spellings, func(s spelling) bool {
		return s.resource == t.message.Desc.FullName() && s.field == a.field.Desc.Name()
	})
	if i < 0 {
		return nil
	}
	s := spellings[i]
	if a.kind != "String" || a.wellKnown != "" || a.enum != nil || a.oneof != nil || !a.required || !a.replace {
		return fmt.Errorf("spelling %s: the field isn't a REQUIRED, IMMUTABLE string", s.field)
	}
	spellingsFound[s] = true
	a.spelling = &spellings[i]
	a.doc = strings.TrimSpace(strings.TrimSpace(strings.TrimSuffix(a.doc, "Changing it replaces the "+words(t.singular)+".")) + " " + s.doc)
	return nil
}

// checkSpellingsFound fails on a spelling declared for a field the API
// doesn't have, or no longer has, in one of the API's packages, those of
// files.
func checkSpellingsFound(files []*protogen.File) error {
	for _, s := range spellings {
		published := slices.ContainsFunc(files, func(f *protogen.File) bool {
			return strings.HasPrefix(string(s.resource), string(f.Desc.Package())+".")
		})
		if published && !spellingsFound[s] {
			return fmt.Errorf("spelling %s: no field %s.%s", s.field, s.resource, s.field)
		}
	}
	return nil
}
