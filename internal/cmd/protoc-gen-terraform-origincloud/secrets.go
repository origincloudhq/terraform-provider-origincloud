package main

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A secret is an output-only string field the API answers with only once,
// in the response that generated it, such as a key pair's private key or a
// service account's client secret: its attribute is sensitive, and keeps
// the value from the create on, since the API never returns it again.
//
// Nothing in the API marks such a field, so each is declared here by the
// API's names, and checked against the API when generating: a secret whose
// resource or field is missing, or whose field isn't an output-only
// string, fails generation. A secret of a package the API doesn't have yet
// is left until it does, so that it is declared before its service is
// published, and the service's first generation already has it.
type secret struct {
	// resource is the message, such as origincloud.compute.v1beta1.KeyPair.
	resource protoreflect.FullName
	field    protoreflect.Name
}

var secrets = []secret{
	{"origincloud.compute.v1beta1.KeyPair", "private_key"},
	{"origincloud.iam.v1beta1.ServiceAccount", "client_secret"},
}

// secretsFound are the secrets whose fields were generated.
var secretsFound = map[secret]bool{}

// applySecret makes a, a resource's top-level attribute, a secret when it
// is declared one.
func applySecret(t resourceType, a *attribute, forResource bool) error {
	if a.field == nil {
		return nil
	}
	d := secret{t.message.Desc.FullName(), a.field.Desc.Name()}
	found := false
	for _, s := range secrets {
		found = found || s == d
	}
	if !found {
		return nil
	}
	if a.kind != "String" || a.wellKnown != "" || a.enum != nil || a.presence || !a.computedOnly() {
		return fmt.Errorf("secret %s: the field isn't an output-only string", d.field)
	}
	secretsFound[d] = true
	a.sensitive = true
	a.kept, a.keep = forResource, forResource
	return nil
}

// checkSecretsFound fails on a secret declared for a field the API doesn't
// have, or no longer has, in one of the API's packages, those of files.
func checkSecretsFound(files []*protogen.File) error {
	for _, s := range secrets {
		published := slices.ContainsFunc(files, func(f *protogen.File) bool {
			return strings.HasPrefix(string(s.resource), string(f.Desc.Package())+".")
		})
		if published && !secretsFound[s] {
			return fmt.Errorf("secret %s: no field %s.%s", s.field, s.resource, s.field)
		}
	}
	return nil
}
