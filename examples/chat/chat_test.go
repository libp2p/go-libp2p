package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/libp2p/go-libp2p/examples/testutils"
)

func TestMain(t *testing.T) {
	var h testutils.LogHarness
	h.Expect("Waiting for incoming connection")
	h.Expect("Established connection to destination")
	h.Expect("Got a new stream!")

	h.Run(t, func() {
		// Create a context that will stop the hosts when the tests end
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		port1, err := testutils.FindFreePort(t, "", 5)
		if err != nil {
			log.Println(err)
			return
		}

		port2, err := testutils.FindFreePort(t, "", 5)
		if err != nil {
			log.Println(err)
			return
		}

		h1, err := makeHost(port1, rand.Reader)
		if err != nil {
			log.Println(err)
			return
		}

		go startPeer(ctx, h1, func(network.Stream) {
			log.Println("Got a new stream!")
			// Sleep a bit to let h2 print the logs we're waiting for
			time.Sleep(500 * time.Millisecond)
			cancel() // end the test
		})

		dest := fmt.Sprintf("/ip4/127.0.0.1/tcp/%v/p2p/%s", port1, h1.ID())

		h2, err := makeHost(port2, rand.Reader)
		if err != nil {
			log.Println(err)
			return
		}

		go func() {
			rw, err := startPeerAndConnect(ctx, h2, dest)
			if err != nil {
				log.Println(err)
				return
			}

			rw.WriteString("test message")
			rw.Flush()
		}()

		<-ctx.Done()
	})
}
