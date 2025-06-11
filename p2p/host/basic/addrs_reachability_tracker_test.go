package basichost

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeManager(t *testing.T) {
	pub1 := ma.StringCast("/ip4/1.1.1.1/tcp/1")
	pub2 := ma.StringCast("/ip4/1.1.1.2/tcp/1")
	pub3 := ma.StringCast("/ip4/1.1.1.3/tcp/1")

	cl := clock.NewMock()

	nextProbe := func(pm *probeManager) []autonatv2.Request {
		reqs := pm.GetProbe()
		if len(reqs) != 0 {
			pm.MarkProbeInProgress(reqs)
		}
		return reqs
	}

	makeNewProbeManager := func(addrs []ma.Multiaddr) *probeManager {
		pm := newProbeManager(cl.Now)
		pm.UpdateAddrs(addrs)
		return pm
	}

	t.Run("addrs updates", func(t *testing.T) {
		pm := newProbeManager(cl.Now)
		pm.UpdateAddrs([]ma.Multiaddr{pub1, pub2})
		for {
			reqs := nextProbe(pm)
			if len(reqs) == 0 {
				break
			}
			pm.CompleteProbe(reqs, autonatv2.Result{Addr: reqs[0].Addr, Idx: 0, Reachability: network.ReachabilityPublic}, nil)
		}
		reachable, _, _ := pm.AppendConfirmedAddrs(nil, nil, nil)
		require.Equal(t, reachable, []ma.Multiaddr{pub1, pub2})
		pm.UpdateAddrs([]ma.Multiaddr{pub3})

		reachable, _, _ = pm.AppendConfirmedAddrs(nil, nil, nil)
		require.Empty(t, reachable)
		require.Len(t, pm.statuses, 1)
	})

	t.Run("inprogress", func(t *testing.T) {
		pm := makeNewProbeManager([]ma.Multiaddr{pub1, pub2})
		reqs1 := pm.GetProbe()
		reqs2 := pm.GetProbe()
		require.Equal(t, reqs1, reqs2)
		for range targetConfidence {
			reqs := nextProbe(pm)
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}, {Addr: pub2, SendDialData: true}})
		}
		for range targetConfidence {
			reqs := nextProbe(pm)
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub2, SendDialData: true}, {Addr: pub1, SendDialData: true}})
		}
		reqs := pm.GetProbe()
		require.Empty(t, reqs)
	})

	t.Run("refusals", func(t *testing.T) {
		pm := makeNewProbeManager([]ma.Multiaddr{pub1, pub2})
		var probes [][]autonatv2.Request
		for range targetConfidence {
			reqs := nextProbe(pm)
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}, {Addr: pub2, SendDialData: true}})
			probes = append(probes, reqs)
		}
		// first one refused second one successful
		for _, p := range probes {
			pm.CompleteProbe(p, autonatv2.Result{Addr: pub2, Idx: 1, Reachability: network.ReachabilityPublic}, nil)
		}
		// the second address is validated!
		probes = nil
		for range targetConfidence {
			reqs := nextProbe(pm)
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}})
			probes = append(probes, reqs)
		}
		reqs := pm.GetProbe()
		require.Empty(t, reqs)
		for _, p := range probes {
			pm.CompleteProbe(p, autonatv2.Result{AllAddrsRefused: true}, nil)
		}
		// all requests refused; no more probes for too many refusals
		reqs = pm.GetProbe()
		require.Empty(t, reqs)

		cl.Add(recentProbeInterval)
		reqs = pm.GetProbe()
		require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}})
	})

	t.Run("successes", func(t *testing.T) {
		pm := makeNewProbeManager([]ma.Multiaddr{pub1, pub2})
		for j := 0; j < 2; j++ {
			for i := 0; i < targetConfidence; i++ {
				reqs := nextProbe(pm)
				pm.CompleteProbe(reqs, autonatv2.Result{Addr: reqs[0].Addr, Idx: 0, Reachability: network.ReachabilityPublic}, nil)
			}
		}
		// all addrs confirmed
		reqs := pm.GetProbe()
		require.Empty(t, reqs)

		cl.Add(highConfidenceAddrProbeInterval + time.Millisecond)
		reqs = nextProbe(pm)
		require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}, {Addr: pub2, SendDialData: true}})
		reqs = nextProbe(pm)
		require.Equal(t, reqs, []autonatv2.Request{{Addr: pub2, SendDialData: true}, {Addr: pub1, SendDialData: true}})
	})

	t.Run("throttling on indeterminate reachability", func(t *testing.T) {
		pm := makeNewProbeManager([]ma.Multiaddr{pub1, pub2})
		reachability := network.ReachabilityPublic
		nextReachability := func() network.Reachability {
			if reachability == network.ReachabilityPublic {
				reachability = network.ReachabilityPrivate
			} else {
				reachability = network.ReachabilityPublic
			}
			return reachability
		}
		// both addresses are indeterminate
		for range 2 * maxRecentDialsPerAddr {
			reqs := nextProbe(pm)
			pm.CompleteProbe(reqs, autonatv2.Result{Addr: reqs[0].Addr, Idx: 0, Reachability: nextReachability()}, nil)
		}
		reqs := pm.GetProbe()
		require.Empty(t, reqs)

		cl.Add(recentProbeInterval + time.Millisecond)
		reqs = pm.GetProbe()
		require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}, {Addr: pub2, SendDialData: true}})
		for range 2 * maxRecentDialsPerAddr {
			reqs := nextProbe(pm)
			pm.CompleteProbe(reqs, autonatv2.Result{Addr: reqs[0].Addr, Idx: 0, Reachability: nextReachability()}, nil)
		}
		reqs = pm.GetProbe()
		require.Empty(t, reqs)
	})

	t.Run("reachabilityUpdate", func(t *testing.T) {
		pm := makeNewProbeManager([]ma.Multiaddr{pub1, pub2})
		for range 2 * targetConfidence {
			reqs := nextProbe(pm)
			if reqs[0].Addr.Equal(pub1) {
				pm.CompleteProbe(reqs, autonatv2.Result{Addr: pub1, Idx: 0, Reachability: network.ReachabilityPublic}, nil)
			} else {
				pm.CompleteProbe(reqs, autonatv2.Result{Addr: pub2, Idx: 0, Reachability: network.ReachabilityPrivate}, nil)
			}
		}

		reachable, unreachable, _ := pm.AppendConfirmedAddrs(nil, nil, nil)
		require.Equal(t, reachable, []ma.Multiaddr{pub1})
		require.Equal(t, unreachable, []ma.Multiaddr{pub2})
	})
	t.Run("expiry", func(t *testing.T) {
		pm := makeNewProbeManager([]ma.Multiaddr{pub1})
		for range 2 * targetConfidence {
			reqs := nextProbe(pm)
			pm.CompleteProbe(reqs, autonatv2.Result{Addr: pub1, Idx: 0, Reachability: network.ReachabilityPublic}, nil)
		}

		reachable, unreachable, _ := pm.AppendConfirmedAddrs(nil, nil, nil)
		require.Equal(t, reachable, []ma.Multiaddr{pub1})
		require.Empty(t, unreachable)

		cl.Add(maxProbeResultTTL + 1*time.Second)
		reachable, unreachable, _ = pm.AppendConfirmedAddrs(nil, nil, nil)
		require.Empty(t, reachable)
		require.Empty(t, unreachable)
	})
}

