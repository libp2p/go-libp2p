package holepunch

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func getCounterValue(t *testing.T, counter *prometheus.CounterVec, labels ...string) int {
	t.Helper()
	m := &dto.Metric{}
	if err := counter.WithLabelValues(labels...).Write(m); err != nil {
		t.Errorf("failed to extract counter value %s", err)
		return 0
	}
	return int(*m.Counter.Value)

}

func TestHolePunchOutcomeCounter(t *testing.T) {
	t1 := ma.StringCast("/ip4/1.2.3.4/tcp/1")
	t2 := ma.StringCast("/ip4/1.2.3.4/tcp/2")

	q1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic")
	q2 := ma.StringCast("/ip4/1.2.3.4/udp/2/quic")

	q1v1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")

	type testcase struct {
		name       string
		theirAddrs []ma.Multiaddr
		ourAddrs   []ma.Multiaddr
		conn       network.ConnMultiaddrs
		result     map[[3]string]int
	}
	testcases := []testcase{
		{
			name:       "connection success",
			theirAddrs: []ma.Multiaddr{t1, q1},
			ourAddrs:   []ma.Multiaddr{t2, q2},
			conn:       &mockConnMultiaddrs{local: t1, remote: t2},
			result: map[[3]string]int{
				[...]string{"ip4", "tcp", "success"}:    1,
				[...]string{"ip4", "quic", "cancelled"}: 1,
			},
		},
		{
			name:       "connection failed",
			theirAddrs: []ma.Multiaddr{t1},
			ourAddrs:   []ma.Multiaddr{t2, q2},
			conn:       nil,
			result: map[[3]string]int{
				[...]string{"ip4", "tcp", "failed"}:               1,
				[...]string{"ip4", "quic", "no_suitable_address"}: 1,
			},
		},
		{
			name:       "no_suitable_address",
			theirAddrs: []ma.Multiaddr{t1, q1},
			ourAddrs:   []ma.Multiaddr{t2, q2, q1v1},
			conn:       &mockConnMultiaddrs{local: q1, remote: q2},
			result: map[[3]string]int{
				[...]string{"ip4", "tcp", "cancelled"}:               1,
				[...]string{"ip4", "quic", "failed"}:                 0,
				[...]string{"ip4", "quic", "success"}:                1,
				[...]string{"ip4", "tcp", "success"}:                 0,
				[...]string{"ip4", "quic-v1", "no_suitable_address"}: 1,
			},
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			hpAddressOutcomesTotal.Reset()
			mt := NewMetricsTracer(WithRegisterer(reg))
			for _, side := range []string{"receiver", "initiator"} {
				mt.HolePunchFinished(side, 1, tc.theirAddrs, tc.ourAddrs, tc.conn)
				for labels, value := range tc.result {
					v := getCounterValue(t, hpAddressOutcomesTotal, side, "1", labels[0], labels[1], labels[2])
					if v != value {
						t.Errorf("Invalid metric value %s: expected: %d got: %d", labels, value, v)
					}
				}
			}
		})
	}
}

type mockConnMultiaddrs struct {
	local, remote ma.Multiaddr
}

func (cma *mockConnMultiaddrs) LocalMultiaddr() ma.Multiaddr {
	return cma.local
}

func (cma *mockConnMultiaddrs) RemoteMultiaddr() ma.Multiaddr {
	return cma.remote
}
