package libp2pwebrtc

import (
	"net"
	"testing"

	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/udpmux"
	"github.com/stretchr/testify/require"
)

func newReuseUDPMux(t *testing.T) reuseUDPMux {
	return reuseUDPMux{
		loopback:    make(map[int]*udpmux.UDPMux),
		specific:    make(map[string]map[int]*udpmux.UDPMux),
		unspecified: make(map[int]*udpmux.UDPMux),
	}
}

func udpAddr(t *testing.T, s string) *net.UDPAddr {
	a, err := net.ResolveUDPAddr("udp", s)
	require.NoError(t, err)
	return a
}

func TestReuseUDPMuxLoopback(t *testing.T) {
	socket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	defer socket.Close()
	r := newReuseUDPMux(t)

	mux := r.Get(udpAddr(t, "127.0.0.1:1"))
	require.Nil(t, mux)

	originalMux := udpmux.NewUDPMux(socket)
	err = r.Put(originalMux)
	require.NoError(t, err)

	mux = r.Get(udpAddr(t, "127.0.0.1:1"))
	require.Equal(t, originalMux, mux)

	mux = r.Get(udpAddr(t, "1.2.3.4:1"))
	require.Nil(t, mux)

	r.Delete(originalMux)
	mux = r.Get(udpAddr(t, "127.0.0.1:1"))
	require.Nil(t, mux)
}

func TestReuseUDPMuxUnspecified(t *testing.T) {
	s1, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	defer s1.Close()

	s2, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	require.NoError(t, err)
	defer s2.Close()

	r := newReuseUDPMux(t)

	loMux := udpmux.NewUDPMux(s1)
	err = r.Put(loMux)
	require.NoError(t, err)

	mux := r.Get(udpAddr(t, "1.2.3.4:1"))
	require.Nil(t, mux)

	unMux := udpmux.NewUDPMux(s2)
	err = r.Put(unMux)
	require.NoError(t, err)

	mux = r.Get(udpAddr(t, "127.0.0.1:1"))
	require.Equal(t, loMux, mux)

	mux = r.Get(udpAddr(t, "1.2.3.4:1"))
	require.Equal(t, unMux, mux)

	r.Delete(loMux)
	mux = r.Get(udpAddr(t, "127.0.0.1:1"))
	require.Equal(t, unMux, mux)
}
