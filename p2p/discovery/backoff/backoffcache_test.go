package backoff

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/p2p/discovery/mocks"
	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	"github.com/libp2p/go-libp2p/core/discovery"
	"github.com/libp2p/go-libp2p/core/peer"

	mockClock "github.com/benbjohnson/clock"
)

type delayedDiscovery struct {
	disc  discovery.Discovery
	delay time.Duration
	clock *mockClock.Mock
}

func (d *delayedDiscovery) Advertise(ctx context.Context, ns string, opts ...discovery.Option) (time.Duration, error) {
	return d.disc.Advertise(ctx, ns, opts...)
}

func (d *delayedDiscovery) FindPeers(ctx context.Context, ns string, opts ...discovery.Option) (<-chan peer.AddrInfo, error) {
	dch, err := d.disc.FindPeers(ctx, ns, opts...)
	if err != nil {
		return nil, err
	}

	ch := make(chan peer.AddrInfo, 32)
	doneCh := make(chan struct{})
	go func() {
		defer close(ch)
		defer close(doneCh)
		for ai := range dch {
			ch <- ai
			d.clock.Sleep(d.delay)
		}
	}()

	// Tick the clock forward to advance the sleep above
	go func() {
		for {
			select {
			case <-doneCh:
				return
			default:
				d.clock.Add(d.delay)
			}
		}
	}()

	return ch, nil
}

func assertNumPeers(t *testing.T, ctx context.Context, d discovery.Discovery, ns string, count int) {
	t.Helper()
	assertNumPeersWithLimit(t, ctx, d, ns, 10, count)
}

func assertNumPeersWithLimit(t *testing.T, ctx context.Context, d discovery.Discovery, ns string, limit int, count int) {
	t.Helper()
	peerCh, err := d.FindPeers(ctx, ns, discovery.Limit(limit))
	if err != nil {
		t.Fatal(err)
	}

	peerset := make(map[peer.ID]struct{})
	for p := range peerCh {
		peerset[p.ID] = struct{}{}
	}

	if len(peerset) != count {
		t.Fatalf("Was supposed to find %d, found %d instead", count, len(peerset))
	}
}

// withClock lets you override the default time.Now() call. Useful for tests.
func withClock(c clock) BackoffDiscoveryOption {
	return func(b *BackoffDiscovery) error {
		b.clock = c
		return nil
	}
}

func TestBackoffDiscoverySingleBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := mockClock.NewMock()
	discServer := mocks.NewDiscoveryServer(clock)

	h1 := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h1.Close()
	h2 := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h2.Close()
	d1 := mocks.NewDiscoveryClient(h1, discServer)
	d2 := mocks.NewDiscoveryClient(h2, discServer)

	bkf := NewExponentialBackoff(
		time.Millisecond*100,
		time.Second*10,
		NoJitter,
		time.Millisecond*100,
		2.5,
		0,
		rand.NewSource(0),
	)
	dCache, err := NewBackoffDiscovery(d1, bkf, withClock(clock))
	if err != nil {
		t.Fatal(err)
	}

	const ns = "test"

	// try adding a peer then find it
	d1.Advertise(ctx, ns, discovery.TTL(time.Hour))
	// Advance clock by one step
	clock.Add(1)
	assertNumPeers(t, ctx, dCache, ns, 1)

	// add a new peer and make sure it is still hidden by the caching layer
	d2.Advertise(ctx, ns, discovery.TTL(time.Hour))
	// Advance clock by one step
	clock.Add(1)
	assertNumPeers(t, ctx, dCache, ns, 1)

	// wait for cache to expire and check for the new peer
	clock.Add(time.Millisecond * 110)
	assertNumPeers(t, ctx, dCache, ns, 2)
}

