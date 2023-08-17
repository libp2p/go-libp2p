//go:build nocover

package autonat

import (
	"math/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/host/autonat/pb"
)

func BenchmarkReachabilityStatus(b *testing.B) {
	b.ReportAllocs()
	mt := NewMetricsTracer()
	for i := 0; i < b.N; i++ {
		mt.ReachabilityStatus(network.Reachability(i % 3))
	}
}

func BenchmarkClientDialResponse(b *testing.B) {
	b.ReportAllocs()
	mt := NewMetricsTracer()
	statuses := []pb.Message_ResponseStatus{
		pb.Message_OK, pb.Message_E_DIAL_ERROR, pb.Message_E_DIAL_REFUSED, pb.Message_E_BAD_REQUEST,
	}
	for i := 0; i < b.N; i++ {
		mt.ReceivedDialResponse(statuses[i%len(statuses)])
	}
}

func BenchmarkServerDialResponse(b *testing.B) {
	b.ReportAllocs()
	mt := NewMetricsTracer()
	statuses := []pb.Message_ResponseStatus{
		pb.Message_OK, pb.Message_E_DIAL_ERROR, pb.Message_E_DIAL_REFUSED, pb.Message_E_BAD_REQUEST,
	}
	for i := 0; i < b.N; i++ {
		mt.OutgoingDialResponse(statuses[i%len(statuses)])
	}
}

func BenchmarkServerDialRefused(b *testing.B) {
	b.ReportAllocs()
	mt := NewMetricsTracer()
	for i := 0; i < b.N; i++ {
		mt.OutgoingDialRefused(rate_limited)
	}
}

func TestMetricsNoAllocNoCover(t *testing.T) {
	mt := NewMetricsTracer()
	statuses := []network.Reachability{
		network.ReachabilityPublic,
		network.ReachabilityPrivate,
		network.ReachabilityUnknown,
	}
	respStatuses := []pb.Message_ResponseStatus{
		pb.Message_OK,
		pb.Message_E_BAD_REQUEST,
		pb.Message_E_DIAL_REFUSED,
		pb.Message_E_INTERNAL_ERROR,
	}
	reasons := []string{
		rate_limited,
		"bad request",
		"no valid address",
	}
	tests := map[string]func(){
		"ReachabilityStatus":           func() { mt.ReachabilityStatus(statuses[rand.Intn(len(statuses))]) },
		"ReachabilityStatusConfidence": func() { mt.ReachabilityStatusConfidence(rand.Intn(4)) },
		"ReceivedDialResponse":         func() { mt.ReceivedDialResponse(respStatuses[rand.Intn(len(respStatuses))]) },
		"OutgoingDialResponse":         func() { mt.OutgoingDialResponse(respStatuses[rand.Intn(len(respStatuses))]) },
		"OutgoingDialRefused":          func() { mt.OutgoingDialRefused(reasons[rand.Intn(len(reasons))]) },
		"NextProbeTime":                func() { mt.NextProbeTime(time.Now()) },
	}
	for method, f := range tests {
		allocs := testing.AllocsPerRun(1000, f)
		if allocs > 0 {
			t.Fatalf("%s alloc test failed expected 0 received %0.2f", method, allocs)
		}
	}
}
