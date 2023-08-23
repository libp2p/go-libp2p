//go:build !go1.21

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"time"

	"github.com/gogo/protobuf/proto"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

func chatInputLoop(ctx context.Context, topic *pubsub.Topic, donec chan struct{}) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		msg := scanner.Text()
		msgId := make([]byte, 10)
		_, err := rand.Read(msgId)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
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
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		err = topic.Publish(ctx, msgBytes)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
	}
	donec <- struct{}{}
}
