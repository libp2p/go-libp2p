package main

import (
	host "gx/ipfs/QmRS46AyqtpJBsf1zmQdeizSDEzo1qkWR7rdEuPFAv8237/go-libp2p-host"
	peer "gx/ipfs/QmXYjuNuxVzXKJCfWasQk1RqkhVLDM9jtUKhqc2WPQmFSB/go-libp2p-peer"
)

func (n Node) signData(data []byte) ([]byte, error) {
	key := n.Peerstore().PrivKey(n.ID())
	res, err := key.Sign(data)
	return res, err
}

func (n Node) verifyData(data []byte, signature []byte, signerHostId peer.ID) bool {
	key := n.Peerstore().PubKey(signerHostId)
	res, err := key.Verify(data, signature)
	return res == true && err == nil
}

// Node type - implements one or more p2p protocols
type Node struct {
	host.Host     // lib-p2p host
	*PingProtocol // ping protocol impl
	*EchoProtocol // echo protocol impl
}

// create a new node with its implemented protocols
func NewNode(host host.Host, done chan bool) *Node {
	node := &Node{Host: host}
	node.PingProtocol = NewPingProtocol(node, done)
	node.EchoProtocol = NewEchoProtocol(node, done)
	return node
}
