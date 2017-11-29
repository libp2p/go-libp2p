package main

import (
	p2p "github.com/avive/go-libp2p/examples/multipro/pb"
	"github.com/gogo/protobuf/proto"
	host "gx/ipfs/QmRS46AyqtpJBsf1zmQdeizSDEzo1qkWR7rdEuPFAv8237/go-libp2p-host"
	peer "gx/ipfs/QmXYjuNuxVzXKJCfWasQk1RqkhVLDM9jtUKhqc2WPQmFSB/go-libp2p-peer"

	crypto "gx/ipfs/QmaPbCnUMBohSGo3KnxEa2bHqyJVVeEEcwtqJAYxerieBo/go-libp2p-crypto"
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

func (n *Node) authenticateMessage(message proto.Message, data *p2p.MessageData) bool {

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

	return n.verifyData(bin, []byte(sign), peerId, data.NodePubKey)
}

func (n *Node) signProtoMessage(message proto.Message) ([]byte, error) {
	data, err := proto.Marshal(message)
	if err != nil {
		return nil, err
	}
	return n.signData(data)
}

func (n *Node) signData(data []byte) ([]byte, error) {
	key := n.Peerstore().PrivKey(n.ID())
	res, err := key.Sign(data)
	return res, err
}

// precondition: we have info about the signer peer in the local peer store
func (n *Node) verifyData(data []byte, signature []byte, peerId peer.ID, pubKeyData []byte) bool {

	key, err := crypto.UnmarshalPublicKey(pubKeyData)

	if err != nil {
		log.Println(err, "Failed to extract key from message key data")
		return false
	}

	// extract node id from the provided public key
	idFromKey, err := peer.IDFromPublicKey(key)

	if err != nil {
		log.Println(err, "Failed to extract peer id from public key")
		return false
	}

	// verify that message author node id matches the provided public key
	if idFromKey != peerId {
		log.Println(err, "Node id and provided public key mismatch")
		return false
	}

	res, err := key.Verify(data, signature)

	if err != nil {
		log.Println(err, "Error authenticating data")
		return false
	}

	return res
}
