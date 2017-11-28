package main

import (
	p2p "github.com/avive/go-libp2p/examples/multipro/pb"
	"github.com/gogo/protobuf/proto"
	host "gx/ipfs/QmRS46AyqtpJBsf1zmQdeizSDEzo1qkWR7rdEuPFAv8237/go-libp2p-host"
	peer "gx/ipfs/QmXYjuNuxVzXKJCfWasQk1RqkhVLDM9jtUKhqc2WPQmFSB/go-libp2p-peer"
	"log"
)

// Node type - a p2p host implementing one or more p2p protocols
type Node struct {
	host.Host     // lib-p2p host
	*PingProtocol // ping protocol impl
	*EchoProtocol // echo protocol impl
	// add other protocols here...
}

// create a new node with its implemented protocols
func NewNode(host host.Host, done chan bool) *Node {
	node := &Node{Host: host}
	node.PingProtocol = NewPingProtocol(node, done)
	node.EchoProtocol = NewEchoProtocol(node, done)
	return node
}

func (n Node) authenticateMessage(message proto.Message, data *p2p.MessageData) bool {

	// store a temp ref to sig and remove it from data
	sign := data.Sign
	data.Sign = ""

	// marshall data without the sig to binary format
	bin, err := proto.Marshal(message)
	if err != nil {
		log.Println(err, "failed to marshal pb message")
		return false
	}

	// restore sig in message data (for possible future use)
	data.Sign = sign

	peerId, err := peer.IDB58Decode(data.NodeId)
	if err != nil {
		log.Println(err, "Failed to decode node id from base58")
		return false
	}

	return n.verifyData(bin, []byte(sign), peerId, []byte(data.NodePubKey))
}

func (n Node) signProtoMessage(message proto.Message) ([]byte, error) {
	data, err := proto.Marshal(message)
	if err != nil {
		return nil, err
	}
	return n.signData(data)
}

func (n Node) signData(data []byte) ([]byte, error) {
	key := n.Peerstore().PrivKey(n.ID())
	res, err := key.Sign(data)
	return res, err
}

// precondition: we have info about the signer peer in the local peer store
func (n Node) verifyData(data []byte, signature []byte, peerId peer.ID, pubKeyData []byte) bool {

	// todo: restore pub key from message and use it
	key := n.Peerstore().PubKey(peerId)

	//todo: fix this
	//key, err := key.UnmarshalPublicKey(pubKeyData)

	if key == nil {
		log.Println("Failed to find public key for %s in local peer store.", peerId.String())
		return false
	}

	res, err := key.Verify(data, signature)
	if err != nil {
		log.Println("Error authenticating data")
		return false
	}

	return res
}
