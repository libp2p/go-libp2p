package autorelay_test

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	relayv1 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv1/relay"
	circuitv2_proto "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"

	"github.com/benbjohnson/clock"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

const protoIDv2 = circuitv2_proto.ProtoIDv2Hop

func numRelays(h host.Host) int {
	return len(usedRelays(h))
}

func usedRelays(h host.Host) []peer.ID {
	m := make(map[peer.ID]struct{})
	for _, addr := range h.Addrs() {
		addr, comp := ma.SplitLast(addr)
		if comp.Protocol().Code != ma.P_CIRCUIT { // not a relay addr
			continue
		}
		_, comp = ma.SplitLast(addr)
		if comp.Protocol().Code != ma.P_P2P {
			panic("expected p2p component")
		}
		id, err := peer.Decode(comp.Value())
		if err != nil {
			panic(err)
		}
		m[id] = struct{}{}
	}
	peers := make([]peer.ID, 0, len(m))
	for id := range m {
		peers = append(peers, id)
	}
	return peers
}

func newPrivateNode(t *testing.T, opts ...autorelay.Option) host.Host {
	t.Helper()
	h, err := libp2p.New(
		libp2p.ForceReachabilityPrivate(),
		libp2p.EnableAutoRelay(opts...),
	)
	require.NoError(t, err)
	return h
}

func newRelay(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(
		libp2p.DisableRelay(),
		libp2p.EnableRelayService(),
		libp2p.ForceReachabilityPublic(),
		libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
			for i, addr := range addrs {
				saddr := addr.String()
				if strings.HasPrefix(saddr, "/ip4/127.0.0.1/") {
					addrNoIP := strings.TrimPrefix(saddr, "/ip4/127.0.0.1")
					addrs[i] = ma.StringCast("/dns4/localhost" + addrNoIP)
				}
			}
			return addrs
		}),
	)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, p := range h.Mux().Protocols() {
			if p == protoIDv2 {
				return true
			}
		}
		return false
	}, 500*time.Millisecond, 10*time.Millisecond)
	return h
}

func newRelayV1(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(
		libp2p.DisableRelay(),
		libp2p.ForceReachabilityPublic(),
		libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
			for i, addr := range addrs {
				saddr := addr.String()
				if strings.HasPrefix(saddr, "/ip4/127.0.0.1/") {
					addrNoIP := strings.TrimPrefix(saddr, "/ip4/127.0.0.1")
					addrs[i] = ma.StringCast("/dns4/localhost" + addrNoIP)
				}
			}
			return addrs
		}),
	)
	require.NoError(t, err)
	r, err := relayv1.NewRelay(h)
	require.NoError(t, err)
	t.Cleanup(func() { r.Close() })
	return h
}

func TestSingleCandidate(t *testing.T) {
	var counter int
	h := newPrivateNode(t,
		autorelay.WithPeerSource(func(_ context.Context, num int) <-chan peer.AddrInfo {
			counter++
			require.Equal(t, 1, num)
			peerChan := make(chan peer.AddrInfo, num)
			defer close(peerChan)
			r := newRelay(t)
			t.Cleanup(func() { r.Close() })
			peerChan <- peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()}
			return peerChan
		}, time.Hour),
		autorelay.WithMaxCandidates(1),
		autorelay.WithNumRelays(99999),
		autorelay.WithBootDelay(0),
	)
	defer h.Close()

	require.Eventually(t, func() bool { return numRelays(h) > 0 }, 3*time.Second, 100*time.Millisecond)
	// test that we don't add any more relays
	require.Never(t, func() bool { return numRelays(h) > 1 }, 200*time.Millisecond, 50*time.Millisecond)
	require.Equal(t, 1, counter, "expected the peer source callback to only have been called once")
}

