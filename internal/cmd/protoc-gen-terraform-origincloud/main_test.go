package main

import (
	"go/format"
	"strings"
	"testing"

	_ "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	_ "google.golang.org/protobuf/types/known/emptypb"
	_ "google.golang.org/protobuf/types/known/fieldmaskpb"
	_ "google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/pluginpb"
)

// widgets is an API of the shape the generator reads: a resource, Widget,
// that can be created, updated, deleted and listed, and a data source,
// Gadget. Both have output-only repeated string fields.
const widgets = `
name: "test/widgets/v1/widget.proto"
package: "test.widgets.v1"
dependency: [
  "google/api/client.proto", "google/api/field_behavior.proto", "google/api/resource.proto",
  "google/longrunning/operations.proto", "google/protobuf/empty.proto", "google/protobuf/field_mask.proto"
]
syntax: "proto3"
options { go_package: "example.com/sdk/widgets/v1;widgetsv1" }
service {
  name: "WidgetService"
  options { [google.api.default_host]: "widgets.origincloudapis.eu" }
  method { name: "GetWidget" input_type: ".test.widgets.v1.GetWidgetRequest" output_type: ".test.widgets.v1.Widget" }
  method { name: "ListWidgets" input_type: ".test.widgets.v1.ListWidgetsRequest" output_type: ".test.widgets.v1.ListWidgetsResponse" }
  method {
    name: "CreateWidget" input_type: ".test.widgets.v1.CreateWidgetRequest" output_type: ".google.longrunning.Operation"
    options { [google.longrunning.operation_info] { response_type: "Widget" metadata_type: "Widget" } }
  }
  method { name: "UpdateWidget" input_type: ".test.widgets.v1.UpdateWidgetRequest" output_type: ".test.widgets.v1.Widget" }
  method { name: "DeleteWidget" input_type: ".test.widgets.v1.NameRequest" output_type: ".google.protobuf.Empty" }
  method { name: "GetGadget" input_type: ".test.widgets.v1.NameRequest" output_type: ".test.widgets.v1.Gadget" }
}
message_type {
  name: "Widget"
  options { [google.api.resource] { type: "widgets.origincloudapis.eu/Widget" pattern: "projects/{project}/widgets/{widget}" singular: "widget" plural: "widgets" } }
  field { name: "name" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: IDENTIFIER } }
  field { name: "aliases" number: 3 label: LABEL_REPEATED type: TYPE_STRING options { [google.api.field_behavior]: OUTPUT_ONLY } }
}
message_type {
  name: "Gadget"
  options { [google.api.resource] { type: "widgets.origincloudapis.eu/Gadget" pattern: "gadgets/{gadget}" singular: "gadget" plural: "gadgets" } }
  field { name: "name" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: IDENTIFIER } }
  field { name: "parts" number: 2 label: LABEL_REPEATED type: TYPE_STRING options { [google.api.field_behavior]: OUTPUT_ONLY } }
}
message_type {
  name: "NameRequest"
  field { name: "name" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING }
}
message_type {
  name: "GetWidgetRequest"
  field { name: "name" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING }
}
message_type {
  name: "ListWidgetsRequest"
  field { name: "parent" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING }
  field { name: "page_size" number: 2 label: LABEL_OPTIONAL type: TYPE_INT32 }
  field { name: "page_token" number: 3 label: LABEL_OPTIONAL type: TYPE_STRING }
}
message_type {
  name: "ListWidgetsResponse"
  field { name: "widgets" number: 1 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Widget" }
  field { name: "next_page_token" number: 2 label: LABEL_OPTIONAL type: TYPE_STRING }
}
message_type {
  name: "CreateWidgetRequest"
  field { name: "parent" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING }
  field { name: "widget" number: 2 label: LABEL_OPTIONAL type: TYPE_MESSAGE type_name: ".test.widgets.v1.Widget" }
}
message_type {
  name: "UpdateWidgetRequest"
  field { name: "widget" number: 1 label: LABEL_OPTIONAL type: TYPE_MESSAGE type_name: ".test.widgets.v1.Widget" }
  field { name: "update_mask" number: 2 label: LABEL_OPTIONAL type: TYPE_MESSAGE type_name: ".google.protobuf.FieldMask" }
}
`

