package main

import (
	"bufio"
	"context"
	"fmt"
	"log"

	host "gx/ipfs/QmRS46AyqtpJBsf1zmQdeizSDEzo1qkWR7rdEuPFAv8237/go-libp2p-host"
	inet "gx/ipfs/QmbD5yKbXahNvoMqzeuNyKQA9vAs9fUvJg2GXeWU1fVqY5/go-libp2p-net"

	uuid "github.com/google/uuid"
	"github.com/ipfs/go-ipfs/thirdparty/assert"
	p2p "github.com/libp2p/go-libp2p/examples/multipro/pb"
	protobufCodec "github.com/multiformats/go-multicodec/protobuf"
)

// pattern: /protocol-name/request-or-response-message/version
const echoRequest = "/echo/echoreq/0.0.1"
const echoResponse = "/echo/echoresp/0.0.1"

type EchoProtocol struct {
	host     host.Host                   // local host
	requests map[string]*p2p.EchoRequest // used to access request data from response handlers
	done     chan bool                   // only for demo purposes to hold main from terminating
}

func NewEchoProtocol(host host.Host, done chan bool) *EchoProtocol {
	e := EchoProtocol{host: host, requests: make(map[string]*p2p.EchoRequest), done: done}
	host.SetStreamHandler(echoRequest, e.onEchoRequest)
	host.SetStreamHandler(echoResponse, e.onEchoResponse)
	return &e
}

// remote peer requests handler
func (e *EchoProtocol) onEchoRequest(s inet.Stream) {
	// get request data
	data := &p2p.EchoRequest{}
	decoder := protobufCodec.Multicodec(nil).Decoder(bufio.NewReader(s))
	err := decoder.Decode(data)
	if err != nil {
		log.Fatal(err)
		return
	}

	log.Printf("%s: Received echo request from %s. Message: %s", s.Conn().LocalPeer(), s.Conn().RemotePeer(), data.Message)

	log.Printf("%s: Sending echo response to %s. Message id: %s...", s.Conn().LocalPeer(), s.Conn().RemotePeer(), data.MessageData.Id)

	// send response to request send using the message string he provided
	resp := &p2p.EchoResponse{
		MessageData: NewMessageData(e.host.ID().String(), data.MessageData.Id, false),
		Message:     data.Message}

	s, respErr := e.host.NewStream(context.Background(), s.Conn().RemotePeer(), echoResponse)
	if respErr != nil {
		log.Fatal(respErr)
		return
	}

	ok := sendDataObject(resp, s)

	if ok {
		log.Printf("%s: Echo response to %s sent.", s.Conn().LocalPeer().String(), s.Conn().RemotePeer().String())
	}
}

// remote echo response handler
func (e *EchoProtocol) onEchoResponse(s inet.Stream) {
	data := &p2p.EchoResponse{}
	decoder := protobufCodec.Multicodec(nil).Decoder(bufio.NewReader(s))
	err := decoder.Decode(data)
	if err != nil {
		return
	}

	// locate request data and remove it if found
	req, ok := e.requests[data.MessageData.Id]
	if ok {
		// remove request from map as we have processed it here
		delete(e.requests, data.MessageData.Id)
	} else {
		log.Print("Failed to locate request data boject for response")
		return
	}

	assert.True(req.Message == data.Message, nil, "Expected echo to respond with request message")

	log.Printf("%s: Received echo response from %s. Message id:%s. Message: %s.", s.Conn().LocalPeer(), s.Conn().RemotePeer(), data.MessageData.Id, data.Message)
	e.done <- true
}

func (e *EchoProtocol) Echo(node *Node) bool {
	log.Printf("%s: Sending echo to: %s....", e.host.ID(), node.host.ID())

	// create message data
	req := &p2p.EchoRequest{
		MessageData: NewMessageData(e.host.ID().String(), uuid.New().String(), false),
		Message:     fmt.Sprintf("Echo from %s", e.host.ID())}

	s, err := e.host.NewStream(context.Background(), node.host.ID(), echoRequest)
	if err != nil {
		log.Fatal(err)
		return false
	}

	ok := sendDataObject(req, s)

	if !ok {
		return false
	}

	// store request so response handler has access to it
	e.requests[req.MessageData.Id] = req
	log.Printf("%s: Echo to: %s was sent. Message Id: %s, Message: %s", e.host.ID(), node.host.ID(), req.MessageData.Id, req.Message)
	return true
}
