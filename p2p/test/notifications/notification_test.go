package notifications

import (
	"bytes"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func portFromString(t *testing.T, s string) int {
	t.Helper()
	p, err := strconv.ParseInt(s, 10, 32)
	require.NoError(t, err)
	return int(p)
}

func TestListenAddressNotif(t *testing.T) {
	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Security(noise.ID, noise.New),
		libp2p.DisableRelay(),
	)
	require.NoError(t, err)
	defer h.Close()
	sub, err := h.EventBus().Subscribe(&event.EvtLocalAddressesUpdated{})
	require.NoError(t, err)
	defer sub.Close()

	var initialAddrs []ma.Multiaddr
	// make sure the event is emitted for the initial listen address
	select {
	case e := <-sub.Out():
		ev := e.(event.EvtLocalAddressesUpdated)
		require.Empty(t, ev.Removed)
		require.Len(t, ev.Current, 2)
		require.Equal(t, event.Added, ev.Current[0].Action)
		initialAddr := ev.Current[0].Address
		initialAddrs = []ma.Multiaddr{initialAddr, initialAddr.Encapsulate(ma.StringCast("/noise"))}
		portStr, err := initialAddr.ValueForProtocol(ma.P_TCP)
		require.NoError(t, err)
		require.NotZero(t, portFromString(t, portStr))
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout")
	}
	slices.SortFunc(initialAddrs, func(a, b ma.Multiaddr) int { return bytes.Compare(a.Bytes(), b.Bytes()) })
	listenAddrs, err := h.Network().InterfaceListenAddresses()
	require.NoError(t, err)
	slices.SortFunc(listenAddrs, func(a, b ma.Multiaddr) int { return bytes.Compare(a.Bytes(), b.Bytes()) })
	require.Equal(t, initialAddrs, listenAddrs)

	// now start listening on another address
	require.NoError(t, h.Network().Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")))
	var addedAddr ma.Multiaddr
	select {
	case e := <-sub.Out():
		ev := e.(event.EvtLocalAddressesUpdated)
		require.Empty(t, ev.Removed)
		require.Len(t, ev.Current, 3)
		var maintainedAddrs []ma.Multiaddr
		for _, e := range ev.Current {
			switch e.Action {
			case event.Added:
				addedAddr = e.Address
			case event.Maintained:
				maintainedAddrs = append(maintainedAddrs, e.Address)
			default:
				t.Fatal("unexpected action")
			}
		}
		slices.SortFunc(maintainedAddrs, func(a, b ma.Multiaddr) int { return bytes.Compare(a.Bytes(), b.Bytes()) })
		require.Equal(t, initialAddrs, maintainedAddrs)
		_, err = addedAddr.ValueForProtocol(ma.P_QUIC_V1)
		require.NoError(t, err)
		portStr, err := addedAddr.ValueForProtocol(ma.P_UDP)
		require.NoError(t, err)
		require.NotZero(t, portFromString(t, portStr))
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout")
	}

	listenAddrs, err = h.Network().InterfaceListenAddresses()
	require.NoError(t, err)
	require.Len(t, listenAddrs, 3)
	for _, addr := range initialAddrs {
		require.Contains(t, listenAddrs, addr)
	}
	require.Contains(t, listenAddrs, addedAddr)
}
