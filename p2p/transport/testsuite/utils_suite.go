package ttransport

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
)

type TransportSubTestFn func(t *testing.T, ta, tb transport.Transport, maddr ma.Multiaddr, peerA peer.ID)

var Subtests = []TransportSubTestFn{
	SubtestProtocols,
	SubtestBasic,
	SubtestCancel,
	SubtestPingPong,

	// Stolen from the stream muxer test suite.
	SubtestStress1Conn1Stream1Msg,
	SubtestStress1Conn1Stream100Msg,
	SubtestStress1Conn100Stream100Msg,
	SubtestStressManyConn10Stream50Msg,
	SubtestStress1Conn1000Stream10Msg,
	SubtestStress1Conn100Stream100Msg10MB,
	SubtestStreamOpenStress,
	SubtestStreamReset,
}

func getFunctionName(i interface{}) string {
	return runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
}

func SubtestTransport(t *testing.T, ta, tb transport.Transport, addr string, peerA peer.ID) {
	t.Helper()
	SubtestTransportWithFs(t, ta, tb, addr, peerA, Subtests)
}

func SubtestTransportWithFs(t *testing.T, ta, tb transport.Transport, addr string, peerA peer.ID, tests []TransportSubTestFn) {
	maddr, err := ma.NewMultiaddr(addr)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range tests {
		t.Run(getFunctionName(f), func(t *testing.T) {
			f(t, ta, tb, maddr, peerA)
		})
	}
}
