//go:build goexperiment.synctest

package simconnlibp2p_test

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"testing/synctest"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/simconn"
	simlibp2p "github.com/libp2p/go-libp2p/p2p/net/simconn/libp2p"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/stretchr/testify/require"
)

func TestSimpleLibp2pNetwork_synctest(t *testing.T) {
	synctest.Run(func() {
		latency := 10 * time.Millisecond
		network, meta, err := simlibp2p.SimpleLibp2pNetwork([]simlibp2p.NodeLinkSettingsAndCount{
			{LinkSettings: simconn.NodeBiDiLinkSettings{
				Downlink: simconn.LinkSettings{BitsPerSecond: 20 * simlibp2p.OneMbps, Latency: latency / 2}, // Divide by two since this is latency for each direction
				Uplink:   simconn.LinkSettings{BitsPerSecond: 20 * simlibp2p.OneMbps, Latency: latency / 2},
			}, Count: 100},
		}, simlibp2p.NetworkSettings{})
		require.NoError(t, err)
		network.Start()
		defer network.Close()

		defer func() {
			for _, node := range meta.Nodes {
				node.Close()
			}
		}()

		// Test random nodes can ping each other
		const numQueries = 100
		for range numQueries {
			i := rand.Intn(len(meta.Nodes))
			j := rand.Intn(len(meta.Nodes))
			for i == j {
				j = rand.Intn(len(meta.Nodes))
			}
			h1 := meta.Nodes[i]
			h2 := meta.Nodes[j]
			t.Logf("connecting %s <-> %s", h1.ID(), h2.ID())
			err := h1.Connect(context.Background(), peer.AddrInfo{
				ID:    h2.ID(),
				Addrs: h2.Addrs(),
			})
			require.NoError(t, err)
			pingA := ping.NewPingService(h1)
			ping.NewPingService(h2)
			time.Sleep(1 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			t.Logf("pinging %s <-> %s", h1.ID(), h2.ID())
			res := pingA.Ping(ctx, meta.Nodes[j].ID())
			result := <-res
			t.Logf("pinged %s <-> %s", h1.ID(), h2.ID())
			require.NoError(t, result.Error)
			t.Logf("ping: (%d) <-> (%d): %v", i, j, result.RTT)
			expectedLatency := 20 * time.Millisecond // RTT is the sum of the latency of the two links
			percentDiff := float64(result.RTT-expectedLatency) / float64(expectedLatency)
			if percentDiff > 0.20 {
				t.Fatalf("latency is wrong: %v. percent off: %v", result.RTT, percentDiff)
			}
		}
	})
}
