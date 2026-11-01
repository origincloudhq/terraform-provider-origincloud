package main

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The attributes fields become, their schema, and their conversions
// between the SDK's messages and the framework's values.
//
// A nested message is an object attribute, and a repeated field a list,
// or a set when its order isn't kept (UNORDERED_LIST); a map of strings to
// messages is a map of objects. A nested message's fields follow the same
// field behaviours as a resource's, except that it has no identifier and
// is sent, and named in an update mask, as a whole with the top-level
// field that holds it.

const (
	attrPackage  = protogen.GoImportPath("github.com/hashicorp/terraform-plugin-framework/attr")
	protoPackage = protogen.GoImportPath("google.golang.org/protobuf/proto")
)

// An attribute of a resource or data source, or of a nested object.
type attribute struct {
	name string // the attribute's, such as "size_gib"
	// goName is the model's field.
	goName string
	field  *protogen.Field // nil for id and the name's variables
	// kind is the model's type: String, Int64, Bool, Map, List, Set or
	// Object.
	kind string
	// elem is the type of a map's, list's or set's scalar elements: String,
	// Int64 or Bool. Empty for object elements.
	elem string
	// message is an object's message, or that of a map's, list's or set's
	// object elements; nested are its attributes, converted by the
	// functions prefix names.
	message *protogen.Message
	nested  []attribute
	prefix  string
	// enum is a string's enum, or that of a collection's string elements.
	enum *protogen.Enum
	doc  string

	required, optional, computed bool
	// keep plans the state's value while it is unknown; a nested
	// attribute's only when that value isn't null, as it is when the object
	// holding it is new.
	keep, inNested bool
	// replace replaces the resource when the attribute changes; when it is
	// also computed, only when it is set.
	replace bool
	// updatable is sent in an update when it changes.
	updatable bool
	// int32 is a number, or a collection's number elements, sent as an
	// int32.
	int32 bool
	// toggle is the attribute's toggle, when methods of their own set and
	// clear it (toggles.go).
	toggle *resolvedToggle
}

// computedOnly says that the configuration can't set the attribute.
func (a attribute) computedOnly() bool { return a.computed && !a.optional }

// collection says that the attribute is a map, list or set.
func (a attribute) collection() bool { return a.kind == "Map" || a.kind == "List" || a.kind == "Set" }

// Where a field's attribute is.
type scope struct {
	t resourceType
	// forResource is a resource's, rather than a data source's.
	forResource bool
	// computed: every attribute is computed, as a data source's are, and
	// those under an output-only field.
	computed bool
	// nested is a nested message's field, inElements one of a settable
	// list's, set's or map's elements, and inSet one of a settable set's.
	nested, inElements, inSet bool
	// replaced: the top-level attribute holding it already replaces the
	// resource when it changes.
	replaced bool
	// chosen: an object holding it is one the API chooses when it is left
	// out (optional and computed).
	chosen bool
	// prefix names the conversion functions of the object holding it.
	prefix string
	// seen are the messages holding it, which it can't hold in turn.
	seen []protoreflect.FullName
}

func attributes(t resourceType, forResource bool, prefix string) ([]attribute, error) {
	singular := words(t.singular)
	var attrs []attribute
	if forResource {
		attrs = append(attrs, attribute{
			name: "id", goName: "ID", kind: "String", computed: true, keep: true,
			doc: fmt.Sprintf("The %s's resource name, the same as `name`.", singular),
		})
		for _, v := range t.variables[:len(t.variables)-1] {
			attrs = append(attrs, attribute{
				name: v, goName: camel(v), kind: "String", required: true, replace: true,
				doc: fmt.Sprintf("The %s the %s is in. Changing it replaces the %s.", words(v), singular, singular),
			})
		}
	}
	s := scope{t: t, forResource: forResource, computed: !forResource, prefix: prefix, seen: []protoreflect.FullName{t.message.Desc.FullName()}}
	for _, f := range t.message.Fields {
		a, err := fieldAttribute(s, f)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.Desc.Name(), err)
		}
		if slices.ContainsFunc(attrs, func(b attribute) bool { return b.name == a.name }) {
			return nil, fmt.Errorf("field %s: the attribute %s is already the name's", f.Desc.Name(), a.name)
		}
		attrs = append(attrs, a)
	}
	if !forResource {
		return attrs, nil
	}
	resolved, err := togglesOf(t)
	if err != nil {
		return nil, err
	}
	for i := range resolved {
		r := &resolved[i]
		if slices.ContainsFunc(attrs, func(b attribute) bool { return b.name == r.attribute }) {
			return nil, fmt.Errorf("toggle %s: the attribute is already a field's", r.attribute)
		}
		// Optional and computed: left out, it follows the API.
		attrs = append(attrs, attribute{
			name: r.attribute, goName: camel(r.attribute), kind: "Bool", doc: r.doc,
			optional: true, computed: true, keep: true, toggle: r,
		})
	}
	return attrs, nil
}

