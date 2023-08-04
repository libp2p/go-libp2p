package swarm

import (
	"fmt"
	"sort"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/test"
	ma "github.com/multiformats/go-multiaddr"
)

func sortAddrDelays(addrDelays []network.AddrDelay) {
	sort.Slice(addrDelays, func(i, j int) bool {
		if addrDelays[i].Delay == addrDelays[j].Delay {
			return addrDelays[i].Addr.String() < addrDelays[j].Addr.String()
		}
		return addrDelays[i].Delay < addrDelays[j].Delay
	})
}

func TestNoDelayDialRanker(t *testing.T) {
	q1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic")
	q1v1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")
	wt1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/webtransport/")
	q2 := ma.StringCast("/ip4/1.2.3.4/udp/2/quic")
	q2v1 := ma.StringCast("/ip4/1.2.3.4/udp/2/quic-v1")
	q3 := ma.StringCast("/ip4/1.2.3.4/udp/3/quic")
	q3v1 := ma.StringCast("/ip4/1.2.3.4/udp/3/quic-v1")
	q4 := ma.StringCast("/ip4/1.2.3.4/udp/4/quic")
	t1 := ma.StringCast("/ip4/1.2.3.5/tcp/1/")

	testCase := []struct {
		name   string
		addrs  []ma.Multiaddr
		output []network.AddrDelay
	}{
		{
			name:  "quic+webtransport filtered when quicv1",
			addrs: []ma.Multiaddr{q1, q2, q3, q4, q1v1, q2v1, q3v1, wt1, t1},
			output: []network.AddrDelay{
				{Addr: q1, Delay: 0},
				{Addr: q2, Delay: 0},
				{Addr: q3, Delay: 0},
				{Addr: q4, Delay: 0},
				{Addr: q1v1, Delay: 0},
				{Addr: q2v1, Delay: 0},
				{Addr: q3v1, Delay: 0},
				{Addr: wt1, Delay: 0},
				{Addr: t1, Delay: 0},
			},
		},
	}
	for _, tc := range testCase {
		t.Run(tc.name, func(t *testing.T) {
			res := NoDelayDialRanker(tc.addrs)
			if len(res) != len(tc.output) {
				log.Errorf("expected %s got %s", tc.output, res)
				t.Errorf("expected elems: %d got: %d", len(tc.output), len(res))
			}
			sortAddrDelays(res)
			sortAddrDelays(tc.output)
			for i := 0; i < len(tc.output); i++ {
				if !tc.output[i].Addr.Equal(res[i].Addr) || tc.output[i].Delay != res[i].Delay {
					t.Fatalf("expected %+v got %+v", tc.output, res)
				}
			}
		})
	}
}

