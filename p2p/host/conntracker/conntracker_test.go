package conntracker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	multiaddr "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TODO:
// - Test proto negotiation event
// - Test Identify failed
// - Test Notifier

// MockClock is a mock implementation of the clock interface
type MockClock struct {
	mu       sync.Mutex
	current  time.Time
	tickers  []tickerMeta
	tickerWG sync.WaitGroup
}

type tickerMeta struct {
	clockTick chan time.Time
}

func (m *MockClock) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, t := range m.tickers {
		close(t.clockTick)
	}
}

// Now returns the fixed time
func (m *MockClock) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// Since returns the duration since the fixed time
func (m *MockClock) Since(t time.Time) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Since(m.current)
}

// NewTicker returns a ticker that ticks at the specified duration
func (m *MockClock) NewTicker(d time.Duration) *time.Ticker {
	m.mu.Lock()
	defer m.mu.Unlock()

	c := make(chan time.Time)
	toFire := m.current.Add(d)
	ticker := &time.Ticker{
		C: c,
	}

	clockTick := make(chan time.Time, 1)
	m.tickers = append(m.tickers, tickerMeta{
		clockTick: clockTick,
	})

	go func() {
		for t := range clockTick {
			for t.After(toFire) || t.Equal(toFire) {
				c <- t
				toFire = toFire.Add(d)
			}
			m.tickerWG.Done()
		}
	}()

	return ticker
}

func (m *MockClock) Advance(d time.Duration) {
	m.mu.Lock()
	m.current = m.current.Add(d)
	current := m.current
	tickers := m.tickers
	m.mu.Unlock()

	m.tickerWG.Add(len(tickers))
	for _, t := range tickers {
		t.clockTick <- current
	}
	m.tickerWG.Wait()
}

type mockNotifier struct {
}

func (m *mockNotifier) Notify(n network.Notifiee)     {}
func (m *mockNotifier) StopNotify(n network.Notifiee) {}

func TestGetConnTracker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eb := eventbus.NewBus()

	clk := &MockClock{}
	connTracker := ConnTracker{clock: clk}

	err := connTracker.Start(eb, &mockNotifier{})
	require.NoError(t, err)
	defer connTracker.Stop()

	idEmitter, err := eb.Emitter(new(event.EvtPeerIdentificationCompleted))
	require.NoError(t, err)
	defer idEmitter.Close()
	connectednessEmitter, err := eb.Emitter(new(event.EvtPeerConnectednessChanged))
	require.NoError(t, err)
	defer connectednessEmitter.Close()

	peerA := test.RandPeerIDFatal(t)
	connA := &mockConn{}

	var wg sync.WaitGroup
	defer func() {
		sem := make(chan struct{})
		go func() {
			defer close(sem)
			wg.Wait()
		}()
		select {
		case <-sem:
		case <-time.After(1 * time.Second):
			assert.Fail(t, "WaitGroup was not completed")
		}
	}()
	wg.Add(1)

	go func() {
		defer wg.Done()
		// Asking for a conn before we have one will block
		c, err := connTracker.GetBestConn(ctx, peerA, GetBestConnOpts{
			OneOf:    []protocol.ID{"/test/1.0.0"},
			FilterFn: NoLimitedConnFilter,
		})
		assert.NoError(t, err)
		assert.Equal(t, connA, c.Conn)
	}()

	// We've connected to a peer
	evt := event.EvtPeerConnectednessChanged{
		Connectedness: network.Connected,
		Peer:          peerA,
	}
	err = connectednessEmitter.Emit(evt)
	require.NoError(t, err)

	idEvt := event.EvtPeerIdentificationCompleted{
		Peer:      peerA,
		Conn:      connA,
		Protocols: []protocol.ID{"/test/1.0.0"},
	}
	err = idEmitter.Emit(idEvt)
	require.NoError(t, err)

	// Getting a connection to peerA should return the connection we just added
	c, err := connTracker.GetBestConn(ctx, peerA, GetBestConnOpts{
		OneOf:    []protocol.ID{"/test/1.0.0"},
		FilterFn: NoLimitedConnFilter,
	})
	require.NoError(t, err)
	require.Equal(t, connA, c.Conn)

	// Advance the clock to trigger the GC
	clk.Advance(1 * time.Hour)

	// Still have the connection
	c, err = connTracker.GetBestConn(ctx, peerA, GetBestConnOpts{
		OneOf:    []protocol.ID{"/test/1.0.0"},
		FilterFn: NoLimitedConnFilter,
	})
	require.NoError(t, err)
	require.Equal(t, connA, c.Conn)

	// Disconnect from the peer
	evt = event.EvtPeerConnectednessChanged{
		Connectedness: network.NotConnected,
		Peer:          peerA,
	}
	err = connectednessEmitter.Emit(evt)
	require.NoError(t, err)

	// Advance the clock to trigger the GC
	clk.Advance(1 * time.Hour)
	time.Sleep(100 * time.Millisecond) // Wait for the other goroutine to GC

	// Should block since we don't have any connection
	ctx, timeoutCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer timeoutCancel()
	_, err = connTracker.GetBestConn(ctx, peerA, GetBestConnOpts{
		OneOf:    []protocol.ID{"/test/1.0.0"},
		FilterFn: NoLimitedConnFilter,
	})
	require.ErrorContains(t, err, "context deadline exceeded")

	_, err = connTracker.GetBestConn(context.Background(), peerA, GetBestConnOpts{
		OneOf:       []protocol.ID{"/test/1.0.0"},
		AllowNoConn: true,
		FilterFn:    NoLimitedConnFilter,
	})
	require.ErrorIs(t, err, ErrNoConn)
}

// TODO test that we get the meta with the conn

type mockConn struct {
	closed bool
}

// Close implements network.Conn.
func (m *mockConn) Close() error {
	m.closed = true
	return nil
}

// ConnState implements network.Conn.
func (m *mockConn) ConnState() network.ConnectionState {
	panic("unimplemented")
}

// GetStreams implements network.Conn.
func (m *mockConn) GetStreams() []network.Stream {
	panic("unimplemented")
}

// ID implements network.Conn.
func (m *mockConn) ID() string {
	panic("unimplemented")
}

// IsClosed implements network.Conn.
func (m *mockConn) IsClosed() bool {
	return m.closed
}

// LocalMultiaddr implements network.Conn.
func (m *mockConn) LocalMultiaddr() multiaddr.Multiaddr {
	panic("unimplemented")
}

// LocalPeer implements network.Conn.
func (m *mockConn) LocalPeer() peer.ID {
	panic("unimplemented")
}

// NewStream implements network.Conn.
func (m *mockConn) NewStream(context.Context) (network.Stream, error) {
	panic("unimplemented")
}

// RemoteMultiaddr implements network.Conn.
func (m *mockConn) RemoteMultiaddr() multiaddr.Multiaddr {
	panic("unimplemented")
}

// RemotePeer implements network.Conn.
func (m *mockConn) RemotePeer() peer.ID {
	panic("unimplemented")
}

// RemotePublicKey implements network.Conn.
func (m *mockConn) RemotePublicKey() crypto.PubKey {
	panic("unimplemented")
}

// Scope implements network.Conn.
func (m *mockConn) Scope() network.ConnScope {
	panic("unimplemented")
}

// Stat implements network.Conn.
func (m *mockConn) Stat() network.ConnStats {
	return network.ConnStats{}
}

var _ network.Conn = &mockConn{}
