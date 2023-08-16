package swarm

import (
	"fmt"
	"testing"

	"github.com/libp2p/go-libp2p/core/test"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestIsFdConsuming(t *testing.T) {
	tcs := map[string]struct {
		addr          string
		isFdConsuming bool
	}{
		"tcp": {
			addr:          "/ip4/127.0.0.1/tcp/20",
			isFdConsuming: true,
		},
		"quic": {
			addr:          "/ip4/127.0.0.1/udp/0/quic-v1",
			isFdConsuming: false,
		},
		"addr-without-registered-transport": {
			addr:          "/ip4/127.0.0.1/tcp/20/ws",
			isFdConsuming: true,
		},
		"relay-tcp": {
			addr:          fmt.Sprintf("/ip4/127.0.0.1/tcp/20/p2p-circuit/p2p/%s", test.RandPeerIDFatal(t)),
			isFdConsuming: true,
		},
		"relay-quic": {
			addr:          fmt.Sprintf("/ip4/127.0.0.1/udp/20/quic/p2p-circuit/p2p/%s", test.RandPeerIDFatal(t)),
			isFdConsuming: false,
		},
		"relay-without-serveraddr": {
			addr:          fmt.Sprintf("/p2p-circuit/p2p/%s", test.RandPeerIDFatal(t)),
			isFdConsuming: true,
		},
		"relay-without-registered-transport-server": {
			addr:          fmt.Sprintf("/ip4/127.0.0.1/tcp/20/ws/p2p-circuit/p2p/%s", test.RandPeerIDFatal(t)),
			isFdConsuming: true,
		},
	}

	for name := range tcs {
		maddr, err := ma.NewMultiaddr(tcs[name].addr)
		require.NoError(t, err, name)
		require.Equal(t, tcs[name].isFdConsuming, isFdConsumingAddr(maddr), name)
	}
}
