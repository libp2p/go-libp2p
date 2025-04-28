package basichost

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/netip"
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
		reachable, _ := pm.AppendConfirmedAddrs(nil, nil)
		require.Equal(t, reachable, []ma.Multiaddr{pub1, pub2})
		pm.UpdateAddrs([]ma.Multiaddr{pub3})

		reachable, _ = pm.AppendConfirmedAddrs(nil, nil)
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

		cl.Add(maxProbeInterval + time.Millisecond)
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

		reachable, unreachable := pm.AppendConfirmedAddrs(nil, nil)
		require.Equal(t, reachable, []ma.Multiaddr{pub1})
		require.Equal(t, unreachable, []ma.Multiaddr{pub2})
	})
	t.Run("expiry", func(t *testing.T) {
		pm := makeNewProbeManager([]ma.Multiaddr{pub1})
		for range 2 * targetConfidence {
			reqs := nextProbe(pm)
			pm.CompleteProbe(reqs, autonatv2.Result{Addr: pub1, Idx: 0, Reachability: network.ReachabilityPublic}, nil)
		}

		reachable, unreachable := pm.AppendConfirmedAddrs(nil, nil)
		require.Equal(t, reachable, []ma.Multiaddr{pub1})
		require.Empty(t, unreachable)

		cl.Add(maxProbeResultTTL + 1*time.Second)
		reachable, unreachable = pm.AppendConfirmedAddrs(nil, nil)
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

	newTracker := func(cli mockAutoNATClient, cl clock.Clock) *addrsReachabilityTracker {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if cl == nil {
			cl = clock.New()
		}
		tr := &addrsReachabilityTracker{
			ctx:                  ctx,
			cancel:               cancel,
			cli:                  cli,
			newAddrs:             make(chan []ma.Multiaddr, 1),
			reachabilityUpdateCh: make(chan struct{}, 1),
			maxConcurrency:       3,
			newAddrsProbeDelay:   0 * time.Second,
			addrTracker:          newProbeManager(cl.Now),
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
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				for i, req := range reqs {
					if req.Addr.Equal(pub1) {
						return autonatv2.Result{Addr: pub1, Idx: i, Reachability: network.ReachabilityPublic}, nil
					} else if req.Addr.Equal(pub2) {
						return autonatv2.Result{Addr: pub2, Idx: i, Reachability: network.ReachabilityPrivate}, nil
					}
				}
				return autonatv2.Result{}, autonatv2.ErrNoPeers
			},
		}
		tr := newTracker(mockClient, nil)
		tr.UpdateAddrs([]ma.Multiaddr{pub2, pub1, pri})
		select {
		case <-tr.reachabilityUpdateCh:
		case <-time.After(2 * time.Second):
			t.Fatal("expected reachability update")
		}
		reachable, unreachable := tr.ConfirmedAddrs()
		require.Equal(t, reachable, []ma.Multiaddr{pub1}, "%s %s", reachable, pub1)
		require.Equal(t, unreachable, []ma.Multiaddr{pub2}, "%s %s", unreachable, pub2)

		tr.UpdateAddrs([]ma.Multiaddr{pub3, pub1, pri})
		select {
		case <-tr.reachabilityUpdateCh:
		case <-time.After(2 * time.Second):
			t.Fatal("expected reachability update")
		}
		reachable, unreachable = tr.ConfirmedAddrs()
		require.Equal(t, reachable, []ma.Multiaddr{pub1}, "%s %s", reachable, pub1)
		require.Empty(t, unreachable)
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
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
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
			reachable, unreachable := tr.ConfirmedAddrs()
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
		reachable, unreachable := tr.ConfirmedAddrs()
		require.Equal(t, reachable, []ma.Multiaddr{pub1})
		require.Empty(t, unreachable)
	})

	t.Run("event update", func(t *testing.T) {
		// allow minConfidence probes to pass
		called := make(chan struct{}, minConfidence)
		notify := make(chan struct{})
		mockClient := mockAutoNATClient{
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
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
		for i := 0; i < minConfidence; i++ {
			select {
			case <-notify:
			case <-time.After(1 * time.Second):
				t.Fatal("expected call to autonat client")
			}
		}
		select {
		case <-tr.reachabilityUpdateCh:
			reachable, unreachable := tr.ConfirmedAddrs()
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
			reachable, unreachable := tr.ConfirmedAddrs()
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
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
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
		// need to update clock after the background goroutine processes the new addrs
		time.Sleep(100 * time.Millisecond)
		cl.Add(1)
		time.Sleep(100 * time.Millisecond)
		require.True(t, drainNotify()) // check that we did receive probes

		cl.Add(maxProbeInterval / 2)
		select {
		case <-notify:
			t.Fatal("unexpected call")
		case <-time.After(50 * time.Millisecond):
		}

		cl.Add(maxProbeInterval/2 + defaultResetInterval) // defaultResetInterval for the next probe time
		select {
		case <-notify:
		case <-time.After(1 * time.Second):
			t.Fatal("expected probe")
		}
	})
}

func TestRunProbes(t *testing.T) {
	pub1 := ma.StringCast("/ip4/1.1.1.1/tcp/1")
	pub2 := ma.StringCast("/ip4/1.1.1.1/tcp/2")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Run("backoff on ErrNoValidPeers", func(t *testing.T) {
		mockClient := mockAutoNATClient{
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				return autonatv2.Result{}, autonatv2.ErrNoPeers
			},
		}

		addrTracker := newProbeManager(time.Now)
		addrTracker.UpdateAddrs([]ma.Multiaddr{pub1})
		result := runProbes(ctx, defaultMaxConcurrency, addrTracker, mockClient)
		require.True(t, result)
		require.Equal(t, addrTracker.InProgressProbes(), 0)
	})

	t.Run("returns backoff on errTooManyConsecutiveFailures", func(t *testing.T) {
		// Create a client that always returns ErrDialRefused
		mockClient := mockAutoNATClient{
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				return autonatv2.Result{}, errors.New("test error")
			},
		}

		addrTracker := newProbeManager(time.Now)
		addrTracker.UpdateAddrs([]ma.Multiaddr{pub1})

		result := runProbes(ctx, defaultMaxConcurrency, addrTracker, mockClient)
		require.True(t, result)
		require.Equal(t, addrTracker.InProgressProbes(), 0)
	})

	t.Run("quits on cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		block := make(chan struct{})
		mockClient := mockAutoNATClient{
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				block <- struct{}{}
				return autonatv2.Result{}, nil
			},
		}

		addrTracker := newProbeManager(time.Now)
		addrTracker.UpdateAddrs([]ma.Multiaddr{pub1})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := runProbes(ctx, defaultMaxConcurrency, addrTracker, mockClient)
			assert.False(t, result)
			assert.Equal(t, addrTracker.InProgressProbes(), 0)
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

		addrTracker := newProbeManager(time.Now)
		addrTracker.UpdateAddrs([]ma.Multiaddr{pub2, pub1})

		mockClient := mockAutoNATClient{
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				for i, req := range reqs {
					if req.Addr.Equal(pub1) {
						return autonatv2.Result{Addr: pub1, Idx: i, Reachability: network.ReachabilityPublic}, nil
					}
				}
				return autonatv2.Result{AllAddrsRefused: true}, nil
			},
		}

		result := runProbes(ctx, defaultMaxConcurrency, addrTracker, mockClient)
		require.False(t, result)

		reachable, unreachable := addrTracker.AppendConfirmedAddrs(nil, nil)
		require.Equal(t, reachable, []ma.Multiaddr{pub1})
		require.Empty(t, unreachable)
		require.Equal(t, addrTracker.InProgressProbes(), 0)
	})

	t.Run("handles completions", func(t *testing.T) {
		addrTracker := newProbeManager(time.Now)
		addrTracker.UpdateAddrs([]ma.Multiaddr{pub2, pub1})

		mockClient := mockAutoNATClient{
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
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

		result := runProbes(ctx, defaultMaxConcurrency, addrTracker, mockClient)
		require.False(t, result)

		reachable, unreachable := addrTracker.AppendConfirmedAddrs(nil, nil)
		require.Equal(t, reachable, []ma.Multiaddr{pub1})
		require.Equal(t, unreachable, []ma.Multiaddr{pub2})
		require.Equal(t, addrTracker.InProgressProbes(), 0)
	})
}

