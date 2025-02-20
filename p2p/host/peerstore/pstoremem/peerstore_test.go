package pstoremem

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peerstore"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestPeerStoreAddrBookOpts(t *testing.T) {
	ps, err := NewPeerstore(WithMaxAddresses(1))
	require.NoError(t, err)
	defer ps.Close()

	ps.AddAddr("p1", ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1"), peerstore.TempAddrTTL)
	res := ps.Addrs("p1")
	require.NotEmpty(t, res)

	ps.AddAddr("p2", ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1"), peerstore.TempAddrTTL)
	res = ps.Addrs("p2")
	require.Empty(t, res)
}