func fieldAttribute(s scope, f *protogen.Field) (attribute, error) {
	a := attribute{name: string(f.Desc.Name()), goName: f.GoName, field: f, doc: prose(f.Comments.Leading)}
	if f.Desc.HasOptionalKeyword() {
		return a, fmt.Errorf("fields with explicit presence (proto3 optional) aren't supported yet")
	}
	if f.Oneof != nil {
		return a, fmt.Errorf("fields of a oneof aren't supported yet")
	}
	behaviors := fieldBehaviors(f)
	has := func(b annotations.FieldBehavior) bool { return slices.Contains(behaviors, b) }
	if len(behaviors) == 0 {
		return a, fmt.Errorf("no google.api.field_behavior")
	}
	unordered := has(annotations.FieldBehavior_UNORDERED_LIST)
	if unordered && (!f.Desc.IsList() || f.Desc.IsMap()) {
		return a, fmt.Errorf("UNORDERED_LIST on a field that isn't repeated")
	}

	var message *protogen.Message
	switch {
	case f.Desc.IsMap():
		if f.Desc.MapKey().Kind() != protoreflect.StringKind {
			return a, fmt.Errorf("only maps keyed by strings are supported")
		}
		a.kind = "Map"
		switch value := f.Message.Fields[1]; value.Desc.Kind() {
		case protoreflect.StringKind:
			a.elem = "String"
		case protoreflect.MessageKind:
			message = value.Message
		default:
			return a, fmt.Errorf("maps of strings to %s aren't supported yet, only to strings and messages", value.Desc.Kind())
		}
	case f.Desc.IsList():
		a.kind = "List"
		if unordered {
			a.kind = "Set"
		}
		if f.Desc.Kind() == protoreflect.MessageKind {
			message = f.Message
			break
		}
		elem, err := scalar(&a, f)
		if err != nil {
			return a, fmt.Errorf("repeated %w", err)
		}
		a.elem = elem
		if a.enum != nil {
			a.doc = strings.TrimSpace(a.doc + " Each one of " + enumValues(a.enum) + ".")
		}
	case f.Desc.Kind() == protoreflect.MessageKind:
		a.kind = "Object"
		message = f.Message
	default:
		kind, err := scalar(&a, f)
		if err != nil {
			return a, err
		}
		a.kind = kind
		if a.enum != nil {
			a.doc = strings.TrimSpace(a.doc + " One of " + enumValues(a.enum) + ".")
		}
	}

	if err := applyBehaviors(s, &a, behaviors); err != nil {
		return a, err
	}
	if message != nil {
		if err := nestedAttributes(s, &a, message); err != nil {
			return a, err
		}
	}
	return a, nil
}

// scalar sets a's int32 and enum for a scalar field, or a repeated field's
// scalar elements, and returns their type.
func scalar(a *attribute, f *protogen.Field) (string, error) {
	switch f.Desc.Kind() {
	case protoreflect.StringKind:
		return "String", nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		a.int32 = true
		return "Int64", nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "Int64", nil
	case protoreflect.BoolKind:
		return "Bool", nil
	case protoreflect.EnumKind:
		a.enum = f.Enum
		return "String", nil
	default:
		return "", fmt.Errorf("%s fields aren't supported yet", f.Desc.Kind())
	}
}

