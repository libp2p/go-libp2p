package network

import (
	"fmt"
	"testing"

	ma "github.com/multiformats/go-multiaddr"

	"github.com/stretchr/testify/require"
)

func TestDedupAddrs(t *testing.T) {
	tcpAddr := ma.StringCast("/ip4/127.0.0.1/tcp/1234")
	quicAddr := ma.StringCast("/ip4/127.0.0.1/udp/1234/quic-v1")
	wsAddr := ma.StringCast("/ip4/127.0.0.1/tcp/1234/ws")

	type testcase struct {
		in, out []ma.Multiaddr
	}

	for i, tc := range []testcase{
		{in: nil, out: nil},
		{in: []ma.Multiaddr{tcpAddr}, out: []ma.Multiaddr{tcpAddr}},
		{in: []ma.Multiaddr{tcpAddr, tcpAddr, tcpAddr}, out: []ma.Multiaddr{tcpAddr}},
		{in: []ma.Multiaddr{tcpAddr, quicAddr, tcpAddr}, out: []ma.Multiaddr{tcpAddr, quicAddr}},
		{in: []ma.Multiaddr{tcpAddr, quicAddr, wsAddr}, out: []ma.Multiaddr{tcpAddr, quicAddr, wsAddr}},
	} {
		tc := tc
		t.Run(fmt.Sprintf("test %d", i), func(t *testing.T) {
			deduped := DedupAddrs(tc.in)
			for _, a := range tc.out {
				require.Contains(t, deduped, a)
			}
		})
	}
}
