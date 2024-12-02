// go:build: unix

package tcp

import (
	"testing"

	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse"
	ttransport "github.com/libp2p/go-libp2p/p2p/transport/testsuite"

	"github.com/stretchr/testify/require"
)

func TestTcpTransportCollectsMetricsWithSharedTcpSocket(t *testing.T) {

	peerA, ia := makeInsecureMuxer(t)
	_, ib := makeInsecureMuxer(t)

	sharedTCPSocketA := tcpreuse.NewConnMgr(false, nil, nil)
	sharedTCPSocketB := tcpreuse.NewConnMgr(false, nil, nil)

	ua, err := tptu.New(ia, muxers, nil, nil, nil)
	require.NoError(t, err)
	ta, err := NewTCPTransport(ua, nil, sharedTCPSocketA, WithMetrics())
	require.NoError(t, err)
	ub, err := tptu.New(ib, muxers, nil, nil, nil)
	require.NoError(t, err)
	tb, err := NewTCPTransport(ub, nil, sharedTCPSocketB, WithMetrics())
	require.NoError(t, err)

	zero := "/ip4/127.0.0.1/tcp/0"

	// Not running any test that needs more than 1 conn because the testsuite
	// opens multiple conns via multiple listeners, which is not expected to work
	// with the shared TCP socket.
	subtestsToRun := []ttransport.TransportSubTestFn{
		ttransport.SubtestProtocols,
		ttransport.SubtestBasic,
		ttransport.SubtestCancel,
		ttransport.SubtestPingPong,

		// Stolen from the stream muxer test suite.
		ttransport.SubtestStress1Conn1Stream1Msg,
		ttransport.SubtestStress1Conn1Stream100Msg,
		ttransport.SubtestStress1Conn100Stream100Msg,
		ttransport.SubtestStress1Conn1000Stream10Msg,
		ttransport.SubtestStress1Conn100Stream100Msg10MB,
		ttransport.SubtestStreamOpenStress,
		ttransport.SubtestStreamReset,
	}

	ttransport.SubtestTransportWithFs(t, ta, tb, zero, peerA, subtestsToRun)
}