func TestSingleRelay(t *testing.T) {
	const numCandidates = 3
	var called bool
	peerChan := make(chan peer.AddrInfo, numCandidates)
	for i := 0; i < numCandidates; i++ {
		r := newRelay(t)
		t.Cleanup(func() { r.Close() })
		peerChan <- peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()}
	}
	close(peerChan)

	h := newPrivateNode(t,
		autorelay.WithPeerSource(func(_ context.Context, num int) <-chan peer.AddrInfo {
			require.False(t, called, "expected the peer source callback to only have been called once")
			called = true
			require.Equal(t, numCandidates, num)
			return peerChan
		}, time.Hour),
		autorelay.WithMaxCandidates(numCandidates),
		autorelay.WithNumRelays(1),
		autorelay.WithBootDelay(0),
	)
	defer h.Close()

	require.Eventually(t, func() bool { return numRelays(h) > 0 }, 5*time.Second, 100*time.Millisecond)
	// test that we don't add any more relays
	require.Never(t, func() bool { return numRelays(h) > 1 }, 200*time.Millisecond, 50*time.Millisecond)
}
func TestPreferRelayV2(t *testing.T) {
	r := newRelay(t)
	defer r.Close()
	// The relay supports both v1 and v2. The v1 stream handler should never be called,
	// if we prefer v2 relays.
	r.SetStreamHandler(relayv1.ProtoID, func(str network.Stream) {
		str.Reset()
		t.Fatal("used relay v1")
	})

	h := newPrivateNode(t,
		autorelay.WithPeerSource(func(context.Context, int) <-chan peer.AddrInfo {
			peerChan := make(chan peer.AddrInfo, 1)
			defer close(peerChan)
			peerChan <- peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()}
			return peerChan
		}, time.Hour),
		autorelay.WithMaxCandidates(1),
		autorelay.WithNumRelays(99999),
		autorelay.WithBootDelay(0),
	)
	defer h.Close()

	require.Eventually(t, func() bool { return numRelays(h) > 0 }, 3*time.Second, 100*time.Millisecond)
}

func TestWaitForCandidates(t *testing.T) {
	peerChan := make(chan peer.AddrInfo)
	h := newPrivateNode(t,
		autorelay.WithPeerSource(func(context.Context, int) <-chan peer.AddrInfo { return peerChan }, time.Hour),
		autorelay.WithMinCandidates(2),
		autorelay.WithNumRelays(1),
		autorelay.WithBootDelay(time.Hour),
	)
	defer h.Close()

	r1 := newRelay(t)
	t.Cleanup(func() { r1.Close() })
	peerChan <- peer.AddrInfo{ID: r1.ID(), Addrs: r1.Addrs()}

	// make sure we don't add any relays yet
	// We need to wait until we have at least 2 candidates before we connect.
	require.Never(t, func() bool { return numRelays(h) > 0 }, 200*time.Millisecond, 50*time.Millisecond)

	r2 := newRelay(t)
	t.Cleanup(func() { r2.Close() })
	peerChan <- peer.AddrInfo{ID: r2.ID(), Addrs: r2.Addrs()}
	require.Eventually(t, func() bool { return numRelays(h) > 0 }, 3*time.Second, 100*time.Millisecond)
}

