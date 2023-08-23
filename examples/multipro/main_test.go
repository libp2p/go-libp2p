//go:build !go1.21

package main

import (
	"fmt"
	"log"
	"testing"

	"github.com/libp2p/go-libp2p/examples/testutils"
)

func TestMain(t *testing.T) {
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

	done := make(chan bool, 1)
	h1 := makeRandomNode(port1, done)
	h2 := makeRandomNode(port2, done)

	var h testutils.LogHarness

	// Sequence of log messages when h1 pings h2
	pingh1h2 := h.NewSequence("ping h1->h2")
	pingh1h2.ExpectPrefix(fmt.Sprintf("%s: Sending ping to: %s", h1.ID(), h2.ID()))
	pingh1h2.ExpectPrefix(fmt.Sprintf("%s: Received ping request from %s", h2.ID(), h1.ID()))
	pingh1h2.ExpectPrefix(fmt.Sprintf("%s: Received ping response from %s", h1.ID(), h2.ID()))

	// Sequence of log messages when h2 pings h1
	pingh2h1 := h.NewSequence("ping h2->h1")
	pingh2h1.ExpectPrefix(fmt.Sprintf("%s: Sending ping to: %s", h2.ID(), h1.ID()))
	pingh2h1.ExpectPrefix(fmt.Sprintf("%s: Received ping request from %s", h1.ID(), h2.ID()))
	pingh2h1.ExpectPrefix(fmt.Sprintf("%s: Received ping response from %s", h2.ID(), h1.ID()))

	// Sequence of log messages when h1 sends echo to h2
	echoh1h2 := h.NewSequence("echo h1->h2")
	echoh1h2.ExpectPrefix(fmt.Sprintf("%s: Sending echo to: %s", h1.ID(), h2.ID()))
	echoh1h2.ExpectPrefix(fmt.Sprintf("%s: Echo response to %s", h2.ID(), h1.ID()))

	// Sequence of log messages when h1 sends echo to h2
	echoh2h1 := h.NewSequence("echo h2->h1")
	echoh2h1.ExpectPrefix(fmt.Sprintf("%s: Sending echo to: %s", h2.ID(), h1.ID()))
	echoh2h1.ExpectPrefix(fmt.Sprintf("%s: Echo response to %s", h1.ID(), h2.ID()))

	h.Run(t, func() {
		run(h1, h2, done)
	})
}
