//go:build nocover

package basichost

import (
	"math/rand"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsNoAllocNoCover(t *testing.T) {
	addrs := []ma.Multiaddr{
		ma.StringCast("/ip4/1.2.3.4/tcp/1"),
		ma.StringCast("/ip4/1.2.3.4/tcp/2"),
		ma.StringCast("/ip4/1.2.3.4/udp/2345/quic"),
		ma.StringCast("/ip4/1.2.3.4/udp/2346/webrtc-direct"),
		ma.StringCast("/ip4/1.2.3.4/tcp/80/ws"),
		ma.StringCast("/ip4/1.2.3.4/tcp/443/wss"),
		ma.StringCast("/ip4/1.2.3.4/udp/443/quic-v1/webtransport"),
	}

	randAddrs := func() []ma.Multiaddr {
		n := rand.Intn(len(addrs))
		k := n + rand.Intn(len(addrs)-n)
		return addrs[n:k]
	}

	mt := newMetricsTracker(withRegisterer(prometheus.DefaultRegisterer))
	tests := map[string]func(){
		"ConfirmedAddrsChanged": func() {
			mt.ConfirmedAddrsChanged(randAddrs(), randAddrs(), randAddrs())
		},
		"ReachabilityTrackerClosed": func() {
			mt.ReachabilityTrackerClosed()
		},
	}

	for method, f := range tests {
		allocs := testing.AllocsPerRun(1000, f)
		if allocs > 0 {
			t.Fatalf("Alloc Test: %s, got: %0.2f, expected: 0 allocs", method, allocs)
		}
	}
}
