package test

import (
	"fmt"
	"testing"

	pstore "github.com/libp2p/go-libp2p/core/peerstore"
)

func BenchmarkPeerstore(b *testing.B, factory PeerstoreFactory, variant string) {
	for _, sz := range []int{1, 10, 100} {
		const N = 10000
		peers := getPeerPairs(b, N, sz)

		b.Run(fmt.Sprintf("AddAddrs-%d", sz), func(b *testing.B) {
			ps, cleanup := factory()
			defer cleanup()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pp := peers[i%N]
				ps.AddAddrs(pp.ID, pp.Addr, pstore.RecentlyConnectedAddrTTL)
			}
		})

		b.Run(fmt.Sprintf("GetAddrs-%d", sz), func(b *testing.B) {
			ps, cleanup := factory()
			defer cleanup()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pp := peers[i%N]
				ps.SetAddrs(pp.ID, pp.Addr, pstore.RecentlyConnectedAddrTTL)
			}
		})

		b.Run(fmt.Sprintf("GetAndClearAddrs-%d", sz), func(b *testing.B) {
			ps, cleanup := factory()
			defer cleanup()
			b.ResetTimer()
			itersPerBM := 10
			for i := 0; i < b.N; i++ {
				for j := 0; j < itersPerBM; j++ {
					pp := peers[(i+j)%N]
					ps.AddAddrs(pp.ID, pp.Addr, pstore.RecentlyConnectedAddrTTL)
				}
				for j := 0; j < itersPerBM; j++ {
					pp := peers[(i+j)%N]
					ps.Addrs(pp.ID)
				}
				for j := 0; j < itersPerBM; j++ {
					pp := peers[(i+j)%N]
					ps.ClearAddrs(pp.ID)
				}
			}
		})

		b.Run(fmt.Sprintf("PeersWithAddrs-%d", sz), func(b *testing.B) {
			ps, cleanup := factory()
			defer cleanup()
			for _, pp := range peers {
				ps.AddAddrs(pp.ID, pp.Addr, pstore.RecentlyConnectedAddrTTL)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = ps.PeersWithAddrs()
			}
		})

		b.Run(fmt.Sprintf("SetAddrs-%d", sz), func(b *testing.B) {
			ps, cleanup := factory()
			defer cleanup()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pp := peers[i%N]
				ps.SetAddrs(pp.ID, pp.Addr, pstore.RecentlyConnectedAddrTTL)
			}
		})
	}
}
