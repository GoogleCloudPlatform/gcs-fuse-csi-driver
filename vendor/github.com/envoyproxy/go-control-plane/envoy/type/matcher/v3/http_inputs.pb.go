// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.30.0
// 	protoc        v5.29.3
// source: envoy/type/matcher/v3/http_inputs.proto

package matcherv3

import (
	_ "github.com/cncf/xds/go/udpa/annotations"
	_ "github.com/envoyproxy/protoc-gen-validate/validate"
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// Match input indicates that matching should be done on a specific request header.
// The resulting input string will be all headers for the given key joined by a comma,
// e.g. if the request contains two 'foo' headers with value 'bar' and 'baz', the input
// string will be 'bar,baz'.
// [#comment:TODO(snowp): Link to unified matching docs.]
// [#extension: envoy.matching.inputs.request_headers]
type HttpRequestHeaderMatchInput struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// The request header to match on.
	HeaderName string `protobuf:"bytes,1,opt,name=header_name,json=headerName,proto3" json:"header_name,omitempty"`
}

func (x *HttpRequestHeaderMatchInput) Reset() {
	*x = HttpRequestHeaderMatchInput{}
	if protoimpl.UnsafeEnabled {
		mi := &file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *HttpRequestHeaderMatchInput) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*HttpRequestHeaderMatchInput) ProtoMessage() {}

func (x *HttpRequestHeaderMatchInput) ProtoReflect() protoreflect.Message {
	mi := &file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use HttpRequestHeaderMatchInput.ProtoReflect.Descriptor instead.
func (*HttpRequestHeaderMatchInput) Descriptor() ([]byte, []int) {
	return file_envoy_type_matcher_v3_http_inputs_proto_rawDescGZIP(), []int{0}
}

func (x *HttpRequestHeaderMatchInput) GetHeaderName() string {
	if x != nil {
		return x.HeaderName
	}
	return ""
}

// Match input indicates that matching should be done on a specific request trailer.
// The resulting input string will be all headers for the given key joined by a comma,
// e.g. if the request contains two 'foo' headers with value 'bar' and 'baz', the input
// string will be 'bar,baz'.
// [#comment:TODO(snowp): Link to unified matching docs.]
// [#extension: envoy.matching.inputs.request_trailers]
type HttpRequestTrailerMatchInput struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// The request trailer to match on.
	HeaderName string `protobuf:"bytes,1,opt,name=header_name,json=headerName,proto3" json:"header_name,omitempty"`
}

func (x *HttpRequestTrailerMatchInput) Reset() {
	*x = HttpRequestTrailerMatchInput{}
	if protoimpl.UnsafeEnabled {
		mi := &file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *HttpRequestTrailerMatchInput) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*HttpRequestTrailerMatchInput) ProtoMessage() {}

func (x *HttpRequestTrailerMatchInput) ProtoReflect() protoreflect.Message {
	mi := &file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use HttpRequestTrailerMatchInput.ProtoReflect.Descriptor instead.
func (*HttpRequestTrailerMatchInput) Descriptor() ([]byte, []int) {
	return file_envoy_type_matcher_v3_http_inputs_proto_rawDescGZIP(), []int{1}
}

func (x *HttpRequestTrailerMatchInput) GetHeaderName() string {
	if x != nil {
		return x.HeaderName
	}
	return ""
}

// Match input indicating that matching should be done on a specific response header.
// The resulting input string will be all headers for the given key joined by a comma,
// e.g. if the response contains two 'foo' headers with value 'bar' and 'baz', the input
// string will be 'bar,baz'.
// [#comment:TODO(snowp): Link to unified matching docs.]
// [#extension: envoy.matching.inputs.response_headers]
type HttpResponseHeaderMatchInput struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// The response header to match on.
	HeaderName string `protobuf:"bytes,1,opt,name=header_name,json=headerName,proto3" json:"header_name,omitempty"`
}

func (x *HttpResponseHeaderMatchInput) Reset() {
	*x = HttpResponseHeaderMatchInput{}
	if protoimpl.UnsafeEnabled {
		mi := &file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *HttpResponseHeaderMatchInput) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*HttpResponseHeaderMatchInput) ProtoMessage() {}