// run generates the provider from widgets, changed by change, and returns
// the generated files by name, or the generator's error.
func run(t *testing.T, change func(*descriptorpb.FileDescriptorProto)) (map[string]string, string) {
	t.Helper()
	saved := toggles
	toggles = nil // the toggles are PVS's
	t.Cleanup(func() { toggles = saved })

	file := new(descriptorpb.FileDescriptorProto)
	if err := prototext.Unmarshal([]byte(widgets), file); err != nil {
		t.Fatal(err)
	}
	if change != nil {
		change(file)
	}
	gen, err := protogen.Options{}.New(request(t, file))
	if err != nil {
		t.Fatal(err)
	}
	if err := generate(gen); err != nil {
		return nil, err.Error()
	}
	resp := gen.Response()
	if resp.Error != nil {
		return nil, resp.GetError()
	}
	files := map[string]string{}
	for _, f := range resp.GetFile() {
		if _, err := format.Source([]byte(f.GetContent())); err != nil {
			t.Errorf("%s isn't Go: %v", f.GetName(), err)
		}
		files[f.GetName()] = f.GetContent()
	}
	return files, ""
}

// request returns the plugin's request to generate file.
func request(t *testing.T, file *descriptorpb.FileDescriptorProto) *pluginpb.CodeGeneratorRequest {
	t.Helper()
	req := &pluginpb.CodeGeneratorRequest{FileToGenerate: []string{file.GetName()}}
	seen := map[string]bool{}
	for _, dep := range file.GetDependency() {
		d, err := protoregistry.GlobalFiles.FindFileByPath(dep)
		if err != nil {
			t.Fatal(err)
		}
		req.ProtoFile = appendWithImports(req.ProtoFile, d, seen)
	}
	req.ProtoFile = append(req.ProtoFile, file)
	return req
}

// appendWithImports appends d to files after its imports, each once.
func appendWithImports(files []*descriptorpb.FileDescriptorProto, d protoreflect.FileDescriptor, seen map[string]bool) []*descriptorpb.FileDescriptorProto {
	if seen[d.Path()] {
		return files
	}
	seen[d.Path()] = true
	for i := 0; i < d.Imports().Len(); i++ {
		files = appendWithImports(files, d.Imports().Get(i).FileDescriptor, seen)
	}
	return append(files, protodesc.ToFileDescriptorProto(d))
}

func message(file *descriptorpb.FileDescriptorProto, name string) *descriptorpb.DescriptorProto {
	for _, m := range file.GetMessageType() {
		if m.GetName() == name {
			return m
		}
	}
	panic("no message " + name)
}

func contains(t *testing.T, name, code string, want ...string) {
	t.Helper()
	// Formatted, so that the alignment of fields doesn't matter.
	formatted, err := format.Source([]byte(code))
	if err != nil {
		t.Fatalf("%s isn't Go: %v", name, err)
	}
	collapsed := strings.Join(strings.Fields(string(formatted)), " ")
	for _, w := range want {
		if !strings.Contains(collapsed, w) {
			t.Errorf("%s has no %q:\n%s", name, w, formatted)
		}
	}
}

func TestRepeatedStringsAreListsOfStrings(t *testing.T) {
	files, failure := run(t, nil)
	if failure != "" {
		t.Fatal(failure)
	}
	// Output only, in a resource: computed, never sent, empty rather than
	// null.
	widget := files["internal/services/widgetsv1/widget.gen.go"]
	contains(t, "widget.gen.go", widget,
		"Aliases types.List `tfsdk:\"aliases\"`",
		`"aliases": schema.ListAttribute{ MarkdownDescription: "", Computed: true, ElementType: types.StringType, },`,
		"m.Aliases = framework.StringList(p.GetAliases())",
	)
	if strings.Contains(widget, "Aliases: ") {
		t.Error("an output-only list is sent")
	}

	gadget := files["internal/services/widgetsv1/gadget.gen.go"]
	contains(t, "gadget.gen.go", gadget,
		`"parts": schema.ListAttribute{ MarkdownDescription: "", Computed: true, ElementType: types.StringType, },`,
		"m.Parts = framework.StringList(p.GetParts())",
	)
}

func TestAListedResourceTypeGetsAListDataSource(t *testing.T) {
	files, failure := run(t, nil)
	if failure != "" {
		t.Fatal(failure)
	}
	widget := files["internal/services/widgetsv1/widget.gen.go"]
	contains(t, "widget.gen.go", widget,
		// A resource's items have a model of their own, without its id.
		"type widgetItemModel struct { Name types.String `tfsdk:\"name\"` Aliases types.List `tfsdk:\"aliases\"` }",
		"type widgetListModel struct { Project types.String `tfsdk:\"project\"` Widgets []widgetItemModel `tfsdk:\"widgets\"` }",
		`TypeName: "_widgets_widgets",`,
		// Paginated: every page.
		`range v1.NewWidgetClient(client).AllWidgets(ctx, &v1.ListWidgetsRequest{Parent: "projects/" + m.Project.ValueString()})`,
		"m.Widgets = []widgetItemModel{}",
	)
	services := files["internal/services/widgetsv1/services.gen.go"]
	contains(t, "services.gen.go", services,
		"framework.NewResource(widgetResource()),",
		"framework.NewListDataSource(widgetListDataSource()),",
		"framework.NewDataSource(gadgetDataSource()),",
	)
	if strings.Contains(services, "gadgetListDataSource") {
		t.Error("a resource type the service doesn't list has a list data source")
	}
}