type mockAutoNATClient struct {
	F func(context.Context, []autonatv2.Request) (autonatv2.Result, error)
}

func (m mockAutoNATClient) GetReachability(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
	return m.F(ctx, reqs)
}

var _ autonatv2Client = mockAutoNATClient{}

func TestAddrsReachabilityTracker(t *testing.T) {
	pub1 := ma.StringCast("/ip4/1.1.1.1/tcp/1")
	pub2 := ma.StringCast("/ip4/1.1.1.2/tcp/1")
	pub3 := ma.StringCast("/ip4/1.1.1.3/tcp/1")
	pri := ma.StringCast("/ip4/192.168.1.1/tcp/1")

	assertFirstEvent := func(t *testing.T, tr *addrsReachabilityTracker, addrs []ma.Multiaddr) {
		select {
		case <-tr.reachabilityUpdateCh:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("expected first event quickly")
		}
		reachable, unreachable, unknown := tr.ConfirmedAddrs()
		require.Empty(t, reachable)
		require.Empty(t, unreachable)
		require.ElementsMatch(t, unknown, addrs, "%s %s", unknown, addrs)
	}

	newTracker := func(cli mockAutoNATClient, cl clock.Clock) *addrsReachabilityTracker {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if cl == nil {
			cl = clock.New()
		}
		tr := &addrsReachabilityTracker{
			ctx:                  ctx,
			cancel:               cancel,
			client:               cli,
			newAddrs:             make(chan []ma.Multiaddr, 1),
			reachabilityUpdateCh: make(chan struct{}, 1),
			maxConcurrency:       3,
			newAddrsProbeDelay:   0 * time.Second,
			probeManager:         newProbeManager(cl.Now),
			clock:                cl,
		}
		err := tr.Start()
		require.NoError(t, err)
		t.Cleanup(func() {
			err := tr.Close()
			assert.NoError(t, err)
		})
		return tr
	}

	t.Run("simple", func(t *testing.T) {
		// pub1 reachable, pub2 unreachable, pub3 ignored
		mockClient := mockAutoNATClient{
			F: func(_ context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				for i, req := range reqs {
					if req.Addr.Equal(pub1) {
						return autonatv2.Result{Addr: pub1, Idx: i, Reachability: network.ReachabilityPublic}, nil
					} else if req.Addr.Equal(pub2) {
						return autonatv2.Result{Addr: pub2, Idx: i, Reachability: network.ReachabilityPrivate}, nil
					}
				}
				return autonatv2.Result{AllAddrsRefused: true}, nil
			},
		}
		tr := newTracker(mockClient, nil)
		tr.UpdateAddrs([]ma.Multiaddr{pub2, pub1, pri})
		assertFirstEvent(t, tr, []ma.Multiaddr{pub1, pub2})

		select {
		case <-tr.reachabilityUpdateCh:
		case <-time.After(2 * time.Second):
			t.Fatal("expected reachability update")
		}
		reachable, unreachable, unknown := tr.ConfirmedAddrs()
		require.Equal(t, reachable, []ma.Multiaddr{pub1}, "%s %s", reachable, pub1)
		require.Equal(t, unreachable, []ma.Multiaddr{pub2}, "%s %s", unreachable, pub2)
		require.Empty(t, unknown)

		tr.UpdateAddrs([]ma.Multiaddr{pub3, pub1, pub2, pri})
		select {
		case <-tr.reachabilityUpdateCh:
		case <-time.After(2 * time.Second):
			t.Fatal("expected reachability update")
		}
		reachable, unreachable, unknown = tr.ConfirmedAddrs()
		t.Logf("Second probe - Reachable: %v, Unreachable: %v, Unknown: %v", reachable, unreachable, unknown)
		require.Equal(t, reachable, []ma.Multiaddr{pub1}, "%s %s", reachable, pub1)
		require.Equal(t, unreachable, []ma.Multiaddr{pub2}, "%s %s", unreachable, pub2)
		require.Equal(t, unknown, []ma.Multiaddr{pub3}, "%s %s", unknown, pub3)
	})

	t.Run("confirmed addrs ordering", func(t *testing.T) {
		mockClient := mockAutoNATClient{
			F: func(_ context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				return autonatv2.Result{Addr: reqs[0].Addr, Idx: 0, Reachability: network.ReachabilityPublic}, nil
			},
		}
		tr := newTracker(mockClient, nil)
		var addrs []ma.Multiaddr
		for i := 0; i < 10; i++ {
			addrs = append(addrs, ma.StringCast(fmt.Sprintf("/ip4/1.1.1.1/tcp/%d", i)))
		}
		slices.SortFunc(addrs, func(a, b ma.Multiaddr) int { return -a.Compare(b) }) // sort in reverse order
		tr.UpdateAddrs(addrs)
		assertFirstEvent(t, tr, addrs)

		select {
		case <-tr.reachabilityUpdateCh:
		case <-time.After(2 * time.Second):
			t.Fatal("expected reachability update")
		}
		reachable, unreachable, _ := tr.ConfirmedAddrs()
		require.Empty(t, unreachable)

		orderedAddrs := slices.Clone(addrs)
		slices.Reverse(orderedAddrs)
		require.Equal(t, reachable, orderedAddrs, "%s %s", reachable, addrs)
	})

	t.Run("backoff", func(t *testing.T) {
		notify := make(chan struct{}, 1)
		drainNotify := func() bool {
			found := false
			for {
				select {
				case <-notify:
					found = true
				default:
					return found
				}
			}
		}

		var allow atomic.Bool
		mockClient := mockAutoNATClient{
			F: func(_ context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				select {
				case notify <- struct{}{}:
				default:
				}
				if !allow.Load() {
					return autonatv2.Result{}, autonatv2.ErrNoPeers
				}
				if reqs[0].Addr.Equal(pub1) {
					return autonatv2.Result{Addr: pub1, Idx: 0, Reachability: network.ReachabilityPublic}, nil
				}
				return autonatv2.Result{AllAddrsRefused: true}, nil
			},
		}

		cl := clock.NewMock()
		tr := newTracker(mockClient, cl)

		// update addrs and wait for initial checks
		tr.UpdateAddrs([]ma.Multiaddr{pub1})
		assertFirstEvent(t, tr, []ma.Multiaddr{pub1})
		// need to update clock after the background goroutine processes the new addrs
		time.Sleep(100 * time.Millisecond)
		cl.Add(1)
		time.Sleep(100 * time.Millisecond)
		require.True(t, drainNotify()) // check that we did receive probes

		backoffInterval := backoffStartInterval
		for i := 0; i < 4; i++ {
			drainNotify()
			cl.Add(backoffInterval / 2)
			select {
			case <-notify:
				t.Fatal("unexpected call")
			case <-time.After(50 * time.Millisecond):
			}
			cl.Add(backoffInterval/2 + 1) // +1 to push it slightly over the backoff interval
			backoffInterval *= 2
			select {
			case <-notify:
			case <-time.After(1 * time.Second):
				t.Fatal("expected probe")
			}
			reachable, unreachable, _ := tr.ConfirmedAddrs()
			require.Empty(t, reachable)
			require.Empty(t, unreachable)
		}
		allow.Store(true)
		drainNotify()
		cl.Add(backoffInterval + 1)
		select {
		case <-tr.reachabilityUpdateCh:
		case <-time.After(1 * time.Second):
			t.Fatal("unexpected reachability update")
		}
		reachable, unreachable, _ := tr.ConfirmedAddrs()
		require.Equal(t, reachable, []ma.Multiaddr{pub1})
		require.Empty(t, unreachable)
	})

	t.Run("event update", func(t *testing.T) {
		// allow minConfidence probes to pass
		called := make(chan struct{}, minConfidence)
		notify := make(chan struct{})
		mockClient := mockAutoNATClient{
			F: func(_ context.Context, _ []autonatv2.Request) (autonatv2.Result, error) {
				select {
				case called <- struct{}{}:
					notify <- struct{}{}
					return autonatv2.Result{Addr: pub1, Idx: 0, Reachability: network.ReachabilityPublic}, nil
				default:
					return autonatv2.Result{AllAddrsRefused: true}, nil
				}
			},
		}

		tr := newTracker(mockClient, nil)
		tr.UpdateAddrs([]ma.Multiaddr{pub1})
		assertFirstEvent(t, tr, []ma.Multiaddr{pub1})

		for i := 0; i < minConfidence; i++ {
			select {
			case <-notify:
			case <-time.After(1 * time.Second):
				t.Fatal("expected call to autonat client")
			}
		}
		select {
		case <-tr.reachabilityUpdateCh:
			reachable, unreachable, _ := tr.ConfirmedAddrs()
			require.Equal(t, reachable, []ma.Multiaddr{pub1})
			require.Empty(t, unreachable)
		case <-time.After(1 * time.Second):
			t.Fatal("expected reachability update")
		}
		tr.UpdateAddrs([]ma.Multiaddr{pub1}) // same addrs shouldn't get update
		select {
		case <-tr.reachabilityUpdateCh:
			t.Fatal("didn't expect reachability update")
		case <-time.After(100 * time.Millisecond):
		}
		tr.UpdateAddrs([]ma.Multiaddr{pub2})
		select {
		case <-tr.reachabilityUpdateCh:
			reachable, unreachable, _ := tr.ConfirmedAddrs()
			require.Empty(t, reachable)
			require.Empty(t, unreachable)
		case <-time.After(1 * time.Second):
			t.Fatal("expected reachability update")
		}
	})

	t.Run("refresh after reset interval", func(t *testing.T) {
		notify := make(chan struct{}, 1)
		drainNotify := func() bool {
			found := false
			for {
				select {
				case <-notify:
					found = true
				default:
					return found
				}
			}
		}

		mockClient := mockAutoNATClient{
			F: func(_ context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				select {
				case notify <- struct{}{}:
				default:
				}
				if reqs[0].Addr.Equal(pub1) {
					return autonatv2.Result{Addr: pub1, Idx: 0, Reachability: network.ReachabilityPublic}, nil
				}
				return autonatv2.Result{AllAddrsRefused: true}, nil
			},
		}

		cl := clock.NewMock()
		tr := newTracker(mockClient, cl)

		// update addrs and wait for initial checks
		tr.UpdateAddrs([]ma.Multiaddr{pub1})
		assertFirstEvent(t, tr, []ma.Multiaddr{pub1})
		// need to update clock after the background goroutine processes the new addrs
		time.Sleep(100 * time.Millisecond)
		cl.Add(1)
		time.Sleep(100 * time.Millisecond)
		require.True(t, drainNotify()) // check that we did receive probes
		cl.Add(highConfidenceAddrProbeInterval / 2)
		select {
		case <-notify:
			t.Fatal("unexpected call")
		case <-time.After(50 * time.Millisecond):
		}

		cl.Add(highConfidenceAddrProbeInterval/2 + defaultReachabilityRefreshInterval) // defaultResetInterval for the next probe time
		select {
		case <-notify:
		case <-time.After(1 * time.Second):
			t.Fatal("expected probe")
		}
	})
}

