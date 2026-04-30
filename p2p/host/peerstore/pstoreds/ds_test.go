package pstoreds

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	pstore "github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/record"
	"github.com/libp2p/go-libp2p/core/test"
	pt "github.com/libp2p/go-libp2p/p2p/host/peerstore/test"

	mockclock "github.com/benbjohnson/clock"
	ds "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func mapDBStore(_ testing.TB) (ds.Batching, func()) {
	store := ds.NewMapDatastore()
	closer := func() {
		store.Close()
	}
	return sync.MutexWrap(store), closer
}

type datastoreFactory func(tb testing.TB) (ds.Batching, func())

var dstores = map[string]datastoreFactory{
	"MapDB": mapDBStore,
}

func TestDsPeerstore(t *testing.T) {
	for name, dsFactory := range dstores {
		t.Run(name, func(t *testing.T) {
			pt.TestPeerstore(t, peerstoreFactory(t, dsFactory, DefaultOpts()))
		})

		t.Run("protobook limits", func(t *testing.T) {
			const limit = 10
			opts := DefaultOpts()
			opts.MaxProtocols = limit
			ds, close := dsFactory(t)
			defer close()
			ps, err := NewPeerstore(context.Background(), ds, opts)
			require.NoError(t, err)
			defer ps.Close()
			pt.TestPeerstoreProtoStoreLimits(t, ps, limit)
		})
	}
}

func TestDsAddrBook(t *testing.T) {
	for name, dsFactory := range dstores {
		t.Run(name+" Cacheful", func(t *testing.T) {
			opts := DefaultOpts()
			opts.GCPurgeInterval = 1 * time.Second
			opts.CacheSize = 1024
			// Shared addr-book suite inserts batches larger than the default
			// per-peer cap; disable the cap so the suite exercises general
			// behavior, not the cap path.
			opts.MaxAddrsPerPeer = 0
			clk := mockclock.NewMock()
			opts.Clock = clk

			pt.TestAddrBook(t, addressBookFactory(t, dsFactory, opts), clk)
		})

		t.Run(name+" Cacheless", func(t *testing.T) {
			opts := DefaultOpts()
			opts.GCPurgeInterval = 1 * time.Second
			opts.CacheSize = 0
			opts.MaxAddrsPerPeer = 0
			clk := mockclock.NewMock()
			opts.Clock = clk

			pt.TestAddrBook(t, addressBookFactory(t, dsFactory, opts), clk)
		})
	}
}

func TestDsMaxAddrsPerPeerEvictsNearestExpiry(t *testing.T) {
	for name, dsFactory := range dstores {
		t.Run(name, func(t *testing.T) {
			opts := DefaultOpts()
			opts.MaxAddrsPerPeer = 3
			clk := mockclock.NewMock()
			opts.Clock = clk

			ds, closeDs := dsFactory(t)
			defer closeDs()
			ab, err := NewAddrBook(context.Background(), ds, opts)
			require.NoError(t, err)
			defer ab.Close()

			const p = peer.ID("peer-cap")
			a1 := ma.StringCast("/ip4/1.2.3.4/tcp/1")
			a2 := ma.StringCast("/ip4/1.2.3.4/tcp/2")
			a3 := ma.StringCast("/ip4/1.2.3.4/tcp/3")
			a4 := ma.StringCast("/ip4/1.2.3.4/tcp/4")

			ab.AddAddr(p, a1, time.Hour)      // furthest expiry
			ab.AddAddr(p, a2, 30*time.Minute) // middle
			ab.AddAddr(p, a3, 10*time.Minute) // nearest expiry, will be evicted first
			require.ElementsMatch(t, []ma.Multiaddr{a1, a2, a3}, ab.Addrs(p))

			ab.AddAddr(p, a4, 45*time.Minute)
			require.ElementsMatch(t, []ma.Multiaddr{a1, a2, a4}, ab.Addrs(p))
		})
	}
}