// A list request's optional fields, such as a filter, are left unset.
func TestAListRequestsOptionalFieldsAreLeftUnset(t *testing.T) {
	files, failure := run(t, func(file *descriptorpb.FileDescriptorProto) {
		m := message(file, "ListWidgetsRequest")
		m.Field = append(m.Field, &descriptorpb.FieldDescriptorProto{
			Name: proto.String("filter"), Number: proto.Int32(4),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			Options: behaviors(annotations.FieldBehavior_OPTIONAL),
		})
	})
	if failure != "" {
		t.Fatal(failure)
	}
	contains(t, "widget.gen.go", files["internal/services/widgetsv1/widget.gen.go"],
		`AllWidgets(ctx, &v1.ListWidgetsRequest{Parent: "projects/" + m.Project.ValueString()})`)
}

func TestTheAPIsTermsBecomeTerraformsInDescriptions(t *testing.T) {
	got := prose(protogen.Comments("For every zone's volumes, list the zones whose `services` name `pvs`\n with the location service\n (`location.origincloudapis.eu`), then each zone's volumes.\n"))
	want := "For every zone's volumes, list the zones whose `services` name `pvs` with the `origincloud_location_zones` data source, then each zone's volumes."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func behaviors(b ...annotations.FieldBehavior) *descriptorpb.FieldOptions {
	options := new(descriptorpb.FieldOptions)
	proto.SetExtension(options, annotations.E_FieldBehavior, b)
	return options
}

// withField adds a field, in prototext, to the message named to, and the
// messages after it, also in prototext, to the file.
func withField(to, field string, messages ...string) func(*descriptorpb.FileDescriptorProto) {
	return func(file *descriptorpb.FileDescriptorProto) {
		f := new(descriptorpb.FieldDescriptorProto)
		if err := prototext.Unmarshal([]byte(field), f); err != nil {
			panic(err)
		}
		m := message(file, to)
		m.Field = append(m.Field, f)
		for _, text := range messages {
			d := new(descriptorpb.DescriptorProto)
			if err := prototext.Unmarshal([]byte(text), d); err != nil {
				panic(err)
			}
			if d.GetOptions().GetMapEntry() {
				m.NestedType = append(m.NestedType, d)
			} else {
				file.MessageType = append(file.MessageType, d)
			}
		}
	}
}

// In an immutable field's elements, any change replaces the resource, so
// an immutable field there needs no identity.
func TestAnImmutableFieldsElementsMayBeImmutable(t *testing.T) {
	files, failure := run(t, withField("Widget", `name: "routes" number: 9 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Route" options { [google.api.field_behavior]: IMMUTABLE }`,
		`name: "Route" field { name: "dest" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: IMMUTABLE } }`))
	if failure != "" {
		t.Fatal(failure)
	}
	contains(t, "widget.gen.go", files["internal/services/widgetsv1/widget.gen.go"],
		`"dest": schema.StringAttribute{ MarkdownDescription: "", Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseNonNullStateForUnknown()}, },`,
		"listplanmodifier.UseStateForUnknown(), framework.ListReplaceIfSent(",
	)
}

// twoPatterns gives Widget a second pattern, before its own, and binds
// CreateWidget's parent to parent when it isn't empty.
func twoPatterns(parent string) func(*descriptorpb.FileDescriptorProto) {
	return func(file *descriptorpb.FileDescriptorProto) {
		r := proto.GetExtension(message(file, "Widget").GetOptions(), annotations.E_Resource).(*annotations.ResourceDescriptor)
		r.Pattern = append([]string{"widgets/{widget}"}, r.Pattern...)
		if parent == "" {
			return
		}
		for _, m := range file.GetService()[0].GetMethod() {
			if m.GetName() == "CreateWidget" {
				proto.SetExtension(m.Options, annotations.E_Http, &annotations.HttpRule{Pattern: &annotations.HttpRule_Post{Post: "/v1/{parent=" + parent + "}/widgets"}, Body: "widget"})
			}
		}
	}
}

// A resource type with two patterns, as images have, a project's and the
// public ones, is created in the one its create's parent takes, and read by
// name in any through a data source.
func TestAResourceWithTwoPatterns(t *testing.T) {
	files, failure := run(t, twoPatterns("projects/*"))
	if failure != "" {
		t.Fatal(failure)
	}
	widget := files["internal/services/widgetsv1/widget.gen.go"]
	contains(t, "widget.gen.go", widget,
		`Parent: "projects/" + plan.Project.ValueString(),`,
		`framework.NameValues("projects/{project}/widgets/{widget}", p.GetName())`,
		"func widgetByNameDataSource() framework.DataSourceSpec[widgetByNameModel, *v1.Widget]",
	)
	contains(t, "services.gen.go", files["internal/services/widgetsv1/services.gen.go"], "framework.NewDataSource(widgetByNameDataSource()),")
	for _, tc := range []struct{ parent, want string }{
		{"", "Widget: CreateWidget has no HTTP binding of its parent to choose one of the patterns"},
		{"organizations/*", "Widget: CreateWidget's parent organizations/* is that of none of the patterns"},
	} {
		if _, failure := run(t, twoPatterns(tc.parent)); !strings.Contains(failure, tc.want) {
			t.Errorf("generation failed with %q, want %q", failure, tc.want)
		}
	}
}

// A list request's required field, such as the organization projects are
// listed in, is a required attribute of the list data source, sent in the
// request.
func TestAListRequestsRequiredFieldIsARequiredAttribute(t *testing.T) {
	files, failure := run(t, func(file *descriptorpb.FileDescriptorProto) {
		m := message(file, "ListWidgetsRequest")
		m.Field = append(m.Field, &descriptorpb.FieldDescriptorProto{
			Name: proto.String("owner"), Number: proto.Int32(4), JsonName: proto.String("owner"),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			Options: behaviors(annotations.FieldBehavior_REQUIRED),
		})
	})
	if failure != "" {
		t.Fatal(failure)
	}
	contains(t, "widget.gen.go", files["internal/services/widgetsv1/widget.gen.go"],
		"type widgetListModel struct { Project types.String `tfsdk:\"project\"` Owner types.String `tfsdk:\"owner\"`",
		`"owner": schema1.StringAttribute{ MarkdownDescription: "", Required: true, },`,
		`&v1.ListWidgetsRequest{Parent: "projects/" + m.Project.ValueString(), Owner: m.Owner.ValueString()}`,
	)
}

// The id the client chooses (AIP-133) is a required attribute replacing
// the resource, sent in the create and read from the name.
func TestAClientChosenIdIsAnAttribute(t *testing.T) {
	files, failure := run(t, withField("CreateWidgetRequest", `name: "widget_id" number: 9 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: REQUIRED }`))
	if failure != "" {
		t.Fatal(failure)
	}
	contains(t, "widget.gen.go", files["internal/services/widgetsv1/widget.gen.go"],
		`"widget_id": schema.StringAttribute{ MarkdownDescription: "The widget's id, the last segment of its name. Changing it replaces the widget.", Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, },`,
		"WidgetId: plan.WidgetId.ValueString(),",
		"m.WidgetId = types.StringValue(values[1])",
	)
}

// withSingleton adds a singleton, projects/{project}/settings, with the
// field given in prototext, and the methods given in prototext.
func withSingleton(field string, methods ...string) func(*descriptorpb.FileDescriptorProto) {
	return func(file *descriptorpb.FileDescriptorProto) {
		withField("NameRequest", `name: "dummy" number: 2 label: LABEL_OPTIONAL type: TYPE_STRING`,
			`name: "Settings"
			 options { [google.api.resource] { type: "widgets.origincloudapis.eu/Settings" pattern: "projects/{project}/settings" singular: "settings" plural: "allSettings" } }
			 field { name: "name" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: IDENTIFIER } }
			 `+field,
			`name: "UpdateSettingsRequest"
			 field { name: "settings" number: 1 label: LABEL_OPTIONAL type: TYPE_MESSAGE type_name: ".test.widgets.v1.Settings" }
			 field { name: "update_mask" number: 2 label: LABEL_OPTIONAL type: TYPE_MESSAGE type_name: ".google.protobuf.FieldMask" }`)(file)
		for _, text := range methods {
			m := new(descriptorpb.MethodDescriptorProto)
			if err := prototext.Unmarshal([]byte(text), m); err != nil {
				panic(err)
			}
			file.Service[0].Method = append(file.Service[0].Method, m)
		}
	}
}

const (
	getSettings    = `name: "GetSettings" input_type: ".test.widgets.v1.NameRequest" output_type: ".test.widgets.v1.Settings"`
	updateSettings = `name: "UpdateSettings" input_type: ".test.widgets.v1.UpdateSettingsRequest" output_type: ".google.longrunning.Operation"
		options { [google.longrunning.operation_info] { response_type: "Settings" metadata_type: "Settings" } }`
	budget = `field { name: "budget" number: 2 label: LABEL_OPTIONAL type: TYPE_INT64 oneof_index: 0 proto3_optional: true options { [google.api.field_behavior]: OPTIONAL } }
		oneof_decl { name: "_budget" }`
)

// A singleton the service updates is a resource, whose create and destroy
// update it; an update answering with an operation is waited on.
func TestASingletonIsAResource(t *testing.T) {
	files, failure := run(t, withSingleton(budget, getSettings, updateSettings))
	if failure != "" {
		t.Fatal(failure)
	}
	settings := files["internal/services/widgetsv1/settings.gen.go"]
	contains(t, "settings.gen.go", settings,
		`resource.Name = "projects/" + plan.Project.ValueString() + "/settings"`,
		`if len(mask) == 0 { return v1.NewWidgetClient(client).GetSettings(ctx, &v1.NameRequest{Name: resource.Name}) }`,
		"return op.Wait(ctx)",
		`Clear: func(ctx context.Context, client *core.Client, name string, fields []string) error { resource := &v1.Settings{Name: name}`,
		"_, err = op.Wait(ctx) return err",
		`if config.Budget.IsNull() && slices.Contains(owned, "budget") { plan.Budget = types.Int64Unknown() }`,
		"Destroying it unsets the settings Terraform set",
	)
	contains(t, "services.gen.go", files["internal/services/widgetsv1/services.gen.go"], "framework.NewResource(settingsResource()),")
}

// A singleton the service only reads is a data source.
func TestAReadOnlySingletonIsADataSource(t *testing.T) {
	files, failure := run(t, withSingleton(`field { name: "limit" number: 2 label: LABEL_OPTIONAL type: TYPE_INT64 options { [google.api.field_behavior]: OUTPUT_ONLY } }`, getSettings))
	if failure != "" {
		t.Fatal(failure)
	}
	contains(t, "services.gen.go", files["internal/services/widgetsv1/services.gen.go"], "framework.NewDataSource(settingsDataSource()),")
	if strings.Contains(files["internal/services/widgetsv1/services.gen.go"], "settingsResource") {
		t.Error("a read-only singleton is a resource")
	}
}

func TestSingletonsThatFailGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*descriptorpb.FileDescriptorProto)
		want   string
	}{
		{
			name:   "an optional field without presence",
			change: withSingleton(`field { name: "budget" number: 2 label: LABEL_OPTIONAL type: TYPE_INT64 options { [google.api.field_behavior]: OPTIONAL } }`, getSettings, updateSettings),
			want:   "Settings: field budget: a singleton's OPTIONAL field needs explicit presence (proto3 optional)",
		},
		{
			name: "a create",
			change: withSingleton(budget, getSettings, updateSettings,
				`name: "CreateSettings" input_type: ".test.widgets.v1.UpdateSettingsRequest" output_type: ".test.widgets.v1.Settings"`,
				`name: "DeleteSettings" input_type: ".test.widgets.v1.NameRequest" output_type: ".google.protobuf.Empty"`),
			want: "Settings: CreateSettings creates a singleton, the only one of its parent (projects/{project}/settings), which isn't supported",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, failure := run(t, tc.change); !strings.Contains(failure, tc.want) {
				t.Errorf("generation failed with %q, want %q", failure, tc.want)
			}
		})
	}
}