func TestBackoff(t *testing.T) {
	const backoff = 20 * time.Second
	cl := clock.NewMock()
	r, err := libp2p.New(
		libp2p.DisableRelay(),
		libp2p.ForceReachabilityPublic(),
		libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
			for i, addr := range addrs {
				saddr := addr.String()
				if strings.HasPrefix(saddr, "/ip4/127.0.0.1/") {
					addrNoIP := strings.TrimPrefix(saddr, "/ip4/127.0.0.1")
					addrs[i] = ma.StringCast("/dns4/localhost" + addrNoIP)
				}
			}
			return addrs
		}),
	)
	require.NoError(t, err)
	defer r.Close()
	var reservations int32
	r.SetStreamHandler(protoIDv2, func(str network.Stream) {
		atomic.AddInt32(&reservations, 1)
		str.Reset()
	})

	var counter int32 // to be used atomically
	h := newPrivateNode(t,
		autorelay.WithPeerSource(func(context.Context, int) <-chan peer.AddrInfo {
			// always return the same node, and make sure we don't try to connect to it too frequently
			atomic.AddInt32(&counter, 1)
			peerChan := make(chan peer.AddrInfo, 1)
			peerChan <- peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()}
			close(peerChan)
			return peerChan
		}, time.Second),
		autorelay.WithNumRelays(1),
		autorelay.WithBootDelay(0),
		autorelay.WithBackoff(backoff),
		autorelay.WithClock(cl),
	)
	defer h.Close()

	require.Eventually(t, func() bool { return atomic.LoadInt32(&reservations) == 1 }, 3*time.Second, 20*time.Millisecond)
	// make sure we don't add any relays yet
	for i := 0; i < 2; i++ {
		cl.Add(backoff / 3)
		require.Equal(t, 1, int(atomic.LoadInt32(&reservations)))
	}
	cl.Add(backoff / 2)
	require.Eventually(t, func() bool { return atomic.LoadInt32(&reservations) == 2 }, 3*time.Second, 20*time.Millisecond)
	require.Less(t, int(atomic.LoadInt32(&counter)), 100) // just make sure we're not busy-looping
	require.Equal(t, 2, int(atomic.LoadInt32(&reservations)))
}

func TestStaticRelays(t *testing.T) {
	const numStaticRelays = 3
	var staticRelays []peer.AddrInfo
	for i := 0; i < numStaticRelays; i++ {
		r := newRelay(t)
		t.Cleanup(func() { r.Close() })
		staticRelays = append(staticRelays, peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()})
	}

	h := newPrivateNode(t,
		autorelay.WithStaticRelays(staticRelays),
		autorelay.WithNumRelays(1),
	)
	defer h.Close()

	require.Eventually(t, func() bool { return numRelays(h) > 0 }, 2*time.Second, 50*time.Millisecond)
}

func TestRelayV1(t *testing.T) {
	t.Run("relay v1 support disabled", func(t *testing.T) {
		peerChan := make(chan peer.AddrInfo, 1)
		r := newRelayV1(t)
		t.Cleanup(func() { r.Close() })
		peerChan <- peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()}
		close(peerChan)

		h := newPrivateNode(t,
			autorelay.WithPeerSource(func(context.Context, int) <-chan peer.AddrInfo { return peerChan }, time.Hour),
			autorelay.WithBootDelay(0),
		)
		defer h.Close()

		require.Never(t, func() bool { return numRelays(h) > 0 }, 250*time.Millisecond, 100*time.Millisecond)
	})

	t.Run("relay v1 support enabled", func(t *testing.T) {
		peerChan := make(chan peer.AddrInfo, 1)
		r := newRelayV1(t)
		t.Cleanup(func() { r.Close() })
		peerChan <- peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()}
		close(peerChan)

		h := newPrivateNode(t,
			autorelay.WithPeerSource(func(context.Context, int) <-chan peer.AddrInfo { return peerChan }, time.Hour),
			autorelay.WithBootDelay(0),
			autorelay.WithCircuitV1Support(),
		)
		defer h.Close()

		require.Eventually(t, func() bool { return numRelays(h) > 0 }, 3*time.Second, 100*time.Millisecond)
	})
}

