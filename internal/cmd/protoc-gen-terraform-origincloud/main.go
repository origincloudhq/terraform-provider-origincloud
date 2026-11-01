// protoc-gen-terraform-origincloud generates the provider's resources and
// data sources from the Origin Cloud APIs, over the Go SDK's clients.
//
// A resource message that a service can get by name (AIP-131) becomes a
// resource when the service can also create and delete it (AIP-133,
// AIP-135), and a data source otherwise. The fields' behaviours (AIP-203)
// make the schema: REQUIRED is required, OUTPUT_ONLY computed, and
// IMMUTABLE replaces the resource when it changes. The variables of the
// resource's name before its own, such as project and zone, are required
// and replace it too. Nested messages, repeated fields and maps of
// messages become nested attributes, lists, sets and maps (fields.go).
//
// A resource type the service also lists (AIP-132) gets a second data
// source, named after its plural, that reads everything the list returns,
// every page of it.
//
// It runs over every file at once (buf's strategy "all"), writing a
// package per API package under internal/services and the list of them in
// internal/provider.
package main

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/pluginpb"
)

const (
	module = "github.com/origincloudhq/terraform-provider-origincloud"

	frameworkPackage  = protogen.GoImportPath(module + "/internal/framework")
	corePackage       = protogen.GoImportPath("go.origincloud.eu/sdk/core")
	contextPackage    = protogen.GoImportPath("context")
	fieldmaskPackage  = protogen.GoImportPath("google.golang.org/protobuf/types/known/fieldmaskpb")
	typesPackage      = protogen.GoImportPath("github.com/hashicorp/terraform-plugin-framework/types")
	resourcePackage   = protogen.GoImportPath("github.com/hashicorp/terraform-plugin-framework/resource")
	datasourcePackage = protogen.GoImportPath("github.com/hashicorp/terraform-plugin-framework/datasource")
	planPackage       = protogen.GoImportPath("github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier")
	validatorPackage  = protogen.GoImportPath("github.com/hashicorp/terraform-plugin-framework/schema/validator")

	resourceSchemaPackage   = protogen.GoImportPath("github.com/hashicorp/terraform-plugin-framework/resource/schema")
	datasourceSchemaPackage = protogen.GoImportPath("github.com/hashicorp/terraform-plugin-framework/datasource/schema")

	operationType = "google.longrunning.Operation"
	emptyType     = "google.protobuf.Empty"
)

// The directories the plugin writes the packages, and the list of them,
// in. The tests generate a test API's elsewhere.
var (
	servicesDir = "internal/services"
	providerDir = "internal/provider"
)

func main() {
	protogen.Options{}.Run(generate)
}

// generate writes the provider's packages for every file to generate.
func generate(gen *protogen.Plugin) error {
	gen.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
	var packages []*servicePackage
	for _, f := range gen.Files {
		if !f.Generate {
			continue
		}
		if err := generateFile(gen, f, &packages); err != nil {
			return fmt.Errorf("%s: %w", f.Desc.Path(), err)
		}
	}
	if err := checkTogglesFound(); err != nil {
		return err
	}
	for _, p := range packages {
		generatePackageList(gen, p)
	}
	generateProviderList(gen, packages)
	return nil
}

// A resource type a service serves, and the methods it has for it.
type resourceType struct {
	file    *protogen.File
	service *protogen.Service
	message *protogen.Message
	// shortcode is the service's, such as "pvs".
	shortcode string
	// singular is the resource's, such as "storageClass".
	singular string
	pattern  string
	// The variables of pattern, such as project, zone and volume.
	variables []string

	// plural is the resource's, such as "storageClasses".
	plural string

	get, create, update, del, list *protogen.Method
}

// A package of the provider, one per API package: its resources and data
// sources.
type servicePackage struct {
	name                   string
	resources, dataSources []constructor
}

// A constructor is a resource's or data source's: the framework function
// that makes it, such as NewDataSource, and the spec function it takes.
type constructor struct {
	framework, spec string
}

func (p *servicePackage) importPath() protogen.GoImportPath {
	return protogen.GoImportPath(module + "/" + servicesDir + "/" + p.name)
}

