package main

import (
	"go/format"
	"strings"
	"testing"

	_ "cloud.google.com/go/longrunning/autogen/longrunningpb"
	_ "google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	_ "google.golang.org/protobuf/types/known/emptypb"
	_ "google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// widgets is an API of the shape the generator reads: a resource, Widget,
// that can be created, updated, deleted and listed, and a data source,
// Gadget. Both have repeated string fields.
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
  field { name: "labels" number: 2 label: LABEL_REPEATED type: TYPE_STRING options { [google.api.field_behavior]: OPTIONAL } }
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

	gen, err := protogen.Options{}.New(req)
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
	widget := files["internal/services/widgetsv1/widget.gen.go"]
	// Settable: optional, sent in creates and updates, null while left out
	// and the API answers with none.
	contains(t, "widget.gen.go", widget,
		"Labels types.List `tfsdk:\"labels\"`",
		`"labels": schema.ListAttribute{ MarkdownDescription: "", Optional: true, ElementType: types.StringType, },`,
		"Labels: framework.StringListToProto(m.Labels),",
		"m.Labels = framework.Optional(m.Labels, framework.StringList(p.GetLabels()), len(p.GetLabels()) == 0)",
		`if !plan.Labels.Equal(state.Labels) { mask = append(mask, "labels") }`,
	)
	// Output only: computed, never sent, empty rather than null.
	contains(t, "widget.gen.go", widget,
		`"aliases": schema.ListAttribute{ MarkdownDescription: "", Computed: true, ElementType: types.StringType, },`,
		"m.Aliases = framework.StringList(p.GetAliases())",
	)
	if strings.Contains(widget, "Aliases: framework.StringListToProto") {
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
		"type widgetItemModel struct { Name types.String `tfsdk:\"name\"` Labels types.List `tfsdk:\"labels\"` Aliases types.List `tfsdk:\"aliases\"` }",
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

func TestUnsupportedFieldsFailGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*descriptorpb.FileDescriptorProto)
		want   string
	}{
		{
			name: "repeated integers",
			change: func(file *descriptorpb.FileDescriptorProto) {
				m := message(file, "Gadget")
				m.Field = append(m.Field, &descriptorpb.FieldDescriptorProto{
					Name: proto.String("sizes"), Number: proto.Int32(3),
					Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
				})
			},
			want: "Gadget: field sizes: repeated int32 fields aren't supported yet, only repeated strings",
		},
		{
			name: "repeated messages",
			change: func(file *descriptorpb.FileDescriptorProto) {
				m := message(file, "Gadget")
				m.Field = append(m.Field, &descriptorpb.FieldDescriptorProto{
					Name: proto.String("names"), Number: proto.Int32(3),
					Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
					TypeName: proto.String(".test.widgets.v1.NameRequest"),
				})
			},
			want: "Gadget: field names: repeated message fields aren't supported yet, only repeated strings",
		},
		{
			name: "a list request's filter",
			change: func(file *descriptorpb.FileDescriptorProto) {
				m := message(file, "ListWidgetsRequest")
				m.Field = append(m.Field, &descriptorpb.FieldDescriptorProto{
					Name: proto.String("filter"), Number: proto.Int32(4),
					Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				})
			},
			want: "Widget: ListWidgets's request field filter isn't supported yet",
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