// applyBehaviors sets whether a is required, optional or computed, and
// what it replaces, from its field behaviours (AIP-203).
func applyBehaviors(s scope, a *attribute, behaviors []annotations.FieldBehavior) error {
	has := func(b annotations.FieldBehavior) bool { return slices.Contains(behaviors, b) }
	if s.computed {
		if !s.forResource && !s.nested && has(annotations.FieldBehavior_IDENTIFIER) {
			a.required = true
		} else {
			a.computed = true
		}
		return nil
	}
	updatable := s.t.update != nil
	immutable := has(annotations.FieldBehavior_IMMUTABLE)
	switch {
	case has(annotations.FieldBehavior_IDENTIFIER):
		if s.nested {
			return fmt.Errorf("a nested message's field can't be an IDENTIFIER")
		}
		if a.name != "name" || a.kind != "String" {
			return fmt.Errorf("the identifier isn't the string field name")
		}
		a.computed, a.keep = true, true
	case has(annotations.FieldBehavior_OUTPUT_ONLY):
		if s.inSet {
			return fmt.Errorf("a field of an UNORDERED_LIST element can't be OUTPUT_ONLY: the element the API answers with would differ from the one set")
		}
		a.computed = true
	case immutable && (s.inSet || s.inElements && !s.replaced):
		// Elements are matched by position or key, not by identity: a kept
		// value could be planned for another element, and adding or
		// reordering elements would replace the resource. In an immutable
		// field's elements, any change replaces it anyway.
		return fmt.Errorf("a field of a repeated or map field's elements can't be IMMUTABLE: an element has no identity to keep its value for, or to replace the resource on")
	case has(annotations.FieldBehavior_REQUIRED):
		a.required = true
		if s.nested {
			a.replace = immutable && !s.replaced
		} else {
			a.replace = immutable || !updatable
			a.updatable = !a.replace
		}
	case immutable:
		// The API chooses a value left out, such as the default storage
		// class, which the state then keeps.
		a.optional, a.computed, a.keep, a.replace = true, true, true, !s.replaced
	case has(annotations.FieldBehavior_OPTIONAL):
		a.optional = true
		if !s.nested {
			a.replace = !updatable
			a.updatable = updatable
		}
	default:
		return fmt.Errorf("its field behaviors %v aren't supported", behaviors)
	}
	if s.chosen && (a.required || a.optional) {
		// Left out, the object is the API's, and Terraform plans each of its
		// fields from the configuration's null unless it is computed.
		a.required, a.optional, a.computed, a.keep, a.replace = false, true, true, false, false
	}
	if a.replace {
		a.doc = strings.TrimSpace(a.doc + " Changing it replaces the " + words(s.t.singular) + ".")
	}
	return nil
}

// nestedAttributes sets the attributes of a's object, or object elements,
// whose message is m.
func nestedAttributes(s scope, a *attribute, m *protogen.Message) error {
	if m.GoIdent.GoImportPath != s.t.message.GoIdent.GoImportPath {
		return fmt.Errorf("%s is another package's message, which isn't supported yet", m.Desc.FullName())
	}
	if slices.Contains(s.seen, m.Desc.FullName()) {
		return fmt.Errorf("%s holds itself, which isn't supported", m.Desc.FullName())
	}
	child := s
	child.nested = true
	child.computed = s.computed || a.computedOnly()
	child.inSet = s.inSet || a.kind == "Set" && !child.computed
	child.inElements = s.inElements || a.collection() && !child.computed
	child.replaced = s.replaced || a.replace
	child.chosen = s.chosen || a.kind == "Object" && a.optional && a.computed
	// A separator per level: spec.rules and spec_rules don't meet.
	child.prefix = s.prefix + "_" + a.goName
	child.seen = append(slices.Clone(s.seen), m.Desc.FullName())
	a.message, a.prefix, a.nested = m, child.prefix, []attribute{}
	for _, f := range m.Fields {
		n, err := fieldAttribute(child, f)
		if err != nil {
			return fmt.Errorf("%s: field %s: %w", m.Desc.Name(), f.Desc.Name(), err)
		}
		n.inNested = true
		a.nested = append(a.nested, n)
	}
	return nil
}

