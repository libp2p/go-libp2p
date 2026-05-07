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
// connection would stall out, and the swarm would leak a reference.
//
// The fix makes AddConn non-blocking and returns false when the channel is
// full. The caller (swarm.addConn) then tears down the connection — it's
// better to reject a connection than to let it sit around as a zombie that
// nobody knows about.
func TestAddConnDoesNotBlockWhenChannelIsFull(t *testing.T) {
	bus := eventbus.NewBus()
	emitter, err := bus.Emitter(&event.EvtPeerConnectednessChanged{})
	if err != nil {
		t.Fatal(err)
	}

	connectedness := func(p peer.ID) network.Connectedness {
		return network.Connected
	}

	cee := newConnectednessEventEmitter(connectedness, emitter)

	// Use a timeout to catch deadlocks — if AddConn blocks, this test
	// will fail instead of hanging forever.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	doneCh := make(chan struct{})
	var dropped int

	go func() {
		defer close(doneCh)

		// The newConns channel only holds 32 events. We blast 100 into it.
		// The first ~32 should succeed, the rest should return false
		// instead of blocking.
		for i := 0; i < 100; i++ {
			p := peer.ID(string([]byte{byte(i % 255)}))
			if !cee.AddConn(p) {
				dropped++
			}
		}
	}()

	select {
	case <-ctx.Done():
		t.Fatal("AddConn blocked! The channel filled up and caused a stall.")
	case <-doneCh:
		t.Logf("All 100 AddConn calls completed without deadlocking (%d dropped)", dropped)
		if dropped == 0 {
			// The background goroutine might have drained some, but we expect
			// at least a few drops when blasting 100 events into a 32-slot channel.
			t.Log("Note: no events were dropped — the background worker kept up. " +
				"This is fine, the important thing is that nothing blocked.")
		}
	}

	cee.Close()
}
