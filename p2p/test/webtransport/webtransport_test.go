package webtransport_test

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/libp2p/go-libp2p"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/test"
	libp2pwebtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func extractCertHashes(addr ma.Multiaddr) []string {
	var certHashesStr []string
	ma.ForEach(addr, func(c ma.Component) bool {
		if c.Protocol().Code == ma.P_CERTHASH {
			certHashesStr = append(certHashesStr, c.Value())
		}
		return true
	})
	return certHashesStr
}

func TestDeterministicCertsAfterReboot(t *testing.T) {
	priv, _, err := test.RandTestKeyPair(ic.Ed25519, 256)
	require.NoError(t, err)

	cl := clock.NewMock()
	// Move one year ahead to avoid edge cases around epoch
	cl.Add(time.Hour * 24 * 365)
	h, err := libp2p.New(libp2p.NoTransports, libp2p.Transport(libp2pwebtransport.New, libp2pwebtransport.WithClock(cl)), libp2p.Identity(priv))
	require.NoError(t, err)
	err = h.Network().Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)

	prevCerthashes := extractCertHashes(h.Addrs()[0])
	h.Close()

	h, err = libp2p.New(libp2p.NoTransports, libp2p.Transport(libp2pwebtransport.New, libp2pwebtransport.WithClock(cl)), libp2p.Identity(priv))
	require.NoError(t, err)
	defer h.Close()
	err = h.Network().Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)

	nextCertHashes := extractCertHashes(h.Addrs()[0])

	for i := range prevCerthashes {
		require.Equal(t, prevCerthashes[i], nextCertHashes[i])
	}
}
