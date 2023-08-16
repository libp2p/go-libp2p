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
	require.Equal(t, maddr.String(), "/ip4/192.168.0.42/udp/1337/quic-v1")
}

func TestConvertToQuicV1Multiaddr(t *testing.T) {
	addr := &net.UDPAddr{IP: net.IPv4(192, 168, 0, 42), Port: 1337}
	maddr, err := ToQuicMultiaddr(addr, quic.Version1)
	require.NoError(t, err)
	require.Equal(t, maddr.String(), "/ip4/192.168.0.42/udp/1337/quic-v1")
}

func TestConvertFromQuicV1Multiaddr(t *testing.T) {
	maddr, err := ma.NewMultiaddr("/ip4/192.168.0.42/udp/1337/quic-v1")
	require.NoError(t, err)
	udpAddr, v, err := FromQuicMultiaddr(maddr)
	require.NoError(t, err)
	require.Equal(t, udpAddr.IP, net.IPv4(192, 168, 0, 42))
	require.Equal(t, udpAddr.Port, 1337)
	require.Equal(t, v, quic.Version1)
}
