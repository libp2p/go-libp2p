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
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddrTrackerGetProbe(t *testing.T) {
	pub1 := ma.StringCast("/ip4/1.1.1.1/tcp/1")
	pub2 := ma.StringCast("/ip4/1.1.1.2/tcp/1")
	pri := ma.StringCast("/ip4/192.168.1.1/tcp/1")

	cl := clock.NewMock()

	t.Run("inprogress probes", func(t *testing.T) {
		tr := &addrsProbeTracker{
			statuses:         make(map[string]*addrStatus),
			inProgressProbes: make(map[string]int),
			now:              cl.Now,
		}

		tr.UpdateAddrs([]ma.Multiaddr{pub1, pub2, pri})
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
		tr := &addrsProbeTracker{
			statuses:         make(map[string]*addrStatus),
			inProgressProbes: make(map[string]int),
			now:              cl.Now,
		}
		tr.UpdateAddrs([]ma.Multiaddr{pub1, pub2, pri})
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
		tr := &addrsProbeTracker{
			statuses:         make(map[string]*addrStatus),
			inProgressProbes: make(map[string]int),
			now:              cl.Now,
		}
		tr.UpdateAddrs([]ma.Multiaddr{pub1, pub2, pri})
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
		tr := &addrsProbeTracker{
			statuses:         make(map[string]*addrStatus),
			inProgressProbes: make(map[string]int),
			now:              cl.Now,
		}
		tr.UpdateAddrs([]ma.Multiaddr{pub1, pub2, pri})
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

		upd := tr.GetReachabilityUpdate()
		require.Equal(t, upd.Reachable, []ma.Multiaddr{pub1})
		require.Equal(t, upd.Unreachable, []ma.Multiaddr{pub2})

		// should expire addrs after 3 hours
		cl.Add(3*time.Hour + 1*time.Second)
		upd = tr.GetReachabilityUpdate()
		require.Empty(t, upd.Reachable)
		require.Empty(t, upd.Unreachable)
	})
}

