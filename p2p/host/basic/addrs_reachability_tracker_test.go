package basichost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddrTrackerGetProbe(t *testing.T) {
	pub1 := ma.StringCast("/ip4/1.1.1.1/tcp/1")
	pub2 := ma.StringCast("/ip4/1.1.1.2/tcp/1")

	cl := clock.NewMock()

	t.Run("inprogress probes", func(t *testing.T) {
		tr := newAddrsTracker(cl.Now, maxRecentProbeResultWindow)

		tr.UpdateAddrs([]ma.Multiaddr{pub1, pub2})
		reqs1 := tr.GetProbe()
		reqs2 := tr.GetProbe()
		require.Equal(t, reqs1, reqs2)
		for i := 0; i < 3; i++ {
			reqs := tr.GetProbe()
			require.NotEmpty(t, reqs)
			tr.MarkProbeInProgress(reqs)
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}, {Addr: pub2, SendDialData: true}})
		}
		for i := 0; i < 3; i++ {
			reqs := tr.GetProbe()
			require.NotEmpty(t, reqs)
			tr.MarkProbeInProgress(reqs)
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub2, SendDialData: true}})
		}
		for i := 0; i < 3; i++ {
			reqs := tr.GetProbe()
			require.Empty(t, reqs)
		}
	})

	t.Run("probe refusals", func(t *testing.T) {
		tr := newAddrsTracker(cl.Now, maxRecentProbeResultWindow)
		tr.UpdateAddrs([]ma.Multiaddr{pub1, pub2})
		var probes [][]autonatv2.Request
		for i := 0; i < 3; i++ {
			reqs := tr.GetProbe()
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}, {Addr: pub2, SendDialData: true}})
			tr.MarkProbeInProgress(reqs)
			probes = append(probes, reqs)
		}
		// first one rejected second one successful
		for i := 0; i < len(probes); i++ {
			tr.CompleteProbe(probes[i], autonatv2.Result{Addr: pub2, Status: pb.DialStatus_OK}, nil)
		}
		// the second address is validated!
		probes = nil
		for i := 0; i < 3; i++ {
			reqs := tr.GetProbe()
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}})
			tr.MarkProbeInProgress(reqs)
			probes = append(probes, reqs)
		}
		reqs := tr.GetProbe()
		require.Empty(t, reqs)
		for i := 0; i < len(probes); i++ {
			tr.CompleteProbe(probes[i], autonatv2.Result{}, autonatv2.ErrDialRefused)
		}
		// all requests refused
		reqs = tr.GetProbe()
		require.Empty(t, reqs)

		cl.Add(10*time.Minute + 5*time.Second)
		reqs = tr.GetProbe()
		require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}})
	})

	t.Run("probe successes", func(t *testing.T) {
		tr := newAddrsTracker(cl.Now, maxRecentProbeResultWindow)
		tr.UpdateAddrs([]ma.Multiaddr{pub1, pub2})
		var probes [][]autonatv2.Request
		for i := 0; i < 3; i++ {
			reqs := tr.GetProbe()
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}, {Addr: pub2, SendDialData: true}})
			tr.MarkProbeInProgress(reqs)
			probes = append(probes, reqs)
		}
		// first one rejected second one successful
		for i := 0; i < len(probes); i++ {
			tr.CompleteProbe(probes[i], autonatv2.Result{Addr: pub1, Status: pb.DialStatus_E_DIAL_ERROR}, nil)
		}
		// the second address is validated!
		probes = nil
		for i := 0; i < 3; i++ {
			reqs := tr.GetProbe()
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub2, SendDialData: true}})
			tr.MarkProbeInProgress(reqs)
			probes = append(probes, reqs)
		}
		reqs := tr.GetProbe()
		require.Empty(t, reqs)
		for i := 0; i < len(probes); i++ {
			tr.CompleteProbe(probes[i], autonatv2.Result{Addr: pub2, Status: pb.DialStatus_OK}, nil)
		}
		// all statueses probed
		reqs = tr.GetProbe()
		require.Empty(t, reqs)

		cl.Add(1*time.Hour + 5*time.Second)
		reqs = tr.GetProbe()
		require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}, {Addr: pub2, SendDialData: true}})
		tr.MarkProbeInProgress(reqs)
		reqs = tr.GetProbe()
		require.Equal(t, reqs, []autonatv2.Request{{Addr: pub2, SendDialData: true}})
	})

	t.Run("reachabilityUpdate", func(t *testing.T) {
		tr := newAddrsTracker(cl.Now, maxRecentProbeResultWindow)
		tr.UpdateAddrs([]ma.Multiaddr{pub1, pub2})
		var probes [][]autonatv2.Request
		for i := 0; i < 3; i++ {
			reqs := tr.GetProbe()
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub1, SendDialData: true}, {Addr: pub2, SendDialData: true}})
			tr.MarkProbeInProgress(reqs)
			probes = append(probes, reqs)
		}
		for i := 0; i < len(probes); i++ {
			tr.CompleteProbe(probes[i], autonatv2.Result{Addr: pub1, Status: pb.DialStatus_OK}, nil)
		}
		probes = nil
		for i := 0; i < 3; i++ {
			reqs := tr.GetProbe()
			require.Equal(t, reqs, []autonatv2.Request{{Addr: pub2, SendDialData: true}})
			tr.MarkProbeInProgress(reqs)
			probes = append(probes, reqs)
		}
		for i := 0; i < len(probes); i++ {
			tr.CompleteProbe(probes[i], autonatv2.Result{Addr: pub2, Status: pb.DialStatus_E_DIAL_ERROR}, nil)
		}

		reachable, unreachable := tr.AppendConfirmedAddrs(nil, nil)
		require.Equal(t, reachable, []ma.Multiaddr{pub1})
		require.Equal(t, unreachable, []ma.Multiaddr{pub2})

		// should expire addrs after 3 hours
		cl.Add(3*time.Hour + 1*time.Second)
		reachable, unreachable = tr.AppendConfirmedAddrs(nil, nil)
		require.Empty(t, reachable)
		require.Empty(t, unreachable)
	})
}