// An OPTIONAL id is optional and computed, the API's choice when left out;
// one neither REQUIRED nor OPTIONAL fails generation.
func TestClientChosenIds(t *testing.T) {
	files, failure := run(t, withField("CreateWidgetRequest", `name: "widget_id" number: 9 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: OPTIONAL }`))
	if failure != "" {
		t.Fatal(failure)
	}
	contains(t, "widget.gen.go", files["internal/services/widgetsv1/widget.gen.go"],
		`"widget_id": schema.StringAttribute{ MarkdownDescription: "The widget's id, the last segment of its name; left out, the API chooses it. Changing it replaces the widget.", Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()}, },`,
		`ClientID: func(m *widgetModel) types.String { return m.WidgetId },`,
		`IDAttribute: "widget_id",`,
		`Pattern: "projects/{project}/widgets/{widget}",`,
	)
	_, failure = run(t, withField("CreateWidgetRequest", `name: "widget_id" number: 9 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: IMMUTABLE }`))
	if want := "CreateWidget's widget_id is neither REQUIRED nor OPTIONAL"; !strings.Contains(failure, want) {
		t.Errorf("generation failed with %q, want %q", failure, want)
	}
}

// withComment gives the field name of the message to a leading comment.
func withComment(to, name, comment string) func(*descriptorpb.FileDescriptorProto) {
	return func(file *descriptorpb.FileDescriptorProto) {
		for i, m := range file.GetMessageType() {
			for j, f := range m.GetField() {
				if m.GetName() == to && f.GetName() == name {
					if file.SourceCodeInfo == nil {
						file.SourceCodeInfo = &descriptorpb.SourceCodeInfo{}
					}
					file.SourceCodeInfo.Location = append(file.SourceCodeInfo.Location, &descriptorpb.SourceCodeInfo_Location{
						Path: []int32{4, int32(i), 2, int32(j)}, Span: []int32{0, 0, 0}, LeadingComments: proto.String(" " + comment + "\n"),
					})
				}
			}
		}
	}
}

