package quicreuse

import (
	"net"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestConvertToQuicMultiaddr(t *testing.T) {
	addr := &net.UDPAddr{IP: net.IPv4(192, 168, 0, 42), Port: 1337}
	maddr, err := ToQuicMultiaddr(addr, quic.Version1)
	require.NoError(t, err)
	require.Equal(t, "/ip4/192.168.0.42/udp/1337/quic-v1", maddr.String())
}

func TestConvertToQuicV1Multiaddr(t *testing.T) {
	addr := &net.UDPAddr{IP: net.IPv4(192, 168, 0, 42), Port: 1337}
	maddr, err := ToQuicMultiaddr(addr, quic.Version1)
	require.NoError(t, err)
	require.Equal(t, "/ip4/192.168.0.42/udp/1337/quic-v1", maddr.String())
}

func TestConvertFromQuicV1Multiaddr(t *testing.T) {
	maddr, err := ma.NewMultiaddr("/ip4/192.168.0.42/udp/1337/quic-v1")
	require.NoError(t, err)
	udpAddr, v, err := FromQuicMultiaddr(maddr)
	require.NoError(t, err)
	require.Equal(t, net.IPv4(192, 168, 0, 42), udpAddr.IP)
	require.Equal(t, 1337, udpAddr.Port)
	require.Equal(t, quic.Version1, v)
}