func enumNames(e *protogen.Enum) []string {
	var names []string
	for _, v := range e.Values {
		if v.Desc.Number() != 0 {
			names = append(names, string(v.Desc.Name()))
		}
	}
	return names
}

// schemaType returns the attribute's schema type, such as
// ListNestedAttribute.
func (a attribute) schemaType() string {
	if a.message != nil {
		if a.kind == "Object" {
			return "SingleNestedAttribute"
		}
		return a.kind + "NestedAttribute"
	}
	return a.kind + "Attribute"
}

// generateAttributes writes a schema's Attributes.
func generateAttributes(g *protogen.GeneratedFile, schemaPackage protogen.GoImportPath, attrs []attribute) {
	c := conv{g}
	g.P("Attributes: map[string]", g.QualifiedGoIdent(schemaPackage.Ident("Attribute")), "{")
	for _, a := range attrs {
		g.P(fmt.Sprintf("%q", a.name), ": ", g.QualifiedGoIdent(schemaPackage.Ident(a.schemaType())), "{")
		g.P("MarkdownDescription: ", fmt.Sprintf("%q", a.doc), ",")
		for _, flag := range []struct {
			set  bool
			name string
		}{{a.required, "Required"}, {a.optional, "Optional"}, {a.computed, "Computed"}} {
			if flag.set {
				g.P(flag.name, ": true,")
			}
		}
		if a.collection() && a.message == nil {
			g.P("ElementType: ", c.types(a.elem+"Type"), ",")
		}
		settable := a.required || a.optional
		switch {
		case a.kind == "Int64" && a.int32 && settable:
			g.P("Validators: []", g.QualifiedGoIdent(validatorPackage.Ident("Int64")), "{", c.framework("Int32"), "()},")
		case a.kind == "String" && a.enum != nil && settable:
			g.P("Validators: []", g.QualifiedGoIdent(validatorPackage.Ident("String")), "{", c.framework("OneOf"), "(", quoted(enumNames(a.enum)), ")},")
		case settable && a.collection():
			var options []string
			if a.enum != nil {
				options = append(options, "OneOf: []string{"+quoted(enumNames(a.enum))+"}")
			}
			if a.int32 {
				options = append(options, "Int32: true")
			}
			g.P("Validators: []", g.QualifiedGoIdent(validatorPackage.Ident(a.kind)), "{", c.framework("ElementCheck"), "{", strings.Join(options, ", "), "}},")
		}
		var modifiers []string
		modifierPackage := protogen.GoImportPath("github.com/hashicorp/terraform-plugin-framework/resource/schema/" + strings.ToLower(a.kind) + "planmodifier")
		switch {
		case a.keep && a.inNested:
			modifiers = append(modifiers, g.QualifiedGoIdent(modifierPackage.Ident("UseNonNullStateForUnknown"))+"()")
		case a.keep:
			modifiers = append(modifiers, g.QualifiedGoIdent(modifierPackage.Ident("UseStateForUnknown"))+"()")
		}
		if a.replace && a.message != nil {
			// What it sends, so that an output-only field changing doesn't
			// replace the resource.
			value := c.types(a.kind)
			modifiers = append(modifiers, c.framework(a.kind+"ReplaceIfSent")+"(func(plan, state "+value+") bool { return "+sentSame(g, a, "plan", "state")+" }, "+fmt.Sprint(a.computed)+")")
		} else if a.replace && a.computed {
			modifiers = append(modifiers, g.QualifiedGoIdent(modifierPackage.Ident("RequiresReplaceIfConfigured"))+"()")
		} else if a.replace {
			modifiers = append(modifiers, g.QualifiedGoIdent(modifierPackage.Ident("RequiresReplace"))+"()")
		}
		if len(modifiers) > 0 {
			g.P("PlanModifiers: []", g.QualifiedGoIdent(planPackage.Ident(a.kind)), "{", strings.Join(modifiers, ", "), "},")
		}
		switch {
		case a.message != nil && a.kind == "Object":
			generateAttributes(g, schemaPackage, a.nested)
		case a.message != nil:
			g.P("NestedObject: ", g.QualifiedGoIdent(schemaPackage.Ident("NestedAttributeObject")), "{")
			generateAttributes(g, schemaPackage, a.nested)
			g.P("},")
		}
		g.P("},")
	}
	g.P("},")
}

