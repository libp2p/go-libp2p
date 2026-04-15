package pstoreds

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	pstore "github.com/libp2p/go-libp2p/core/peerstore"
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