func TestDialOutcome(t *testing.T) {
	cases := []struct {
		inputs             string
		wantRequiredProbes int
		wantReachability   network.Reachability
	}{
		{
			inputs:             "SSSSSSSSSSS",
			wantRequiredProbes: 0,
			wantReachability:   network.ReachabilityPublic,
		},
		{
			inputs:             "SSSSSSSSSSF",
			wantRequiredProbes: 1,
			wantReachability:   network.ReachabilityPublic,
		},
		{
			inputs:             "SFSFSFSFSSSS",
			wantRequiredProbes: 0,
			wantReachability:   network.ReachabilityPublic,
		},
		{
			inputs:             "SSSSSSSSSFSF",
			wantRequiredProbes: 2,
			wantReachability:   network.ReachabilityUnknown,
		},
		{
			inputs:             "S",
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
			ao := addrOutcomes{}
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
				now = now.Add(maxProbeInterval + 10*time.Microsecond)
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

	var addrs []ma.Multiaddr
	for range 20 {
		addrs = append(addrs, ma.StringCast(fmt.Sprintf("/ip4/1.1.1.1/tcp/%d", rand.Intn(1000))))
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
	// The only constraint we force is that result.Idx < len(reqs)
	client := mockAutoNATClient{
		F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
			if rand.Intn(3) == 0 {
				// some address confirmed
				x := rand.Intn(3)
				rch := network.Reachability(x)
				n := rand.Intn(len(reqs))
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
			outcome := outcomes[rand.Intn(len(outcomes))]
			return outcome.Result, outcome.Err
		},
	}

	// TODO: Move this to go-multiaddrs
	randProto := func() ma.Multiaddr {
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
		s := protoTemplates[rand.Intn(len(protoTemplates))]
		if strings.Count(s, "%d") == 1 {
			return ma.StringCast(fmt.Sprintf(s, rand.Intn(1000)))
		}
		return ma.StringCast(fmt.Sprintf(s, rand.Intn(1000), rand.Intn(1000)))
	}

	randIP := func() ma.Multiaddr {
		x := rand.Intn(2)
		if x == 0 {
			i := rand.Int31()
			ip := netip.AddrFrom4([4]byte{byte(i), byte(i >> 8), byte(i >> 16), byte(i >> 24)})
			return ma.StringCast(fmt.Sprintf("/ip4/%s/tcp/1", ip))
		}
		a, b := rand.Int63(), rand.Int63()
		if rand.Intn(2) == 0 {
			pubIP := net.ParseIP("2005::") // Public IP address
			a = int64(binary.LittleEndian.Uint64(pubIP[0:8]))
		}
		ip := netip.AddrFrom16([16]byte{
			byte(a), byte(a >> 8), byte(a >> 16), byte(a >> 24),
			byte(a >> 32), byte(a >> 40), byte(a >> 48), byte(a >> 56),
			byte(b), byte(b >> 8), byte(b >> 16), byte(b >> 24),
			byte(b >> 32), byte(b >> 40), byte(b >> 48), byte(b >> 56),
		})
		return ma.StringCast(fmt.Sprintf("/ip6/%s/tcp/1", ip))
	}

	newAddrs := func() ma.Multiaddr {
		switch rand.Intn(5) {
		case 0:
			return randIP().Encapsulate(randProto())
		case 1:
			return randProto()
		case 2:
			return nil
		default:
			return randProto().Encapsulate(randIP())
		}
	}

	randDNSAddr := func(hostName string) ma.Multiaddr {
		dnsProtos := []string{"dns", "dns4", "dns6", "dnsaddr"}
		if hostName == "" {
			hostName = "localhost"
		}
		hostName = strings.ReplaceAll(hostName, "\\", "")
		hostName = strings.ReplaceAll(hostName, "/", "")
		da := ma.StringCast(fmt.Sprintf("/%s/%s/", dnsProtos[rand.Intn(len(dnsProtos))], hostName))
		return da.Encapsulate(randProto())
	}

	const maxAddrs = 1000
	getAddrs := func(numAddrs int, hostNames []byte) []ma.Multiaddr {
		numAddrs = ((numAddrs % maxAddrs) + maxAddrs) % maxAddrs
		addrs := make([]ma.Multiaddr, numAddrs)
		for i := range numAddrs {
			addrs[i] = newAddrs()
		}
		maxDNSAddrs := 10
		for i := 0; i < len(hostNames) && i < maxDNSAddrs; i += 2 {
			ed := min(i+2, len(hostNames))
			addrs = append(addrs, randDNSAddr(string(hostNames[i:ed])))
		}
		return addrs
	}

	cl := clock.NewMock()
	f.Fuzz(func(t *testing.T, i int, hostNames []byte) {
		tr := newAddrsReachabilityTracker(client, nil, cl)
		require.NoError(t, tr.Start())
		tr.UpdateAddrs(getAddrs(i, hostNames))

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
