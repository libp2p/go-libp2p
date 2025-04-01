package simconnlibp2p

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/simconn"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/stretchr/testify/require"
)

func TestSimpleSimNetPing(t *testing.T) {
	router := &simconn.SimpleSimNet{}

	const bandwidth = 10 * OneMbps
	const latency = 10 * time.Millisecond
	linkSettings := simconn.NodeBiDiLinkSettings{
		Downlink: simconn.LinkSettings{
			BitsPerSecond: bandwidth,
			Latency:       latency / 2,
		},
		Uplink: simconn.LinkSettings{
			BitsPerSecond: bandwidth,
			Latency:       latency / 2,
		},
	}

	hostA := MustNewHost(t,
		libp2p.ListenAddrStrings("/ip4/1.0.0.1/udp/8000/quic-v1"),
		QUICSimConnSimpleNet(router, linkSettings),
	)
	hostB := MustNewHost(t,
		libp2p.ListenAddrStrings("/ip4/1.0.0.2/udp/8000/quic-v1"),
		QUICSimConnSimpleNet(router, linkSettings),
	)

	router.Start()
	defer router.Close()

	err := hostA.Connect(context.Background(), peer.AddrInfo{
		ID:    hostB.ID(),
		Addrs: hostB.Addrs(),
	})
	require.NoError(t, err)

	pingA := ping.NewPingService(hostA)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	res := pingA.Ping(ctx, hostB.ID())
	result := <-res
	require.NoError(t, result.Error)
	t.Logf("pingA -> pingB: %v", result.RTT)

	expectedLatency := latency * 2 // RTT is the sum of the latency of the two links
	percentDiff := float64(result.RTT-expectedLatency) / float64(expectedLatency)
	if percentDiff > 0.20 {
		t.Fatalf("latency is wrong: %v. percent off: %v", result.RTT, percentDiff)
	}
}
