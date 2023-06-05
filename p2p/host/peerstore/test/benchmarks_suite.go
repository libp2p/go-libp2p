package test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	pstore "github.com/libp2p/go-libp2p/core/peerstore"
)

var peerstoreBenchmarks = map[string]func(pstore.Peerstore, chan *peerpair) func(*testing.B){
	"AddAddrs": benchmarkAddAddrs,
	"SetAddrs": benchmarkSetAddrs,
	"GetAddrs": benchmarkGetAddrs,
	// The in-between get allows us to benchmark the read-through cache.
	"AddGetAndClearAddrs": benchmarkAddGetAndClearAddrs,
	// Calls PeersWithAddr on a peerstore with 1000 peers.
	"Get1000PeersWithAddrs": benchmarkGet1000PeersWithAddrs,
}

func BenchmarkPeerstore(b *testing.B, factory PeerstoreFactory, variant string) {
	// Parameterises benchmarks to tackle peers with 1, 10, 100 multiaddrs.
	params := []struct {
		n  int
		ch chan *peerpair
	}{
		{1, make(chan *peerpair, 100)},
		{10, make(chan *peerpair, 100)},
		{100, make(chan *peerpair, 100)},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start all test peer producing goroutines, where each produces peers with as many
	// multiaddrs as the n field in the param struct.
	for _, p := range params {
		go AddressProducer(ctx, b, p.ch, p.n)
	}

	// So tests are always run in the same order.
	ordernames := make([]string, 0, len(peerstoreBenchmarks))
	for name := range peerstoreBenchmarks {
		ordernames = append(ordernames, name)
	}
	sort.Strings(ordernames)

	for _, name := range ordernames {
		bench := peerstoreBenchmarks[name]
		for _, p := range params {
			// Create a new peerstore.
			ps, closeFunc := factory()

			// Run the test.
			b.Run(fmt.Sprintf("%s-%dAddrs-%s", name, p.n, variant), bench(ps, p.ch))

			// Cleanup.
			if closeFunc != nil {
				closeFunc()
			}
		}
	}
}

func benchmarkAddAddrs(ps pstore.Peerstore, addrs chan *peerpair) func(*testing.B) {
	return func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			pp := <-addrs
			ps.AddAddrs(pp.ID, pp.Addr, pstore.PermanentAddrTTL)
		}
	}
}

func benchmarkSetAddrs(ps pstore.Peerstore, addrs chan *peerpair) func(*testing.B) {
	return func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			pp := <-addrs
			ps.SetAddrs(pp.ID, pp.Addr, pstore.PermanentAddrTTL)
		}
	}
}

func benchmarkGetAddrs(ps pstore.Peerstore, addrs chan *peerpair) func(*testing.B) {
	return func(b *testing.B) {
		pp := <-addrs
		ps.SetAddrs(pp.ID, pp.Addr, pstore.PermanentAddrTTL)

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = ps.Addrs(pp.ID)
		}
	}
}

func benchmarkAddGetAndClearAddrs(ps pstore.Peerstore, addrs chan *peerpair) func(*testing.B) {
	return func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			pp := <-addrs
			ps.AddAddrs(pp.ID, pp.Addr, pstore.PermanentAddrTTL)
			ps.Addrs(pp.ID)
			ps.ClearAddrs(pp.ID)
		}
	}
}

func benchmarkGet1000PeersWithAddrs(ps pstore.Peerstore, addrs chan *peerpair) func(*testing.B) {
	return func(b *testing.B) {
		var peers = make([]*peerpair, 1000)
		for i := range peers {
			pp := <-addrs
			ps.AddAddrs(pp.ID, pp.Addr, pstore.PermanentAddrTTL)
			peers[i] = pp
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = ps.PeersWithAddrs()
		}
	}
}
