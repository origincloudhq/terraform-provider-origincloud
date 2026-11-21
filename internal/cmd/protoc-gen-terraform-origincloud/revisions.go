package main

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A revision is a field of a resource that the API never changes in place,
// such as a launch template's recipe (AIP-162): a change adds a revision
// of it, numbered by the API, and the resource shows the newest. Its field
// is REQUIRED and IMMUTABLE, so that the generic generation would replace
// the resource when it changes; declared here, the resource keeps it
// instead and adds a revision, with the method that does.
//
// The API names the resource read at a revision with its number,
// `projects/p/launchTemplates/web@3`, and reads the newest at `@latest`.
// The state holds the resource's own name, and the resource is read at
// its newest revision, which is the recipe the configuration describes.
//
// The attribute follows names the field that says which revision the
// resource's users get, a template's default version: left out of the
// configuration, it is the new revision whenever the revised field changes.
//
// Nothing in the API marks such a field, so each is declared here, by the
// API's names, and checked against the API when generating: a declaration
// whose resource, fields or methods are missing fails it. A declaration
// of a package the API doesn't have yet is left until it does, as a secret
// is.
type revision struct {
	// resource is the message, such as
	// origincloud.compute.v1beta1.LaunchTemplate.
	resource protoreflect.FullName
	// field is the revised field; follows the int32 field that points at a
	// revision.
	field, follows, newest protoreflect.Name
	// create is the method that adds a revision, and list the one that
	// lists them, by their names in the resource's service, such as
	// CreateLaunchTemplateVersion.
	create, list protoreflect.Name
	// singular and plural name the revisions, such as "version", in the
	// docs and the data source that lists them.
	singular, plural string
}

var revisions = []revision{
	{
		resource: "origincloud.compute.v1beta1.LaunchTemplate",
		field:    "launch_template_data",
		follows:  "default_version",
		newest:   "latest_version",
		create:   "CreateLaunchTemplateVersion",
		list:     "ListLaunchTemplateVersions",
		singular: "version",
		plural:   "versions",
	},
}

// revisionsFound are the revisions whose resource was generated.
var revisionsFound = map[protoreflect.FullName]bool{}

// A resolved revision has its methods.
type resolvedRevision struct {
	revision
	create, list *protogen.Method
	message      *protogen.Message
}

// applyRevision finds t's declared revision, if it has one, and checks it
// against the API.
func applyRevision(t *resourceType) error {
	i := slices.IndexFunc(revisions, func(r revision) bool { return r.resource == t.message.Desc.FullName() })
	if i < 0 {
		return nil
	}
	r := revisions[i]
	for _, name := range []protoreflect.Name{r.field, r.follows, r.newest} {
		f := fieldNamed(t.message, string(name))
		if f == nil {
			return fmt.Errorf("revised: no field %s", name)
		}
	}
	revised := fieldNamed(t.message, string(r.field))
	behaviors := fieldBehaviors(revised)
	if revised.Message == nil || revised.Desc.IsList() || !slices.Contains(behaviors, annotations.FieldBehavior_REQUIRED) || !slices.Contains(behaviors, annotations.FieldBehavior_IMMUTABLE) {
		return fmt.Errorf("revised: %s isn't a REQUIRED, IMMUTABLE message", r.field)
	}
	follows := fieldNamed(t.message, string(r.follows))
	if follows.Desc.Kind() != protoreflect.Int32Kind || !slices.Contains(fieldBehaviors(follows), annotations.FieldBehavior_OPTIONAL) || follows.Desc.HasPresence() || t.update == nil {
		return fmt.Errorf("revised: %s isn't an OPTIONAL int32 without presence, or the resource has no update", r.follows)
	}
	res := &resolvedRevision{revision: r, message: t.message}
	for _, m := range t.service.Methods {
		switch m.Desc.Name() {
		case r.create:
			res.create = m
		case r.list:
			res.list = m
		}
	}
	switch {
	case res.create == nil || res.list == nil:
		return fmt.Errorf("revised: the service has no %s or no %s", r.create, r.list)
	case fieldNamed(res.create.Input, "name") == nil || fieldOfType(res.create.Input, revised.Message) == nil || res.create.Output != t.message:
		return fmt.Errorf("revised: %s must take the resource's name and %s, and answer with the %s", r.create, r.field, t.message.Desc.Name())
	case fieldOfType(res.list.Output, t.message) == nil || !fieldOfType(res.list.Output, t.message).Desc.IsList() || fieldNamed(res.list.Input, "name") == nil:
		return fmt.Errorf("revised: %s must take the resource's name and answer with a list of the %s", r.list, t.message.Desc.Name())
	}
	revisionsFound[r.resource] = true
	methodsCalled[res.create.Desc.FullName()] = true
	methodsCalled[res.list.Desc.FullName()] = true
	t.revision = res
	return nil
}