func TestAddrStatus(t *testing.T) {
	now := time.Now()
	probeResultWindow := maxRecentProbeResultWindow

	type input struct {
		At               time.Time
		Success, Refused bool
	}
	type testCase struct {
		inputs       []input
		probeCount   int
		reachability network.Reachability
	}
	tests := []testCase{
		{
			inputs: []input{
				{At: now, Success: true},
			},
			probeCount:   2,
			reachability: network.ReachabilityUnknown,
		},
		{
			inputs: []input{
				{At: now, Success: false},
				{At: now, Success: true},
				{At: now, Success: true},
				{At: now, Success: true},
			},
			probeCount:   1,
			reachability: network.ReachabilityPublic,
		},
		{
			inputs: []input{
				{At: now, Success: true},
				{At: now, Success: false},
				{At: now, Success: false},
				{At: now, Success: false},
			},
			probeCount:   1,
			reachability: network.ReachabilityPrivate,
		},
		{
			inputs: []input{
				{At: now, Success: false},
				{At: now, Success: false},
				{At: now, Success: false},
				{At: now, Success: false},
				{At: now, Success: false},
				{At: now, Success: false},
				{At: now, Success: true},
				{At: now, Success: true},
				{At: now, Success: true},
				{At: now, Success: true},
			},
			probeCount:   0,
			reachability: network.ReachabilityPublic,
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("test-%d", i), func(t *testing.T) {
			s := &addrStatus{Addr: ma.StringCast("/ip4/1.1.1.1/tcp/1")}
			for _, inp := range tt.inputs {
				if inp.Refused {
					s.AddRefusal(now)
				} else {
					s.AddResult(now, inp.Success)
				}
				s.Trim(probeResultWindow)
			}
			require.Equal(t, tt.reachability, s.Reachability())
			require.Equal(t, tt.probeCount, s.ProbeCount(now))
		})
	}
}

func TestAddrStatusRefused(t *testing.T) {
	s := &addrStatus{Addr: ma.StringCast("/ip4/1.1.1.1/tcp/1")}
	now := time.Now()
	for i := 0; i < maxConsecutiveRefusals-1; i++ {
		s.AddRefusal(now)
	}
	require.Equal(t, s.ProbeCount(now), 3)
	s.AddRefusal(now)
	require.Equal(t, s.ProbeCount(now), 0)
	require.Equal(t, s.ProbeCount(now.Add(addrRefusedProbeInterval+(1*time.Nanosecond))), 1) // +1 to push it over the threshold

	s.AddResult(now, true)
	require.Equal(t, s.ProbeCount(now), 2)
	require.Equal(t, s.consecutiveRefusals.Count, 0)
}

type mockAutoNATClient struct {
	F func(context.Context, []autonatv2.Request) (autonatv2.Result, error)
}

func (m mockAutoNATClient) GetReachability(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
	return m.F(ctx, reqs)
}

var _ autonatv2Client = mockAutoNATClient{}