func TestDsMaxAddrsPerPeerEnforcedOnSetAddrs(t *testing.T) {
	for name, dsFactory := range dstores {
		t.Run(name, func(t *testing.T) {
			opts := DefaultOpts()
			opts.MaxAddrsPerPeer = 2
			clk := mockclock.NewMock()
			opts.Clock = clk

			ds, closeDs := dsFactory(t)
			defer closeDs()
			ab, err := NewAddrBook(context.Background(), ds, opts)
			require.NoError(t, err)
			defer ab.Close()

			const p = peer.ID("peer-setaddrs")
			a1 := ma.StringCast("/ip4/1.2.3.4/tcp/1")
			a2 := ma.StringCast("/ip4/1.2.3.4/tcp/2")
			a3 := ma.StringCast("/ip4/1.2.3.4/tcp/3")

			ab.AddAddr(p, a1, time.Hour)      // furthest expiry
			ab.AddAddr(p, a2, 10*time.Minute) // nearest expiry, eviction target
			require.ElementsMatch(t, []ma.Multiaddr{a1, a2}, ab.Addrs(p))

			// SetAddrs with a new addr hits the cap; nearest-expiry a2 must go.
			ab.SetAddrs(p, []ma.Multiaddr{a3}, 30*time.Minute)
			require.ElementsMatch(t, []ma.Multiaddr{a1, a3}, ab.Addrs(p))
		})
	}
}

// TestDsMaxAddrsPerPeerDisabled verifies that MaxAddrsPerPeer = 0 disables
// the cap so callers can store more than the default 64 addrs per peer.
func TestDsMaxAddrsPerPeerDisabled(t *testing.T) {
	for name, dsFactory := range dstores {
		t.Run(name, func(t *testing.T) {
			opts := DefaultOpts()
			opts.MaxAddrsPerPeer = 0
			clk := mockclock.NewMock()
			opts.Clock = clk

			ds, closeDs := dsFactory(t)
			defer closeDs()
			ab, err := NewAddrBook(context.Background(), ds, opts)
			require.NoError(t, err)
			defer ab.Close()

			const p = peer.ID("peer-disabled")
			const n = 200 // well above the default cap of 64
			addrs := make([]ma.Multiaddr, n)
			for i := range addrs {
				addrs[i] = ma.StringCast(fmt.Sprintf("/ip4/1.2.3.4/tcp/%d", i+1))
				ab.AddAddr(p, addrs[i], time.Hour)
			}
			require.Len(t, ab.Addrs(p), n)
		})
	}
}

func TestDsMaxAddrsPerPeerDoesNotEvictConnected(t *testing.T) {
	for name, dsFactory := range dstores {
		t.Run(name, func(t *testing.T) {
			opts := DefaultOpts()
			opts.MaxAddrsPerPeer = 2
			clk := mockclock.NewMock()
			opts.Clock = clk

			ds, closeDs := dsFactory(t)
			defer closeDs()
			ab, err := NewAddrBook(context.Background(), ds, opts)
			require.NoError(t, err)
			defer ab.Close()

			const p = peer.ID("peer-connected")
			live := ma.StringCast("/ip4/1.2.3.4/tcp/1")
			a1 := ma.StringCast("/ip4/1.2.3.4/tcp/2")
			a2 := ma.StringCast("/ip4/1.2.3.4/tcp/3")
			a3 := ma.StringCast("/ip4/1.2.3.4/tcp/4")

			ab.AddAddr(p, live, pstore.ConnectedAddrTTL)
			ab.AddAddr(p, a1, 10*time.Minute)
			ab.AddAddr(p, a2, 20*time.Minute)
			require.ElementsMatch(t, []ma.Multiaddr{live, a1, a2}, ab.Addrs(p))

			// Adding a third unconnected addr must evict an unconnected one
			// (a1 has the soonest expiry), never the connected addr.
			ab.AddAddr(p, a3, 30*time.Minute)
			require.ElementsMatch(t, []ma.Multiaddr{live, a2, a3}, ab.Addrs(p))
		})
	}
}

