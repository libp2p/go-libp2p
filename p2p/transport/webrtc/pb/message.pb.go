// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.4
// 	protoc        v5.29.2
// source: p2p/transport/webrtc/pb/message.proto

package pb

import (
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"

	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type Message_Flag int32

const (
	// The sender will no longer send messages on the stream.
	Message_FIN Message_Flag = 0
	// The sender will no longer read messages on the stream. Incoming data is
	// being discarded on receipt.
	Message_STOP_SENDING Message_Flag = 1
	// The sender abruptly terminates the sending part of the stream. The
	// receiver can discard any data that it already received on that stream.
	Message_RESET Message_Flag = 2
	// Sending the FIN_ACK flag acknowledges the previous receipt of a message
	// with the FIN flag set. Receiving a FIN_ACK flag gives the recipient
	// confidence that the remote has received all sent messages.
	Message_FIN_ACK Message_Flag = 3
)

// Enum value maps for Message_Flag.
var (
	Message_Flag_name = map[int32]string{
		0: "FIN",
		1: "STOP_SENDING",
		2: "RESET",
		3: "FIN_ACK",
	}
	Message_Flag_value = map[string]int32{
		"FIN":          0,
		"STOP_SENDING": 1,
		"RESET":        2,
		"FIN_ACK":      3,
	}
)

func (x Message_Flag) Enum() *Message_Flag {
	p := new(Message_Flag)
	*p = x
	return p
}

func (x Message_Flag) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (Message_Flag) Descriptor() protoreflect.EnumDescriptor {
	return file_p2p_transport_webrtc_pb_message_proto_enumTypes[0].Descriptor()
}

func (Message_Flag) Type() protoreflect.EnumType {
	return &file_p2p_transport_webrtc_pb_message_proto_enumTypes[0]
}

func (x Message_Flag) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Do not use.
func (x *Message_Flag) UnmarshalJSON(b []byte) error {
	num, err := protoimpl.X.UnmarshalJSONEnum(x.Descriptor(), b)
	if err != nil {
		return err
	}
	*x = Message_Flag(num)
	return nil
}

// Deprecated: Use Message_Flag.Descriptor instead.
func (Message_Flag) EnumDescriptor() ([]byte, []int) {
	return file_p2p_transport_webrtc_pb_message_proto_rawDescGZIP(), []int{0, 0}
}

type Message struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Flag          *Message_Flag          `protobuf:"varint,1,opt,name=flag,enum=Message_Flag" json:"flag,omitempty"`
	Message       []byte                 `protobuf:"bytes,2,opt,name=message" json:"message,omitempty"`
	ErrorCode     *uint32                `protobuf:"varint,3,opt,name=errorCode" json:"errorCode,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Message) Reset() {
	*x = Message{}
	mi := &file_p2p_transport_webrtc_pb_message_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Message) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Message) ProtoMessage() {}

func (x *Message) ProtoReflect() protoreflect.Message {
	mi := &file_p2p_transport_webrtc_pb_message_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Message.ProtoReflect.Descriptor instead.
func (*Message) Descriptor() ([]byte, []int) {
	return file_p2p_transport_webrtc_pb_message_proto_rawDescGZIP(), []int{0}
}

func (x *Message) GetFlag() Message_Flag {
	if x != nil && x.Flag != nil {
		return *x.Flag
	}
	return Message_FIN
}

func (x *Message) GetMessage() []byte {
	if x != nil {
		return x.Message
	}
	return nil
}

func (x *Message) GetErrorCode() uint32 {
	if x != nil && x.ErrorCode != nil {
		return *x.ErrorCode
	}
	return 0
}

var File_p2p_transport_webrtc_pb_message_proto protoreflect.FileDescriptor

var file_p2p_transport_webrtc_pb_message_proto_rawDesc = string([]byte{
	0x0a, 0x25, 0x70, 0x32, 0x70, 0x2f, 0x74, 0x72, 0x61, 0x6e, 0x73, 0x70, 0x6f, 0x72, 0x74, 0x2f,
	0x77, 0x65, 0x62, 0x72, 0x74, 0x63, 0x2f, 0x70, 0x62, 0x2f, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67,
	0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0x9f, 0x01, 0x0a, 0x07, 0x4d, 0x65, 0x73, 0x73,
	0x61, 0x67, 0x65, 0x12, 0x21, 0x0a, 0x04, 0x66, 0x6c, 0x61, 0x67, 0x18, 0x01, 0x20, 0x01, 0x28,
	0x0e, 0x32, 0x0d, 0x2e, 0x4d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x2e, 0x46, 0x6c, 0x61, 0x67,
	0x52, 0x04, 0x66, 0x6c, 0x61, 0x67, 0x12, 0x18, 0x0a, 0x07, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67,
	0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x07, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65,
	0x12, 0x1c, 0x0a, 0x09, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x43, 0x6f, 0x64, 0x65, 0x18, 0x03, 0x20,
	0x01, 0x28, 0x0d, 0x52, 0x09, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x43, 0x6f, 0x64, 0x65, 0x22, 0x39,
	0x0a, 0x04, 0x46, 0x6c, 0x61, 0x67, 0x12, 0x07, 0x0a, 0x03, 0x46, 0x49, 0x4e, 0x10, 0x00, 0x12,
	0x10, 0x0a, 0x0c, 0x53, 0x54, 0x4f, 0x50, 0x5f, 0x53, 0x45, 0x4e, 0x44, 0x49, 0x4e, 0x47, 0x10,
	0x01, 0x12, 0x09, 0x0a, 0x05, 0x52, 0x45, 0x53, 0x45, 0x54, 0x10, 0x02, 0x12, 0x0b, 0x0a, 0x07,
	0x46, 0x49, 0x4e, 0x5f, 0x41, 0x43, 0x4b, 0x10, 0x03, 0x42, 0x35, 0x5a, 0x33, 0x67, 0x69, 0x74,
	0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x6c, 0x69, 0x62, 0x70, 0x32, 0x70, 0x2f, 0x67,
	0x6f, 0x2d, 0x6c, 0x69, 0x62, 0x70, 0x32, 0x70, 0x2f, 0x70, 0x32, 0x70, 0x2f, 0x74, 0x72, 0x61,
	0x6e, 0x73, 0x70, 0x6f, 0x72, 0x74, 0x2f, 0x77, 0x65, 0x62, 0x72, 0x74, 0x63, 0x2f, 0x70, 0x62,
})

var (
	file_p2p_transport_webrtc_pb_message_proto_rawDescOnce sync.Once
	file_p2p_transport_webrtc_pb_message_proto_rawDescData []byte
)

func file_p2p_transport_webrtc_pb_message_proto_rawDescGZIP() []byte {
	file_p2p_transport_webrtc_pb_message_proto_rawDescOnce.Do(func() {
		file_p2p_transport_webrtc_pb_message_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_p2p_transport_webrtc_pb_message_proto_rawDesc), len(file_p2p_transport_webrtc_pb_message_proto_rawDesc)))
	})
	return file_p2p_transport_webrtc_pb_message_proto_rawDescData
}

var file_p2p_transport_webrtc_pb_message_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_p2p_transport_webrtc_pb_message_proto_msgTypes = make([]protoimpl.MessageInfo, 1)
var file_p2p_transport_webrtc_pb_message_proto_goTypes = []any{
	(Message_Flag)(0), // 0: Message.Flag
	(*Message)(nil),   // 1: Message
}
var file_p2p_transport_webrtc_pb_message_proto_depIdxs = []int32{
	0, // 0: Message.flag:type_name -> Message.Flag
	1, // [1:1] is the sub-list for method output_type
	1, // [1:1] is the sub-list for method input_type
	1, // [1:1] is the sub-list for extension type_name
	1, // [1:1] is the sub-list for extension extendee
	0, // [0:1] is the sub-list for field type_name
}

func init() { file_p2p_transport_webrtc_pb_message_proto_init() }
func file_p2p_transport_webrtc_pb_message_proto_init() {
	if File_p2p_transport_webrtc_pb_message_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_p2p_transport_webrtc_pb_message_proto_rawDesc), len(file_p2p_transport_webrtc_pb_message_proto_rawDesc)),
			NumEnums:      1,
			NumMessages:   1,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_p2p_transport_webrtc_pb_message_proto_goTypes,
		DependencyIndexes: file_p2p_transport_webrtc_pb_message_proto_depIdxs,
		EnumInfos:         file_p2p_transport_webrtc_pb_message_proto_enumTypes,
		MessageInfos:      file_p2p_transport_webrtc_pb_message_proto_msgTypes,
	}.Build()
	File_p2p_transport_webrtc_pb_message_proto = out.File
	file_p2p_transport_webrtc_pb_message_proto_goTypes = nil
	file_p2p_transport_webrtc_pb_message_proto_depIdxs = nil
}
