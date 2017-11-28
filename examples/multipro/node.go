package main

import (
	host "gx/ipfs/QmRS46AyqtpJBsf1zmQdeizSDEzo1qkWR7rdEuPFAv8237/go-libp2p-host"
	peer "gx/ipfs/QmXYjuNuxVzXKJCfWasQk1RqkhVLDM9jtUKhqc2WPQmFSB/go-libp2p-peer"
	"github.com/gogo/protobuf/proto"

	p2p "github.com/libp2p/go-libp2p/examples/multipro/pb"
	"log"
)

func (n Node) authenticateMessage(message proto.Message, data *p2p.MessageData) bool {

	// store a temp ref to sig and remove it from data
	sign := data.Sign
	data.Sign = ""

	//log.Print("Signature: %s", []byte(sign))

	// marshall data without the sig to binary format
	bin, err := proto.Marshal(message)
	if err != nil {
		// todo: log
		return false
	}

	// restore sig in message data (for possible future use)
	data.Sign = sign

	peerId, err := peer.IDB58Decode(data.NodeId)

	if err != nil {
		log.Fatal(err, "Failed to decode node id")
		return false
	}

	return n.verifyData(bin, []byte(sign), peerId)
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
func (n Node) verifyData(data []byte, signature []byte, peerId peer.ID) bool {
	key := n.Peerstore().PubKey(peerId)
	//log.Print ("%s %s %s", peerId, key, peerId.String())
	res, err := key.Verify(data, signature)
	return res == true && err == nil
}

// Node type - a host with one or more implemented p2p protocols
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
