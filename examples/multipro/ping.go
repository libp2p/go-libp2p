package main

import (
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"

	proto "github.com/gogo/protobuf/proto"
	uuid "github.com/google/uuid"
	p2p "github.com/libp2p/go-libp2p/examples/multipro/pb"
)

// pattern: /protocol-name/request-or-response-message/version
const pingRequest = "/ping/pingreq/0.0.1"
const pingResponse = "/ping/pingresp/0.0.1"

// PingProtocol type
type PingProtocol struct {
	node     *Node // local host
	mu       sync.Mutex
	requests map[string]*p2p.PingRequest // used to access request data from response handlers. Protected by mu
	done     chan bool                   // only for demo purposes to stop main from terminating
}

func NewPingProtocol(node *Node, done chan bool) *PingProtocol {
	p := &PingProtocol{node: node, requests: make(map[string]*p2p.PingRequest), done: done}
	node.SetStreamHandler(pingRequest, p.onPingRequest)
	node.SetStreamHandler(pingResponse, p.onPingResponse)
	return p
}

// remote peer requests handler
func (p *PingProtocol) onPingRequest(s network.Stream) {

	// get request data
	data := &p2p.PingRequest{}
	buf, err := io.ReadAll(s)
	if err != nil {
		s.Reset()
		log.Println(err)
		return
	}
	s.Close()

	// unmarshal it
	err = proto.Unmarshal(buf, data)
	if err != nil {
		log.Println(err)
		return
	}

	log.Printf("%s: Received ping request from %s. Message: %s", s.Conn().LocalPeer(), s.Conn().RemotePeer(), data.Message)

	valid := p.node.authenticateMessage(data, data.MessageData)

	if !valid {
		log.Println("Failed to authenticate message")
		return
	}

	// generate response message
	log.Printf("%s: Sending ping response to %s. Message id: %s...", s.Conn().LocalPeer(), s.Conn().RemotePeer(), data.MessageData.Id)

	resp := &p2p.PingResponse{MessageData: p.node.NewMessageData(data.MessageData.Id, false),
		Message: fmt.Sprintf("Ping response from %s", p.node.ID())}

	// sign the data
	signature, err := p.node.signProtoMessage(resp)
	if err != nil {
		log.Println("failed to sign response")
		return
	}

	// add the signature to the message
	resp.MessageData.Sign = signature

	// send the response
	ok := p.node.sendProtoMessage(s.Conn().RemotePeer(), pingResponse, resp)

	if ok {
		log.Printf("%s: Ping response to %s sent.", s.Conn().LocalPeer().String(), s.Conn().RemotePeer().String())
	}
	p.done <- true
}

// remote ping response handler
func (p *PingProtocol) onPingResponse(s network.Stream) {
	data := &p2p.PingResponse{}
	buf, err := io.ReadAll(s)
	if err != nil {
		s.Reset()
		log.Println(err)
		return
	}
	s.Close()

	// unmarshal it
	err = proto.Unmarshal(buf, data)
	if err != nil {
		log.Println(err)
		return
	}

	valid := p.node.authenticateMessage(data, data.MessageData)

	if !valid {
		log.Println("Failed to authenticate message")
		return
	}

	// locate request data and remove it if found
	p.mu.Lock()
	_, ok := p.requests[data.MessageData.Id]
	if ok {
		// remove request from map as we have processed it here
		delete(p.requests, data.MessageData.Id)
	} else {
		log.Println("Failed to locate request data boject for response")
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	log.Printf("%s: Received ping response from %s. Message id:%s. Message: %s.", s.Conn().LocalPeer(), s.Conn().RemotePeer(), data.MessageData.Id, data.Message)
	p.done <- true
}

func (p *PingProtocol) Ping(host host.Host) bool {
	log.Printf("%s: Sending ping to: %s....", p.node.ID(), host.ID())

	// create message data
	req := &p2p.PingRequest{MessageData: p.node.NewMessageData(uuid.New().String(), false),
		Message: fmt.Sprintf("Ping from %s", p.node.ID())}

	// sign the data
	signature, err := p.node.signProtoMessage(req)
	if err != nil {
		log.Println("failed to sign pb data")
		return false
	}

	// add the signature to the message
	req.MessageData.Sign = signature

	// store ref request so response handler has access to it
	p.mu.Lock()
	p.requests[req.MessageData.Id] = req
	p.mu.Unlock()

	ok := p.node.sendProtoMessage(host.ID(), pingRequest, req)
	if !ok {
		return false
	}

	log.Printf("%s: Ping to: %s was sent. Message Id: %s, Message: %s", p.node.ID(), host.ID(), req.MessageData.Id, req.Message)
	return true
}