func TestAddrReachabilityTracker(t *testing.T) {
	pub1, _ := ma.NewMultiaddr("/ip4/1.1.1.1/tcp/1")
	pub2, _ := ma.NewMultiaddr("/ip4/1.1.1.2/tcp/1")
	pub3, _ := ma.NewMultiaddr("/ip4/1.1.1.3/tcp/1")
	pri, _ := ma.NewMultiaddr("/ip4/192.168.1.1/tcp/1")

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
			addrTracker:          newAddrsTracker(cl.Now, maxRecentProbeResultWindow),
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
		mockClient := mockAutoNATClient{
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				for _, req := range reqs {
					if req.Addr.Equal(pub1) {
						return autonatv2.Result{Addr: pub1, Status: pb.DialStatus_OK}, nil
					} else if req.Addr.Equal(pub2) {
						return autonatv2.Result{Addr: pub2, Status: pb.DialStatus_E_DIAL_ERROR}, nil
					}
				}
				return autonatv2.Result{}, autonatv2.ErrDialRefused
			},
		}
		tr := newTracker(mockClient, nil)
		tr.UpdateAddrs([]ma.Multiaddr{pub3, pub1, pri})
		select {
		case <-tr.reachabilityUpdateCh:
		case <-time.After(2 * time.Second):
			t.Fatal("expected reachability update")
		}
		reachable, unreachable := tr.ConfirmedAddrs()
		require.Equal(t, reachable, []ma.Multiaddr{pub1}, "%s %s", reachable, pub1)
		require.Empty(t, unreachable)

		tr.UpdateAddrs([]ma.Multiaddr{pub3, pub1, pub2, pri})
		select {
		case <-tr.reachabilityUpdateCh:
		case <-time.After(1 * time.Second):
			t.Fatal("unexpected call")
		}
		reachable, unreachable = tr.ConfirmedAddrs()
		require.Equal(t, reachable, []ma.Multiaddr{pub1}, "%s %s", reachable, pub1)
		require.Equal(t, unreachable, []ma.Multiaddr{pub2}, "%s %s", unreachable, pub2)
	})

	t.Run("backoff", func(t *testing.T) {
		notify := make(chan struct{}, 1)
		drainNotify := func() {
			for {
				select {
				case <-notify:
				default:
					return
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
					return autonatv2.Result{}, autonatv2.ErrNoValidPeers
				}
				if reqs[0].Addr.Equal(pub1) {
					return autonatv2.Result{Addr: pub1, Status: pb.DialStatus_OK}, nil
				}
				return autonatv2.Result{}, autonatv2.ErrDialRefused
			},
		}

		cl := clock.NewMock()
		tr := newTracker(mockClient, cl)

		// update addrs and wait for initial checks
		tr.UpdateAddrs([]ma.Multiaddr{pub1})
		// need to update clock after the background goroutine processes the new addrs
		time.Sleep(100 * time.Millisecond)
		cl.Add(1)
		select {
		case <-tr.reachabilityUpdateCh:
			reachable, unreachable := tr.ConfirmedAddrs()
			require.Empty(t, reachable)
			require.Empty(t, unreachable)
		case <-time.After(1 * time.Second):
			t.Fatal("unexpected call")
		}

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
			case <-tr.reachabilityUpdateCh:
			case <-time.After(1 * time.Second):
				t.Fatal("unexpected call")
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
			t.Fatal("unexpected call")
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
				case <-ctx.Done():
					return autonatv2.Result{}, ctx.Err()
				case called <- struct{}{}:
					notify <- struct{}{}
				}
				return autonatv2.Result{Addr: pub1, Status: pb.DialStatus_OK}, nil
			},
		}

		tr := newTracker(mockClient, clock.New())
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
			t.Fatal("didn't expect reachability update")
		case <-time.After(1 * time.Second):
		}
		tr.UpdateAddrs([]ma.Multiaddr{pub1})
		select {
		case <-tr.reachabilityUpdateCh:
		case <-time.After(1 * time.Second):
			t.Fatal("expected reachability update")
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
				return autonatv2.Result{}, autonatv2.ErrNoValidPeers
			},
		}

		addrTracker := newAddrsTracker(time.Now, maxRecentProbeResultWindow)
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

		addrTracker := newAddrsTracker(time.Now, maxRecentProbeResultWindow)
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

		addrTracker := newAddrsTracker(time.Now, maxRecentProbeResultWindow)
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

		addrTracker := newAddrsTracker(time.Now, maxRecentProbeResultWindow)
		addrTracker.UpdateAddrs([]ma.Multiaddr{pub2, pub1})

		mockClient := mockAutoNATClient{
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				for _, req := range reqs {
					if req.Addr.Equal(pub1) {
						return autonatv2.Result{Addr: pub1, Status: pb.DialStatus_OK}, nil
					}
				}
				return autonatv2.Result{}, autonatv2.ErrDialRefused
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
		addrTracker := newAddrsTracker(time.Now, maxRecentProbeResultWindow)
		addrTracker.UpdateAddrs([]ma.Multiaddr{pub2, pub1})

		mockClient := mockAutoNATClient{
			F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				for _, req := range reqs {
					if req.Addr.Equal(pub1) {
						return autonatv2.Result{Addr: pub1, Status: pb.DialStatus_OK}, nil
					}
					if req.Addr.Equal(pub2) {
						return autonatv2.Result{Addr: pub2, Status: pb.DialStatus_E_DIAL_ERROR}, nil
					}
				}
				return autonatv2.Result{}, autonatv2.ErrDialRefused
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

func BenchmarkAddrTracker(b *testing.B) {
	cl := clock.NewMock()
	t := newAddrsTracker(cl.Now, maxRecentProbeResultWindow)

	var addrs []ma.Multiaddr
	for i := 0; i < 20; i++ {
		addrs = append(addrs, ma.StringCast(fmt.Sprintf("/ip4/1.1.1.1/tcp/%d", i)))
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
		t.CompleteProbe(pp, autonatv2.Result{Addr: pp[0].Addr, Status: pb.DialStatus_OK}, nil)
	}
}