// generateFile writes the resources and data sources of one file's
// services into its API package's directory.
func generateFile(gen *protogen.Plugin, f *protogen.File, packages *[]*servicePackage) error {
	pkg := string(f.GoPackageName)
	i := slices.IndexFunc(*packages, func(p *servicePackage) bool { return p.name == pkg })
	if i < 0 {
		*packages = append(*packages, &servicePackage{name: pkg})
		i = len(*packages) - 1
	}
	p := (*packages)[i]
	for _, s := range f.Services {
		host, _ := proto.GetExtension(s.Desc.Options(), annotations.E_DefaultHost).(string)
		if host == "" {
			continue // served by every service, such as operations
		}
		shortcode, _, _ := strings.Cut(host, ".")
		for _, t := range resourceTypes(f, s, shortcode) {
			g := gen.NewGeneratedFile(path.Join(servicesDir, pkg, snake(t.singular)+".gen.go"), p.importPath())
			g.P("// Code generated by protoc-gen-terraform-origincloud. DO NOT EDIT.")
			g.P("// source: ", f.Desc.Path())
			g.P()
			g.P("package ", pkg)
			var err error
			if t.create != nil {
				err = generateResource(g, t)
				p.resources = append(p.resources, constructor{"NewResource", lowerFirst(t.message.GoIdent.GoName) + "Resource"})
			} else {
				err = generateDataSource(g, t)
				p.dataSources = append(p.dataSources, constructor{"NewDataSource", lowerFirst(t.message.GoIdent.GoName) + "DataSource"})
			}
			if err == nil && t.list != nil {
				err = generateListDataSource(g, t)
				p.dataSources = append(p.dataSources, constructor{"NewListDataSource", lowerFirst(t.message.GoIdent.GoName) + "ListDataSource"})
			}
			if err != nil {
				return fmt.Errorf("%s: %w", t.message.Desc.Name(), err)
			}
		}
	}
	return nil
}

// generatePackageList writes the constructors of a package's resources
// and data sources.
func generatePackageList(gen *protogen.Plugin, p *servicePackage) {
	if len(p.resources)+len(p.dataSources) == 0 {
		return
	}
	g := gen.NewGeneratedFile(path.Join(servicesDir, p.name, "services.gen.go"), p.importPath())
	g.P("// Code generated by protoc-gen-terraform-origincloud. DO NOT EDIT.")
	g.P()
	g.P("package ", p.name)
	for _, list := range []struct {
		name, kind   string
		pkg          protogen.GoImportPath
		constructors []constructor
	}{
		{"Resources", "Resource", resourcePackage, p.resources},
		{"DataSources", "DataSource", datasourcePackage, p.dataSources},
	} {
		constructor := "func() " + g.QualifiedGoIdent(list.pkg.Ident(list.kind))
		g.P()
		g.P("// ", list.name, " returns the constructors of the package's ", strings.TrimSpace(words(list.name)), ".")
		g.P("func ", list.name, "() []", constructor, " {")
		g.P("return []", constructor, "{")
		for _, c := range list.constructors {
			g.P(g.QualifiedGoIdent(frameworkPackage.Ident(c.framework)), "(", c.spec, "()),")
		}
		g.P("}")
		g.P("}")
	}
}

// resourceTypes returns the resource types a service gets by name.
func resourceTypes(f *protogen.File, s *protogen.Service, shortcode string) []resourceType {
	methods := map[string]*protogen.Method{}
	for _, m := range s.Methods {
		methods[string(m.Desc.Name())] = m
	}
	var types []resourceType
	for _, m := range s.Methods {
		message := m.Output
		descriptor, _ := proto.GetExtension(message.Desc.Options(), annotations.E_Resource).(*annotations.ResourceDescriptor)
		name := string(message.Desc.Name())
		if descriptor == nil || string(m.Desc.Name()) != "Get"+name || len(descriptor.GetPattern()) != 1 {
			continue
		}
		pattern := descriptor.GetPattern()[0]
		types = append(types, resourceType{
			file: f, service: s, message: message,
			shortcode: shortcode,
			singular:  descriptor.GetSingular(),
			plural:    descriptor.GetPlural(),
			pattern:   pattern,
			variables: patternVariables(pattern),
			get:       m,
			create:    methods["Create"+name],
			update:    methods["Update"+name],
			del:       methods["Delete"+name],
			list:      methods["List"+upperFirst(descriptor.GetPlural())],
		})
	}
	return types
}