// The client-chosen id's description starts with its field's comment; a
// parent's variable doesn't count among the attributes the guard against
// replacing with the same id checks, as another parent is another name;
// a oneof's immutable member kept from the state does, by the plan.
func TestAClientChosenIdsDescriptionAndGuard(t *testing.T) {
	files, failure := run(t, func(file *descriptorpb.FileDescriptorProto) {
		withField("CreateWidgetRequest", `name: "widget_id" number: 9 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: REQUIRED }`)(file)
		withComment("CreateWidgetRequest", "widget_id", "Unique in the project, and never used again.")(file)
		withField("Widget", `name: "zone" number: 10 label: LABEL_OPTIONAL type: TYPE_STRING oneof_index: 0 options { [google.api.field_behavior]: IMMUTABLE }`)(file)
		withField("Widget", `name: "region" number: 11 label: LABEL_OPTIONAL type: TYPE_STRING oneof_index: 0 options { [google.api.field_behavior]: OPTIONAL }`)(file)
		withField("Widget", `name: "size" number: 12 label: LABEL_OPTIONAL type: TYPE_INT64 options { [google.api.field_behavior]: OPTIONAL [google.api.field_behavior]: IMMUTABLE }`)(file)
		message(file, "Widget").OneofDecl = []*descriptorpb.OneofDescriptorProto{{Name: proto.String("place")}}
	})
	if failure != "" {
		t.Fatal(failure)
	}
	widget := files["internal/services/widgetsv1/widget.gen.go"]
	contains(t, "widget.gen.go", widget,
		`MarkdownDescription: "Unique in the project, and never used again. The widget's id, the last segment of its name. Changing it replaces the widget."`,
		`Replaced: func(plan, state, config *widgetModel) []string { var replaced []string if !plan.Zone.Equal(state.Zone) { replaced = append(replaced, "zone") } if !config.Size.IsNull() && !plan.Size.Equal(state.Size) { replaced = append(replaced, "size") } return replaced },`,
	)
}