// applyRevisionAttribute makes a, a top-level attribute of t's resource, the
// revised field or the one that follows it, when t has a revision.
func applyRevisionAttribute(t resourceType, a *attribute, forResource bool) error {
	if t.revision == nil || !forResource || a.field == nil {
		return nil
	}
	switch a.field.Desc.Name() {
	case t.revision.field:
		if a.message == nil || !a.replace {
			return fmt.Errorf("the revised field isn't a message that replaces the resource")
		}
		a.replace, a.updatable = false, true
		a.doc = strings.TrimSpace(strings.TrimSuffix(a.doc, "Changing it replaces the "+words(t.singular)+".")) +
			" Changing it adds a " + t.revision.singular + "; the " + words(t.singular) + " shows its newest."
	case t.revision.follows:
		if a.kind != "Int64" || !a.optional || a.computed {
			return fmt.Errorf("the field that follows the revisions isn't an optional number")
		}
		a.computed = true
		a.modifiers = append(a.modifiers, func(c conv) string {
			return c.framework("Int64FollowsRevision") + "(" + fmt.Sprintf("%q", t.revision.field) + ")"
		})
		a.doc = strings.TrimSpace(a.doc + " Left out, it is the newest version after every change to `" + string(t.revision.field) + "`, and what it was otherwise.")
	}
	return nil
}

// checkRevisionsFound fails on a revision declared for a resource the API
// doesn't have, or no longer has, in one of the API's packages, those of
// files.
func checkRevisionsFound(files []*protogen.File) error {
	for _, r := range revisions {
		published := slices.ContainsFunc(files, func(f *protogen.File) bool {
			return strings.HasPrefix(string(r.resource), string(f.Desc.Package())+".")
		})
		if published && !revisionsFound[r.resource] {
			return fmt.Errorf("revised: no resource type %s", r.resource)
		}
	}
	return nil
}

// generateRevision writes the spec's revision, when the resource has one.
func generateRevision(g *protogen.GeneratedFile, t resourceType, message, client string) {
	r := t.revision
	if r == nil {
		return
	}
	field := func(name protoreflect.Name) string { return fieldNamed(t.message, string(name)).GoName }
	ctx := g.QualifiedGoIdent(contextPackage.Ident("Context"))
	coreClient := "*" + g.QualifiedGoIdent(corePackage.Ident("Client"))
	frameworkName := g.QualifiedGoIdent(frameworkPackage.Ident("Revision"))
	input := g.QualifiedGoIdent(r.create.Input.GoIdent)
	g.P("Revision: &", frameworkName, "[", message, "]{")
	g.P("Field: ", fmt.Sprintf("%q", r.field), ",")
	g.P("Latest: \"@latest\",")
	g.P("Name: func(p ", message, ") string { return p.GetName() },")
	g.P("Unversioned: func(p ", message, ") { p.Name = ", g.QualifiedGoIdent(frameworkPackage.Ident("WithoutRevision")), "(p.GetName()) },")
	g.P("Revise: func(ctx ", ctx, ", client ", coreClient, ", resource ", message, ") (", message, ", error) {")
	g.P("return ", client, ".", r.create.GoName, "(ctx, &", input, "{",
		fieldNamed(r.create.Input, "name").GoName, ": resource.GetName(), ",
		fieldOfType(r.create.Input, fieldNamed(t.message, string(r.field)).Message).GoName, ": resource.Get", field(r.field), "()})")
	g.P("},")
	g.P("Follow: func(resource, revised ", message, ") {")
	g.P("if resource.Get", field(r.follows), "() == 0 {")
	g.P("resource.", field(r.follows), " = revised.Get", field(r.newest), "()")
	g.P("}")
	g.P("},")
	g.P("},")
}

