package libp2pquic

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	mocknetwork "github.com/libp2p/go-libp2p/core/network/mocks"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/quic-go/quic-go"
	"go.uber.org/mock/gomock"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func newTransport(t *testing.T, rcmgr network.ResourceManager) tpt.Transport {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	key, err := ic.UnmarshalRsaPrivateKey(x509.MarshalPKCS1PrivateKey(rsaKey))
	require.NoError(t, err)
	tr, err := NewTransport(key, newConnManager(t), nil, nil, rcmgr)
	require.NoError(t, err)
	return tr
}

func TestListenAddr(t *testing.T) {
	tr := newTransport(t, nil)
	defer tr.(io.Closer).Close()

	t.Run("for IPv4", func(t *testing.T) {
		localAddrV1 := ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")
		ln, err := tr.Listen(localAddrV1)
		require.NoError(t, err)
		defer ln.Close()
		port := ln.Addr().(*net.UDPAddr).Port
		require.NotZero(t, port)

		var multiaddrsStrings []string
		for _, a := range []ma.Multiaddr{ln.Multiaddr()} {
			multiaddrsStrings = append(multiaddrsStrings, a.String())
		}
		require.Contains(t, multiaddrsStrings, fmt.Sprintf("/ip4/127.0.0.1/udp/%d/quic-v1", port))
	})

	t.Run("for IPv6", func(t *testing.T) {
		localAddrV1 := ma.StringCast("/ip6/::/udp/0/quic-v1")
		ln, err := tr.Listen(localAddrV1)
		require.NoError(t, err)
		defer ln.Close()
		port := ln.Addr().(*net.UDPAddr).Port
		require.NotZero(t, port)
		var multiaddrsStrings []string
		for _, a := range []ma.Multiaddr{ln.Multiaddr()} {
			multiaddrsStrings = append(multiaddrsStrings, a.String())
		}
		require.Contains(t, multiaddrsStrings, fmt.Sprintf("/ip6/::/udp/%d/quic-v1", port))
	})
}

func TestAccepting(t *testing.T) {
	tr := newTransport(t, nil)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"))
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		ln.Accept()
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("Accept didn't block")
	default:
	}
	require.NoError(t, ln.Close())
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Accept didn't return after the listener was closed")
	}
}

func TestAcceptAfterClose(t *testing.T) {
	tr := newTransport(t, nil)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"))
	require.NoError(t, err)
	require.NoError(t, ln.Close())
	_, err = ln.Accept()
	require.Error(t, err)
}

func TestCorrectNumberOfVirtualListeners(t *testing.T) {
	tr := newTransport(t, nil)
	tpt := tr.(*transport)
	defer tr.(io.Closer).Close()

	localAddrV1 := ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")
	ln, err := tr.Listen(localAddrV1)
	require.NoError(t, err)
	udpAddr, _, err := quicreuse.FromQuicMultiaddr(localAddrV1)
	require.NoError(t, err)

	require.NoError(t, err)
	require.Len(t, tpt.listeners[udpAddr.String()], 1)
	ln.Close()
	require.Empty(t, tpt.listeners[udpAddr.String()])
}

func TestCleanupConnWhenBlocked(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockRcmgr := mocknetwork.NewMockResourceManager(ctrl)
	mockRcmgr.EXPECT().OpenConnection(network.DirInbound, false, gomock.Any()).DoAndReturn(func(network.Direction, bool, ma.Multiaddr) (network.ConnManagementScope, error) {
		// Block the connection
		return nil, fmt.Errorf("connections blocked")
	})

	server := newTransport(t, mockRcmgr)
	serverTpt := server.(*transport)
	defer server.(io.Closer).Close()

	localAddrV1 := ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")
	ln, err := server.Listen(localAddrV1)
	require.NoError(t, err)
	defer ln.Close()
	go ln.Accept()

	client := newTransport(t, nil)
	ctx := context.Background()

	var quicErr *quic.ApplicationError = &quic.ApplicationError{}
	conn, err := client.Dial(ctx, ln.Multiaddr(), serverTpt.localPeer)
	if err != nil && errors.As(err, &quicErr) {
		// We hit our expected application error
		return
	}

	// No error yet, let's continue using the conn
	s, err := conn.OpenStream(ctx)
	if err != nil && errors.As(err, &quicErr) {
		// We hit our expected application error
		return
	}

	// No error yet, let's continue using the conn
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	b := [1]byte{}
	_, err = s.Read(b[:])
	if err != nil && errors.As(err, &quicErr) {
		// We hit our expected application error
		return
	}

	t.Fatalf("expected application error, got %v", err)
}
