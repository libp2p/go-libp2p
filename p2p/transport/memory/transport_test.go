package memory

import (
	"context"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"io"
	"testing"

	tpt "github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func getTransport(t *testing.T) (tpt.Transport, peer.ID) {
	t.Helper()
	privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	require.NoError(t, err)
	rcmgr := &network.NullResourceManager{}
	require.NoError(t, err)
	tr, err := NewTransport(privKey, nil, rcmgr)
	require.NoError(t, err)
	peerID, err := peer.IDFromPrivateKey(privKey)
	require.NoError(t, err)
	t.Cleanup(func() { rcmgr.Close() })
	return tr, peerID
}

func TestMemoryProtocol(t *testing.T) {
	t.Parallel()
	tr, _ := getTransport(t)
	defer tr.(io.Closer).Close()

	protocols := tr.Protocols()
	if len(protocols) > 1 {
		t.Fatalf("expected at most one protocol, got %v", protocols)
	}

	if protocols[0] != ma.P_MEMORY {
		t.Fatalf("expected the supported protocol to be memory, got %d", protocols[0])
	}
}

func TestCanDial(t *testing.T) {
	t.Parallel()
	tr, _ := getTransport(t)
	defer tr.(io.Closer).Close()

	invalid := []string{
		"/ip4/127.0.0.1/udp/1234",
		"/ip4/5.5.5.5/tcp/1234",
		"/dns/google.com/udp/443/quic-v1",
		"/ip4/127.0.0.1/udp/1234/quic",
	}
	valid := []string{
		"/memory/1234",
		"/memory/1337123",
	}
	for _, s := range invalid {
		invalidAddr, err := ma.NewMultiaddr(s)
		require.NoError(t, err)
		if tr.CanDial(invalidAddr) {
			t.Errorf("didn't expect to be able to dial a non-memory address (%s)", invalidAddr)
		}
	}
	for _, s := range valid {
		validAddr, err := ma.NewMultiaddr(s)
		require.NoError(t, err)
		if !tr.CanDial(validAddr) {
			t.Errorf("expected to be able to dial memory address (%s)", validAddr)
		}
	}
}

func TestTransport_Listen(t *testing.T) {
	t.Parallel()
	server, _ := getTransport(t)
	defer server.(io.Closer).Close()

	addr, err := ma.NewMultiaddr("/memory/1234")
	require.NoError(t, err)
	serverListener, err := server.Listen(addr)
	require.NoError(t, err)
	defer serverListener.Close()
	lma := serverListener.Multiaddr()
	require.Equal(t, addr, lma)
}

func TestTransport_Dial(t *testing.T) {
	t.Parallel()
	server, serverPeerID := getTransport(t)
	client, clientPeerID := getTransport(t)
	defer func() {
		if server != nil {
			err := server.(io.Closer).Close()
			require.NoError(t, err)
		}
	}()

	defer func() {
		if client != nil {
			err := client.(io.Closer).Close()
			require.NoError(t, err)
		}
	}()

	serverAddr, err := ma.NewMultiaddr("/memory/1234")
	require.NoError(t, err)
	serverListener, err := server.Listen(serverAddr)
	require.NoError(t, err)
	defer func() {
		if serverListener != nil {
			err = serverListener.Close()
			require.NoError(t, err)
		}
	}()

	c, err := client.Dial(context.Background(), serverAddr, serverPeerID)
	require.NoError(t, err)
	defer func() {
		if c != nil {
			err = c.Close()
			require.NoError(t, err)
		}
	}()

	require.Equal(t, serverAddr, c.RemoteMultiaddr())
	require.Equal(t, clientPeerID, c.LocalPeer())
	require.Equal(t, serverPeerID, c.RemotePeer())

	// Try to dial address with no listener
	otherAddr, err := ma.NewMultiaddr("/memory/4321")
	require.NoError(t, err)

	_, err = client.Dial(context.Background(), otherAddr, serverPeerID)
	require.Error(t, err)
}