func quoted(values []string) string {
	q := make([]string, len(values))
	for i, v := range values {
		q[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(q, ", ")
}

// generateConversions writes the functions name+"ToProto", for a
// resource, and name+"FromProto", and those of the nested objects.
func generateConversions(g *protogen.GeneratedFile, t resourceType, name, model, message string, attrs []attribute, forResource bool) {
	c := conv{g}
	generateNested(g, attrs)

	if forResource {
		g.P()
		g.P("func ", name, "ToProto(m *", model, ") ", message, " {")
		g.P("return &", g.QualifiedGoIdent(t.message.GoIdent), "{")
		for _, a := range attrs {
			if a.field == nil || a.computedOnly() && a.name != "name" {
				continue
			}
			g.P(a.goName, ": ", c.toProto(a, "m."+a.goName), ",")
		}
		g.P("}")
		g.P("}")
	}

	g.P()
	g.P("func ", name, "FromProto(p ", message, ", m *", model, ") error {")
	if forResource {
		g.P("values, err := ", c.framework("NameValues"), "(", fmt.Sprintf("%q", t.pattern), ", p.GetName())")
		g.P("if err != nil {")
		g.P("return err")
		g.P("}")
	}
	for i, a := range attrs {
		if a.toggle != nil {
			g.P("m.", a.goName, " = ", c.types("BoolValue"), "(p.Get", a.toggle.state.GoName, "() == ", g.QualifiedGoIdent(a.toggle.on.GoIdent), ")")
			continue
		}
		if a.field == nil {
			if a.name == "id" {
				g.P("m.ID = ", c.types("StringValue"), "(p.GetName())")
			} else {
				g.P("m.", a.goName, " = ", c.types("StringValue"), "(values[", i-1, "])")
			}
			continue
		}
		g.P("m.", a.goName, " = ", c.fromProto(a, "p.Get"+a.field.GoName+"()", "m."+a.goName))
	}
	g.P("return nil")
	g.P("}")
}

// generateNested writes the attribute types and conversions of the
// attributes' nested objects, and of theirs.
func generateNested(g *protogen.GeneratedFile, attrs []attribute) {
	c := conv{g}
	for _, a := range attrs {
		if a.message == nil {
			continue
		}
		message := "*" + g.QualifiedGoIdent(a.message.GoIdent)
		object := c.types("Object")

		g.P()
		g.P("func ", a.prefix, "AttrTypes() map[string]", c.attr("Type"), " {")
		g.P("return map[string]", c.attr("Type"), "{")
		for _, n := range a.nested {
			g.P(fmt.Sprintf("%q", n.name), ": ", c.attrType(n), ",")
		}
		g.P("}")
		g.P("}")

		g.P()
		g.P("func ", a.prefix, "FromProto(p ", message, ", prior ", object, ") ", object, " {")
		g.P("return ", c.types("ObjectValueMust"), "(", a.prefix, "AttrTypes(), map[string]", c.attr("Value"), "{")
		for _, n := range a.nested {
			prior := c.framework("Attr") + "(prior, " + fmt.Sprintf("%q", n.name) + ", " + c.null(n) + ")"
			g.P(fmt.Sprintf("%q", n.name), ": ", c.fromProto(n, "p.Get"+n.field.GoName+"()", prior), ",")
		}
		g.P("})")
		g.P("}")

		g.P()
		g.P("func ", a.prefix, "ToProto(o ", object, ") ", message, " {")
		g.P("if o.IsNull() || o.IsUnknown() {")
		g.P("return nil")
		g.P("}")
		g.P("return &", g.QualifiedGoIdent(a.message.GoIdent), "{")
		for _, n := range a.nested {
			if n.computedOnly() {
				continue
			}
			v := c.framework("Attr") + "(o, " + fmt.Sprintf("%q", n.name) + ", " + c.null(n) + ")"
			g.P(n.field.GoName, ": ", c.toProto(n, v), ",")
		}
		g.P("}")
		g.P("}")

		generateNested(g, a.nested)
	}
}

// updateChanged returns the condition under which an updatable attribute
// is named in an update mask: what it sends differs between the plan and
// the state. A nested message or repeated field is sent, and named, as a
// whole.
func updateChanged(g *protogen.GeneratedFile, a attribute) string {
	if a.message == nil {
		return "!plan." + a.goName + ".Equal(state." + a.goName + ")"
	}
	return "!" + sentSame(g, a, "plan."+a.goName, "state."+a.goName)
}

// sentSame returns the condition under which plan and state, values of an
// attribute holding nested messages, send the same: their output-only
// fields, which the plan doesn't know, don't count.
func sentSame(g *protogen.GeneratedFile, a attribute, plan, state string) string {
	c := conv{g}
	same := map[string]string{
		"Object": g.QualifiedGoIdent(protoPackage.Ident("Equal")),
		"List":   c.framework("SameMessages"),
		"Set":    c.framework("SameMessageSets"),
		"Map":    c.framework("SameMessageMaps"),
	}[a.kind]
	return same + "(" + c.toProto(a, plan) + ", " + c.toProto(a, state) + ")"
}

// conv writes the expressions converting attributes.
type conv struct{ g *protogen.GeneratedFile }

func (c conv) types(ident string) string { return c.g.QualifiedGoIdent(typesPackage.Ident(ident)) }
func (c conv) framework(ident string) string {
	return c.g.QualifiedGoIdent(frameworkPackage.Ident(ident))
}
func (c conv) attr(ident string) string { return c.g.QualifiedGoIdent(attrPackage.Ident(ident)) }

// objectType returns the type of a's objects.
func (c conv) objectType(a attribute) string {
	return c.types("ObjectType") + "{AttrTypes: " + a.prefix + "AttrTypes()}"
}

// elemType returns the type of a collection's elements.
func (c conv) elemType(a attribute) string {
	if a.message != nil {
		return c.objectType(a)
	}
	return c.types(a.elem + "Type")
}

// attrType returns the attribute's type.
func (c conv) attrType(a attribute) string {
	switch {
	case a.kind == "Object":
		return c.objectType(a)
	case a.collection():
		return c.types(a.kind+"Type") + "{ElemType: " + c.elemType(a) + "}"
	default:
		return c.types(a.kind + "Type")
	}
}

// null returns the attribute's null value.
func (c conv) null(a attribute) string {
	switch {
	case a.kind == "Object":
		return c.types("ObjectNull") + "(" + a.prefix + "AttrTypes())"
	case a.collection():
		return c.types(a.kind+"Null") + "(" + c.elemType(a) + ")"
	default:
		return c.types(a.kind+"Null") + "()"
	}
}

// goElem returns the Go type of a repeated scalar field's elements.
func (c conv) goElem(a attribute) string {
	switch {
	case a.enum != nil:
		return c.g.QualifiedGoIdent(a.enum.GoIdent)
	case a.int32:
		return "int32"
	case a.elem == "Int64":
		return "int64"
	case a.elem == "Bool":
		return "bool"
	default:
		return "string"
	}
}

// enumFromName returns the expression of the enum value named s.
func (c conv) enumFromName(e *protogen.Enum, s string) string {
	values := c.g.QualifiedGoIdent(protogen.GoIdent{GoName: e.GoIdent.GoName + "_value", GoImportPath: e.GoIdent.GoImportPath})
	return c.g.QualifiedGoIdent(e.GoIdent) + "(" + values + "[" + s + "])"
}

// fromProto returns the expression of a's value from get, its field's,
// with prior, its prior value's: an optional attribute left out stays
// null while the API answers with its zero value.
func (c conv) fromProto(a attribute, get, prior string) string {
	if a.computedOnly() {
		prior = c.null(a)
	}
	var value, zero string
	switch {
	case a.message != nil && a.kind == "Object":
		value = c.framework("Object") + "(" + get + ", " + prior + ", " + a.prefix + "AttrTypes(), " + a.prefix + "FromProto)"
	case a.message != nil && a.kind == "List":
		value = c.framework("MessageList") + "(" + a.prefix + "AttrTypes(), " + get + ", " + prior + ", " + a.prefix + "FromProto)"
	case a.message != nil && a.kind == "Set":
		value = c.framework("MessageSet") + "(" + a.prefix + "AttrTypes(), " + get + ", " + prior + ", " + a.prefix + "FromProto, " + a.prefix + "ToProto)"
	case a.message != nil:
		value = c.framework("MessageMap") + "(" + a.prefix + "AttrTypes(), " + get + ", " + prior + ", " + a.prefix + "FromProto)"
	case a.kind == "Map":
		value = c.framework("StringMap") + "(" + get + ")"
	case (a.kind == "List" || a.kind == "Set") && a.elem == "String" && a.enum == nil:
		value = c.framework("String"+a.kind) + "(" + get + ")"
	case a.kind == "List" || a.kind == "Set":
		value = c.framework(a.kind+"Of") + "(" + c.elemType(a) + ", " + get + ", " + c.scalarFrom(a) + ")"
	case a.enum != nil:
		value, zero = c.types("StringValue")+"("+get+".String())", get+" == 0"
	case a.kind == "Int64":
		value, zero = c.types("Int64Value")+"(int64("+get+"))", get+" == 0"
	case a.kind == "Bool":
		value, zero = c.types("BoolValue")+"("+get+")", "!"+get
	default:
		value, zero = c.types("StringValue")+"("+get+")", get+` == ""`
	}
	if a.collection() {
		zero = "len(" + get + ") == 0"
	}
	if a.optional && !a.computed && zero != "" {
		value = c.framework("Optional") + "(" + prior + ", " + value + ", " + zero + ")"
	}
	return value
}

// scalarFrom returns the function converting a repeated scalar field's
// element.
func (c conv) scalarFrom(a attribute) string {
	switch {
	case a.enum != nil:
		return "func(v " + c.goElem(a) + ") " + c.types("String") + " { return " + c.types("StringValue") + "(v.String()) }"
	case a.int32:
		return "func(v int32) " + c.types("Int64") + " { return " + c.types("Int64Value") + "(int64(v)) }"
	default:
		return c.types(a.elem + "Value")
	}
}

// toProto returns the expression of a's field from v, its value.
func (c conv) toProto(a attribute, v string) string {
	switch {
	case a.message != nil && a.kind == "Object":
		return a.prefix + "ToProto(" + v + ")"
	case a.message != nil:
		helper := "Elements"
		if a.kind == "Map" {
			helper = "MapElements"
		}
		message := "*" + c.g.QualifiedGoIdent(a.message.GoIdent)
		return c.framework(helper) + "(" + v + ", func(e " + c.attr("Value") + ") " + message + " { return " + a.prefix + "ToProto(e.(" + c.types("Object") + ")) })"
	case a.kind == "Map":
		return c.framework("StringMapToProto") + "(" + v + ")"
	case a.kind == "List" || a.kind == "Set":
		return c.framework("Elements") + "(" + v + ", func(e " + c.attr("Value") + ") " + c.goElem(a) + " { return " + c.scalarTo(a, "e.("+c.types(a.elem)+")") + " })"
	default:
		return c.scalarTo(a, v)
	}
}

// scalarTo returns the expression of a scalar field, or element, from v,
// a value of a's scalar type.
func (c conv) scalarTo(a attribute, v string) string {
	switch {
	case a.enum != nil:
		return c.enumFromName(a.enum, v+".ValueString()")
	case a.int32:
		return "int32(" + v + ".ValueInt64())"
	case a.kind == "List" || a.kind == "Set":
		return v + ".Value" + a.elem + "()"
	default:
		return v + ".Value" + a.kind + "()"
	}
}