func TestRefreshReachability(t *testing.T) {
	pub1 := ma.StringCast("/ip4/1.1.1.1/tcp/1")
	pub2 := ma.StringCast("/ip4/1.1.1.1/tcp/2")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	newTracker := func(client autonatv2Client, pm *probeManager) *addrsReachabilityTracker {
		return &addrsReachabilityTracker{
			probeManager:   pm,
			client:         client,
			clock:          clock.New(),
			maxConcurrency: 3,
			ctx:            ctx,
			cancel:         cancel,
		}
	}
	t.Run("backoff on ErrNoValidPeers", func(t *testing.T) {
		mockClient := mockAutoNATClient{
			F: func(_ context.Context, _ []autonatv2.Request) (autonatv2.Result, error) {
				return autonatv2.Result{}, autonatv2.ErrNoPeers
			},
		}

		addrTracker := newProbeManager(time.Now)
		addrTracker.UpdateAddrs([]ma.Multiaddr{pub1})
		r := newTracker(mockClient, addrTracker)
		res := r.refreshReachability()
		require.True(t, <-res.BackoffCh)
		require.Equal(t, addrTracker.InProgressProbes(), 0)
	})

	t.Run("returns backoff on errTooManyConsecutiveFailures", func(t *testing.T) {
		// Create a client that always returns ErrDialRefused
		mockClient := mockAutoNATClient{
			F: func(_ context.Context, _ []autonatv2.Request) (autonatv2.Result, error) {
				return autonatv2.Result{}, errors.New("test error")
			},
		}

		pm := newProbeManager(time.Now)
		pm.UpdateAddrs([]ma.Multiaddr{pub1})
		r := newTracker(mockClient, pm)
		result := r.refreshReachability()
		require.True(t, <-result.BackoffCh)
		require.Equal(t, pm.InProgressProbes(), 0)
	})

	t.Run("quits on cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		block := make(chan struct{})
		mockClient := mockAutoNATClient{
			F: func(_ context.Context, _ []autonatv2.Request) (autonatv2.Result, error) {
				block <- struct{}{}
				return autonatv2.Result{}, nil
			},
		}

		pm := newProbeManager(time.Now)
		pm.UpdateAddrs([]ma.Multiaddr{pub1})
		r := &addrsReachabilityTracker{
			ctx:          ctx,
			cancel:       cancel,
			client:       mockClient,
			probeManager: pm,
			clock:        clock.New(),
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := r.refreshReachability()
			assert.False(t, <-result.BackoffCh)
			assert.Equal(t, pm.InProgressProbes(), 0)
		}()

		cancel()
		time.Sleep(50 * time.Millisecond) // wait for the cancellation to be processed

	outer:
		for i := 0; i < defaultMaxConcurrency; i++ {
			select {
			case <-block:
			default:
				break outer
			}
		}
		select {
		case <-block:
			t.Fatal("expected no more requests")
		case <-time.After(50 * time.Millisecond):
		}
		wg.Wait()
	})

	t.Run("handles refusals", func(t *testing.T) {
		pub1, _ := ma.NewMultiaddr("/ip4/1.1.1.1/tcp/1")

		mockClient := mockAutoNATClient{
			F: func(_ context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				for i, req := range reqs {
					if req.Addr.Equal(pub1) {
						return autonatv2.Result{Addr: pub1, Idx: i, Reachability: network.ReachabilityPublic}, nil
					}
				}
				return autonatv2.Result{AllAddrsRefused: true}, nil
			},
		}

		pm := newProbeManager(time.Now)
		pm.UpdateAddrs([]ma.Multiaddr{pub2, pub1})
		r := newTracker(mockClient, pm)

		result := r.refreshReachability()
		require.False(t, <-result.BackoffCh)

		reachable, unreachable, _ := pm.AppendConfirmedAddrs(nil, nil, nil)
		require.Equal(t, reachable, []ma.Multiaddr{pub1})
		require.Empty(t, unreachable)
		require.Equal(t, pm.InProgressProbes(), 0)
	})

	t.Run("handles completions", func(t *testing.T) {
		mockClient := mockAutoNATClient{
			F: func(_ context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				for i, req := range reqs {
					if req.Addr.Equal(pub1) {
						return autonatv2.Result{Addr: pub1, Idx: i, Reachability: network.ReachabilityPublic}, nil
					}
					if req.Addr.Equal(pub2) {
						return autonatv2.Result{Addr: pub2, Idx: i, Reachability: network.ReachabilityPrivate}, nil
					}
				}
				return autonatv2.Result{AllAddrsRefused: true}, nil
			},
		}
		pm := newProbeManager(time.Now)
		pm.UpdateAddrs([]ma.Multiaddr{pub2, pub1})
		r := newTracker(mockClient, pm)
		result := r.refreshReachability()
		require.False(t, <-result.BackoffCh)

		reachable, unreachable, _ := pm.AppendConfirmedAddrs(nil, nil, nil)
		require.Equal(t, reachable, []ma.Multiaddr{pub1})
		require.Equal(t, unreachable, []ma.Multiaddr{pub2})
		require.Equal(t, pm.InProgressProbes(), 0)
	})
}

