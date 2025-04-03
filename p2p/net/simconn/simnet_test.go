package simconn

import (
	"math"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const oneMbps = 1_000_000

func newConn(router *SimpleSimNet, address *net.UDPAddr, linkSettings NodeBiDiLinkSettings) *SimConn {
	c := NewSimConn(address, router)
	router.AddNode(address, c, linkSettings)
	return c
}

func TestSimpleSimNet(t *testing.T) {
	router := &SimpleSimNet{}

	const bandwidth = 10 * oneMbps
	const latency = 10 * time.Millisecond
	linkSettings := NodeBiDiLinkSettings{
		Downlink: LinkSettings{
			BitsPerSecond: bandwidth,
			Latency:       latency / 2,
		},
		Uplink: LinkSettings{
			BitsPerSecond: bandwidth,
			Latency:       latency / 2,
		},
	}

	addressA := net.UDPAddr{
		IP:   net.ParseIP("1.0.0.1"),
		Port: 8000,
	}
	connA := newConn(router, &addressA, linkSettings)
	addressB := net.UDPAddr{
		IP:   net.ParseIP("1.0.0.2"),
		Port: 8000,
	}
	connB := newConn(router, &addressB, linkSettings)

	router.Start()
	defer router.Close()

	start := time.Now()
	connA.WriteTo([]byte("hello"), &addressB)
	buf := make([]byte, 1024)
	n, from, err := connB.ReadFrom(buf)
	require.NoError(t, err)
	require.Equal(t, "hello", string(buf[:n]))
	require.Equal(t, addressA.String(), from.String())
	observedLatency := time.Since(start)

	expectedLatency := latency
	percentDiff := math.Abs(float64(observedLatency-expectedLatency) / float64(expectedLatency))
	t.Logf("observed latency: %v, expected latency: %v, percent diff: %v", observedLatency, expectedLatency, percentDiff)
	if percentDiff > 0.30 {
		t.Fatalf("latency is wrong: %v. percent off: %v", observedLatency, percentDiff)
	}
}