func (x *HttpResponseHeaderMatchInput) ProtoReflect() protoreflect.Message {
	mi := &file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use HttpResponseHeaderMatchInput.ProtoReflect.Descriptor instead.
func (*HttpResponseHeaderMatchInput) Descriptor() ([]byte, []int) {
	return file_envoy_type_matcher_v3_http_inputs_proto_rawDescGZIP(), []int{2}
}

func (x *HttpResponseHeaderMatchInput) GetHeaderName() string {
	if x != nil {
		return x.HeaderName
	}
	return ""
}

// Match input indicates that matching should be done on a specific response trailer.
// The resulting input string will be all headers for the given key joined by a comma,
// e.g. if the request contains two 'foo' headers with value 'bar' and 'baz', the input
// string will be 'bar,baz'.
// [#comment:TODO(snowp): Link to unified matching docs.]
// [#extension: envoy.matching.inputs.response_trailers]
type HttpResponseTrailerMatchInput struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// The response trailer to match on.
	HeaderName string `protobuf:"bytes,1,opt,name=header_name,json=headerName,proto3" json:"header_name,omitempty"`
}

func (x *HttpResponseTrailerMatchInput) Reset() {
	*x = HttpResponseTrailerMatchInput{}
	if protoimpl.UnsafeEnabled {
		mi := &file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *HttpResponseTrailerMatchInput) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*HttpResponseTrailerMatchInput) ProtoMessage() {}

func (x *HttpResponseTrailerMatchInput) ProtoReflect() protoreflect.Message {
	mi := &file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use HttpResponseTrailerMatchInput.ProtoReflect.Descriptor instead.
func (*HttpResponseTrailerMatchInput) Descriptor() ([]byte, []int) {
	return file_envoy_type_matcher_v3_http_inputs_proto_rawDescGZIP(), []int{3}
}

func (x *HttpResponseTrailerMatchInput) GetHeaderName() string {
	if x != nil {
		return x.HeaderName
	}
	return ""
}

// Match input indicates that matching should be done on a specific query parameter.
// The resulting input string will be the first query parameter for the value
// 'query_param'.
// [#extension: envoy.matching.inputs.query_params]
type HttpRequestQueryParamMatchInput struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// The query parameter to match on.
	QueryParam string `protobuf:"bytes,1,opt,name=query_param,json=queryParam,proto3" json:"query_param,omitempty"`
}

func (x *HttpRequestQueryParamMatchInput) Reset() {
	*x = HttpRequestQueryParamMatchInput{}
	if protoimpl.UnsafeEnabled {
		mi := &file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[4]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *HttpRequestQueryParamMatchInput) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*HttpRequestQueryParamMatchInput) ProtoMessage() {}