func TestAddrStatusProbeCount(t *testing.T) {
	cases := []struct {
		inputs             string
		wantRequiredProbes int
		wantReachability   network.Reachability
	}{
		{
			inputs:             "",
			wantRequiredProbes: 3,
			wantReachability:   network.ReachabilityUnknown,
		},
		{
			inputs:             "S",
			wantRequiredProbes: 2,
			wantReachability:   network.ReachabilityUnknown,
		},
		{
			inputs:             "SS",
			wantRequiredProbes: 1,
			wantReachability:   network.ReachabilityPublic,
		},
		{
			inputs:             "SSS",
			wantRequiredProbes: 0,
			wantReachability:   network.ReachabilityPublic,
		},
		{
			inputs:             "SSSSSSSF",
			wantRequiredProbes: 1,
			wantReachability:   network.ReachabilityPublic,
		},
		{
			inputs:             "SFSFSSSS",
			wantRequiredProbes: 0,
			wantReachability:   network.ReachabilityPublic,
		},
		{
			inputs:             "SSSSSFSF",
			wantRequiredProbes: 2,
			wantReachability:   network.ReachabilityUnknown,
		},
		{
			inputs:             "FF",
			wantRequiredProbes: 1,
			wantReachability:   network.ReachabilityPrivate,
		},
	}
	for _, c := range cases {
		t.Run(c.inputs, func(t *testing.T) {
			now := time.Time{}.Add(1 * time.Second)
			ao := addrStatus{}
			for _, r := range c.inputs {
				if r == 'S' {
					ao.AddOutcome(now, network.ReachabilityPublic, 5)
				} else {
					ao.AddOutcome(now, network.ReachabilityPrivate, 5)
				}
				now = now.Add(1 * time.Second)
			}
			require.Equal(t, ao.RequiredProbeCount(now), c.wantRequiredProbes)
			require.Equal(t, ao.Reachability(), c.wantReachability)
			if c.wantRequiredProbes == 0 {
				now = now.Add(highConfidenceAddrProbeInterval + 10*time.Microsecond)
				require.Equal(t, ao.RequiredProbeCount(now), 1)
			}

			now = now.Add(1 * time.Second)
			ao.RemoveBefore(now)
			require.Len(t, ao.outcomes, 0)
		})
	}
}

