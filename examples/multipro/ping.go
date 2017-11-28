package main

import (
	"bufio"
	"context"
	"fmt"
	"log"

	host "gx/ipfs/QmRS46AyqtpJBsf1zmQdeizSDEzo1qkWR7rdEuPFAv8237/go-libp2p-host"
	inet "gx/ipfs/QmbD5yKbXahNvoMqzeuNyKQA9vAs9fUvJg2GXeWU1fVqY5/go-libp2p-net"

	uuid "github.com/google/uuid"
	p2p "github.com/libp2p/go-libp2p/examples/multipro/pb"
	protobufCodec "github.com/multiformats/go-multicodec/protobuf"
)

// pattern: /protocol-name/request-or-response-message/version
const pingRequest = "/ping/pingreq/0.0.1"
const pingResponse = "/ping/pingresp/0.0.1"

// PingProtocol type
type PingProtocol struct {
	host     host.Host                   // local host
	requests map[string]*p2p.PingRequest // used to access request data from response handlers
	done     chan bool                   // only for demo purposes to stop main from terminating
}

func NewPingProtocol(host host.Host, done chan bool) *PingProtocol {
	p := &PingProtocol{host: host, requests: make(map[string]*p2p.PingRequest), done: done}
	host.SetStreamHandler(pingRequest, p.onPingRequest)
	host.SetStreamHandler(pingResponse, p.onPingResponse)
	return p
}

// remote peer requests handler
func (p *PingProtocol) onPingRequest(s inet.Stream) {

	// get request data
	data := &p2p.PingRequest{}
	decoder := protobufCodec.Multicodec(nil).Decoder(bufio.NewReader(s))
	err := decoder.Decode(data)
	if err != nil {
		log.Fatal(err)
		return
	}

	log.Printf("%s: Received ping request from %s. Message: %s", s.Conn().LocalPeer(), s.Conn().RemotePeer(), data.Message)

	// send response to sender
	log.Printf("%s: Sending ping response to %s. Message id: %s...", s.Conn().LocalPeer(), s.Conn().RemotePeer(), data.MessageData.Id)
	resp := &p2p.PingResponse{MessageData: NewMessageData(p.host.ID().String(), data.MessageData.Id, false),
		Message: fmt.Sprintf("Ping response from %s", p.host.ID())}

	s, respErr := p.host.NewStream(context.Background(), s.Conn().RemotePeer(), pingResponse)
	if respErr != nil {
		log.Fatal(respErr)
		return
	}

	ok := sendDataObject(resp, s)

	if ok {
		log.Printf("%s: Ping response to %s sent.", s.Conn().LocalPeer().String(), s.Conn().RemotePeer().String())
	}
}

// remote ping response handler
func (p *PingProtocol) onPingResponse(s inet.Stream) {
	data := &p2p.PingResponse{}
	decoder := protobufCodec.Multicodec(nil).Decoder(bufio.NewReader(s))
	err := decoder.Decode(data)
	if err != nil {
		return
	}

	// locate request data and remove it if found
	_, ok := p.requests[data.MessageData.Id]
	if ok {
		// remove request from map as we have processed it here
		delete(p.requests, data.MessageData.Id)
	} else {
		log.Print("Failed to locate request data boject for response")
		return
	}

	log.Printf("%s: Received ping response from %s. Message id:%s. Message: %s.", s.Conn().LocalPeer(), s.Conn().RemotePeer(), data.MessageData.Id, data.Message)
	p.done <- true
}

func (p *PingProtocol) Ping(node *Node) bool {
	log.Printf("%s: Sending ping to: %s....", p.host.ID(), node.host.ID())

	// create message data
	req := &p2p.PingRequest{MessageData: NewMessageData(p.host.ID().String(), uuid.New().String(), false),
		Message: fmt.Sprintf("Ping from %s", p.host.ID())}

	s, err := p.host.NewStream(context.Background(), node.host.ID(), pingRequest)
	if err != nil {
		log.Fatal(err)
		return false
	}

	ok := sendDataObject(req, s)

	if !ok {
		return false
	}

	// store ref request so response handler has access to it
	p.requests[req.MessageData.Id] = req
	log.Printf("%s: Ping to: %s was sent. Message Id: %s, Message: %s", p.host.ID(), node.host.ID(), req.MessageData.Id, req.Message)
	return true
}