func (x *HttpRequestQueryParamMatchInput) ProtoReflect() protoreflect.Message {
	mi := &file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[4]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use HttpRequestQueryParamMatchInput.ProtoReflect.Descriptor instead.
func (*HttpRequestQueryParamMatchInput) Descriptor() ([]byte, []int) {
	return file_envoy_type_matcher_v3_http_inputs_proto_rawDescGZIP(), []int{4}
}

func (x *HttpRequestQueryParamMatchInput) GetQueryParam() string {
	if x != nil {
		return x.QueryParam
	}
	return ""
}

var File_envoy_type_matcher_v3_http_inputs_proto protoreflect.FileDescriptor

var file_envoy_type_matcher_v3_http_inputs_proto_rawDesc = []byte{
	0x0a, 0x27, 0x65, 0x6e, 0x76, 0x6f, 0x79, 0x2f, 0x74, 0x79, 0x70, 0x65, 0x2f, 0x6d, 0x61, 0x74,
	0x63, 0x68, 0x65, 0x72, 0x2f, 0x76, 0x33, 0x2f, 0x68, 0x74, 0x74, 0x70, 0x5f, 0x69, 0x6e, 0x70,
	0x75, 0x74, 0x73, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x15, 0x65, 0x6e, 0x76, 0x6f, 0x79,
	0x2e, 0x74, 0x79, 0x70, 0x65, 0x2e, 0x6d, 0x61, 0x74, 0x63, 0x68, 0x65, 0x72, 0x2e, 0x76, 0x33,
	0x1a, 0x1d, 0x75, 0x64, 0x70, 0x61, 0x2f, 0x61, 0x6e, 0x6e, 0x6f, 0x74, 0x61, 0x74, 0x69, 0x6f,
	0x6e, 0x73, 0x2f, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a,
	0x17, 0x76, 0x61, 0x6c, 0x69, 0x64, 0x61, 0x74, 0x65, 0x2f, 0x76, 0x61, 0x6c, 0x69, 0x64, 0x61,
	0x74, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0x4b, 0x0a, 0x1b, 0x48, 0x74, 0x74, 0x70,
	0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x48, 0x65, 0x61, 0x64, 0x65, 0x72, 0x4d, 0x61, 0x74,
	0x63, 0x68, 0x49, 0x6e, 0x70, 0x75, 0x74, 0x12, 0x2c, 0x0a, 0x0b, 0x68, 0x65, 0x61, 0x64, 0x65,
	0x72, 0x5f, 0x6e, 0x61, 0x6d, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x42, 0x0b, 0xfa, 0x42,
	0x08, 0x72, 0x06, 0xc8, 0x01, 0x00, 0xc0, 0x01, 0x01, 0x52, 0x0a, 0x68, 0x65, 0x61, 0x64, 0x65,
	0x72, 0x4e, 0x61, 0x6d, 0x65, 0x22, 0x4c, 0x0a, 0x1c, 0x48, 0x74, 0x74, 0x70, 0x52, 0x65, 0x71,
	0x75, 0x65, 0x73, 0x74, 0x54, 0x72, 0x61, 0x69, 0x6c, 0x65, 0x72, 0x4d, 0x61, 0x74, 0x63, 0x68,
	0x49, 0x6e, 0x70, 0x75, 0x74, 0x12, 0x2c, 0x0a, 0x0b, 0x68, 0x65, 0x61, 0x64, 0x65, 0x72, 0x5f,
	0x6e, 0x61, 0x6d, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x42, 0x0b, 0xfa, 0x42, 0x08, 0x72,
	0x06, 0xc8, 0x01, 0x00, 0xc0, 0x01, 0x01, 0x52, 0x0a, 0x68, 0x65, 0x61, 0x64, 0x65, 0x72, 0x4e,
	0x61, 0x6d, 0x65, 0x22, 0x4c, 0x0a, 0x1c, 0x48, 0x74, 0x74, 0x70, 0x52, 0x65, 0x73, 0x70, 0x6f,
	0x6e, 0x73, 0x65, 0x48, 0x65, 0x61, 0x64, 0x65, 0x72, 0x4d, 0x61, 0x74, 0x63, 0x68, 0x49, 0x6e,
	0x70, 0x75, 0x74, 0x12, 0x2c, 0x0a, 0x0b, 0x68, 0x65, 0x61, 0x64, 0x65, 0x72, 0x5f, 0x6e, 0x61,
	0x6d, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x42, 0x0b, 0xfa, 0x42, 0x08, 0x72, 0x06, 0xc8,
	0x01, 0x00, 0xc0, 0x01, 0x01, 0x52, 0x0a, 0x68, 0x65, 0x61, 0x64, 0x65, 0x72, 0x4e, 0x61, 0x6d,
	0x65, 0x22, 0x4d, 0x0a, 0x1d, 0x48, 0x74, 0x74, 0x70, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73,
	0x65, 0x54, 0x72, 0x61, 0x69, 0x6c, 0x65, 0x72, 0x4d, 0x61, 0x74, 0x63, 0x68, 0x49, 0x6e, 0x70,
	0x75, 0x74, 0x12, 0x2c, 0x0a, 0x0b, 0x68, 0x65, 0x61, 0x64, 0x65, 0x72, 0x5f, 0x6e, 0x61, 0x6d,
	0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x42, 0x0b, 0xfa, 0x42, 0x08, 0x72, 0x06, 0xc8, 0x01,
	0x00, 0xc0, 0x01, 0x01, 0x52, 0x0a, 0x68, 0x65, 0x61, 0x64, 0x65, 0x72, 0x4e, 0x61, 0x6d, 0x65,
	0x22, 0x4b, 0x0a, 0x1f, 0x48, 0x74, 0x74, 0x70, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x51,
	0x75, 0x65, 0x72, 0x79, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x4d, 0x61, 0x74, 0x63, 0x68, 0x49, 0x6e,
	0x70, 0x75, 0x74, 0x12, 0x28, 0x0a, 0x0b, 0x71, 0x75, 0x65, 0x72, 0x79, 0x5f, 0x70, 0x61, 0x72,
	0x61, 0x6d, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x42, 0x07, 0xfa, 0x42, 0x04, 0x72, 0x02, 0x10,
	0x01, 0x52, 0x0a, 0x71, 0x75, 0x65, 0x72, 0x79, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x42, 0x88, 0x01,
	0xba, 0x80, 0xc8, 0xd1, 0x06, 0x02, 0x10, 0x02, 0x0a, 0x23, 0x69, 0x6f, 0x2e, 0x65, 0x6e, 0x76,
	0x6f, 0x79, 0x70, 0x72, 0x6f, 0x78, 0x79, 0x2e, 0x65, 0x6e, 0x76, 0x6f, 0x79, 0x2e, 0x74, 0x79,
	0x70, 0x65, 0x2e, 0x6d, 0x61, 0x74, 0x63, 0x68, 0x65, 0x72, 0x2e, 0x76, 0x33, 0x42, 0x0f, 0x48,
	0x74, 0x74, 0x70, 0x49, 0x6e, 0x70, 0x75, 0x74, 0x73, 0x50, 0x72, 0x6f, 0x74, 0x6f, 0x50, 0x01,
	0x5a, 0x46, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x65, 0x6e, 0x76,
	0x6f, 0x79, 0x70, 0x72, 0x6f, 0x78, 0x79, 0x2f, 0x67, 0x6f, 0x2d, 0x63, 0x6f, 0x6e, 0x74, 0x72,
	0x6f, 0x6c, 0x2d, 0x70, 0x6c, 0x61, 0x6e, 0x65, 0x2f, 0x65, 0x6e, 0x76, 0x6f, 0x79, 0x2f, 0x74,
	0x79, 0x70, 0x65, 0x2f, 0x6d, 0x61, 0x74, 0x63, 0x68, 0x65, 0x72, 0x2f, 0x76, 0x33, 0x3b, 0x6d,
	0x61, 0x74, 0x63, 0x68, 0x65, 0x72, 0x76, 0x33, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_envoy_type_matcher_v3_http_inputs_proto_rawDescOnce sync.Once
	file_envoy_type_matcher_v3_http_inputs_proto_rawDescData = file_envoy_type_matcher_v3_http_inputs_proto_rawDesc
)

func file_envoy_type_matcher_v3_http_inputs_proto_rawDescGZIP() []byte {
	file_envoy_type_matcher_v3_http_inputs_proto_rawDescOnce.Do(func() {
		file_envoy_type_matcher_v3_http_inputs_proto_rawDescData = protoimpl.X.CompressGZIP(file_envoy_type_matcher_v3_http_inputs_proto_rawDescData)
	})
	return file_envoy_type_matcher_v3_http_inputs_proto_rawDescData
}

var file_envoy_type_matcher_v3_http_inputs_proto_msgTypes = make([]protoimpl.MessageInfo, 5)
var file_envoy_type_matcher_v3_http_inputs_proto_goTypes = []interface{}{
	(*HttpRequestHeaderMatchInput)(nil),     // 0: envoy.type.matcher.v3.HttpRequestHeaderMatchInput
	(*HttpRequestTrailerMatchInput)(nil),    // 1: envoy.type.matcher.v3.HttpRequestTrailerMatchInput
	(*HttpResponseHeaderMatchInput)(nil),    // 2: envoy.type.matcher.v3.HttpResponseHeaderMatchInput
	(*HttpResponseTrailerMatchInput)(nil),   // 3: envoy.type.matcher.v3.HttpResponseTrailerMatchInput
	(*HttpRequestQueryParamMatchInput)(nil), // 4: envoy.type.matcher.v3.HttpRequestQueryParamMatchInput
}
var file_envoy_type_matcher_v3_http_inputs_proto_depIdxs = []int32{
	0, // [0:0] is the sub-list for method output_type
	0, // [0:0] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

func init() { file_envoy_type_matcher_v3_http_inputs_proto_init() }
func file_envoy_type_matcher_v3_http_inputs_proto_init() {
	if File_envoy_type_matcher_v3_http_inputs_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*HttpRequestHeaderMatchInput); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*HttpRequestTrailerMatchInput); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*HttpResponseHeaderMatchInput); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[3].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*HttpResponseTrailerMatchInput); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_envoy_type_matcher_v3_http_inputs_proto_msgTypes[4].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*HttpRequestQueryParamMatchInput); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_envoy_type_matcher_v3_http_inputs_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   5,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_envoy_type_matcher_v3_http_inputs_proto_goTypes,
		DependencyIndexes: file_envoy_type_matcher_v3_http_inputs_proto_depIdxs,
		MessageInfos:      file_envoy_type_matcher_v3_http_inputs_proto_msgTypes,
	}.Build()
	File_envoy_type_matcher_v3_http_inputs_proto = out.File
	file_envoy_type_matcher_v3_http_inputs_proto_rawDesc = nil
	file_envoy_type_matcher_v3_http_inputs_proto_goTypes = nil
	file_envoy_type_matcher_v3_http_inputs_proto_depIdxs = nil
}
