package webrtc_w3c

//go:generate protoc --proto_path=$PWD:$PWD/../../.. --go_out=. --go_opt=Mpb/webrtc.proto=./pb pb/webrtc.proto
