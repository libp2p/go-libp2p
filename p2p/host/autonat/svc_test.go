package autonat

import (
	"context"
	"os"
	"testing"
	"time"

	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func makeAutoNATConfig(t *testing.T) *config {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t))
	dh := bhost.NewBlankHost(swarmt.GenSwarm(t))
	c := config{host: h, dialer: dh.Network()}
	_ = defaults(&c)
	c.forceReachability = true
	c.dialPolicy.allowSelfDials = true
	return &c
}

func makeAutoNATService(t *testing.T, c *config) *autoNATService {
	as, err := newAutoNATService(c)
	if err != nil {
		t.Fatal(err)
	}
	as.Enable()

	return as
}

func makeAutoNATClient(t *testing.T) (host.Host, Client) {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t))
	cli := NewAutoNATClient(h, nil)
	return h, cli
}

// Note: these tests assume that the host has only private network addresses!
func TestAutoNATServiceDialRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := makeAutoNATConfig(t)
	defer c.host.Close()
	defer c.dialer.Close()

	c.dialTimeout = 1 * time.Second
	c.dialPolicy.allowSelfDials = false
	_ = makeAutoNATService(t, c)
	hc, ac := makeAutoNATClient(t)
	defer hc.Close()
	connect(t, c.host, hc)

	_, err := ac.DialBack(ctx, c.host.ID())
	if err == nil {
		t.Fatal("Dial back succeeded unexpectedly!")
	}

	if !IsDialRefused(err) {
		t.Fatal(err)
	}
}

func TestAutoNATServiceDialSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := makeAutoNATConfig(t)
	defer c.host.Close()
	defer c.dialer.Close()

	_ = makeAutoNATService(t, c)

	hc, ac := makeAutoNATClient(t)
	defer hc.Close()
	connect(t, c.host, hc)

	_, err := ac.DialBack(ctx, c.host.ID())
	if err != nil {
		t.Fatalf("Dial back failed: %s", err.Error())
	}
}

func TestAutoNATServiceDialRateLimiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := makeAutoNATConfig(t)
	defer c.host.Close()
	defer c.dialer.Close()

	c.dialTimeout = 200 * time.Millisecond
	c.throttleResetPeriod = 200 * time.Millisecond
	c.throttleResetJitter = 0
	c.throttlePeerMax = 1
	_ = makeAutoNATService(t, c)

	hc, ac := makeAutoNATClient(t)
	defer hc.Close()
	connect(t, c.host, hc)

	_, err := ac.DialBack(ctx, c.host.ID())
	if err != nil {
		t.Fatal(err)
	}

	_, err = ac.DialBack(ctx, c.host.ID())
	if err == nil {
		t.Fatal("Dial back succeeded unexpectedly!")
	}

	if !IsDialRefused(err) {
		t.Fatal(err)
	}

	time.Sleep(400 * time.Millisecond)

	_, err = ac.DialBack(ctx, c.host.ID())
	if err != nil {
		t.Fatal(err)
	}
}

func TestAutoNATServiceGlobalLimiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := makeAutoNATConfig(t)
	defer c.host.Close()
	defer c.dialer.Close()

	c.dialTimeout = time.Second
	c.throttleResetPeriod = 10 * time.Second
	c.throttleResetJitter = 0
	c.throttlePeerMax = 1
	c.throttleGlobalMax = 5
	_ = makeAutoNATService(t, c)

	hs := c.host

	for i := 0; i < 5; i++ {
		hc, ac := makeAutoNATClient(t)
		connect(t, hs, hc)

		_, err := ac.DialBack(ctx, hs.ID())
		if err != nil {
			t.Fatal(err)
		}
	}

	hc, ac := makeAutoNATClient(t)
	defer hc.Close()
	connect(t, hs, hc)
	_, err := ac.DialBack(ctx, hs.ID())
	if err == nil {
		t.Fatal("Dial back succeeded unexpectedly!")
	}

	if !IsDialRefused(err) {
		t.Fatal(err)
	}
}

func TestAutoNATServiceRateLimitJitter(t *testing.T) {
	c := makeAutoNATConfig(t)
	defer c.host.Close()
	defer c.dialer.Close()

	dur := 100 * time.Millisecond
	if os.Getenv("CI") != "" {
		dur = 200 * time.Millisecond
	}

	c.throttleResetPeriod = dur
	c.throttleResetJitter = dur
	c.throttleGlobalMax = 1
	svc := makeAutoNATService(t, c)
	svc.mx.Lock()
	svc.globalReqs = 1
	svc.mx.Unlock()

	require.Eventually(t, func() bool {
		svc.mx.Lock()
		defer svc.mx.Unlock()
		return svc.globalReqs == 0
	}, dur*5/2, 10*time.Millisecond, "reset of rate limitter occured slower than expected")
}

func TestAutoNATServiceStartup(t *testing.T) {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h.Close()
	dh := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer dh.Close()
	an, err := New(h, EnableService(dh.Network()))
	an.(*AmbientAutoNAT).config.dialPolicy.allowSelfDials = true
	if err != nil {
		t.Fatal(err)
	}

	hc, ac := makeAutoNATClient(t)
	connect(t, h, hc)

	_, err = ac.DialBack(context.Background(), h.ID())
	if err != nil {
		t.Fatal("autonat service be active in unknown mode.")
	}

	sub, _ := h.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))

	anc := an.(*AmbientAutoNAT)
	anc.recordObservation(autoNATResult{Reachability: network.ReachabilityPublic, address: ma.StringCast("/ip4/127.0.0.1/tcp/1234")})

	<-sub.Out()

	_, err = ac.DialBack(context.Background(), h.ID())
	if err != nil {
		t.Fatalf("autonat should be active, was %v", err)
	}
	if an.Status() != network.ReachabilityPublic {
		t.Fatalf("autonat should report public, but didn't")
	}
}