// A singleton's required field is sent, but neither owned nor unset.
func TestASingletonsRequiredFieldIsntOwned(t *testing.T) {
	files, failure := run(t, withSingleton(budget+` field { name: "contact" number: 3 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: REQUIRED } }`, getSettings, updateSettings))
	if failure != "" {
		t.Fatal(failure)
	}
	contains(t, "settings.gen.go", files["internal/services/widgetsv1/settings.gen.go"],
		`if framework.IsSet(plan.Contact) { mask = append(mask, "contact") }`,
		`Configured: func(config *settingsModel) []string { var fields []string if !config.Budget.IsNull() { fields = append(fields, "budget") } return fields },`,
	)
}

// A list's required number and enum filters are attributes too.
func TestAListsRequiredNumberAndEnumFilters(t *testing.T) {
	files, failure := run(t, func(file *descriptorpb.FileDescriptorProto) {
		file.EnumType = append(file.EnumType, &descriptorpb.EnumDescriptorProto{Name: proto.String("Kind"), Value: []*descriptorpb.EnumValueDescriptorProto{
			{Name: proto.String("KIND_UNSPECIFIED"), Number: proto.Int32(0)}, {Name: proto.String("ROUND"), Number: proto.Int32(1)},
		}})
		m := message(file, "ListWidgetsRequest")
		m.Field = append(m.Field,
			&descriptorpb.FieldDescriptorProto{Name: proto.String("size"), Number: proto.Int32(4), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), Options: behaviors(annotations.FieldBehavior_REQUIRED)},
			&descriptorpb.FieldDescriptorProto{Name: proto.String("kind"), Number: proto.Int32(5), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), TypeName: proto.String(".test.widgets.v1.Kind"), Options: behaviors(annotations.FieldBehavior_REQUIRED)},
		)
	})
	if failure != "" {
		t.Fatal(failure)
	}
	contains(t, "widget.gen.go", files["internal/services/widgetsv1/widget.gen.go"],
		`"size": schema1.Int64Attribute{ MarkdownDescription: "", Required: true, Validators: []validator.Int64{framework.Int32()}, },`,
		`"kind": schema1.StringAttribute{ MarkdownDescription: "One of `+"`ROUND`"+`.", Required: true, Validators: []validator.String{framework.OneOf("ROUND")}, },`,
		`Size: int32(m.Size.ValueInt64()), Kind: v1.Kind(v1.Kind_value[m.Kind.ValueString()])}`,
	)
}

