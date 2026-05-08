package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

type connectednessEventRecorder struct {
	events []event.EvtPeerConnectednessChanged
}

var _ event.Emitter = (*connectednessEventRecorder)(nil)

func (r *connectednessEventRecorder) Emit(evt any) error {
	r.events = append(r.events, evt.(event.EvtPeerConnectednessChanged))
	return nil
}

func (r *connectednessEventRecorder) Close() error {
	return nil
}

// TestAddConnQueuesBurstWithoutEmitterDrain verifies that a burst of new
// connections can be queued even if the emitter goroutine has not drained
// newConns yet. This avoids stalls from the old fixed-size channel while
// preserving the invariant that every connection is queued before AddConn
// returns.
func TestAddConnQueuesBurstWithoutEmitterDrain(t *testing.T) {
	const burstSize = 100

	ctx := t.Context()
	emitter := &connectednessEventRecorder{}

	cee := &connectednessEventEmitter{
		lastEvent: make(map[peer.ID]network.Connectedness),
		connectedness: func(peer.ID) network.Connectedness {
			return network.Connected
		},
		emitter:      emitter,
		newConnNotif: make(chan struct{}, 1),
		ctx:          ctx,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		for i := 0; i < burstSize; i++ {
			p := peer.ID(string([]byte{byte(i % 255)}))
			cee.AddConn(p)
		}
	}()

	select {
	case <-ctx.Done():
		t.Fatalf("AddConn blocked during a %d-connection burst", burstSize)
	case <-doneCh:
	}

	cee.newConnsMx.Lock()
	queued := len(cee.newConns)
	cee.newConnsMx.Unlock()
	if queued != burstSize {
		t.Fatalf("expected %d queued connections, got %d", burstSize, queued)
	}

	if !cee.sendConnAddedNotifications() {
		t.Fatal("expected queued connections to be drained")
	}

	if len(emitter.events) != burstSize {
		t.Fatalf("expected %d connectedness events, got %d", burstSize, len(emitter.events))
	}

	cee.newConnsMx.Lock()
	queued = len(cee.newConns)
	cee.newConnsMx.Unlock()
	if queued != 0 {
		t.Fatalf("expected queued connections to be cleared, got %d", queued)
	}
}
