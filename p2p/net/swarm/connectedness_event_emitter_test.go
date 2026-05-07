package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
)

// We found an issue where the connectedness event emitter could accidentally
// freeze a connection. When a new connection is made, the swarm calls AddConn
// to emit a "Connected" event. But AddConn pushes to a channel with a strict
// limit of 32. 
//
// If the background worker reading from this channel gets delayed (like if an
// event subscriber is slow), the channel fills up. Because the push to the
// channel was a blocking operation, AddConn would just wait forever. Worse,
// the swarm holds a notification lock when calling AddConn, so the entire
// connection would stall out, and the swarm would leak a reference!
//
// This test makes sure that our fix works: if the channel fills up, AddConn
// should just drop the event and move on instead of freezing everything.
func TestAddConnDoesNotBlockWhenChannelIsFull(t *testing.T) {
	bus := eventbus.NewBus()
	emitter, err := bus.Emitter(&event.EvtPeerConnectednessChanged{})
	if err != nil {
		t.Fatal(err)
	}

	// Just a dummy connectedness function to keep the emitter happy.
	connectedness := func(p peer.ID) network.Connectedness {
		return network.Connected
	}

	// Start up the emitter. This kicks off the background goroutine that
	// reads from the newConns channel.
	cee := newConnectednessEventEmitter(connectedness, emitter)

	// We want to simulate the channel filling up. The easiest way to test
	// this without getting into the internal locks is to just blast it with
	// events faster than it can process them. The channel only holds 32 events.
	
	// We'll use a short timeout context. If AddConn blocks, this context
	// will timeout and fail the test. If our fix works, it will breeze through.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		
		// Send 100 events back-to-back. The channel only holds 32, so
		// if AddConn is still blocking, it will definitely stall out here.
		for i := 0; i < 100; i++ {
			// Generate a quick dummy peer ID
			p := peer.ID(string([]byte{byte(i % 255)}))
			cee.AddConn(p)
		}
	}()

	select {
	case <-ctx.Done():
		t.Fatal("AddConn blocked! The channel filled up and caused a stall.")
	case <-doneCh:
		// Success! It hammered through all 100 calls without getting stuck.
		t.Log("Awesome, AddConn completed all 100 calls without deadlocking.")
	}

	// Clean up behind ourselves
	cee.Close()
}