func TestAddrStatus(t *testing.T) {
	at := time.Now()

	type input struct {
		At               time.Time
		Success, Refused bool
	}
	type testCase struct {
		inputs []input
		output probeSummary
	}
	tests := []testCase{
		{
			inputs: []input{
				{At: at, Success: true, Refused: false},
			},
			output: probeSummary{Successes: 1, LastProbeTime: at},
		},
		{
			inputs: []input{
				{At: at, Success: false, Refused: false},
			},
			output: probeSummary{Failures: 1, LastProbeTime: at},
		},
		{
			inputs: []input{
				{At: at, Success: false, Refused: false},
				{At: at, Success: true, Refused: false},
				{At: at, Success: true, Refused: false},
			},
			output: probeSummary{Successes: 2, Failures: 1, LastProbeTime: at},
		},
		{
			inputs: []input{
				{At: at, Success: false, Refused: false},
				{At: at, Success: true, Refused: false},
				{At: at, Success: true, Refused: false},
				{At: at, Refused: true},
				{At: at, Refused: true},
				{At: at, Refused: true},
				{At: at, Refused: true},
			},
			output: probeSummary{ConsecutiveRefusals: 4, Successes: 2, Failures: 1, LastProbeTime: at},
		},
		{
			inputs: []input{
				{At: at, Refused: true}, // ignored because success/failure later
				{At: at, Success: false, Refused: false},
				{At: at, Success: false, Refused: false},
				{At: at, Success: true, Refused: false},
				{At: at, Success: true, Refused: false},
				{At: at, Success: false, Refused: false},
				{At: at, Success: true, Refused: false},
			},
			output: probeSummary{Successes: 3, Failures: 2, LastProbeTime: at}, // only last 5 are relevant
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("test-%d", i), func(t *testing.T) {
			s := &addrStatus{Addr: ma.StringCast("/ip4/1.1.1.1/tcp/1")}
			for _, inp := range tt.inputs {
				s.AddResult(at, inp.Success, inp.Refused)
			}
			require.Equal(t, tt.output, s.Summary())
		})
	}
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

	addrTracker := &addrsProbeTracker{
		statuses:         make(map[string]*addrStatus),
		inProgressProbes: make(map[string]int),
		now:              time.Now,
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tr := &addrsReachabilityTracker{
		ctx:                  ctx,
		cancel:               cancel,
		cli:                  mockClient,
		reachabilityUpdateCh: make(chan reachabilityUpdate, 10), // Buffered to prevent blocking
		maxConcurrency:       2,
		bootDelay:            0,
		addrTracker:          addrTracker,
		newAddrs:             make(chan []ma.Multiaddr, 10),
		clock:                clock.New(),
	}
	err := tr.Start()
	require.NoError(t, err)
	defer tr.Close()

	tr.UpdateAddrs([]ma.Multiaddr{pub3, pub1})
	upd := <-tr.reachabilityUpdateCh
	require.Empty(t, upd.Unreachable)
	require.Equal(t, upd.Reachable, []ma.Multiaddr{pub1}, "%s %s", upd.Reachable, pub1)
	tr.UpdateAddrs([]ma.Multiaddr{pub3, pub1, pub2})
	upd = <-tr.reachabilityUpdateCh
	require.Equal(t, upd.Reachable, []ma.Multiaddr{pub1}, "%s %s", upd.Reachable, pub1)
	require.Equal(t, upd.Unreachable, []ma.Multiaddr{pub2}, "%s %s", upd.Unreachable, pub2)
}

func TestAddrReachabilityTrackerBackoff(t *testing.T) {
	pub1, _ := ma.NewMultiaddr("/ip4/1.1.1.1/tcp/1")
	pub2, _ := ma.NewMultiaddr("/ip4/1.1.1.2/tcp/1")

	addrTracker := &addrsProbeTracker{
		statuses:         make(map[string]*addrStatus),
		inProgressProbes: make(map[string]int),
		now:              time.Now,
	}

	called := make(chan struct{}, 100)
	var allow atomic.Bool
	mockClient := mockAutoNATClient{
		F: func(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
			called <- struct{}{}
			if !allow.Load() {
				return autonatv2.Result{}, autonatv2.ErrNoValidPeers
			}
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cl := clock.NewMock()
	bootDelay := 2 * time.Second
	tr := &addrsReachabilityTracker{
		ctx:                  ctx,
		cancel:               cancel,
		cli:                  mockClient,
		reachabilityUpdateCh: make(chan reachabilityUpdate, 10), // Buffered to prevent blocking
		maxConcurrency:       2,
		bootDelay:            bootDelay,
		addrTracker:          addrTracker,
		newAddrs:             make(chan []ma.Multiaddr, 10),
		clock:                cl,
	}
	err := tr.Start()
	require.NoError(t, err)
	defer tr.Close()

	tr.UpdateAddrs([]ma.Multiaddr{pub2, pub1})
	time.Sleep(100 * time.Millisecond)

	epsilon := 2 * time.Millisecond
	cl.Add(bootDelay + epsilon)
	upd := <-tr.reachabilityUpdateCh
	require.Empty(t, upd.Reachable)
	require.Empty(t, upd.Unreachable)

	drain := func() {
		for {
			select {
			case <-called:
			default:
				return
			}
		}
	}
	backoffInterval := 5 * time.Second
	for i := 0; i < 3; i++ {
		drain()
		select {
		case <-called:
			t.Fatal("unexpected call")
		case <-time.After(100 * time.Millisecond):
		}
		cl.Add(backoffInterval / 2)
		select {
		case <-called:
			t.Fatal("unexpected call")
		case <-time.After(100 * time.Millisecond):
		}
		cl.Add(backoffInterval/2 + epsilon)
		backoffInterval *= 2
		upd := <-tr.reachabilityUpdateCh
		require.Empty(t, upd.Reachable)
		require.Empty(t, upd.Unreachable)
	}
	allow.Store(true)
	drain()
	cl.Add(backoffInterval + epsilon)
	upd = <-tr.reachabilityUpdateCh
	require.Equal(t, upd.Reachable, []ma.Multiaddr{pub1})
	require.Equal(t, upd.Unreachable, []ma.Multiaddr{pub2})
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

		addrTracker := newAddrsTracker(time.Now)
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

		addrTracker := newAddrsTracker(time.Now)
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

		addrTracker := newAddrsTracker(time.Now)
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

		addrTracker := newAddrsTracker(time.Now)
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

		update := addrTracker.GetReachabilityUpdate()
		require.Equal(t, update.Reachable, []ma.Multiaddr{pub1})
		require.Equal(t, addrTracker.InProgressProbes(), 0)
	})

	t.Run("handles completions", func(t *testing.T) {
		addrTracker := newAddrsTracker(time.Now)
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

		update := addrTracker.GetReachabilityUpdate()
		require.Equal(t, update.Reachable, []ma.Multiaddr{pub1})
		require.Equal(t, update.Unreachable, []ma.Multiaddr{pub2})
		require.Equal(t, addrTracker.InProgressProbes(), 0)
	})
}

func BenchmarkAddrTracker(b *testing.B) {
	cl := clock.NewMock()
	t := newAddrsTracker(cl.Now)

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
