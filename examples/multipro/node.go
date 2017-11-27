package main

import (
	"bufio"
	"log"
	"time"

	host "gx/ipfs/QmRS46AyqtpJBsf1zmQdeizSDEzo1qkWR7rdEuPFAv8237/go-libp2p-host"
	inet "gx/ipfs/QmbD5yKbXahNvoMqzeuNyKQA9vAs9fUvJg2GXeWU1fVqY5/go-libp2p-net"

	protobufCodec "github.com/multiformats/go-multicodec/protobuf"

	p2p "github.com/libp2p/go-libp2p/examples/multipro/pb"
)

// node version
const clientVersion = "go-p2p-node/0.0.1"

// helper method - writes a protobuf go data object to a network stream
// data - reference of protobuf go data object to send (not the object itself)
// s - network stream to write the data to
func sendDataObject(data interface{}, s inet.Stream) bool {
	writer := bufio.NewWriter(s)
	enc := protobufCodec.Multicodec(nil).Encoder(writer)
	err := enc.Encode(data)
	if err != nil {
		log.Fatal(err)
		return false
	}
	writer.Flush()
	return true
}

// helper method - generate message data shared between all node's p2p protocols
// nodeId - message author id
// messageId - unique for requests, copied from request for responses
func NewMessageData(nodeId string, messageId string, gossip bool) *p2p.MessageData {
	return &p2p.MessageData{ClientVersion: clientVersion,
		NodeId:    nodeId,
		Timestamp: time.Now().Unix(),
		Id:        messageId,
		Gossip:    gossip}
}

// Node type - implements one or more p2p protocols
type Node struct {
	host         host.Host     // lib-p2p host
	pingProtocol *PingProtocol // ping protocol impl
	echoProtocol *EchoProtocol // echp protocl imp
}

// create a new node with its implemented protocols
func NewNode(host host.Host, done chan bool) *Node {
	return &Node{host: host,
		pingProtocol: NewPingProtocol(host, done),
		echoProtocol: NewEchoProtocol(host, done)}
}
