package itest

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"

	"github.com/stretchr/testify/require"
)

func makeRcmgrOption(t *testing.T, cfg rcmgr.ConcreteLimitConfig) func(int) libp2p.Option {
	return func(i int) libp2p.Option {
		var opts []rcmgr.Option
		if os.Getenv("LIBP2P_TEST_RCMGR_TRACE") == "1" {
			opts = append(opts, rcmgr.WithTrace(fmt.Sprintf("%s-%d.json.gz", t.Name(), i)))
		}

		mgr, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(cfg), opts...)
		require.NoError(t, err)
		return libp2p.ResourceManager(mgr)
	}
}

func closeRcmgrs(echos []*Echo) {
	for _, e := range echos {
		e.Host.Network().ResourceManager().Close()
	}
}

func waitForConnection(t *testing.T, src, dest *Echo) {
	require.Eventually(t, func() bool {
		return src.Host.Network().Connectedness(dest.Host.ID()) == network.Connected &&
			dest.Host.Network().Connectedness(src.Host.ID()) == network.Connected
	}, time.Second, 10*time.Millisecond)
}

func TestResourceManagerConnInbound(t *testing.T) {
	// this test checks that we can not exceed the inbound conn limit at system level
	// we specify: 1 conn per peer, 3 conns total, and we try to create 4 conns
	cfg := rcmgr.PartialLimitConfig{
		System: rcmgr.ResourceLimits{
			ConnsInbound:    3,
			ConnsOutbound:   1024,
			Conns:           1024,
			StreamsOutbound: rcmgr.Unlimited,
		},
		PeerDefault: rcmgr.ResourceLimits{
			ConnsInbound:  1,
			ConnsOutbound: 1,
			Conns:         1,
		},
	}.Build(rcmgr.DefaultLimits.AutoScale())

	echos := createEchos(t, 5, makeRcmgrOption(t, cfg))
	defer closeEchos(echos)
	defer closeRcmgrs(echos)

	for i := 1; i < 4; i++ {
		err := echos[i].Host.Connect(context.Background(), peer.AddrInfo{ID: echos[0].Host.ID()})
		if err != nil {
			t.Fatal(err)
		}
		waitForConnection(t, echos[i], echos[0])
	}

	for i := 1; i < 4; i++ {
		count := len(echos[i].Host.Network().ConnsToPeer(echos[0].Host.ID()))
		if count != 1 {
			t.Fatalf("expected %d connections to peer, got %d", 1, count)
		}
	}

	err := echos[4].Host.Connect(context.Background(), peer.AddrInfo{ID: echos[0].Host.ID()})
	if err == nil {
		t.Fatal("expected ResourceManager to block incoming connection")
	}
}

func TestResourceManagerConnOutbound(t *testing.T) {
	// this test checks that we can not exceed the inbound conn limit at system level
	// we specify: 1 conn per peer, 3 conns total, and we try to create 4 conns
	cfg := rcmgr.PartialLimitConfig{
		System: rcmgr.ResourceLimits{
			ConnsInbound:  1024,
			ConnsOutbound: 3,
			Conns:         1024,
		},
		PeerDefault: rcmgr.ResourceLimits{
			ConnsInbound:  1,
			ConnsOutbound: 1,
			Conns:         1,
		},
	}.Build(rcmgr.DefaultLimits.AutoScale())
	echos := createEchos(t, 5, makeRcmgrOption(t, cfg))
	defer closeEchos(echos)
	defer closeRcmgrs(echos)

	for i := 1; i < 4; i++ {
		err := echos[0].Host.Connect(context.Background(), peer.AddrInfo{ID: echos[i].Host.ID()})
		if err != nil {
			t.Fatal(err)
		}
		waitForConnection(t, echos[0], echos[i])
	}

	for i := 1; i < 4; i++ {
		count := len(echos[i].Host.Network().ConnsToPeer(echos[0].Host.ID()))
		if count != 1 {
			t.Fatalf("expected %d connections to peer, got %d", 1, count)
		}
	}

	err := echos[0].Host.Connect(context.Background(), peer.AddrInfo{ID: echos[4].Host.ID()})
	if err == nil {
		t.Fatal("expected ResourceManager to block incoming connection")
	}
}

func TestResourceManagerServiceInbound(t *testing.T) {
	// this test checks that we can not exceed the inbound stream limit at service level
	// we specify: 3 streams for the service, and we try to create 4 streams
	cfg := rcmgr.PartialLimitConfig{
		ServiceDefault: rcmgr.ResourceLimits{
			StreamsInbound:  3,
			StreamsOutbound: 1024,
			Streams:         1024,
		},
	}.Build(rcmgr.DefaultLimits.AutoScale())
	echos := createEchos(t, 5, makeRcmgrOption(t, cfg))
	defer closeEchos(echos)
	defer closeRcmgrs(echos)

	for i := 1; i < 5; i++ {
		err := echos[i].Host.Connect(context.Background(), peer.AddrInfo{ID: echos[0].Host.ID()})
		if err != nil {
			t.Fatal(err)
		}
		waitForConnection(t, echos[i], echos[0])
	}

	ready := make(chan struct{})
	echos[0].BeforeDone(waitForChannel(ready, time.Minute))

	var eg sync.WaitGroup
	echos[0].Done(eg.Done)

	var once sync.Once
	var wg sync.WaitGroup
	for i := 1; i < 5; i++ {
		eg.Add(1)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			err := echos[i].Echo(echos[0].Host.ID(), "hello libp2p")
			if err != nil {
				t.Log(err)
				once.Do(func() {
					close(ready)
				})
			}
		}(i)
	}
	wg.Wait()
	eg.Wait()

	checkEchoStatus(t, echos[0], EchoStatus{
		StreamsIn:             4,
		EchosIn:               3,
		EchosOut:              3,
		ResourceServiceErrors: 1,
	})
}

