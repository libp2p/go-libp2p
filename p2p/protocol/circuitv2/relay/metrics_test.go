package relay

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/util"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func getCounterValue(counter prometheus.Counter) (int, error) {
	m := &dto.Metric{}
	if err := counter.Write(m); err != nil {
		return 0, err
	}
	return int(*m.Counter.Value), nil
}

func getRandAddrInfo(t *testing.T) peer.AddrInfo {
	t.Helper()
	pid1 := test.RandPeerIDFatal(t)
	addrs1 := []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/udp/1/quic")}
	peer1 := peer.AddrInfo{ID: pid1, Addrs: addrs1}
	return peer1
}

func TestClientDisconnectionReason(t *testing.T) {
	reg := prometheus.NewRegistry()

	peer1 := getRandAddrInfo(t)

	mt := NewMetricsTracer(WithRegisterer(reg))

	// Trigger Disconnected Event
	expiry := time.Now().Add(10 * time.Second)
	mt.PeerDisconnected(peer1.ID, expiry)

	// This should increment "client disconnected"
	mt.ConnectionRequestHandled(util.PeerInfoToPeerV2(peer1), pb.Status_NO_RESERVATION)
	clientDisconnect, err := getCounterValue(connectionRejectionsTotal.WithLabelValues("client disconnected"))
	if err != nil {
		t.Errorf("Unexpected error %s", err)
	}
	noReservation, err := getCounterValue(connectionRejectionsTotal.WithLabelValues("no reservation"))
	if err != nil {
		t.Errorf("Unexpected err %s", err)
	}
	if clientDisconnect != 1 || noReservation != 0 {
		t.Errorf("Invalid count values: expected clientDisconnect: 1, noReservation: 0, got: clientDisconnect: %d, noReservation: %d ", clientDisconnect, noReservation)
	}

	peer2 := getRandAddrInfo(t)
	// This should increment "no reservation"
	mt.ConnectionRequestHandled(util.PeerInfoToPeerV2(peer2), pb.Status_NO_RESERVATION)
	clientDisconnect, err = getCounterValue(connectionRejectionsTotal.WithLabelValues("client disconnected"))
	if err != nil {
		t.Errorf("Unexpected error %s", err)
	}
	noReservation, err = getCounterValue(connectionRejectionsTotal.WithLabelValues("no reservation"))
	if err != nil {
		t.Errorf("Unexpected err %s", err)
	}
	if clientDisconnect != 1 || noReservation != 1 {
		t.Errorf("Invalid count values: expected clientDisconnect: 1, noReservation: 0, got: clientDisconnect: %d, noReservation: %d ", clientDisconnect, noReservation)
	}
}
