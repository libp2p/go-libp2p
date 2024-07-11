package autonatv2

import (
	"errors"
	"math/rand"
	"testing"

	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsNoAllocNoCover(t *testing.T) {
	mt := NewMetricsTracer(prometheus.DefaultRegisterer)
	respStatuses := []pb.DialResponse_ResponseStatus{
		pb.DialResponse_E_DIAL_REFUSED,
		pb.DialResponse_OK,
	}
	dialStatuses := []pb.DialStatus{
		pb.DialStatus_OK,
		pb.DialStatus_E_DIAL_BACK_ERROR,
	}
	errs := []error{
		nil,
		errBadRequest,
		errDialDataRefused,
		errors.New("write failed"),
	}
	addrs := []ma.Multiaddr{
		nil,
		ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1"),
		ma.StringCast("/ip4/1.1.1.1/tcp/1/"),
	}
	tests := map[string]func(){
		"CompletedRequest": func() {
			mt.CompletedRequest(EventDialRequestCompleted{
				Error:            errs[rand.Intn(len(errs))],
				ResponseStatus:   respStatuses[rand.Intn(len(respStatuses))],
				DialStatus:       dialStatuses[rand.Intn(len(dialStatuses))],
				DialDataRequired: rand.Intn(2) == 1,
				DialedAddr:       addrs[rand.Intn(len(addrs))],
			})
		},
	}
	for method, f := range tests {
		allocs := testing.AllocsPerRun(10000, f)
		if allocs > 0 {
			t.Fatalf("%s alloc test failed expected 0 received %0.2f", method, allocs)
		}
	}
}