func TestDelayRankerQUICDelay(t *testing.T) {
	q1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic")
	q1v1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")
	wt1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/webtransport/")
	q2 := ma.StringCast("/ip4/1.2.3.4/udp/2/quic")
	q2v1 := ma.StringCast("/ip4/1.2.3.4/udp/2/quic-v1")
	q3 := ma.StringCast("/ip4/1.2.3.4/udp/3/quic")
	q3v1 := ma.StringCast("/ip4/1.2.3.4/udp/3/quic-v1")
	q4 := ma.StringCast("/ip4/1.2.3.4/udp/4/quic")

	q1v16 := ma.StringCast("/ip6/1::2/udp/1/quic-v1")
	q2v16 := ma.StringCast("/ip6/1::2/udp/2/quic-v1")
	q3v16 := ma.StringCast("/ip6/1::2/udp/3/quic-v1")

	testCase := []struct {
		name   string
		addrs  []ma.Multiaddr
		output []network.AddrDelay
	}{
		{
			name:  "quic-ipv4",
			addrs: []ma.Multiaddr{q1, q2, q3, q4},
			output: []network.AddrDelay{
				{Addr: q1, Delay: 0},
				{Addr: q2, Delay: PublicQUICDelay},
				{Addr: q3, Delay: PublicQUICDelay},
				{Addr: q4, Delay: PublicQUICDelay},
			},
		},
		{
			name:  "quic-ipv6",
			addrs: []ma.Multiaddr{q1v16, q2v16, q3v16},
			output: []network.AddrDelay{
				{Addr: q1v16, Delay: 0},
				{Addr: q2v16, Delay: PublicQUICDelay},
				{Addr: q3v16, Delay: PublicQUICDelay},
			},
		},
		{
			name:  "quic-ip4-ip6",
			addrs: []ma.Multiaddr{q1, q1v16, q2v1, q3, q4},
			output: []network.AddrDelay{
				{Addr: q1v16, Delay: 0},
				{Addr: q2v1, Delay: PublicQUICDelay},
				{Addr: q1, Delay: 2 * PublicQUICDelay},
				{Addr: q3, Delay: 2 * PublicQUICDelay},
				{Addr: q4, Delay: 2 * PublicQUICDelay},
			},
		},
		{
			name:  "quic-quic-v1-webtransport",
			addrs: []ma.Multiaddr{q1v16, q1, q2, q3, q4, q1v1, q2v1, q3v1, wt1},
			output: []network.AddrDelay{
				{Addr: q1v16, Delay: 0},
				{Addr: q1v1, Delay: PublicQUICDelay},
				{Addr: q2v1, Delay: 2 * PublicQUICDelay},
				{Addr: q3v1, Delay: 2 * PublicQUICDelay},
				{Addr: q1, Delay: 2 * PublicQUICDelay},
				{Addr: q2, Delay: 2 * PublicQUICDelay},
				{Addr: q3, Delay: 2 * PublicQUICDelay},
				{Addr: q4, Delay: 2 * PublicQUICDelay},
				{Addr: wt1, Delay: 2 * PublicQUICDelay},
			},
		},
		{
			name:  "wt-ranking",
			addrs: []ma.Multiaddr{q1v16, q2v16, q3v16, q2, wt1},
			output: []network.AddrDelay{
				{Addr: q1v16, Delay: 0},
				{Addr: q2, Delay: PublicQUICDelay},
				{Addr: wt1, Delay: 2 * PublicQUICDelay},
				{Addr: q2v16, Delay: 2 * PublicQUICDelay},
				{Addr: q3v16, Delay: 2 * PublicQUICDelay},
			},
		},
	}
	for _, tc := range testCase {
		t.Run(tc.name, func(t *testing.T) {
			res := DefaultDialRanker(tc.addrs)
			if len(res) != len(tc.output) {
				log.Errorf("expected %s got %s", tc.output, res)
				t.Errorf("expected elems: %d got: %d", len(tc.output), len(res))
			}
			sortAddrDelays(res)
			sortAddrDelays(tc.output)
			for i := 0; i < len(tc.output); i++ {
				if !tc.output[i].Addr.Equal(res[i].Addr) || tc.output[i].Delay != res[i].Delay {
					t.Fatalf("expected %+v got %+v", tc.output, res)
				}
			}
		})
	}
}

