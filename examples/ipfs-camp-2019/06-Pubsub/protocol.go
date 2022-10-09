package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const chatProtocol = protocol.ID("/libp2p/chat/1.0.0")

// TODO: Replace this handler with a function that handles message from a
// pubsub Subscribe channel.
func chatHandler(s network.Stream) {
	data, err := io.ReadAll(s)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Println("Received:", string(data))
}

// TODO: Replace this with a send function that publishes the string messages
// on our pubsub topic.
func chatSend(msg string, s network.Stream) error {
	fmt.Println("Sending:", msg)
	w := bufio.NewWriter(s)
	n, err := w.WriteString(msg)
	if n != len(msg) {
		return fmt.Errorf("expected to write %d bytes, wrote %d", len(msg), n)
	}
	if err != nil {
		return err
	}
	if err = w.Flush(); err != nil {
		return err
	}
	s.Close()
	data, err := io.ReadAll(s)
	if err != nil {
		return err
	}
	if len(data) > 0 {
		fmt.Println("Received:", string(data))
	}
	return nil
}

func chatInputLoop(ctx context.Context, h host.Host, donec chan struct{}) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		msg := scanner.Text()
		for _, peer := range h.Network().Peers() {
			if _, err := h.Peerstore().SupportsProtocols(peer, string(chatProtocol)); err == nil {
				s, err := h.NewStream(ctx, peer, chatProtocol)
				defer func() {
					if err != nil {
						fmt.Fprintln(os.Stderr, err)
					}
				}()
				if err != nil {
					continue
				}
				err = chatSend(msg, s)
			}
		}
	}
	donec <- struct{}{}
}