// generateRevisionsDataSource writes the data source that lists a resource's
// revisions, each as the resource at it: the items of the resource's list
// data source, for one resource.
func generateRevisionsDataSource(g *protogen.GeneratedFile, t resourceType) error {
	r := t.revision
	name := lowerFirst(t.message.GoIdent.GoName)
	items := fieldOfType(r.list.Output, t.message)
	itemAttrs, err := attributes(t, false, name+"Item")
	if err != nil {
		return err
	}
	for i := range itemAttrs {
		itemAttrs[i].required, itemAttrs[i].computed = false, true
	}
	itemModel, itemFromProto := name+"ItemModel", name+"ItemFromProto"
	typeName := "_" + t.shortcode + "_" + snake(t.singular) + "_" + snake(r.plural)
	model := name + "RevisionsModel"
	attribute := snake(t.singular)
	schemaPackage := datasourceSchemaPackage
	client := g.QualifiedGoIdent(t.file.GoImportPath.Ident("New"+strings.TrimSuffix(t.service.GoName, "Service")+"Client")) + "(client)"
	spec := g.QualifiedGoIdent(frameworkPackage.Ident("ListDataSourceSpec"))
	paginated := fieldNamed(r.list.Input, "page_token") != nil && fieldNamed(r.list.Output, "next_page_token") != nil
	if !paginated {
		return fmt.Errorf("%s isn't paginated", r.list.Desc.Name())
	}
	doc := fmt.Sprintf("The %s whose %s to list: its `id`, or its relative name.", words(t.singular), r.plural)

	g.P()
	g.P("// ", model, " is origincloud", typeName, "'s state.")
	g.P("type ", model, " struct {")
	g.P(camel(attribute), " ", g.QualifiedGoIdent(typesPackage.Ident("String")), " `tfsdk:\"", attribute, "\"`")
	g.P(camel(r.plural), " []", itemModel, " `tfsdk:\"", r.plural, "\"`")
	g.P("}")

	g.P()
	g.P("func ", name, "RevisionsSchema() ", g.QualifiedGoIdent(schemaPackage.Ident("Schema")), " {")
	g.P("return ", g.QualifiedGoIdent(schemaPackage.Ident("Schema")), "{")
	g.P("MarkdownDescription: ", fmt.Sprintf("%q", prose(r.list.Comments.Leading)), ",")
	g.P("Attributes: map[string]", g.QualifiedGoIdent(schemaPackage.Ident("Attribute")), "{")
	g.P(fmt.Sprintf("%q", attribute), ": ", g.QualifiedGoIdent(schemaPackage.Ident("StringAttribute")), "{")
	g.P("MarkdownDescription: ", fmt.Sprintf("%q", doc), ",")
	g.P("Required: true,")
	g.P("},")
	g.P(fmt.Sprintf("%q", r.plural), ": ", g.QualifiedGoIdent(schemaPackage.Ident("ListNestedAttribute")), "{")
	g.P("MarkdownDescription: ", fmt.Sprintf("%q", prose(items.Comments.Leading)), ",")
	g.P("Computed: true,")
	g.P("NestedObject: ", g.QualifiedGoIdent(schemaPackage.Ident("NestedAttributeObject")), "{")
	generateAttributes(g, schemaPackage, itemAttrs)
	g.P("},")
	g.P("},")
	g.P("},")
	g.P("}")
	g.P("}")

	g.P()
	g.P("func ", name, "RevisionsDataSource() ", spec, "[", model, "] {")
	g.P("return ", spec, "[", model, "]{")
	g.P("TypeName: ", fmt.Sprintf("%q", typeName), ",")
	g.P("Schema: ", name, "RevisionsSchema(),")
	g.P("List: func(ctx ", g.QualifiedGoIdent(contextPackage.Ident("Context")), ", client *", g.QualifiedGoIdent(corePackage.Ident("Client")), ", m *", model, ") error {")
	g.P("name, err := ", g.QualifiedGoIdent(frameworkPackage.Ident("RelativeName")), "(", fmt.Sprintf("%q", t.shortcode), ", m.", camel(attribute), ".ValueString())")
	g.P("if err != nil {")
	g.P("return err")
	g.P("}")
	// Empty rather than null when the list is.
	g.P("m.", camel(r.plural), " = []", itemModel, "{}")
	g.P("for p, err := range ", client, ".All", strings.TrimPrefix(string(r.list.Desc.Name()), "List"), "(ctx, &", g.QualifiedGoIdent(r.list.Input.GoIdent), "{", fieldNamed(r.list.Input, "name").GoName, ": name}) {")
	g.P("if err != nil {")
	g.P("return err")
	g.P("}")
	g.P("var item ", itemModel)
	g.P("if err := ", itemFromProto, "(p, &item); err != nil {")
	g.P("return err")
	g.P("}")
	g.P("m.", camel(r.plural), " = append(m.", camel(r.plural), ", item)")
	g.P("}")
	g.P("return nil")
	g.P("},")
	g.P("}")
	g.P("}")
	return nil
}