func TestConnectOnDisconnect(t *testing.T) {
	const num = 3
	peerChan := make(chan peer.AddrInfo, num)
	relays := make([]host.Host, 0, num)
	for i := 0; i < 3; i++ {
		r := newRelay(t)
		t.Cleanup(func() { r.Close() })
		peerChan <- peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()}
		relays = append(relays, r)
	}
	h := newPrivateNode(t,
		autorelay.WithPeerSource(func(context.Context, int) <-chan peer.AddrInfo { return peerChan }, time.Hour),
		autorelay.WithMinCandidates(1),
		autorelay.WithMaxCandidates(num),
		autorelay.WithNumRelays(1),
		autorelay.WithBootDelay(0),
	)
	defer h.Close()

	require.Eventually(t, func() bool { return numRelays(h) > 0 }, 3*time.Second, 100*time.Millisecond)
	relaysInUse := usedRelays(h)
	require.Len(t, relaysInUse, 1)
	oldRelay := relaysInUse[0]

	for _, r := range relays {
		if r.ID() == oldRelay {
			r.Close()
		}
	}

	require.Eventually(t, func() bool { return numRelays(h) > 0 }, 3*time.Second, 100*time.Millisecond)
	relaysInUse = usedRelays(h)
	require.Len(t, relaysInUse, 1)
	require.NotEqualf(t, oldRelay, relaysInUse[0], "old relay should not be used again")
}

func TestMaxAge(t *testing.T) {
	cl := clock.NewMock()

	const num = 4
	peerChan1 := make(chan peer.AddrInfo, num)
	peerChan2 := make(chan peer.AddrInfo, num)
	relays1 := make([]host.Host, 0, num)
	relays2 := make([]host.Host, 0, num)
	for i := 0; i < num; i++ {
		r1 := newRelay(t)
		t.Cleanup(func() { r1.Close() })
		peerChan1 <- peer.AddrInfo{ID: r1.ID(), Addrs: r1.Addrs()}
		relays1 = append(relays1, r1)
		r2 := newRelay(t)
		t.Cleanup(func() { r2.Close() })
		relays2 = append(relays2, r2)
	}
	close(peerChan1)
	peerChans := make(chan chan peer.AddrInfo, 2)
	peerChans <- peerChan1
	peerChans <- peerChan2
	close(peerChans)

	h := newPrivateNode(t,
		autorelay.WithPeerSource(func(context.Context, int) <-chan peer.AddrInfo {
			c, ok := <-peerChans
			if !ok {
				t.Fatal("unexpected call to PeerSource")
			}
			return c
		}, time.Second),
		autorelay.WithNumRelays(1),
		autorelay.WithMaxCandidates(100),
		autorelay.WithBootDelay(0),
		autorelay.WithMaxCandidateAge(20*time.Minute),
		autorelay.WithClock(cl),
	)
	defer h.Close()

	require.Eventually(t, func() bool { return numRelays(h) > 0 }, 3*time.Second, 100*time.Millisecond)
	relays := usedRelays(h)
	require.Len(t, relays, 1)

	waitFor := 500 * time.Millisecond
	tick := 100 * time.Millisecond
	if os.Getenv("CI") != "" {
		// Only increase the waitFor since we are increasing the mock clock every tick.
		waitFor *= 10
	}
	require.Eventually(t, func() bool {
		// we don't know exactly when the timer is reset, just advance our timer multiple times if necessary
		cl.Add(time.Second)
		return len(peerChans) == 0
	}, waitFor, tick)

	cl.Add(10 * time.Minute)
	for _, r := range relays2 {
		peerChan2 <- peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()}
	}
	cl.Add(11 * time.Minute)
	// by now the 3 relays should have been garbage collected
	var oldRelay peer.ID
	for _, r := range relays1 {
		if r.ID() == relays[0] {
			oldRelay = r.ID()
			r.Close()
		}
	}
	require.NotEmpty(t, oldRelay)

	require.Eventually(t, func() bool {
		relays = usedRelays(h)
		if len(relays) != 1 {
			return false
		}
		return relays[0] != oldRelay
	}, 3*time.Second, 100*time.Millisecond)

	require.Len(t, relays, 1)
	ids := make([]peer.ID, 0, len(relays2))
	for _, r := range relays2 {
		ids = append(ids, r.ID())
	}
	require.Contains(t, ids, relays[0])
}
