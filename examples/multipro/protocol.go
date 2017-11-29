package main

import (
	"bufio"
	p2p "github.com/avive/go-libp2p/examples/multipro/pb"
	"github.com/gogo/protobuf/proto"
	protobufCodec "github.com/multiformats/go-multicodec/protobuf"
	"gx/ipfs/QmXYjuNuxVzXKJCfWasQk1RqkhVLDM9jtUKhqc2WPQmFSB/go-libp2p-peer"
	inet "gx/ipfs/QmbD5yKbXahNvoMqzeuNyKQA9vAs9fUvJg2GXeWU1fVqY5/go-libp2p-net"
	"log"
	"time"
)

// node version
const clientVersion = "go-p2p-node/0.0.1"

// helper method - writes a protobuf go data object to a network stream
// data - reference of protobuf go data object to send (not the object itself)
// s - network stream to write the data to
func sendProtoMessage(data proto.Message, s inet.Stream) bool {
	writer := bufio.NewWriter(s)
	enc := protobufCodec.Multicodec(nil).Encoder(writer)
	err := enc.Encode(data)
	if err != nil {
		log.Println(err)
		return false
	}
	writer.Flush()
	return true
}

// helper method - generate message data shared between all node's p2p protocols
// messageId - unique for requests, copied from request for responses
func NewMessageData(node *Node, messageId string, gossip bool) *p2p.MessageData {

	// Create protobufs bin data for a public key
	nodePubKey, err := node.Peerstore().PubKey(node.ID()).Bytes()

	if err != nil {
		panic("Failed to get public key for sender from local peer store.")
	}

	return &p2p.MessageData{ClientVersion: clientVersion,
		NodeId:     peer.IDB58Encode(node.ID()),
		NodePubKey: nodePubKey,
		Timestamp:  time.Now().Unix(),
		Id:         messageId,
		Gossip:     gossip}
}
