package framework

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Timestamps and durations are strings in their JSON form, which the API
// itself uses: RFC 3339 for a google.protobuf.Timestamp, such as
// 2026-11-01T12:00:00Z, and seconds for a google.protobuf.Duration, such as
// 90s or 1.5s. An unset field is null. A value the configuration wrote
// differently, such as with another time zone, is kept while it is the
// same instant or length.

// Timestamp returns a timestamp field as a string attribute.
func Timestamp(p *timestamppb.Timestamp, prior types.String) types.String {
	return wellKnown(p, prior, TimestampToProto)
}

// Duration returns a duration field as a string attribute.
func Duration(p *durationpb.Duration, prior types.String) types.String {
	return wellKnown(p, prior, DurationToProto)
}

func wellKnown[P interface {
	proto.Message
	comparable
}](p P, prior types.String, toProto func(types.String) P) types.String {
	var nilP P
	if p == nilP {
		return types.StringNull()
	}
	if same := toProto(prior); same != nilP && proto.Equal(same, p) {
		return prior
	}
	encoded, err := protojson.Marshal(p)
	if err != nil {
		return types.StringNull() // an invalid message from the API
	}
	s, _ := strconv.Unquote(string(encoded))
	return types.StringValue(s)
}

// TimestampToProto returns a string attribute as a timestamp field; nil
// when it is null, unknown or invalid.
func TimestampToProto(v types.String) *timestamppb.Timestamp {
	return parseWellKnown(v, &timestamppb.Timestamp{})
}

// DurationToProto returns a string attribute as a duration field; nil
// when it is null, unknown or invalid.
func DurationToProto(v types.String) *durationpb.Duration {
	return parseWellKnown(v, &durationpb.Duration{})
}

func parseWellKnown[P proto.Message](v types.String, p P) P {
	var nilP P
	if v.IsNull() || v.IsUnknown() {
		return nilP
	}
	if err := protojson.Unmarshal([]byte(strconv.Quote(v.ValueString())), p); err != nil {
		return nilP
	}
	return p
}

// TimestampFormat validates that a string attribute is an RFC 3339
// timestamp, as a timestamp field takes it.
func TimestampFormat() validator.String {
	return formatValidator{"an RFC 3339 timestamp, such as 2026-11-01T12:00:00Z", func(v types.String) bool { return TimestampToProto(v) != nil }}
}

// DurationFormat validates that a string attribute is a duration in
// seconds, as a duration field takes it.
func DurationFormat() validator.String {
	return formatValidator{"a duration in seconds, such as 90s or 1.5s", func(v types.String) bool { return DurationToProto(v) != nil }}
}

type formatValidator struct {
	description string
	valid       func(types.String) bool
}

func (v formatValidator) Description(context.Context) string {
	return "value must be " + v.description
}

func (v formatValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v formatValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || v.valid(req.ConfigValue) {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", fmt.Sprintf("%s, got %q", v.Description(ctx), req.ConfigValue.ValueString()))
}

// Fields with explicit presence (proto3 optional) are pointers in the SDK:
// unset, nil, is null.

func PresentString(p *string) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(*p)
}

func PresentInt32(p *int32) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*p))
}

func PresentInt64(p *int64) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*p)
}

func PresentBool(p *bool) types.Bool {
	if p == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*p)
}

// An enum is an SDK enum type.
type enum interface {
	~int32
	String() string
}

func PresentEnum[E enum](p *E) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue((*p).String())
}

func StringPtr(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return proto.String(v.ValueString())
}

func Int32Ptr(v types.Int64) *int32 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return proto.Int32(int32(v.ValueInt64()))
}

func Int64Ptr(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return proto.Int64(v.ValueInt64())
}

func BoolPtr(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return proto.Bool(v.ValueBool())
}

// EnumPtr returns a string attribute as an enum field with explicit
// presence, the enum's values by name being values.
func EnumPtr[E enum](v types.String, values map[string]int32) *E {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	e := E(values[v.ValueString()])
	return &e
}

// Kept returns the value of an output-only field the API answers with only
// once, such as a generated private key in the create's response: the
// prior value while the API answers with none.
func Kept(prior types.String, value string) types.String {
	switch {
	case value != "":
		return types.StringValue(value)
	case prior.IsUnknown():
		return types.StringNull()
	default:
		return prior
	}
}

// IsSet says that an attribute is set: neither null nor unknown.
func IsSet(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }

// Is says that v, a oneof's value, holds the member whose wrapper type is
// W.
func Is[W any](v any) bool {
	_, ok := v.(W)
	return ok
}

// When returns a if cond, else b: a oneof member's value when it is the one
// the oneof holds, null otherwise.
func When[T attr.Value](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}
