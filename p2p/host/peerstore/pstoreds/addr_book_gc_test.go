package pstoreds

import (
	"context"
	"testing"
	"time"

	pstore "github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/test"

	mockClock "github.com/benbjohnson/clock"
	"github.com/ipfs/go-datastore/query"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

var lookaheadQuery = query.Query{Prefix: gcLookaheadBase.String(), KeysOnly: true}

type testProbe struct {
	t  *testing.T
	ab pstore.AddrBook
}

func (tp *testProbe) countLookaheadEntries() (i int) {
	results, err := tp.ab.(*dsAddrBook).ds.Query(context.Background(), lookaheadQuery)
	if err != nil {
		tp.t.Fatal(err)
	}

	defer results.Close()
	for range results.Next() {
		i++
	}
	return i
}

func (tp *testProbe) clearCache() {
	for _, k := range tp.ab.(*dsAddrBook).cache.Keys() {
		tp.ab.(*dsAddrBook).cache.Remove(k)
	}
}

func TestGCLookahead(t *testing.T) {
	opts := DefaultOpts()

	// effectively disable automatic GC for this test.
	opts.GCInitialDelay = 90 * time.Hour
	opts.GCLookaheadInterval = 10 * time.Second
	opts.GCPurgeInterval = 1 * time.Second

	factory := addressBookFactory(t, leveldbStore, opts)
	ab, closeFn := factory()
	gc := ab.(*dsAddrBook).gc
	defer closeFn()

	tp := &testProbe{t, ab}

	ids := test.GeneratePeerIDs(10)
	addrs := test.GenerateAddrs(100)

	// lookahead is 10 seconds, so these entries will be outside the lookahead window.
	ab.AddAddrs(ids[0], addrs[:10], time.Hour)
	ab.AddAddrs(ids[1], addrs[10:20], time.Hour)
	ab.AddAddrs(ids[2], addrs[20:30], time.Hour)

	gc.populateLookahead()
	if i := tp.countLookaheadEntries(); i != 0 {
		t.Errorf("expected no GC lookahead entries, got: %v", i)
	}

	// change addresses of a peer to have TTL 1 second, placing them in the lookahead window.
	ab.UpdateAddrs(ids[1], time.Hour, time.Second)

	// Purge the cache, to exercise a different path in the lookahead cycle.
	tp.clearCache()

	gc.populateLookahead()
	if i := tp.countLookaheadEntries(); i != 1 {
		t.Errorf("expected 1 GC lookahead entry, got: %v", i)
	}

	// change addresses of another to have TTL 5 second, placing them in the lookahead window.
	ab.UpdateAddrs(ids[2], time.Hour, 5*time.Second)
	gc.populateLookahead()
	if i := tp.countLookaheadEntries(); i != 2 {
		t.Errorf("expected 2 GC lookahead entries, got: %v", i)
	}
}

func TestGCPurging(t *testing.T) {
	opts := DefaultOpts()

	// effectively disable automatic GC for this test.
	opts.GCInitialDelay = 90 * time.Hour
	opts.GCLookaheadInterval = 20 * time.Second
	opts.GCPurgeInterval = 1 * time.Second
	clk := mockClock.NewMock()
	opts.Clock = clk

	factory := addressBookFactory(t, leveldbStore, opts)
	ab, closeFn := factory()
	gc := ab.(*dsAddrBook).gc
	defer closeFn()

	tp := &testProbe{t, ab}

	ids := test.GeneratePeerIDs(10)
	addrs := test.GenerateAddrs(100)

	// stagger addresses within the lookahead window, but stagger them.
	ab.AddAddrs(ids[0], addrs[:10], 1*time.Second)
	ab.AddAddrs(ids[1], addrs[30:40], 1*time.Second)
	ab.AddAddrs(ids[2], addrs[60:70], 1*time.Second)

	ab.AddAddrs(ids[0], addrs[10:20], 4*time.Second)
	ab.AddAddrs(ids[1], addrs[40:50], 4*time.Second)

	ab.AddAddrs(ids[0], addrs[20:30], 10*time.Second)
	ab.AddAddrs(ids[1], addrs[50:60], 10*time.Second)

	// this is inside the window, but it will survive the purges we do in the test.
	ab.AddAddrs(ids[3], addrs[70:80], 15*time.Second)

	gc.populateLookahead()
	if i := tp.countLookaheadEntries(); i != 4 {
		t.Errorf("expected 4 GC lookahead entries, got: %v", i)
	}

	clk.Add(2 * time.Second)
	gc.purgeLookahead()
	if i := tp.countLookaheadEntries(); i != 3 {
		t.Errorf("expected 3 GC lookahead entries, got: %v", i)
	}

	// Purge the cache, to exercise a different path in the purge cycle.
	tp.clearCache()

	clk.Add(5 * time.Second)
	gc.purgeLookahead()
	if i := tp.countLookaheadEntries(); i != 3 {
		t.Errorf("expected 3 GC lookahead entries, got: %v", i)
	}

	clk.Add(5 * time.Second)
	gc.purgeLookahead()
	if i := tp.countLookaheadEntries(); i != 1 {
		t.Errorf("expected 1 GC lookahead entries, got: %v", i)
	}
	if i := len(ab.PeersWithAddrs()); i != 1 {
		t.Errorf("expected 1 entries in database, got: %v", i)
	}
	if p := ab.PeersWithAddrs()[0]; p != ids[3] {
		t.Errorf("expected remaining peer to be #3, got: %v, expected: %v", p, ids[3])
	}
}

func TestGCDelay(t *testing.T) {
	ids := test.GeneratePeerIDs(10)
	addrs := test.GenerateAddrs(100)

	clk := mockClock.NewMock()
	opts := DefaultOpts()
	opts.GCInitialDelay = 2 * time.Second
	opts.GCLookaheadInterval = 1 * time.Minute
	opts.GCPurgeInterval = 30 * time.Second
	opts.Clock = clk

	factory := addressBookFactory(t, leveldbStore, opts)
	ab, closeFn := factory()
	defer closeFn()
	// give the background Go routine some time to start
	time.Sleep(100 * time.Millisecond)

	tp := &testProbe{t, ab}

	ab.AddAddrs(ids[0], addrs, 1*time.Second)

	// immediately after we should be having no lookahead entries.
	if i := tp.countLookaheadEntries(); i != 0 {
		t.Fatalf("expected no lookahead entries, got: %d", i)
	}

	// after the initial delay has passed.
	clk.Add(3 * time.Second)
	require.Eventually(t, func() bool { return tp.countLookaheadEntries() == 1 }, 3000*time.Millisecond, 10*time.Millisecond, "expected 1 lookahead entry")
}

func TestGCLookaheadDisabled(t *testing.T) {
	ids := test.GeneratePeerIDs(10)
	addrs := test.GenerateAddrs(100)

	opts := DefaultOpts()

	// effectively disable automatic GC for this test.
	opts.GCInitialDelay = 90 * time.Hour
	opts.GCLookaheadInterval = 0 // disable lookahead
	opts.GCPurgeInterval = 9 * time.Hour
	clk := mockClock.NewMock()
	opts.Clock = clk

	factory := addressBookFactory(t, leveldbStore, opts)
	ab, closeFn := factory()
	defer closeFn()

	tp := &testProbe{t, ab}

	// four peers:
	//   ids[0] has 10 addresses, all of which expire in 500ms.
	//   ids[1] has 20 addresses; 50% expire in 500ms and 50% in 10 hours.
	//   ids[2] has 10 addresses; all expire in 10 hours.
	//   ids[3] has 60 addresses; all expire in 10 hours.
	ab.AddAddrs(ids[0], addrs[:10], 500*time.Millisecond)
	ab.AddAddrs(ids[1], addrs[10:20], 500*time.Millisecond)
	ab.AddAddrs(ids[1], addrs[20:30], 10*time.Hour)
	ab.AddAddrs(ids[2], addrs[30:40], 10*time.Hour)
	ab.AddAddrs(ids[3], addrs[40:], 10*time.Hour)

	clk.Add(100 * time.Millisecond)

	if i := tp.countLookaheadEntries(); i != 0 {
		t.Errorf("expected no GC lookahead entries, got: %v", i)
	}

	clk.Add(500 * time.Millisecond)
	gc := ab.(*dsAddrBook).gc
	gc.purgeFunc()

	var empty []ma.Multiaddr
	test.AssertAddressesEqual(t, empty, ab.Addrs(ids[0]))
	test.AssertAddressesEqual(t, addrs[20:30], ab.Addrs(ids[1]))
	test.AssertAddressesEqual(t, addrs[30:40], ab.Addrs(ids[2]))
	test.AssertAddressesEqual(t, addrs[40:], ab.Addrs(ids[3]))
}

func BenchmarkLookaheadCycle(b *testing.B) {
	ids := test.GeneratePeerIDs(100)
	addrs := test.GenerateAddrs(100)

	opts := DefaultOpts()

	opts.GCInitialDelay = 2 * time.Hour
	opts.GCLookaheadInterval = 2 * time.Hour
	opts.GCPurgeInterval = 6 * time.Hour

	factory := addressBookFactory(b, leveldbStore, opts)
	ab, closeFn := factory()
	defer closeFn()

	inside, outside := 1*time.Minute, 48*time.Hour
	for i, id := range ids {
		var ttl time.Duration
		if i%2 == 0 {
			ttl = inside
		} else {
			ttl = outside
		}
		ab.AddAddrs(id, addrs, ttl)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ab.(*dsAddrBook).gc.populateLookahead()
	}
}
