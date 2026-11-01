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
	// wellKnown is a string's google.protobuf type: Timestamp or Duration.
	wellKnown string
	// presence: the field has explicit presence (proto3 optional), so
	// unset is null. chosenObject: a message field marked optional.
	presence, chosenObject bool
	// oneof is the oneof the field is a member of; oneofRequired says that
	// one of its members must be set.
	oneof         *protogen.Oneof
	oneofRequired bool
	// kept is an output-only value the API answers with only once, kept
	// from then on (secrets.go); sensitive hides it.
	kept, sensitive bool
	doc             string

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
	// inList is one of a settable list's elements, matched by position.
	inList bool
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
		if err == nil {
			err = applySecret(t, &a, forResource)
		}
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.Desc.Name(), err)
		}
		if a.sensitive && !forResource {
			continue // a secret is the create's only: no read has it
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
	switch {
	case f.Desc.HasOptionalKeyword() && f.Desc.Kind() == protoreflect.MessageKind:
		// A message field has presence anyway: optional, it is an object
		// the API chooses when it is left out.
		a.chosenObject = true
	case f.Desc.HasOptionalKeyword():
		a.presence = true
	case f.Oneof != nil:
		a.oneof = f.Oneof
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
			if t := wellKnownTypes[value.Message.Desc.FullName()]; t != "" {
				return a, fmt.Errorf("maps of strings to google.protobuf.%s aren't supported yet", t)
			}
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
			if t := wellKnownTypes[f.Message.Desc.FullName()]; t != "" {
				return a, fmt.Errorf("repeated google.protobuf.%s fields aren't supported yet", t)
			}
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
	case f.Desc.Kind() == protoreflect.MessageKind && wellKnownTypes[f.Message.Desc.FullName()] != "":
		a.kind, a.wellKnown = "String", wellKnownTypes[f.Message.Desc.FullName()]
		a.doc = strings.TrimSpace(a.doc + " " + wellKnownFormats[a.wellKnown])
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
	if a.oneof != nil && a.required {
		// Any member may be the one set: the oneof is required instead.
		a.required, a.optional, a.oneofRequired = false, true, true
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
	case (a.presence || a.chosenObject) && s.inList && !s.replaced:
		// Kept by position, a value could be planned for another element.
		return fmt.Errorf("a field with explicit presence (proto3 optional) can't be in a list's elements: they are matched by position, so the value kept for one could be planned for another")
	case has(annotations.FieldBehavior_OPTIONAL):
		a.optional = true
		// With explicit presence, the API chooses the value left out, and
		// the state keeps it until one is set.
		a.computed, a.keep = a.presence || a.chosenObject, a.presence || a.chosenObject
		if a.presence || a.chosenObject {
			a.doc = strings.TrimSpace(a.doc + " Left out, the API chooses it; removing it keeps the current value.")
		}
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
	if m.Desc.ParentFile().Package() == "google.protobuf" {
		return fmt.Errorf("%s fields aren't supported yet, of google.protobuf only Timestamp and Duration", m.Desc.FullName())
	}
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
	child.inList = s.inList || a.kind == "List" && !child.computed
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

// The google.protobuf messages that are strings in their JSON form, and the
// form, for the attributes' descriptions.
var (
	wellKnownTypes = map[protoreflect.FullName]string{
		"google.protobuf.Timestamp": "Timestamp",
		"google.protobuf.Duration":  "Duration",
	}
	wellKnownFormats = map[string]string{
		"Timestamp": "An RFC 3339 timestamp, such as `2026-11-01T12:00:00Z`.",
		"Duration":  "A duration in seconds, such as `90s` or `1.5s`.",
	}
)

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
		if a.sensitive {
			g.P("Sensitive: true,")
		}
		if a.collection() && a.message == nil {
			g.P("ElementType: ", c.types(a.elem+"Type"), ",")
		}
		if validators := attributeValidators(c, a, attrs); len(validators) > 0 {
			g.P("Validators: []", g.QualifiedGoIdent(validatorPackage.Ident(a.kind)), "{", strings.Join(validators, ", "), "},")
		}
		var modifiers []string
		modifierPackage := protogen.GoImportPath("github.com/hashicorp/terraform-plugin-framework/resource/schema/" + strings.ToLower(a.kind) + "planmodifier")
		switch {
		case a.keep && a.inNested:
			modifiers = append(modifiers, g.QualifiedGoIdent(modifierPackage.Ident("UseNonNullStateForUnknown"))+"()")
		case a.keep:
			modifiers = append(modifiers, g.QualifiedGoIdent(modifierPackage.Ident("UseStateForUnknown"))+"()")
		}
		// A oneof's member kept from the state, such as an immutable one the
		// API chose, is planned null while the configuration sets another:
		// it would otherwise be the one sent. Its replacement then follows
		// the plan, not the configuration: an immutable member going from
		// set to null, as the oneof switches away from it, is a change.
		ifConfigured := a.computed
		if a.oneof != nil && a.keep {
			modifiers = append(modifiers, c.framework("OneofOthers")+"{"+quoted(oneofOthers(a, attrs))+"}")
			ifConfigured = false
		}
		if a.replace && a.message != nil {
			// What it sends, so that an output-only field changing doesn't
			// replace the resource.
			value := c.types(a.kind)
			modifiers = append(modifiers, c.framework(a.kind+"ReplaceIfSent")+"(func(plan, state "+value+") bool { return "+sentSame(g, a, "plan", "state")+" }, "+fmt.Sprint(ifConfigured)+")")
		} else if a.replace && a.wellKnown != "" {
			// The same instant or length, however written.
			same := g.QualifiedGoIdent(protoPackage.Ident("Equal")) + "(" + c.toProto(a, "plan") + ", " + c.toProto(a, "state") + ")"
			modifiers = append(modifiers, c.framework("StringReplaceIfSent")+"(func(plan, state "+c.types("String")+") bool { return "+same+" }, "+fmt.Sprint(ifConfigured)+")")
		} else if a.replace && ifConfigured {
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

// oneofOthers returns the names of the other settable members of a's
// oneof, among attrs.
func oneofOthers(a attribute, attrs []attribute) []string {
	var others []string
	for _, b := range attrs {
		if b.oneof == a.oneof && b.name != a.name && (b.required || b.optional) {
			others = append(others, b.name)
		}
	}
	return others
}

// attributeValidators returns the validators of a, one of attrs.
func attributeValidators(c conv, a attribute, attrs []attribute) []string {
	settable := a.required || a.optional
	var validators []string
	switch {
	case a.kind == "Int64" && a.int32 && settable:
		validators = append(validators, c.framework("Int32")+"()")
	case a.kind == "String" && a.enum != nil && settable:
		validators = append(validators, c.framework("OneOf")+"("+quoted(enumNames(a.enum))+")")
	case a.kind == "String" && a.wellKnown != "" && settable:
		validators = append(validators, c.framework(a.wellKnown+"Format")+"()")
	case settable && a.collection():
		var options []string
		if a.enum != nil {
			options = append(options, "OneOf: []string{"+quoted(enumNames(a.enum))+"}")
		}
		if a.int32 {
			options = append(options, "Int32: true")
		}
		validators = append(validators, c.framework("ElementCheck")+"{"+strings.Join(options, ", ")+"}")
	}
	if a.oneof != nil && settable {
		// The first settable member also checks that one is set, when the
		// oneof is required: any of its members being REQUIRED.
		var others []string
		first, required := "", false
		for _, b := range attrs {
			if b.oneof != a.oneof || !(b.required || b.optional) {
				continue
			}
			if first == "" {
				first = b.name
			}
			if b.name != a.name {
				others = append(others, fmt.Sprintf("%q", b.name))
			}
			required = required || b.oneofRequired
		}
		check := c.framework("Oneof") + "{Others: []string{" + strings.Join(others, ", ") + "}"
		if required && first == a.name {
			check += ", Required: true"
		}
		validators = append(validators, check+"}")
	}
	return validators
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
		var sent []attribute
		for _, a := range attrs {
			if a.field != nil && (!a.computedOnly() || a.name == "name") {
				sent = append(sent, a)
			}
		}
		generateToProtoBody(g, t.message, sent, func(a attribute) string { return "m." + a.goName })
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
		g.P("m.", a.goName, " = ", c.fromProto(a, "p", "m."+a.goName))
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
		if slices.ContainsFunc(a.nested, func(n attribute) bool { return n.presence }) {
			// A field with presence is read without a getter.
			g.P("if p == nil {")
			g.P("p = &", g.QualifiedGoIdent(a.message.GoIdent), "{}")
			g.P("}")
		}
		g.P("return ", c.types("ObjectValueMust"), "(", a.prefix, "AttrTypes(), map[string]", c.attr("Value"), "{")
		for _, n := range a.nested {
			prior := c.framework("Attr") + "(prior, " + fmt.Sprintf("%q", n.name) + ", " + c.null(n) + ")"
			g.P(fmt.Sprintf("%q", n.name), ": ", c.fromProto(n, "p", prior), ",")
		}
		g.P("})")
		g.P("}")

		g.P()
		g.P("func ", a.prefix, "ToProto(o ", object, ") ", message, " {")
		g.P("if o.IsNull() || o.IsUnknown() {")
		g.P("return nil")
		g.P("}")
		var sent []attribute
		for _, n := range a.nested {
			if !n.computedOnly() {
				sent = append(sent, n)
			}
		}
		generateToProtoBody(g, a.message, sent, func(n attribute) string {
			return c.framework("Attr") + "(o, " + fmt.Sprintf("%q", n.name) + ", " + c.null(n) + ")"
		})
		g.P("}")

		generateNested(g, a.nested)
	}
}

// generateToProtoBody writes the statements returning m, a message of the
// attributes sent, each value as valueOf returns it. A oneof's members are
// set after, as the one set of them.
func generateToProtoBody(g *protogen.GeneratedFile, m *protogen.Message, sent []attribute, valueOf func(attribute) string) {
	c := conv{g}
	literal := "&" + g.QualifiedGoIdent(m.GoIdent) + "{"
	if !slices.ContainsFunc(sent, func(a attribute) bool { return a.oneof != nil }) {
		g.P("return ", literal)
		for _, a := range sent {
			g.P(a.field.GoName, ": ", c.toProto(a, valueOf(a)), ",")
		}
		g.P("}")
		return
	}
	g.P("out := ", literal)
	for _, a := range sent {
		if a.oneof == nil {
			g.P(a.field.GoName, ": ", c.toProto(a, valueOf(a)), ",")
		}
	}
	g.P("}")
	var done []*protogen.Oneof
	for _, a := range sent {
		if a.oneof == nil || slices.Contains(done, a.oneof) {
			continue
		}
		done = append(done, a.oneof)
		g.P("switch {")
		for _, b := range sent {
			if b.oneof == a.oneof {
				g.P("case ", c.framework("IsSet"), "(", valueOf(b), "):")
				g.P("out.", a.oneof.GoName, " = &", g.QualifiedGoIdent(b.field.GoIdent), "{", b.field.GoName, ": ", c.toProto(b, valueOf(b)), "}")
			}
		}
		g.P("}")
	}
	g.P("return out")
}

// updateChanged returns the condition under which an updatable attribute
// is named in an update mask: what it sends differs between the plan and
// the state. A nested message or repeated field is sent, and named, as a
// whole.
func updateChanged(g *protogen.GeneratedFile, a attribute) string {
	c := conv{g}
	if a.wellKnown != "" {
		// The same instant or length, however written.
		return "!" + g.QualifiedGoIdent(protoPackage.Ident("Equal")) + "(" + c.toProto(a, "plan."+a.goName) + ", " + c.toProto(a, "state."+a.goName) + ")"
	}
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

// fromProto returns the expression of a's value from its field in p, with
// prior, its prior value's: an optional attribute left out stays null
// while the API answers with its zero value.
func (c conv) fromProto(a attribute, p, prior string) string {
	get := p + ".Get" + a.field.GoName + "()"
	if a.computedOnly() && !a.kept {
		prior = c.null(a)
	}
	var value, zero string
	switch {
	case a.kept:
		value = c.framework("Kept") + "(" + prior + ", " + get + ")"
	case a.wellKnown != "":
		value = c.framework(a.wellKnown) + "(" + get + ", " + prior + ")"
	case a.presence:
		value = c.framework("Present"+c.presenceType(a)) + "(" + p + "." + a.field.GoName + ")"
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
	switch {
	case a.oneof != nil:
		// Null unless it is the member the oneof holds.
		is := c.framework("Is") + "[*" + c.g.QualifiedGoIdent(a.field.GoIdent) + "](" + p + ".Get" + a.oneof.GoName + "())"
		value = c.framework("When") + "(" + is + ", " + value + ", " + c.null(a) + ")"
	case a.optional && !a.computed && zero != "":
		value = c.framework("Optional") + "(" + prior + ", " + value + ", " + zero + ")"
	}
	return value
}

// presenceType names the framework's conversions of a field with explicit
// presence: String, Int32, Int64, Bool or Enum.
func (c conv) presenceType(a attribute) string {
	switch {
	case a.enum != nil:
		return "Enum"
	case a.int32:
		return "Int32"
	default:
		if a.kind == "Int64" {
			return "Int64"
		}
		return a.kind
	}
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
	case a.wellKnown != "":
		return c.framework(a.wellKnown+"ToProto") + "(" + v + ")"
	case a.presence && a.enum != nil:
		values := c.g.QualifiedGoIdent(protogen.GoIdent{GoName: a.enum.GoIdent.GoName + "_value", GoImportPath: a.enum.GoIdent.GoImportPath})
		return c.framework("EnumPtr") + "[" + c.g.QualifiedGoIdent(a.enum.GoIdent) + "](" + v + ", " + values + ")"
	case a.presence:
		return c.framework(c.presenceType(a)+"Ptr") + "(" + v + ")"
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