func TestResourceManagerServicePeerInbound(t *testing.T) {
	// this test checks that we cannot exceed the per peer inbound stream limit at service level
	// we specify: 2 streams per peer for echo, and we try to create 3 streams
	cfg := rcmgr.DefaultLimits
	cfg.AddServicePeerLimit(
		EchoService,
		rcmgr.BaseLimit{StreamsInbound: 2, StreamsOutbound: 1024, Streams: 1024, Memory: 9999999},
		rcmgr.BaseLimitIncrease{},
	)
	limits := cfg.AutoScale()

	echos := createEchos(t, 5, makeRcmgrOption(t, limits))
	defer closeEchos(echos)
	defer closeRcmgrs(echos)

	for i := 1; i < 5; i++ {
		err := echos[i].Host.Connect(context.Background(), peer.AddrInfo{ID: echos[0].Host.ID()})
		if err != nil {
			t.Fatal(err)
		}
		waitForConnection(t, echos[i], echos[0])
	}

	echos[0].BeforeDone(waitForBarrier(4, time.Minute))

	var eg sync.WaitGroup
	echos[0].Done(eg.Done)

	var wg sync.WaitGroup
	for i := 1; i < 5; i++ {
		eg.Add(1)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			err := echos[i].Echo(echos[0].Host.ID(), "hello libp2p")
			if err != nil {
				t.Log(err)
			}
		}(i)
	}
	wg.Wait()
	eg.Wait()

	checkEchoStatus(t, echos[0], EchoStatus{
		StreamsIn:             4,
		EchosIn:               4,
		EchosOut:              4,
		ResourceServiceErrors: 0,
	})

	ready := make(chan struct{})
	echos[0].BeforeDone(waitForChannel(ready, time.Minute))

	var once sync.Once
	for i := 0; i < 3; i++ {
		eg.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()

			err := echos[2].Echo(echos[0].Host.ID(), "hello libp2p")
			if err != nil {
				t.Log(err)
				once.Do(func() {
					close(ready)
				})
			}
		}()
	}
	wg.Wait()
	eg.Wait()

	checkEchoStatus(t, echos[0], EchoStatus{
		StreamsIn:             7,
		EchosIn:               6,
		EchosOut:              6,
		ResourceServiceErrors: 1,
	})
}

func waitForBarrier(count int32, timeout time.Duration) func() error {
	ready := make(chan struct{})
	var wait atomic.Int32
	wait.Store(count)
	return func() error {
		if wait.Add(-1) == 0 {
			close(ready)
		}

		select {
		case <-ready:
			return nil
		case <-time.After(timeout):
			return fmt.Errorf("timeout")
		}
	}
}

func waitForChannel(ready chan struct{}, timeout time.Duration) func() error {
	return func() error {
		select {
		case <-ready:
			return nil
		case <-time.After(timeout):
			return fmt.Errorf("timeout")
		}
	}
}

func TestReadmeExample(t *testing.T) {
	// Start with the default scaling limits.
	scalingLimits := rcmgr.DefaultLimits

	// Add limits around included libp2p protocols
	libp2p.SetDefaultServiceLimits(&scalingLimits)

	// Turn the scaling limits into a concrete set of limits using `.AutoScale`. This
	// scales the limits proportional to your system memory.
	scaledDefaultLimits := scalingLimits.AutoScale()

	// Tweak certain settings
	cfg := rcmgr.PartialLimitConfig{
		System: rcmgr.ResourceLimits{
			// Allow unlimited outbound streams
			StreamsOutbound: rcmgr.Unlimited,
		},
		// Everything else is default. The exact values will come from `scaledDefaultLimits` above.
	}

	// Create our limits by using our cfg and replacing the default values with values from `scaledDefaultLimits`
	limits := cfg.Build(scaledDefaultLimits)

	// The resource manager expects a limiter, se we create one from our limits.
	limiter := rcmgr.NewFixedLimiter(limits)

	// Metrics are enabled by default. If you want to disable metrics, use the
	// WithMetricsDisabled option
	// Initialize the resource manager
	rm, err := rcmgr.NewResourceManager(limiter, rcmgr.WithMetricsDisabled())
	if err != nil {
		panic(err)
	}

	// Create a libp2p host
	host, err := libp2p.New(libp2p.ResourceManager(rm))
	if err != nil {
		panic(err)
	}
	host.Close()
}
