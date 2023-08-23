//go:build !go1.21

package main

import (
	"context"
	"log"
	"testing"

	"github.com/libp2p/go-libp2p/examples/testutils"
)

func TestMain(t *testing.T) {
	var h testutils.LogHarness
	h.Expect("listening for connections")
	h.Expect("sender opening stream")
	h.Expect("sender saying hello")
	h.Expect("listener received new stream")
	h.Expect("read: Hello, world!")
	h.Expect(`read reply: "Hello, world!\n"`)

	h.Run(t, func() {
		// Create a context that will stop the hosts when the tests end
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Get a tcp port for the listener
		lport, err := testutils.FindFreePort(t, "", 5)
		if err != nil {
			log.Println(err)
			return
		}

		// Get a tcp port for the sender
		sport, err := testutils.FindFreePort(t, "", 5)
		if err != nil {
			log.Println(err)
			return
		}

		// Make listener
		lh, err := makeBasicHost(lport, true, 1)
		if err != nil {
			log.Println(err)
			return
		}

		startListener(ctx, lh, lport, true)

		// Make sender
		listenAddr := getHostAddress(lh)
		sh, err := makeBasicHost(sport, true, 2)
		if err != nil {
			log.Println(err)
			return
		}

		runSender(ctx, sh, listenAddr)
	})
}
