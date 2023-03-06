package webrtc_w3c

//go:generate protoc --proto_path=$PWD:$PWD/../../.. --go_out=. --go_opt=Mpb/message.proto=./pb pb/message.proto