var patternVariable = regexp.MustCompile(`\{([a-z_]+)\}`)

func patternVariables(pattern string) []string {
	var variables []string
	for _, m := range patternVariable.FindAllStringSubmatch(pattern, -1) {
		variables = append(variables, m[1])
	}
	return variables
}

func fieldBehaviors(f *protogen.Field) []annotations.FieldBehavior {
	behaviors, _ := proto.GetExtension(f.Desc.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	return behaviors
}

func enumValues(e *protogen.Enum) string {
	var values []string
	for _, v := range e.Values {
		if v.Desc.Number() != 0 {
			values = append(values, "`"+string(v.Desc.Name())+"`")
		}
	}
	return strings.Join(values, ", ")
}

func generateResource(g *protogen.GeneratedFile, t resourceType) error {
	if t.del == nil {
		return fmt.Errorf("Create%s without Delete%[1]s", t.message.Desc.Name())
	}
	attrs, err := attributes(t, true, lowerFirst(t.message.GoIdent.GoName))
	if err != nil {
		return err
	}
	model := lowerFirst(t.message.GoIdent.GoName) + "Model"
	message := "*" + g.QualifiedGoIdent(t.message.GoIdent)
	client := g.QualifiedGoIdent(t.file.GoImportPath.Ident("New"+strings.TrimSuffix(t.service.GoName, "Service")+"Client")) + "(client)"
	ctx := g.QualifiedGoIdent(contextPackage.Ident("Context"))
	coreClient := "*" + g.QualifiedGoIdent(corePackage.Ident("Client"))
	typeName := "_" + t.shortcode + "_" + snake(t.singular)

	generateModel(g, model, "origincloud"+typeName+"'s state", attrs)
	generateSchema(g, t, resourceSchemaPackage, attrs)
	generateConversions(g, t, lowerFirst(t.message.GoIdent.GoName), model, message, attrs, true)

	g.P()
	g.P("func ", lowerFirst(t.message.GoIdent.GoName), "Resource() ", g.QualifiedGoIdent(frameworkPackage.Ident("ResourceSpec")), "[", model, ", ", message, "] {")
	g.P("return ", g.QualifiedGoIdent(frameworkPackage.Ident("ResourceSpec")), "[", model, ", ", message, "]{")
	g.P("TypeName: ", fmt.Sprintf("%q", typeName), ",")
	g.P("Schema: ", lowerFirst(t.message.GoIdent.GoName), "Schema(),")
	g.P("Name: func(m *", model, ") string { return m.Name.ValueString() },")
	g.P("ToProto: ", lowerFirst(t.message.GoIdent.GoName), "ToProto,")
	g.P("FromProto: ", lowerFirst(t.message.GoIdent.GoName), "FromProto,")

	g.P("UpdateMask: func(plan, state *", model, ") []string {")
	g.P("var mask []string")
	for _, a := range attrs {
		if a.updatable {
			g.P("if ", updateChanged(g, a), " {")
			g.P("mask = append(mask, ", fmt.Sprintf("%q", a.field.Desc.Name()), ")")
			g.P("}")
		}
	}
	g.P("return mask")
	g.P("},")

	if gone := deletedState(t.message); gone != nil {
		g.P("Gone: func(p ", message, ") bool { return p.GetState() == ", g.QualifiedGoIdent(gone.GoIdent), " },")
	}

	// Create
	createInput := t.create.Input
	resourceField := fieldOfType(createInput, t.message)
	if resourceField == nil {
		return fmt.Errorf("Create%s's request has no %[1]s field", t.message.Desc.Name())
	}
	if result := operationResult(t.create); result != t.message.Desc.FullName() {
		return fmt.Errorf("Create%s doesn't answer with an operation whose result is the %[1]s", t.message.Desc.Name())
	}
	g.P("Create: func(ctx ", ctx, ", client ", coreClient, ", plan *", model, ") (*", g.QualifiedGoIdent(corePackage.Ident("Operation")), "[", message, "], error) {")
	g.P("return ", client, ".", t.create.GoName, "(ctx, &", g.QualifiedGoIdent(createInput.GoIdent), "{")
	if len(t.variables) > 1 {
		if fieldNamed(createInput, "parent") == nil {
			return fmt.Errorf("Create%s's request has no parent", t.message.Desc.Name())
		}
		g.P("Parent: ", parentExpression(t.pattern, "plan"), ",")
	}
	g.P(resourceField.GoName, ": ", lowerFirst(t.message.GoIdent.GoName), "ToProto(plan),")
	g.P("})")
	g.P("},")

	// Get
	g.P("Get: func(ctx ", ctx, ", client ", coreClient, ", name string) (", message, ", error) {")
	g.P("return ", client, ".", t.get.GoName, "(ctx, &", g.QualifiedGoIdent(t.get.Input.GoIdent), "{Name: name})")
	g.P("},")

	// Update
	if t.update != nil {
		field := fieldOfType(t.update.Input, t.message)
		if field == nil || fieldNamed(t.update.Input, "update_mask") == nil {
			return fmt.Errorf("Update%s's request has no %[1]s or no update_mask", t.message.Desc.Name())
		}
		g.P("Update: func(ctx ", ctx, ", client ", coreClient, ", resource ", message, ", mask []string) (", message, ", error) {")
		call := client + "." + t.update.GoName + "(ctx, &" + g.QualifiedGoIdent(t.update.Input.GoIdent) + "{" +
			field.GoName + ": resource, UpdateMask: &" + g.QualifiedGoIdent(fieldmaskPackage.Ident("FieldMask")) + "{Paths: mask}})"
		switch operationResult(t.update) {
		case t.message.Desc.FullName():
			g.P("op, err := ", call)
			g.P("if err != nil {")
			g.P("return nil, err")
			g.P("}")
			g.P("return op.Wait(ctx)")
		case "":
			if t.update.Output != t.message {
				return fmt.Errorf("Update%s doesn't answer with the %[1]s", t.message.Desc.Name())
			}
			g.P("return ", call)
		default:
			return fmt.Errorf("Update%s's operation doesn't result in the %[1]s", t.message.Desc.Name())
		}
		g.P("},")
	}

	// Delete
	g.P("Delete: func(ctx ", ctx, ", client ", coreClient, ", name string) error {")
	call := client + "." + t.del.GoName + "(ctx, &" + g.QualifiedGoIdent(t.del.Input.GoIdent) + "{Name: name})"
	if operationResult(t.del) != "" {
		g.P("op, err := ", call)
		g.P("if err != nil {")
		g.P("return err")
		g.P("}")
		g.P("_, err = op.Wait(ctx)")
		g.P("return err")
	} else {
		g.P("_, err := ", call)
		g.P("return err")
	}
	g.P("},")

	generateToggles(g, t, model, message, client, attrs)

	g.P("}")
	g.P("}")
	return nil
}

// generateToggles writes the spec's toggles, when the resource has any.
func generateToggles(g *protogen.GeneratedFile, t resourceType, model, message, client string, attrs []attribute) {
	toggleType := g.QualifiedGoIdent(frameworkPackage.Ident("Toggle")) + "[" + model + ", " + message + "]"
	ctx := g.QualifiedGoIdent(contextPackage.Ident("Context"))
	coreClient := "*" + g.QualifiedGoIdent(corePackage.Ident("Client"))
	operation := "*" + g.QualifiedGoIdent(corePackage.Ident("Operation")) + "[" + message + "]"
	first := true
	for _, a := range attrs {
		if a.toggle == nil {
			continue
		}
		if first {
			g.P("Toggles: []", toggleType, "{")
			first = false
		}
		state := "p.Get" + a.toggle.state.GoName + "()"
		g.P("{")
		g.P("Planned: func(m *", model, ") ", g.QualifiedGoIdent(typesPackage.Ident("Bool")), " { return m.", a.goName, " },")
		g.P("IsSet: func(p ", message, ") bool { return ", state, " == ", g.QualifiedGoIdent(a.toggle.on.GoIdent), " },")
		g.P("IsClear: func(p ", message, ") bool { return ", state, " == ", g.QualifiedGoIdent(a.toggle.off.GoIdent), " },")
		for _, m := range []struct {
			field  string
			method *protogen.Method
		}{{"Set", a.toggle.set}, {"Clear", a.toggle.clear}} {
			g.P(m.field, ": func(ctx ", ctx, ", client ", coreClient, ", name string) (", operation, ", error) {")
			g.P("return ", client, ".", m.method.GoName, "(ctx, &", g.QualifiedGoIdent(m.method.Input.GoIdent), "{Name: name})")
			g.P("},")
		}
		if a.toggle.clearBeforeDelete {
			g.P("ClearBeforeDelete: true,")
		}
		g.P("},")
	}
	if !first {
		g.P("},")
	}
}

func generateDataSource(g *protogen.GeneratedFile, t resourceType) error {
	attrs, err := attributes(t, false, lowerFirst(t.message.GoIdent.GoName))
	if err != nil {
		return err
	}
	model := lowerFirst(t.message.GoIdent.GoName) + "Model"
	message := "*" + g.QualifiedGoIdent(t.message.GoIdent)
	client := g.QualifiedGoIdent(t.file.GoImportPath.Ident("New"+strings.TrimSuffix(t.service.GoName, "Service")+"Client")) + "(client)"
	typeName := "_" + t.shortcode + "_" + snake(t.singular)
	spec := g.QualifiedGoIdent(frameworkPackage.Ident("DataSourceSpec"))

	generateModel(g, model, "origincloud"+typeName+"'s state", attrs)
	generateSchema(g, t, datasourceSchemaPackage, attrs)
	generateConversions(g, t, lowerFirst(t.message.GoIdent.GoName), model, message, attrs, false)

	g.P()
	g.P("func ", lowerFirst(t.message.GoIdent.GoName), "DataSource() ", spec, "[", model, ", ", message, "] {")
	g.P("return ", spec, "[", model, ", ", message, "]{")
	g.P("TypeName: ", fmt.Sprintf("%q", typeName), ",")
	g.P("Schema: ", lowerFirst(t.message.GoIdent.GoName), "Schema(),")
	g.P("Name: func(m *", model, ") string { return m.Name.ValueString() },")
	g.P("FromProto: ", lowerFirst(t.message.GoIdent.GoName), "FromProto,")
	g.P("Get: func(ctx ", g.QualifiedGoIdent(contextPackage.Ident("Context")), ", client *", g.QualifiedGoIdent(corePackage.Ident("Client")), ", name string) (", message, ", error) {")
	g.P("return ", client, ".", t.get.GoName, "(ctx, &", g.QualifiedGoIdent(t.get.Input.GoIdent), "{Name: name})")
	g.P("},")
	g.P("}")
	g.P("}")
	return nil
}

// generateListDataSource writes the data source that reads everything a
// resource type's List method returns. Its items are the data source's
// attributes, all computed: a data source type's items reuse its model and
// conversion, a resource type's get their own, without the resource's id,
// name variables and toggles.
func generateListDataSource(g *protogen.GeneratedFile, t resourceType) error {
	name := lowerFirst(t.message.GoIdent.GoName)
	method := t.list.Desc.Name()
	items := fieldOfType(t.list.Output, t.message)
	if items == nil || !items.Desc.IsList() {
		return fmt.Errorf("%s's response has no repeated %s field", method, t.message.Desc.Name())
	}
	paginated := fieldNamed(t.list.Input, "page_token") != nil && fieldNamed(t.list.Output, "next_page_token") != nil
	// Other fields, such as a filter, are left unset, which lists
	// everything; a required one fails generation.
	for _, f := range t.list.Input.Fields {
		switch f.Desc.Name() {
		case "parent", "page_size", "page_token":
		default:
			if slices.Contains(fieldBehaviors(f), annotations.FieldBehavior_REQUIRED) {
				return fmt.Errorf("%s's request field %s is required, which isn't supported yet", method, f.Desc.Name())
			}
		}
	}
	if hasParent := fieldNamed(t.list.Input, "parent") != nil; hasParent != (len(t.variables) > 1) {
		return fmt.Errorf("%s's request has a parent only when the resource's name has one", method)
	}

	// A resource's items have conversions of their own.
	prefix := name
	if t.create != nil {
		prefix = name + "Item"
	}
	itemAttrs, err := attributes(t, false, prefix)
	if err != nil {
		return err
	}
	for i := range itemAttrs {
		itemAttrs[i].required, itemAttrs[i].computed = false, true
	}
	itemModel, itemFromProto := name+"Model", name+"FromProto"
	message := "*" + g.QualifiedGoIdent(t.message.GoIdent)
	typeName := "_" + t.shortcode + "_" + snake(t.plural)
	if t.create != nil {
		// A resource: its state isn't an item's.
		itemModel, itemFromProto = name+"ItemModel", name+"ItemFromProto"
		generateModel(g, itemModel, "an item of origincloud"+typeName, itemAttrs)
		generateConversions(g, t, name+"Item", itemModel, message, itemAttrs, false)
	}

	listAttr := snake(t.plural)
	if t.plural == "" || listAttr == snake(t.singular) {
		return fmt.Errorf("the resource's plural %q isn't distinct from its singular", t.plural)
	}
	var attrs []attribute
	for _, v := range t.variables[:len(t.variables)-1] {
		attrs = append(attrs, attribute{
			name: v, goName: camel(v), kind: "String", required: true,
			doc: fmt.Sprintf("The %s whose %s to list.", words(v), words(t.plural)),
		})
	}
	listDoc := prose(items.Comments.Leading)
	if paginated {
		listDoc = fmt.Sprintf("The %s listed, from every page.", words(t.plural))
	}
	model := name + "ListModel"
	spec := g.QualifiedGoIdent(frameworkPackage.Ident("ListDataSourceSpec"))
	schemaPackage := datasourceSchemaPackage
	client := g.QualifiedGoIdent(t.file.GoImportPath.Ident("New"+strings.TrimSuffix(t.service.GoName, "Service")+"Client")) + "(client)"

	g.P()
	g.P("// ", model, " is origincloud", typeName, "'s state.")
	g.P("type ", model, " struct {")
	for _, a := range attrs {
		g.P(a.goName, " ", g.QualifiedGoIdent(typesPackage.Ident(a.kind)), " `tfsdk:\"", a.name, "\"`")
	}
	g.P(items.GoName, " []", itemModel, " `tfsdk:\"", listAttr, "\"`")
	g.P("}")

	g.P()
	g.P("func ", name, "ListSchema() ", g.QualifiedGoIdent(schemaPackage.Ident("Schema")), " {")
	g.P("return ", g.QualifiedGoIdent(schemaPackage.Ident("Schema")), "{")
	g.P("MarkdownDescription: ", fmt.Sprintf("%q", prose(t.list.Comments.Leading)), ",")
	g.P("Attributes: map[string]", g.QualifiedGoIdent(schemaPackage.Ident("Attribute")), "{")
	for _, a := range attrs {
		g.P(fmt.Sprintf("%q", a.name), ": ", g.QualifiedGoIdent(schemaPackage.Ident("StringAttribute")), "{")
		g.P("MarkdownDescription: ", fmt.Sprintf("%q", a.doc), ",")
		g.P("Required: true,")
		g.P("},")
	}
	g.P(fmt.Sprintf("%q", listAttr), ": ", g.QualifiedGoIdent(schemaPackage.Ident("ListNestedAttribute")), "{")
	g.P("MarkdownDescription: ", fmt.Sprintf("%q", listDoc), ",")
	g.P("Computed: true,")
	g.P("NestedObject: ", g.QualifiedGoIdent(schemaPackage.Ident("NestedAttributeObject")), "{")
	generateAttributes(g, schemaPackage, itemAttrs)
	g.P("},")
	g.P("},")
	g.P("},")
	g.P("}")
	g.P("}")

	request := "&" + g.QualifiedGoIdent(t.list.Input.GoIdent) + "{"
	if len(t.variables) > 1 {
		request += "Parent: " + parentExpression(t.pattern, "m")
	}
	request += "}"
	g.P()
	g.P("func ", name, "ListDataSource() ", spec, "[", model, "] {")
	g.P("return ", spec, "[", model, "]{")
	g.P("TypeName: ", fmt.Sprintf("%q", typeName), ",")
	g.P("Schema: ", name, "ListSchema(),")
	g.P("List: func(ctx ", g.QualifiedGoIdent(contextPackage.Ident("Context")), ", client *", g.QualifiedGoIdent(corePackage.Ident("Client")), ", m *", model, ") error {")
	// Empty rather than null when the list is.
	g.P("m.", items.GoName, " = []", itemModel, "{}")
	if paginated {
		g.P("for p, err := range ", client, ".All", items.GoName, "(ctx, ", request, ") {")
		g.P("if err != nil {")
		g.P("return err")
		g.P("}")
	} else {
		g.P("page, err := ", client, ".", t.list.GoName, "(ctx, ", request, ")")
		g.P("if err != nil {")
		g.P("return err")
		g.P("}")
		g.P("for _, p := range page.Get", items.GoName, "() {")
	}
	g.P("var item ", itemModel)
	g.P("if err := ", itemFromProto, "(p, &item); err != nil {")
	g.P("return err")
	g.P("}")
	g.P("m.", items.GoName, " = append(m.", items.GoName, ", item)")
	g.P("}")
	g.P("return nil")
	g.P("},")
	g.P("}")
	g.P("}")
	return nil
}

func generateModel(g *protogen.GeneratedFile, model, doc string, attrs []attribute) {
	g.P()
	g.P("// ", model, " is ", doc, ".")
	g.P("type ", model, " struct {")
	for _, a := range attrs {
		g.P(a.goName, " ", g.QualifiedGoIdent(typesPackage.Ident(a.kind)), " `tfsdk:\"", a.name, "\"`")
	}
	g.P("}")
}

func generateSchema(g *protogen.GeneratedFile, t resourceType, schemaPackage protogen.GoImportPath, attrs []attribute) {
	g.P()
	g.P("func ", lowerFirst(t.message.GoIdent.GoName), "Schema() ", g.QualifiedGoIdent(schemaPackage.Ident("Schema")), " {")
	g.P("return ", g.QualifiedGoIdent(schemaPackage.Ident("Schema")), "{")
	g.P("MarkdownDescription: ", fmt.Sprintf("%q", prose(t.message.Comments.Leading)), ",")
	generateAttributes(g, schemaPackage, attrs)
	g.P("}")
	g.P("}")
}

// generateProviderList writes the constructors of every package's
// resources and data sources, for the provider to serve.
func generateProviderList(gen *protogen.Plugin, packages []*servicePackage) {
	g := gen.NewGeneratedFile(path.Join(providerDir, "services.gen.go"), protogen.GoImportPath(module+"/"+providerDir))
	g.P("// Code generated by protoc-gen-terraform-origincloud. DO NOT EDIT.")
	g.P()
	g.P("package ", path.Base(providerDir))
	for _, list := range []struct {
		name, kind string
		pkg        protogen.GoImportPath
	}{
		{"Resources", "Resource", resourcePackage},
		{"DataSources", "DataSource", datasourcePackage},
	} {
		constructor := "func() " + g.QualifiedGoIdent(list.pkg.Ident(list.kind))
		g.P()
		g.P("// service", list.name, " returns the constructors of every package's ", strings.TrimSpace(words(list.name)), ".")
		g.P("func service", list.name, "() []", constructor, " {")
		g.P("var all []", constructor)
		for _, p := range packages {
			if len(p.resources)+len(p.dataSources) > 0 {
				g.P("all = append(all, ", g.QualifiedGoIdent(p.importPath().Ident(list.name)), "()...)")
			}
		}
		g.P("return all")
		g.P("}")
	}
}

// deletedState returns the DELETED value of a resource's state enum, for
// resources the API still returns for a while after deleting them.
func deletedState(m *protogen.Message) *protogen.EnumValue {
	state := fieldNamed(m, "state")
	if state == nil || state.Enum == nil {
		return nil
	}
	for _, v := range state.Enum.Values {
		if v.Desc.Name() == "DELETED" {
			return v
		}
	}
	return nil
}

// operationResult returns the result type of a method answering with an
// operation; empty for any other method.
func operationResult(m *protogen.Method) protoreflect.FullName {
	info, _ := proto.GetExtension(m.Desc.Options(), longrunningpb.E_OperationInfo).(*longrunningpb.OperationInfo)
	if m.Output.Desc.FullName() != operationType || info == nil {
		return ""
	}
	name := protoreflect.FullName(info.GetResponseType())
	if !strings.Contains(string(name), ".") {
		name = m.Desc.ParentFile().Package().Append(name.Name())
	}
	return name
}

// parentExpression returns the Go expression building a resource's parent
// from a model's variables: "projects/" + plan.Project.ValueString() + ...
func parentExpression(pattern, model string) string {
	segments := strings.Split(pattern, "/")
	var parts []string
	literal := ""
	for i, s := range segments[:len(segments)-2] {
		if i > 0 {
			literal += "/"
		}
		v := patternVariable.FindStringSubmatch(s)
		if v == nil {
			literal += s
			continue
		}
		if literal != "" {
			parts = append(parts, fmt.Sprintf("%q", literal))
			literal = ""
		}
		parts = append(parts, model+"."+camel(v[1])+".ValueString()")
	}
	if literal != "" {
		parts = append(parts, fmt.Sprintf("%q", literal))
	}
	return strings.Join(parts, " + ")
}

func fieldOfType(m *protogen.Message, t *protogen.Message) *protogen.Field {
	for _, f := range m.Fields {
		if f.Message == t {
			return f
		}
	}
	return nil
}

func fieldNamed(m *protogen.Message, name string) *protogen.Field {
	for _, f := range m.Fields {
		if string(f.Desc.Name()) == name {
			return f
		}
	}
	return nil
}

// A proto comment without the API linter's directives, `(-- ... --)`.
var linterDirective = regexp.MustCompile(`(?s)\(--.*?--\)`)

// terraformTerms rewrite what the API's comments tell a client to call
// into what a configuration reads instead. A comment worded otherwise is
// left as the API has it.
var terraformTerms = []struct{ api, terraform string }{
	{"with the location service (`location.origincloudapis.eu`)", "with the `origincloud_location_zones` data source"},
}

// prose returns a comment as Markdown: its lines joined, its paragraphs
// kept, and the terms of terraformTerms rewritten.
func prose(c protogen.Comments) string {
	text := strings.TrimSpace(linterDirective.ReplaceAllString(string(c), ""))
	var paragraphs []string
	for _, p := range strings.Split(text, "\n\n") {
		var lines []string
		for _, line := range strings.Split(p, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) > 0 {
			paragraphs = append(paragraphs, strings.Join(lines, " "))
		}
	}
	text = strings.Join(paragraphs, "\n\n")
	for _, r := range terraformTerms {
		text = strings.ReplaceAll(text, r.api, r.terraform)
	}
	return text
}

// snake returns a lowerCamel name in snake case: storageClass is
// storage_class.
func snake(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsUpper(r) {
			b.WriteByte('_')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// words returns a name as words: storage_class or storageClass is
// "storage class".
func words(s string) string { return strings.ReplaceAll(snake(s), "_", " ") }

// camel returns a snake_case name in Go's CamelCase: storage_class is
// StorageClass.
func camel(s string) string {
	var b strings.Builder
	for _, part := range strings.Split(s, "_") {
		if part != "" {
			b.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
	}
	return b.String()
}

func lowerFirst(s string) string { return strings.ToLower(s[:1]) + s[1:] }

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