// A secret declared for a field the API hasn't, or that isn't an
// output-only string, fails generation; one of a package the API hasn't yet
// waits for it.
func TestSecretsAreCheckedAgainstTheAPI(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secret secret
		want   string // empty: generation succeeds
	}{
		{"no such field", secret{"test.widgets.v1.Widget", "token"}, "secret token: no field test.widgets.v1.Widget.token"},
		{"no such message", secret{"test.widgets.v1.Gizmo", "token"}, "secret token: no field test.widgets.v1.Gizmo.token"},
		{"a message nested in one", secret{"test.widgets.v1.Widget.Part", "token"}, "secret token: no field test.widgets.v1.Widget.Part.token"},
		{"a settable field", secret{"test.widgets.v1.Widget", "name"}, "secret name: the field isn't an output-only string"},
		{"a package the API hasn't yet", secret{"test.gizmos.v1.Gizmo", "token"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := secrets
			secrets = []secret{tc.secret}
			t.Cleanup(func() { secrets = saved })
			_, failure := run(t, nil)
			if tc.want == "" && failure != "" || !strings.Contains(failure, tc.want) {
				t.Errorf("generation failed with %q, want %q", failure, tc.want)
			}
		})
	}
}

func TestUnsupportedFieldsFailGeneration(t *testing.T) {
	const optional = `options { [google.api.field_behavior]: OPTIONAL }`
	for _, tc := range []struct {
		name   string
		change func(*descriptorpb.FileDescriptorProto)
		want   string
	}{
		{
			name:   "repeated doubles",
			change: withField("Gadget", `name: "sizes" number: 9 label: LABEL_REPEATED type: TYPE_DOUBLE options { [google.api.field_behavior]: OUTPUT_ONLY }`),
			want:   "Gadget: field sizes: repeated double fields aren't supported yet",
		},
		{
			name: "a map of strings to numbers",
			change: withField("Widget", `name: "sizes" number: 9 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Widget.SizesEntry" `+optional,
				`name: "SizesEntry" options { map_entry: true }
				 field { name: "key" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING }
				 field { name: "value" number: 2 label: LABEL_OPTIONAL type: TYPE_INT32 }`),
			want: "Widget: field sizes: maps of strings to int32 aren't supported yet, only to strings and messages",
		},
		{
			name: "a map keyed by numbers",
			change: withField("Widget", `name: "sizes" number: 9 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Widget.SizesEntry" `+optional,
				`name: "SizesEntry" options { map_entry: true }
				 field { name: "key" number: 1 label: LABEL_OPTIONAL type: TYPE_INT32 }
				 field { name: "value" number: 2 label: LABEL_OPTIONAL type: TYPE_STRING }`),
			want: "Widget: field sizes: only maps keyed by strings are supported",
		},
		{
			name:   "an unordered field that isn't repeated",
			change: withField("Widget", `name: "owner" number: 9 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: OPTIONAL [google.api.field_behavior]: UNORDERED_LIST }`),
			want:   "Widget: field owner: UNORDERED_LIST on a field that isn't repeated",
		},
		{
			name: "an output-only field of a set's elements",
			change: withField("Widget", `name: "ports" number: 9 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Port" options { [google.api.field_behavior]: OPTIONAL [google.api.field_behavior]: UNORDERED_LIST }`,
				`name: "Port" field { name: "id" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: OUTPUT_ONLY } }`),
			want: "Widget: field ports: Port: field id: a field of an UNORDERED_LIST element can't be OUTPUT_ONLY",
		},
		{
			name: "an immutable field of a set's elements",
			change: withField("Widget", `name: "ports" number: 9 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Port" options { [google.api.field_behavior]: OPTIONAL [google.api.field_behavior]: UNORDERED_LIST }`,
				`name: "Port" field { name: "number" number: 1 label: LABEL_OPTIONAL type: TYPE_INT32 options { [google.api.field_behavior]: REQUIRED [google.api.field_behavior]: IMMUTABLE } }`),
			want: "Widget: field ports: Port: field number: a field of a repeated or map field's elements can't be IMMUTABLE: an element has no identity",
		},
		{
			name: "an immutable field in an immutable set's elements",
			change: withField("Widget", `name: "ports" number: 9 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Port" options { [google.api.field_behavior]: IMMUTABLE [google.api.field_behavior]: UNORDERED_LIST }`,
				`name: "Port" field { name: "number" number: 1 label: LABEL_OPTIONAL type: TYPE_INT32 options { [google.api.field_behavior]: IMMUTABLE } }`),
			want: "Widget: field ports: Port: field number: a field of a repeated or map field's elements can't be IMMUTABLE",
		},
		{
			name: "a field with presence in a list's elements",
			change: withField("Widget", `name: "windows" number: 9 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Window" `+optional,
				`name: "Window" field { name: "weight" number: 1 label: LABEL_OPTIONAL type: TYPE_INT32 oneof_index: 0 proto3_optional: true `+optional+` } oneof_decl { name: "_weight" }`),
			want: "Widget: field windows: Window: field weight: a field with explicit presence (proto3 optional) can't be in a list's elements",
		},
		{
			name: "an immutable field the API chooses in a list's elements",
			change: withField("Widget", `name: "rules" number: 9 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Rule" `+optional,
				`name: "Rule" field { name: "zone" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: IMMUTABLE } }`),
			want: "Widget: field rules: Rule: field zone: a field of a repeated or map field's elements can't be IMMUTABLE",
		},
		{
			name: "a required immutable field in a list's elements",
			change: withField("Widget", `name: "routes" number: 9 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Route" `+optional,
				`name: "Route" field { name: "dest" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: REQUIRED [google.api.field_behavior]: IMMUTABLE } }`),
			want: "Widget: field routes: Route: field dest: a field of a repeated or map field's elements can't be IMMUTABLE",
		},
		{
			name: "an immutable field in a map's values, a level down",
			change: withField("Widget", `name: "routes" number: 9 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Widget.RoutesEntry" `+optional,
				`name: "RoutesEntry" options { map_entry: true }
				 field { name: "key" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING }
				 field { name: "value" number: 2 label: LABEL_OPTIONAL type: TYPE_MESSAGE type_name: ".test.widgets.v1.Route" }`,
				`name: "Route" field { name: "via" number: 1 label: LABEL_OPTIONAL type: TYPE_MESSAGE type_name: ".test.widgets.v1.Hop" `+optional+` }`,
				`name: "Hop" field { name: "metric" number: 1 label: LABEL_OPTIONAL type: TYPE_INT32 options { [google.api.field_behavior]: IMMUTABLE } }`),
			want: "Widget: field routes: Route: field via: Hop: field metric: a field of a repeated or map field's elements can't be IMMUTABLE",
		},
		{
			name: "a nested message's identifier",
			change: withField("Widget", `name: "spec" number: 9 label: LABEL_OPTIONAL type: TYPE_MESSAGE type_name: ".test.widgets.v1.Spec" `+optional,
				`name: "Spec" field { name: "name" number: 1 label: LABEL_OPTIONAL type: TYPE_STRING options { [google.api.field_behavior]: IDENTIFIER } }`),
			want: "Widget: field spec: Spec: field name: a nested message's field can't be an IDENTIFIER",
		},
		{
			name:   "a google.protobuf message other than Timestamp and Duration",
			change: withField("Widget", `name: "mask" number: 9 label: LABEL_OPTIONAL type: TYPE_MESSAGE type_name: ".google.protobuf.FieldMask" `+optional),
			want:   "Widget: field mask: google.protobuf.FieldMask fields aren't supported yet, of google.protobuf only Timestamp and Duration",
		},
		{
			name:   "another package's message",
			change: withField("Widget", `name: "op" number: 9 label: LABEL_OPTIONAL type: TYPE_MESSAGE type_name: ".google.longrunning.Operation" `+optional),
			want:   "Widget: field op: google.longrunning.Operation is another package's message, which isn't supported yet",
		},
		{
			name: "repeated timestamps",
			change: func(file *descriptorpb.FileDescriptorProto) {
				file.Dependency = append(file.Dependency, "google/protobuf/timestamp.proto")
				withField("Widget", `name: "times" number: 9 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".google.protobuf.Timestamp" `+optional)(file)
			},
			want: "Widget: field times: repeated google.protobuf.Timestamp fields aren't supported yet",
		},
		{
			name: "a message holding itself",
			change: withField("Widget", `name: "tree" number: 9 label: LABEL_OPTIONAL type: TYPE_MESSAGE type_name: ".test.widgets.v1.Tree" `+optional,
				`name: "Tree" field { name: "children" number: 1 label: LABEL_REPEATED type: TYPE_MESSAGE type_name: ".test.widgets.v1.Tree" options { [google.api.field_behavior]: OPTIONAL } }`),
			want: "Widget: field tree: Tree: field children: test.widgets.v1.Tree holds itself, which isn't supported",
		},
		{
			name: "a list request's required repeated field",
			change: func(file *descriptorpb.FileDescriptorProto) {
				m := message(file, "ListWidgetsRequest")
				m.Field = append(m.Field, &descriptorpb.FieldDescriptorProto{
					Name: proto.String("owners"), Number: proto.Int32(4),
					Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					Options: behaviors(annotations.FieldBehavior_REQUIRED),
				})
			},
			want: "Widget: ListWidgets's request field owners is required, and only a required string, number, boolean or enum is supported",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, failure := run(t, tc.change)
			if !strings.Contains(failure, tc.want) {
				t.Errorf("generation failed with %q, want %q", failure, tc.want)
			}
		})
	}
}
