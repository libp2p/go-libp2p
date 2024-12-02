package pstoremem

import (
	"container/heap"
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestPeerAddrsNextExpiry(t *testing.T) {
	paa := newPeerAddrs()
	pa := &paa
	a1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")
	a2 := ma.StringCast("/ip4/1.2.3.4/udp/2/quic-v1")

	// t1 is before t2
	t1 := time.Time{}.Add(1 * time.Second)
	t2 := time.Time{}.Add(2 * time.Second)
	paa.Insert(&expiringAddr{Addr: a1, Expiry: t1, TTL: 10 * time.Second, Peer: "p1"})
	paa.Insert(&expiringAddr{Addr: a2, Expiry: t2, TTL: 10 * time.Second, Peer: "p2"})

	if pa.NextExpiry() != t1 {
		t.Fatal("expiry should be set to t1, got", pa.NextExpiry())
	}
}

func peerAddrsInput(n int) []*expiringAddr {
	expiringAddrs := make([]*expiringAddr, n)
	for i := 0; i < n; i++ {
		port := i % 65535
		a := ma.StringCast(fmt.Sprintf("/ip4/1.2.3.4/udp/%d/quic-v1", port))
		e := time.Time{}.Add(time.Duration(i) * time.Second)
		p := peer.ID(fmt.Sprintf("p%d", i))
		expiringAddrs[i] = &expiringAddr{Addr: a, Expiry: e, TTL: 10 * time.Second, Peer: p}
	}
	return expiringAddrs
}

func TestPeerAddrsHeapProperty(t *testing.T) {
	paa := newPeerAddrs()
	pa := &paa

	const N = 10000
	expiringAddrs := peerAddrsInput(N)
	for i := 0; i < N; i++ {
		paa.Insert(expiringAddrs[i])
	}

	for i := 0; i < N; i++ {
		ea, ok := pa.PopIfExpired(expiringAddrs[i].Expiry)
		require.True(t, ok, "pos: %d", i)
		require.Equal(t, ea.Addr, expiringAddrs[i].Addr)

		ea, ok = pa.PopIfExpired(expiringAddrs[i].Expiry)
		require.False(t, ok)
		require.Nil(t, ea)
	}
}

func TestPeerAddrsHeapPropertyDeletions(t *testing.T) {
	paa := newPeerAddrs()
	pa := &paa

	const N = 10000
	expiringAddrs := peerAddrsInput(N)
	for i := 0; i < N; i++ {
		paa.Insert(expiringAddrs[i])
	}

	// delete every 3rd element
	for i := 0; i < N; i += 3 {
		paa.Delete(expiringAddrs[i])
	}

	for i := 0; i < N; i++ {
		ea, ok := pa.PopIfExpired(expiringAddrs[i].Expiry)
		if i%3 == 0 {
			require.False(t, ok)
			require.Nil(t, ea)
		} else {
			require.True(t, ok)
			require.Equal(t, ea.Addr, expiringAddrs[i].Addr)
		}

		ea, ok = pa.PopIfExpired(expiringAddrs[i].Expiry)
		require.False(t, ok)
		require.Nil(t, ea)
	}
}

func TestPeerAddrsHeapPropertyUpdates(t *testing.T) {
	paa := newPeerAddrs()
	pa := &paa

	const N = 10000
	expiringAddrs := peerAddrsInput(N)
	for i := 0; i < N; i++ {
		heap.Push(pa, expiringAddrs[i])
	}

	// update every 3rd element to expire at the end
	var endElements []ma.Multiaddr
	for i := 0; i < N; i += 3 {
		expiringAddrs[i].Expiry = time.Time{}.Add(1000_000 * time.Second)
		pa.Update(expiringAddrs[i])
		endElements = append(endElements, expiringAddrs[i].Addr)
	}

	for i := 0; i < N; i++ {
		if i%3 == 0 {
			continue // skip the elements at the end
		}
		ea, ok := pa.PopIfExpired(expiringAddrs[i].Expiry)
		require.True(t, ok, "pos: %d", i)
		require.Equal(t, ea.Addr, expiringAddrs[i].Addr)

		ea, ok = pa.PopIfExpired(expiringAddrs[i].Expiry)
		require.False(t, ok)
		require.Nil(t, ea)
	}

	for len(endElements) > 0 {
		ea, ok := pa.PopIfExpired(time.Time{}.Add(1000_000 * time.Second))
		require.True(t, ok)
		require.Contains(t, endElements, ea.Addr)
		endElements = slices.DeleteFunc(endElements, func(a ma.Multiaddr) bool { return ea.Addr.Equal(a) })
	}
}

// TestPeerAddrsExpiry tests for multiple element expiry with PopIfExpired.
func TestPeerAddrsExpiry(t *testing.T) {
	const T = 100_000
	for x := 0; x < T; x++ {
		paa := newPeerAddrs()
		pa := &paa
		// Try a lot of random inputs.
		// T > 5*((5^5)*5) (=15k)
		// So this should test for all possible 5 element inputs.
		const N = 5
		expiringAddrs := peerAddrsInput(N)
		for i := 0; i < N; i++ {
			expiringAddrs[i].Expiry = time.Time{}.Add(time.Duration(1+rand.Intn(N)) * time.Second)
		}
		for i := 0; i < N; i++ {
			pa.Insert(expiringAddrs[i])
		}

		expiry := time.Time{}.Add(time.Duration(1+rand.Intn(N)) * time.Second)
		expected := []ma.Multiaddr{}
		for i := 0; i < N; i++ {
			if !expiry.Before(expiringAddrs[i].Expiry) {
				expected = append(expected, expiringAddrs[i].Addr)
			}
		}
		got := []ma.Multiaddr{}
		for {
			ea, ok := pa.PopIfExpired(expiry)
			if !ok {
				break
			}
			got = append(got, ea.Addr)
		}
		expiries := []int{}
		for i := 0; i < N; i++ {
			expiries = append(expiries, expiringAddrs[i].Expiry.Second())
		}
		require.ElementsMatch(t, expected, got, "failed for input: element expiries: %v, expiry: %v", expiries, expiry.Second())
	}
}

func TestPeerLimits(t *testing.T) {
	ab := NewAddrBook()
	defer ab.Close()
	ab.maxUnconnectedAddrs = 1024

	peers := peerAddrsInput(2048)
	for _, p := range peers {
		ab.AddAddr(p.Peer, p.Addr, p.TTL)
	}
	require.Equal(t, 1024, ab.addrs.NumUnconnectedAddrs())
}

func BenchmarkPeerAddrs(b *testing.B) {
	sizes := [...]int{1, 10, 100, 1000, 10_000, 100_000, 1000_000}
	for _, sz := range sizes {
		b.Run(fmt.Sprintf("%d", sz), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				paa := newPeerAddrs()
				pa := &paa
				expiringAddrs := peerAddrsInput(sz)
				for i := 0; i < sz; i++ {
					pa.Insert(expiringAddrs[i])
				}
				b.StartTimer()
				for {
					_, ok := pa.PopIfExpired(expiringAddrs[len(expiringAddrs)-1].Expiry)
					if !ok {
						break
					}
				}
			}
		})
	}

}