func TestBackoffDiscoveryMultipleBackoff(t *testing.T) {
	clock := mockClock.NewMock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discServer := mocks.NewDiscoveryServer(clock)

	h1 := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h1.Close()
	h2 := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h2.Close()
	d1 := mocks.NewDiscoveryClient(h1, discServer)
	d2 := mocks.NewDiscoveryClient(h2, discServer)

	// Startup delay is 0ms. First backoff after finding data is 100ms, second backoff is 250ms.
	bkf := NewExponentialBackoff(
		time.Millisecond*100,
		time.Second*10,
		NoJitter,
		time.Millisecond*100,
		2.5,
		0,
		rand.NewSource(0),
	)
	dCache, err := NewBackoffDiscovery(d1, bkf, withClock(clock))
	if err != nil {
		t.Fatal(err)
	}

	const ns = "test"

	// try adding a peer then find it
	d1.Advertise(ctx, ns, discovery.TTL(time.Hour))
	// Advance clock by one step
	clock.Add(1)
	assertNumPeers(t, ctx, dCache, ns, 1)

	// wait a little to make sure the extra request doesn't modify the backoff
	clock.Add(time.Millisecond * 50) // 50 < 100
	assertNumPeers(t, ctx, dCache, ns, 1)

	// wait for backoff to expire and check if we increase it
	clock.Add(time.Millisecond * 60) // 50+60 > 100
	assertNumPeers(t, ctx, dCache, ns, 1)

	d2.Advertise(ctx, ns, discovery.TTL(time.Millisecond*400))

	clock.Add(time.Millisecond * 150) // 150 < 250
	assertNumPeers(t, ctx, dCache, ns, 1)

	clock.Add(time.Millisecond * 150) // 150 + 150 > 250
	assertNumPeers(t, ctx, dCache, ns, 2)

	// check that the backoff has been reset
	// also checks that we can decrease our peer count (i.e. not just growing a set)
	clock.Add(time.Millisecond * 110) // 110 > 100, also 150+150+110>400
	assertNumPeers(t, ctx, dCache, ns, 1)
}

func TestBackoffDiscoverySimultaneousQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := mockClock.NewMock()
	discServer := mocks.NewDiscoveryServer(clock)

	// Testing with n larger than most internal buffer sizes (32)
	n := 40
	advertisers := make([]discovery.Discovery, n)

	for i := 0; i < n; i++ {
		h := bhost.NewBlankHost(swarmt.GenSwarm(t))
		defer h.Close()
		advertisers[i] = mocks.NewDiscoveryClient(h, discServer)
	}

	d1 := &delayedDiscovery{advertisers[0], time.Millisecond * 10, clock}

	bkf := NewFixedBackoff(time.Millisecond * 200)
	dCache, err := NewBackoffDiscovery(d1, bkf, withClock(clock))
	if err != nil {
		t.Fatal(err)
	}

	const ns = "test"

	for _, a := range advertisers {
		if _, err := a.Advertise(ctx, ns, discovery.TTL(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	// Advance clock by one step
	clock.Add(1)

	ch1, err := dCache.FindPeers(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}

	<-ch1
	ch2, err := dCache.FindPeers(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}

	szCh2 := 0
	for ai := range ch2 {
		_ = ai
		szCh2++
	}

	szCh1 := 1
	for range ch1 {
		szCh1++
	}

	if szCh1 != n && szCh2 != n {
		t.Fatalf("Channels returned %d, %d elements instead of %d", szCh1, szCh2, n)
	}
}

func TestBackoffDiscoveryCacheCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := mockClock.NewMock()
	discServer := mocks.NewDiscoveryServer(clock)

	// Testing with n larger than most internal buffer sizes (32)
	n := 40
	advertisers := make([]discovery.Discovery, n)

	for i := 0; i < n; i++ {
		h := bhost.NewBlankHost(swarmt.GenSwarm(t))
		defer h.Close()
		advertisers[i] = mocks.NewDiscoveryClient(h, discServer)
	}

	h1 := bhost.NewBlankHost(swarmt.GenSwarm(t))
	d1 := mocks.NewDiscoveryClient(h1, discServer)

	discoveryInterval := time.Millisecond * 10

	bkf := NewFixedBackoff(discoveryInterval)
	dCache, err := NewBackoffDiscovery(d1, bkf, withClock(clock))
	if err != nil {
		t.Fatal(err)
	}

	const ns = "test"

	// add speers
	for i := 0; i < n; i++ {
		advertisers[i].Advertise(ctx, ns, discovery.TTL(time.Hour))
	}
	// Advance clock by one step
	clock.Add(1)

	// Request all peers, all will be present
	assertNumPeersWithLimit(t, ctx, dCache, ns, n, n)

	// Request peers with a lower limit
	assertNumPeersWithLimit(t, ctx, dCache, ns, n-1, n-1)

	// Wait a little time but don't allow cache to expire
	clock.Add(discoveryInterval / 10)

	// Request peers with a lower limit this time using cache
	// Here we are testing that the cache logic does not block when there are more peers known than the limit requested
	// See https://github.com/libp2p/go-libp2p-discovery/issues/67
	assertNumPeersWithLimit(t, ctx, dCache, ns, n-1, n-1)

	// Wait for next discovery so next request will bypass cache
	clock.Add(time.Millisecond * 100)

	// Ask for all peers again
	assertNumPeersWithLimit(t, ctx, dCache, ns, n, n)
}