// TestDsConsumePeerRecordReplacesStaleAddrs verifies replace-semantics on a
// newer signed peer record: addrs dropped from the new record are evicted,
// while unsigned addrs and addrs held by a live connection are kept.
func TestDsConsumePeerRecordReplacesStaleAddrs(t *testing.T) {
	for name, dsFactory := range dstores {
		t.Run(name, func(t *testing.T) {
			opts := DefaultOpts()
			store, closeDs := dsFactory(t)
			defer closeDs()
			ab, err := NewAddrBook(context.Background(), store, opts)
			require.NoError(t, err)
			defer ab.Close()

			priv, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
			require.NoError(t, err)
			id, err := peer.IDFromPrivateKey(priv)
			require.NoError(t, err)

			keep := ma.StringCast("/ip4/1.2.3.4/tcp/1")
			drop := ma.StringCast("/ip4/1.2.3.4/tcp/2")
			unsigned := ma.StringCast("/ip4/1.2.3.4/tcp/3")
			connected := ma.StringCast("/ip4/1.2.3.4/tcp/4")

			rec1 := peer.NewPeerRecord()
			rec1.PeerID = id
			rec1.Seq = 1
			rec1.Addrs = []ma.Multiaddr{keep, drop, connected}
			env1, err := record.Seal(rec1, priv)
			require.NoError(t, err)

			accepted, err := ab.ConsumePeerRecord(env1, time.Hour)
			require.NoError(t, err)
			require.True(t, accepted)

			ab.AddAddr(id, connected, pstore.ConnectedAddrTTL)
			ab.AddAddr(id, unsigned, time.Hour)
			require.ElementsMatch(t, []ma.Multiaddr{keep, drop, connected, unsigned}, ab.Addrs(id))

			rec2 := peer.NewPeerRecord()
			rec2.PeerID = id
			rec2.Seq = 2
			rec2.Addrs = []ma.Multiaddr{keep}
			env2, err := record.Seal(rec2, priv)
			require.NoError(t, err)

			accepted, err = ab.ConsumePeerRecord(env2, time.Hour)
			require.NoError(t, err)
			require.True(t, accepted)

			require.ElementsMatch(t, []ma.Multiaddr{keep, connected, unsigned}, ab.Addrs(id))
		})
	}
}

func TestDsKeyBook(t *testing.T) {
	for name, dsFactory := range dstores {
		t.Run(name, func(t *testing.T) {
			pt.TestKeyBook(t, keyBookFactory(t, dsFactory, DefaultOpts()))
		})
	}
}

func BenchmarkDsKeyBook(b *testing.B) {
	for name, dsFactory := range dstores {
		b.Run(name, func(b *testing.B) {
			pt.BenchmarkKeyBook(b, keyBookFactory(b, dsFactory, DefaultOpts()))
		})
	}
}

func BenchmarkDsPeerstore(b *testing.B) {
	caching := DefaultOpts()
	caching.CacheSize = 1024

	cacheless := DefaultOpts()
	cacheless.CacheSize = 0

	for name, dsFactory := range dstores {
		b.Run(name, func(b *testing.B) {
			pt.BenchmarkPeerstore(b, peerstoreFactory(b, dsFactory, caching), "Caching")
		})
		b.Run(name, func(b *testing.B) {
			pt.BenchmarkPeerstore(b, peerstoreFactory(b, dsFactory, cacheless), "Cacheless")
		})
	}
}

func peerstoreFactory(tb testing.TB, storeFactory datastoreFactory, opts Options) pt.PeerstoreFactory {
	return func() (pstore.Peerstore, func()) {
		store, storeCloseFn := storeFactory(tb)
		ps, err := NewPeerstore(context.Background(), store, opts)
		if err != nil {
			tb.Fatal(err)
		}
		closer := func() {
			ps.Close()
			storeCloseFn()
		}
		return ps, closer
	}
}

func addressBookFactory(tb testing.TB, storeFactory datastoreFactory, opts Options) pt.AddrBookFactory {
	return func() (pstore.AddrBook, func()) {
		store, closeFunc := storeFactory(tb)
		ab, err := NewAddrBook(context.Background(), store, opts)
		if err != nil {
			tb.Fatal(err)
		}
		closer := func() {
			ab.Close()
			closeFunc()
		}
		return ab, closer
	}
}

func keyBookFactory(tb testing.TB, storeFactory datastoreFactory, opts Options) pt.KeyBookFactory {
	return func() (pstore.KeyBook, func()) {
		store, storeCloseFn := storeFactory(tb)
		kb, err := NewKeyBook(context.Background(), store, opts)
		if err != nil {
			tb.Fatal(err)
		}
		return kb, storeCloseFn
	}
}
