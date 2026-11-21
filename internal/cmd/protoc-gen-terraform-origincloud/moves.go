package main

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A move is a resource type the API renamed, such as DnsPolicy to
// ResolverPolicy: the new type's resource takes the state of the old one,
// when a `moved` block says so (the framework's Move). The old type's
// attributes and names are rewritten for the new.
//
// Nothing in the API marks a rename, so each is declared here, by the API's
// name of the new type, and checked against the API when generating: a
// move whose type is missing fails generation. A declaration of a package
// the API doesn't have yet is left until it does, as a secret is.
type move struct {
	// resource is the new message, such as
	// origincloud.network.v1beta1.ResolverPolicy.
	resource protoreflect.FullName
	// typeName is the old resource's Terraform type name after the
	// provider's, such as _network_dns_policy.
	typeName string
	// attributes are the old resource's attributes that were renamed, by
	// the old name; segments the old collection names of its names, by the
	// old one.
	attributes map[string]string
	segments   map[string]string
}

var moves = []move{
	{
		resource: "origincloud.network.v1beta1.ResolverPolicy",
		typeName: "_network_dns_policy",
		segments: map[string]string{"dnsPolicies": "resolverPolicies"},
	},
	{
		resource:   "origincloud.network.v1beta1.ResolverPolicyAssociation",
		typeName:   "_network_dns_policy_association",
		attributes: map[string]string{"dns_policy": "resolver_policy"},
		segments:   map[string]string{"dnsPolicies": "resolverPolicies", "dnsPolicyAssociations": "resolverPolicyAssociations"},
	},
}

// movesFound are the moves whose resource was generated.
var movesFound = map[protoreflect.FullName]bool{}

// generateMoves declares the types t's resource replaces.
func generateMoves(g *protogen.GeneratedFile, t resourceType) {
	name := t.message.Desc.FullName()
	for _, m := range moves {
		if m.resource != name {
			continue
		}
		movesFound[name] = true
		g.P("MovedFrom: []", g.QualifiedGoIdent(frameworkPackage.Ident("Move")), "{{")
		g.P("TypeName: ", fmt.Sprintf("%q", m.typeName), ",")
		g.P("Attributes: map[string]string{", mapLiteral(m.attributes), "},")
		g.P("Segments: map[string]string{", mapLiteral(m.segments), "},")
		g.P("}},")
	}
}

// mapLiteral writes m's entries in order.
func mapLiteral(m map[string]string) string {
	var entries []string
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}) {
		entries = append(entries, fmt.Sprintf("%q: %q", k, m[k]))
	}
	return strings.Join(entries, ", ")
}

// checkMovesFound fails on a move whose resource the API doesn't have, or
// no longer has, in one of the API's packages, those of files.
func checkMovesFound(files []*protogen.File) error {
	for _, m := range moves {
		published := slices.ContainsFunc(files, func(f *protogen.File) bool {
			return strings.HasPrefix(string(m.resource), string(f.Desc.Package())+".")
		})
		if published && !movesFound[m.resource] {
			return fmt.Errorf("moved from %s: no resource type %s", m.typeName, m.resource)
		}
	}
	return nil
}
