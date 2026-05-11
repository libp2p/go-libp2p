package swarm

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"

	"github.com/stretchr/testify/require"
)

// peerOnlyConn satisfies transport.CapableConn but only answers RemotePeer().
// All other interface methods are nil and will panic if called — the emitter
// only ever asks for the peer ID.
type peerOnlyConn struct {
	transport.CapableConn
	p peer.ID
}

func (p *peerOnlyConn) RemotePeer() peer.ID { return p.p }

func newTestConn(t *testing.T) *Conn {
	t.Helper()
	return &Conn{conn: &peerOnlyConn{p: test.RandPeerIDFatal(t)}}
}

type notifEvent struct {
	conn  *Conn
	isAdd bool // true = Connected, false = Disconnected
}

// recordingCallbacks returns onConnected and onDisconnected callbacks that
// append to a shared slice, plus a snapshot getter.
func recordingCallbacks() (onConnected func(*Conn), onDisconnected func(*Conn), get func() []notifEvent) {
	var mu sync.Mutex
	events := []notifEvent{}
	onConnected = func(c *Conn) {
		mu.Lock()
		events = append(events, notifEvent{conn: c, isAdd: true})
		mu.Unlock()
	}
	onDisconnected = func(c *Conn) {
		mu.Lock()
		events = append(events, notifEvent{conn: c, isAdd: false})
		mu.Unlock()
	}
	get = func() []notifEvent {
		mu.Lock()
		defer mu.Unlock()
		out := make([]notifEvent, len(events))
		copy(out, events)
		return out
	}
	return
}

func newEmitterForTest(t *testing.T, onConnected, onDisconnected func(*Conn)) *connectionEventsEmitter {
	t.Helper()
	bus := eventbus.NewBus()
	em, err := bus.Emitter(new(event.EvtPeerConnectednessChanged))
	require.NoError(t, err)
	connectedness := func(peer.ID) network.Connectedness { return network.NotConnected }
	e := newConnectionEventsEmitter(connectedness, em, onConnected, onDisconnected)
	t.Cleanup(func() { e.Close() })
	return e
}

// TestEmitter_RemoveConnBeforeAddConn verifies that a RemoveConn arriving
// before AddConn is parked, and the deferred Disconnected fires only after
// AddConn has dispatched Connected, in the right order.
func TestEmitter_RemoveConnBeforeAddConn(t *testing.T) {
	onConnected, onDisconnected, events := recordingCallbacks()
	e := newEmitterForTest(t, onConnected, onDisconnected)
	c := newTestConn(t)

	e.RemoveConn(c)

	e.notifsLk.Lock()
	_, parked := e.pendingDisconnect[c]
	e.notifsLk.Unlock()
	require.True(t, parked, "RemoveConn before AddConn should park")
	require.Empty(t, events(), "no Notifiee callbacks fire from a parked RemoveConn")

	e.AddConn(c)

	// Disconnected is dispatched from a goroutine in the parked path.
	require.Eventually(t, func() bool { return len(events()) == 2 }, time.Second, time.Millisecond)

	got := events()
	require.Len(t, got, 2)
	require.True(t, got[0].isAdd, "first event must be Connected")
	require.False(t, got[1].isAdd, "second event must be Disconnected")
	require.Same(t, c, got[0].conn)
	require.Same(t, c, got[1].conn)

	e.notifsLk.Lock()
	require.Empty(t, e.pendingDisconnect)
	require.Empty(t, e.connected)
	e.notifsLk.Unlock()
}

// TestEmitter_NormalOrder verifies the standard AddConn-then-RemoveConn flow
// dispatches one Connected and one Disconnected, and leaves no leaked state.
func TestEmitter_NormalOrder(t *testing.T) {
	onConnected, onDisconnected, events := recordingCallbacks()
	e := newEmitterForTest(t, onConnected, onDisconnected)
	c := newTestConn(t)

	e.AddConn(c)

	e.notifsLk.Lock()
	_, conn := e.connected[c]
	e.notifsLk.Unlock()
	require.True(t, conn, "AddConn should mark the conn as connected")

	e.RemoveConn(c)

	got := events()
	require.Len(t, got, 2)
	require.True(t, got[0].isAdd)
	require.False(t, got[1].isAdd)

	e.notifsLk.Lock()
	require.Empty(t, e.connected)
	require.Empty(t, e.pendingDisconnect)
	e.notifsLk.Unlock()
}

// TestEmitter_ConcurrentAddRemoveOrdering exercises many conns in parallel,
// each with a randomized AddConn/RemoveConn order. For every conn, we must see
// exactly one Connected followed by exactly one Disconnected (never reversed),
// and no map entries should leak.
func TestEmitter_ConcurrentAddRemoveOrdering(t *testing.T) {
	const N = 200

	var perConnMu sync.Mutex
	connectedSeen := make(map[*Conn]int)
	disconnectedSeen := make(map[*Conn]int)
	var orderViolations atomic.Int64

	onConnected := func(c *Conn) {
		perConnMu.Lock()
		if disconnectedSeen[c] > 0 {
			orderViolations.Add(1)
		}
		connectedSeen[c]++
		perConnMu.Unlock()
	}
	onDisconnected := func(c *Conn) {
		perConnMu.Lock()
		if connectedSeen[c] == 0 {
			orderViolations.Add(1)
		}
		disconnectedSeen[c]++
		perConnMu.Unlock()
	}
	e := newEmitterForTest(t, onConnected, onDisconnected)

	conns := make([]*Conn, N)
	for i := range conns {
		conns[i] = newTestConn(t)
	}

	var wg sync.WaitGroup
	for i := range N {
		c := conns[i]
		addFirst := rand.Intn(2) == 0
		jitter := time.Duration(rand.Intn(1000)) * time.Microsecond
		wg.Add(2)
		if addFirst {
			go func() { defer wg.Done(); e.AddConn(c) }()
			go func() {
				defer wg.Done()
				time.Sleep(jitter)
				e.RemoveConn(c)
			}()
		} else {
			go func() { defer wg.Done(); e.RemoveConn(c) }()
			go func() {
				defer wg.Done()
				time.Sleep(jitter)
				e.AddConn(c)
			}()
		}
	}
	wg.Wait()

	// Parked-RemoveConn dispatches Disconnected from a goroutine, so wait for
	// every conn to have seen one.
	require.Eventually(t, func() bool {
		perConnMu.Lock()
		defer perConnMu.Unlock()
		for _, c := range conns {
			if connectedSeen[c] != 1 || disconnectedSeen[c] != 1 {
				return false
			}
		}
		return true
	}, 5*time.Second, 5*time.Millisecond)

	require.Zero(t, orderViolations.Load(), "no per-conn ordering violations expected")

	e.notifsLk.Lock()
	require.Empty(t, e.connected, "connected map should drain")
	require.Empty(t, e.pendingDisconnect, "pendingDisconnect map should drain")
	e.notifsLk.Unlock()
}
