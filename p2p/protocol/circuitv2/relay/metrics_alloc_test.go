//go:build nocover

package relay

import (
	"math/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/util"
)

func TestNoCoverNoAlloc(t *testing.T) {
	statuses := []pbv2.Status{
		pbv2.Status_OK,
		pbv2.Status_NO_RESERVATION,
		pbv2.Status_RESOURCE_LIMIT_EXCEEDED,
		pbv2.Status_PERMISSION_DENIED,
	}
	peerIds := []peer.ID{peer.ID("p1"), peer.ID("p2"), peer.ID("p3")}
	peers := []*pbv2.Peer{
		util.PeerInfoToPeerV2(peer.AddrInfo{ID: test.RandPeerIDFatal(t)}),
		util.PeerInfoToPeerV2(peer.AddrInfo{ID: test.RandPeerIDFatal(t)}),
		util.PeerInfoToPeerV2(peer.AddrInfo{ID: test.RandPeerIDFatal(t)}),
	}
	mt := NewMetricsTracer()
	tests := map[string]func(){
		"RelayStatus":               func() { mt.RelayStatus(rand.Intn(2) == 1) },
		"ConnectionOpened":          func() { mt.ConnectionOpened() },
		"ConnectionClosed":          func() { mt.ConnectionClosed(time.Duration(rand.Intn(10)) * time.Second) },
		"ConnectionRequestHandled":  func() { mt.ConnectionRequestHandled(peers[rand.Intn(len(peers))], statuses[rand.Intn(len(statuses))]) },
		"ReservationAllowed":        func() { mt.ReservationAllowed(peerIds[rand.Intn(len(peerIds))], rand.Intn(2) == 1) },
		"ReservationExpired":        func() { mt.ReservationExpired(rand.Intn(10)) },
		"ReservationRequestHandled": func() { mt.ReservationRequestHandled(statuses[rand.Intn(len(statuses))]) },
		"BytesTransferred":          func() { mt.BytesTransferred(rand.Intn(1000)) },
		"PeerDisconnected":          func() { mt.PeerDisconnected(peerIds[rand.Intn(len(peerIds))], time.Time{}) },
		"GC":                        func() { mt.GC() },
	}
	for method, f := range tests {
		allocs := testing.AllocsPerRun(1000, f)
		if allocs > 0 {
			t.Fatalf("Alloc Test: %s, got: %0.2f, expected: 0 allocs", method, allocs)
		}
	}
}
