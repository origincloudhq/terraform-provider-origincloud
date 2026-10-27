package framework

import (
	"context"
	"fmt"
	"math"
	"strings"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	longrunningv1beta1 "go.origincloud.eu/sdk/longrunning/v1beta1"
)

// NameValues returns the values a resource name gives the variables of
// pattern, in order: projects/p/zones/z/volumes/v has p, z and v for
// projects/{project}/zones/{zone}/volumes/{volume}.
func NameValues(pattern, name string) ([]string, error) {
	want := strings.Split(pattern, "/")
	got := strings.Split(name, "/")
	if len(got) != len(want) {
		return nil, fmt.Errorf("%q isn't a name of the form %s", name, pattern)
	}
	var values []string
	for i, segment := range want {
		switch {
		case strings.HasPrefix(segment, "{"):
			if got[i] == "" {
				return nil, fmt.Errorf("%q isn't a name of the form %s", name, pattern)
			}
			values = append(values, got[i])
		case got[i] != segment:
			return nil, fmt.Errorf("%q isn't a name of the form %s", name, pattern)
		}
	}
	return values, nil
}

// Optional returns the state of an optional attribute the API answered
// with value, zero when the API answered with its zero value: an
// attribute left out stays null, rather than becoming "" or {}.
func Optional[T attr.Value](prior, value T, zero bool) T {
	if zero && prior.IsNull() {
		return prior
	}
	return value
}

// StringMap returns a map field as a map attribute.
func StringMap(m map[string]string) types.Map {
	elements := make(map[string]attr.Value, len(m))
	for k, v := range m {
		elements[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, elements)
}

// StringMapToProto returns a map attribute as a map field.
func StringMapToProto(m types.Map) map[string]string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := make(map[string]string, len(m.Elements()))
	for k, v := range m.Elements() {
		out[k] = v.(types.String).ValueString()
	}
	return out
}

// OperationTarget returns the resource an operation acts on, from its
// metadata; empty when it has none.
func OperationTarget(op *longrunningpb.Operation) string {
	var metadata longrunningv1beta1.OperationMetadata
	if op.GetMetadata() == nil || op.GetMetadata().UnmarshalTo(&metadata) != nil {
		return ""
	}
	return metadata.GetTarget()
}

// Int32 validates that a number attribute fits the int32 field it is sent
// as.
func Int32() validator.Int64 { return int32Validator{} }

type int32Validator struct{}

func (int32Validator) Description(context.Context) string {
	return fmt.Sprintf("value must be between %d and %d", math.MinInt32, math.MaxInt32)
}

func (v int32Validator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v int32Validator) ValidateInt64(ctx context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := req.ConfigValue.ValueInt64(); n < math.MinInt32 || n > math.MaxInt32 {
		resp.Diagnostics.AddAttributeError(req.Path, "Value out of range", fmt.Sprintf("%s, got %d", v.Description(ctx), n))
	}
}