func BenchmarkAddrTracker(b *testing.B) {
	cl := clock.NewMock()
	t := newProbeManager(cl.Now)

	addrs := make([]ma.Multiaddr, 20)
	for i := range addrs {
		addrs[i] = ma.StringCast(fmt.Sprintf("/ip4/1.1.1.1/tcp/%d", rand.Intn(1000)))
	}
	t.UpdateAddrs(addrs)
	b.ReportAllocs()
	b.ResetTimer()
	p := t.GetProbe()
	for i := 0; i < b.N; i++ {
		pp := t.GetProbe()
		if len(pp) == 0 {
			pp = p
		}
		t.MarkProbeInProgress(pp)
		t.CompleteProbe(pp, autonatv2.Result{Addr: pp[0].Addr, Idx: 0, Reachability: network.ReachabilityPublic}, nil)
	}
}

func FuzzAddrsReachabilityTracker(f *testing.F) {
	type autonatv2Response struct {
		Result autonatv2.Result
		Err    error
	}

	newMockClient := func(b []byte) mockAutoNATClient {
		count := 0
		return mockAutoNATClient{
			F: func(_ context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				if len(b) == 0 {
					return autonatv2.Result{}, nil
				}
				count = (count + 1) % len(b)
				if b[count]%3 == 0 {
					// some address confirmed
					c1 := (count + 1) % len(b)
					c2 := (count + 2) % len(b)
					rch := network.Reachability(b[c1] % 3)
					n := int(b[c2]) % len(reqs)
					return autonatv2.Result{
						Addr:         reqs[n].Addr,
						Idx:          n,
						Reachability: rch,
					}, nil
				}
				outcomes := []autonatv2Response{
					{Result: autonatv2.Result{AllAddrsRefused: true}},
					{Err: errors.New("test error")},
					{Err: autonatv2.ErrPrivateAddrs},
					{Err: autonatv2.ErrNoPeers},
					{Result: autonatv2.Result{}, Err: nil},
					{Result: autonatv2.Result{Addr: reqs[0].Addr, Idx: 0, Reachability: network.ReachabilityPublic}},
					{Result: autonatv2.Result{
						Addr:            reqs[0].Addr,
						Idx:             0,
						Reachability:    network.ReachabilityPublic,
						AllAddrsRefused: true,
					}},
					{Result: autonatv2.Result{
						Addr:            reqs[0].Addr,
						Idx:             len(reqs) - 1, // invalid idx
						Reachability:    network.ReachabilityPublic,
						AllAddrsRefused: false,
					}},
				}
				outcome := outcomes[int(b[count])%len(outcomes)]
				return outcome.Result, outcome.Err
			},
		}
	}

	// TODO: Move this to go-multiaddrs
	getProto := func(protos []byte) ma.Multiaddr {
		protoType := 0
		if len(protos) > 0 {
			protoType = int(protos[0])
		}

		port1, port2 := 0, 0
		if len(protos) > 1 {
			port1 = int(protos[1])
		}
		if len(protos) > 2 {
			port2 = int(protos[2])
		}
		protoTemplates := []string{
			"/tcp/%d/",
			"/udp/%d/",
			"/udp/%d/quic-v1/",
			"/udp/%d/quic-v1/tcp/%d",
			"/udp/%d/quic-v1/webtransport/",
			"/udp/%d/webrtc/",
			"/udp/%d/webrtc-direct/",
			"/unix/hello/",
		}
		s := protoTemplates[protoType%len(protoTemplates)]
		port1 %= (1 << 16)
		if strings.Count(s, "%d") == 1 {
			return ma.StringCast(fmt.Sprintf(s, port1))
		}
		port2 %= (1 << 16)
		return ma.StringCast(fmt.Sprintf(s, port1, port2))
	}

	getIP := func(ips []byte) ma.Multiaddr {
		ipType := 0
		if len(ips) > 0 {
			ipType = int(ips[0])
		}
		ips = ips[1:]
		var x, y int64
		split := 128 / 8
		if len(ips) < split {
			split = len(ips)
		}
		var b [8]byte
		copy(b[:], ips[:split])
		x = int64(binary.LittleEndian.Uint64(b[:]))
		clear(b[:])
		copy(b[:], ips[split:])
		y = int64(binary.LittleEndian.Uint64(b[:]))

		var ip netip.Addr
		switch ipType % 3 {
		case 0:
			ip = netip.AddrFrom4([4]byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)})
			return ma.StringCast(fmt.Sprintf("/ip4/%s/", ip))
		case 1:
			pubIP := net.ParseIP("2005::") // Public IP address
			x := int64(binary.LittleEndian.Uint64(pubIP[0:8]))
			ip = netip.AddrFrom16([16]byte{
				byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24),
				byte(x >> 32), byte(x >> 40), byte(x >> 48), byte(x >> 56),
				byte(y), byte(y >> 8), byte(y >> 16), byte(y >> 24),
				byte(y >> 32), byte(y >> 40), byte(y >> 48), byte(y >> 56),
			})
			return ma.StringCast(fmt.Sprintf("/ip6/%s/", ip))
		default:
			ip := netip.AddrFrom16([16]byte{
				byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24),
				byte(x >> 32), byte(x >> 40), byte(x >> 48), byte(x >> 56),
				byte(y), byte(y >> 8), byte(y >> 16), byte(y >> 24),
				byte(y >> 32), byte(y >> 40), byte(y >> 48), byte(y >> 56),
			})
			return ma.StringCast(fmt.Sprintf("/ip6/%s/", ip))
		}
	}

	getAddr := func(addrType int, ips, protos []byte) ma.Multiaddr {
		switch addrType % 4 {
		case 0:
			return getIP(ips).Encapsulate(getProto(protos))
		case 1:
			return getProto(protos)
		case 2:
			return nil
		default:
			return getIP(ips).Encapsulate(getProto(protos))
		}
	}

	getDNSAddr := func(hostNameBytes, protos []byte) ma.Multiaddr {
		hostName := strings.ReplaceAll(string(hostNameBytes), "\\", "")
		hostName = strings.ReplaceAll(hostName, "/", "")
		if hostName == "" {
			hostName = "localhost"
		}
		dnsType := 0
		if len(hostNameBytes) > 0 {
			dnsType = int(hostNameBytes[0])
		}
		dnsProtos := []string{"dns", "dns4", "dns6", "dnsaddr"}
		da := ma.StringCast(fmt.Sprintf("/%s/%s/", dnsProtos[dnsType%len(dnsProtos)], hostName))
		return da.Encapsulate(getProto(protos))
	}

	const maxAddrs = 1000
	getAddrs := func(numAddrs int, ips, protos, hostNames []byte) []ma.Multiaddr {
		if len(ips) == 0 || len(protos) == 0 || len(hostNames) == 0 {
			return nil
		}
		numAddrs = ((numAddrs % maxAddrs) + maxAddrs) % maxAddrs
		addrs := make([]ma.Multiaddr, numAddrs)
		ipIdx := 0
		protoIdx := 0
		for i := range numAddrs {
			addrs[i] = getAddr(i, ips[ipIdx:], protos[protoIdx:])
			ipIdx = (ipIdx + 1) % len(ips)
			protoIdx = (protoIdx + 1) % len(protos)
		}
		maxDNSAddrs := 10
		protoIdx = 0
		for i := 0; i < len(hostNames) && i < maxDNSAddrs; i += 2 {
			ed := min(i+2, len(hostNames))
			addrs = append(addrs, getDNSAddr(hostNames[i:ed], protos[protoIdx:]))
			protoIdx = (protoIdx + 1) % len(protos)
		}
		return addrs
	}

	cl := clock.NewMock()
	f.Fuzz(func(t *testing.T, numAddrs int, ips, protos, hostNames, autonatResponses []byte) {
		tr := newAddrsReachabilityTracker(newMockClient(autonatResponses), nil, cl, nil)
		require.NoError(t, tr.Start())
		tr.UpdateAddrs(getAddrs(numAddrs, ips, protos, hostNames))

		// fuzz tests need to finish in 10 seconds for some reason
		// https://github.com/golang/go/issues/48157
		// https://github.com/golang/go/commit/5d24203c394e6b64c42a9f69b990d94cb6c8aad4#diff-4e3b9481b8794eb058998e2bec389d3db7a23c54e67ac0f7259a3a5d2c79fd04R474-R483
		const maxIters = 20
		for range maxIters {
			cl.Add(5 * time.Minute)
			time.Sleep(100 * time.Millisecond)
		}
		require.NoError(t, tr.Close())
	})
}
