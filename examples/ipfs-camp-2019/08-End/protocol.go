package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"time"

	"strings"

	"github.com/gogo/protobuf/proto"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	peer "github.com/libp2p/go-libp2p/core/peer"
)

func sendMessage(ctx context.Context, topic *pubsub.Topic, msg string) {
	msgId := make([]byte, 10)
	_, err := rand.Read(msgId)
	defer func() {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	if err != nil {
		return
	}
	now := time.Now().Unix()
	req := &Request{
		Type: Request_SEND_MESSAGE.Enum(),
		SendMessage: &SendMessage{
			Id:      msgId,
			Data:    []byte(msg),
			Created: &now,
		},
	}
	msgBytes, err := proto.Marshal(req)
	if err != nil {
		return
	}
	err = topic.Publish(ctx, msgBytes)
}

func updatePeer(ctx context.Context, topic *pubsub.Topic, id peer.ID, handle string) {
	oldHandle, ok := handles[id.String()]
	if !ok {
		oldHandle = id.ShortString()
	}
	handles[id.String()] = handle

	req := &Request{
		Type: Request_UPDATE_PEER.Enum(),
		UpdatePeer: &UpdatePeer{
			UserHandle: []byte(handle),
		},
	}
	reqBytes, err := proto.Marshal(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	err = topic.Publish(ctx, reqBytes)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	fmt.Printf("%s -> %s\n", oldHandle, handle)
}

func chatInputLoop(ctx context.Context, h host.Host, topic *pubsub.Topic, donec chan struct{}) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		msg := scanner.Text()
		if strings.HasPrefix(msg, "/name ") {
			newHandle := strings.TrimPrefix(msg, "/name ")
			newHandle = strings.TrimSpace(newHandle)
			updatePeer(ctx, topic, h.ID(), newHandle)
		} else {
			sendMessage(ctx, topic, msg)
		}
	}
	donec <- struct{}{}
}