func TestDelayRankerTCPDelay(t *testing.T) {
	q1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic")
	q1v1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")
	q2 := ma.StringCast("/ip4/1.2.3.4/udp/2/quic")
	q2v1 := ma.StringCast("/ip4/1.2.3.4/udp/2/quic-v1")
	q3 := ma.StringCast("/ip4/1.2.3.4/udp/3/quic")

	q1v16 := ma.StringCast("/ip6/1::2/udp/1/quic-v1")
	q2v16 := ma.StringCast("/ip6/1::2/udp/2/quic-v1")
	q3v16 := ma.StringCast("/ip6/1::2/udp/3/quic-v1")

	t1 := ma.StringCast("/ip4/1.2.3.5/tcp/1/")
	t1v6 := ma.StringCast("/ip6/1::2/tcp/1")
	t2 := ma.StringCast("/ip4/1.2.3.4/tcp/2")

	testCase := []struct {
		name   string
		addrs  []ma.Multiaddr
		output []network.AddrDelay
	}{
		{
			name:  "quic-with-tcp-ip6-ip4",
			addrs: []ma.Multiaddr{q1, q1v1, q1v16, q2v16, q3v16, q2v1, t1, t2},
			output: []network.AddrDelay{
				{Addr: q1v16, Delay: 0},
				{Addr: q1v1, Delay: PublicQUICDelay},
				{Addr: q1, Delay: 2 * PublicQUICDelay},
				{Addr: q2v16, Delay: 2 * PublicQUICDelay},
				{Addr: q3v16, Delay: 2 * PublicQUICDelay},
				{Addr: q2v1, Delay: 2 * PublicQUICDelay},
				{Addr: t1, Delay: 3 * PublicQUICDelay},
				{Addr: t2, Delay: 3 * PublicQUICDelay},
			},
		},
		{
			name:  "quic-ip4-with-tcp",
			addrs: []ma.Multiaddr{q1, q2, q3, t1, t2, t1v6},
			output: []network.AddrDelay{
				{Addr: q1, Delay: 0},
				{Addr: q2, Delay: PublicQUICDelay},
				{Addr: q3, Delay: PublicQUICDelay},
				{Addr: t1, Delay: PublicQUICDelay + PublicTCPDelay},
				{Addr: t2, Delay: PublicQUICDelay + PublicTCPDelay},
				{Addr: t1v6, Delay: PublicQUICDelay + PublicTCPDelay},
			},
		},
		{
			name:  "tcp-ip4-ip6",
			addrs: []ma.Multiaddr{t1, t2, t1v6},
			output: []network.AddrDelay{
				{Addr: t1v6, Delay: 0},
				{Addr: t1, Delay: 0},
				{Addr: t2, Delay: 0},
			},
		},
	}
	for _, tc := range testCase {
		t.Run(tc.name, func(t *testing.T) {
			res := DefaultDialRanker(tc.addrs)
			if len(res) != len(tc.output) {
				log.Errorf("expected %s got %s", tc.output, res)
				t.Errorf("expected elems: %d got: %d", len(tc.output), len(res))
			}
			sortAddrDelays(res)
			sortAddrDelays(tc.output)
			for i := 0; i < len(tc.output); i++ {
				if !tc.output[i].Addr.Equal(res[i].Addr) || tc.output[i].Delay != res[i].Delay {
					t.Fatalf("expected %+v got %+v", tc.output, res)
				}
			}
		})
	}
}

func TestDelayRankerRelay(t *testing.T) {
	q1 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic")
	q2 := ma.StringCast("/ip4/1.2.3.4/udp/2/quic")

	pid := test.RandPeerIDFatal(t)
	r1 := ma.StringCast(fmt.Sprintf("/ip4/1.2.3.4/tcp/1/p2p-circuit/p2p/%s", pid))
	r2 := ma.StringCast(fmt.Sprintf("/ip4/1.2.3.4/udp/1/quic/p2p-circuit/p2p/%s", pid))

	testCase := []struct {
		name   string
		addrs  []ma.Multiaddr
		output []network.AddrDelay
	}{
		{
			name:  "relay address delayed",
			addrs: []ma.Multiaddr{q1, q2, r1, r2},
			output: []network.AddrDelay{
				{Addr: q1, Delay: 0},
				{Addr: q2, Delay: PublicQUICDelay},
				{Addr: r2, Delay: RelayDelay},
				{Addr: r1, Delay: PublicTCPDelay + RelayDelay},
			},
		},
	}
	for _, tc := range testCase {
		t.Run(tc.name, func(t *testing.T) {
			res := DefaultDialRanker(tc.addrs)
			if len(res) != len(tc.output) {
				log.Errorf("expected %s got %s", tc.output, res)
				t.Errorf("expected elems: %d got: %d", len(tc.output), len(res))
			}
			sortAddrDelays(res)
			sortAddrDelays(tc.output)
			for i := 0; i < len(tc.output); i++ {
				if !tc.output[i].Addr.Equal(res[i].Addr) || tc.output[i].Delay != res[i].Delay {
					t.Fatalf("expected %+v got %+v", tc.output, res)
				}
			}
		})
	}
}
