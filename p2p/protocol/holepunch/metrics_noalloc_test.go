//go:build nocover

package holepunch

import (
	"math/rand"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	ma "github.com/multiformats/go-multiaddr"
)

func TestNoCoverNoAllocMetrics(t *testing.T) {
	addrs1 := [][]ma.Multiaddr{
		{
			ma.StringCast("/ip4/0.0.0.0/tcp/1"),
			ma.StringCast("/ip4/1.2.3.4/udp/2/quic"),
		},
		nil,
	}
	addrs2 := [][]ma.Multiaddr{
		{
			ma.StringCast("/ip4/1.2.3.4/tcp/3"),
			ma.StringCast("/ip4/1.2.3.4/udp/4/quic"),
		},
		nil,
	}
	conns := []network.ConnMultiaddrs{
		&mockConnMultiaddrs{local: addrs1[0][0], remote: addrs2[0][0]},
		nil,
	}
	sides := []string{"initiator", "receiver"}
	mt := NewMetricsTracer()
	testcases := map[string]func(){
		"DirectDialFinished": func() { mt.DirectDialFinished(rand.Intn(2) == 1) },
		"HolePunchFinished": func() {
			mt.HolePunchFinished(sides[rand.Intn(len(sides))], rand.Intn(maxRetries), addrs1[rand.Intn(len(addrs1))],
				addrs2[rand.Intn(len(addrs2))], conns[rand.Intn(len(conns))])
		},
	}
	for method, f := range testcases {
		t.Run(method, func(t *testing.T) {
			cnt := testing.AllocsPerRun(1000, f)
			if cnt > 0 {
				t.Errorf("%s Failed: expected 0 allocs got %0.2f", method, cnt)
			}
		})
	}
}
