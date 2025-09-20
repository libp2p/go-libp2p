package insecure

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"

	"github.com/stretchr/testify/require"
)

// Run a set of sessions through the session setup and verification.
func TestConnections(t *testing.T) {
	clientTpt := newTestTransport(t, crypto.RSA, 2048)
	serverTpt := newTestTransport(t, crypto.Ed25519, 1024)

	clientConn, serverConn, clientErr, serverErr := connect(t, clientTpt, serverTpt, serverTpt.LocalPeer(), "")
	require.NoError(t, clientErr)
	require.NoError(t, serverErr)
	testIDs(t, clientTpt, serverTpt, clientConn, serverConn)
	testKeys(t, clientTpt, serverTpt, clientConn, serverConn)
	testReadWrite(t, clientConn, serverConn)
}

func TestPeerIdMatchInbound(t *testing.T) {
	clientTpt := newTestTransport(t, crypto.RSA, 2048)
	serverTpt := newTestTransport(t, crypto.Ed25519, 1024)

	clientConn, serverConn, clientErr, serverErr := connect(t, clientTpt, serverTpt, serverTpt.LocalPeer(), clientTpt.LocalPeer())
	require.NoError(t, clientErr)
	require.NoError(t, serverErr)
	testIDs(t, clientTpt, serverTpt, clientConn, serverConn)
	testKeys(t, clientTpt, serverTpt, clientConn, serverConn)
	testReadWrite(t, clientConn, serverConn)
}

func TestPeerIDMismatchInbound(t *testing.T) {
	clientTpt := newTestTransport(t, crypto.RSA, 2048)
	serverTpt := newTestTransport(t, crypto.Ed25519, 1024)

	_, _, _, serverErr := connect(t, clientTpt, serverTpt, serverTpt.LocalPeer(), "a-random-peer")
	require.Error(t, serverErr)
	require.Contains(t, serverErr.Error(), "remote peer sent unexpected peer ID")
}

func TestPeerIDMismatchOutbound(t *testing.T) {
	clientTpt := newTestTransport(t, crypto.RSA, 2048)
	serverTpt := newTestTransport(t, crypto.Ed25519, 1024)

	_, _, clientErr, _ := connect(t, clientTpt, serverTpt, "a random peer", "")
	require.Error(t, clientErr)
	require.Contains(t, clientErr.Error(), "remote peer sent unexpected peer ID")
}

func newTestTransport(t *testing.T, typ, bits int) *Transport {
	priv, pub, err := crypto.GenerateKeyPair(typ, bits)
	require.NoError(t, err)
	id, err := peer.IDFromPublicKey(pub)
	require.NoError(t, err)
	return NewWithIdentity("/test/1.0.0", id, priv)
}

// Create a new pair of connected TCP sockets.
func newConnPair(t *testing.T) (net.Conn, net.Conn) {
	lstnr, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err, "failed to listen")

	var clientErr error
	var client net.Conn
	done := make(chan struct{})

	go func() {
		defer close(done)
		addr := lstnr.Addr()
		client, clientErr = net.Dial(addr.Network(), addr.String())
	}()

	server, err := lstnr.Accept()
	require.NoError(t, err, "failed to accept")

	<-done
	lstnr.Close()
	require.NoError(t, clientErr, "failed to connect")
	return client, server
}

func connect(t *testing.T, clientTpt, serverTpt *Transport, clientExpectsID, serverExpectsID peer.ID) (clientConn sec.SecureConn, serverConn sec.SecureConn, clientErr, serverErr error) {
	client, server := newConnPair(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		clientConn, clientErr = clientTpt.SecureOutbound(context.TODO(), client, clientExpectsID)
	}()
	serverConn, serverErr = serverTpt.SecureInbound(context.TODO(), server, serverExpectsID)
	<-done
	return
}

// Check the peer IDs
func testIDs(t *testing.T, clientTpt, serverTpt *Transport, clientConn, serverConn sec.SecureConn) {
	t.Helper()
	require.Equal(t, clientConn.LocalPeer(), clientTpt.LocalPeer(), "Client Local Peer ID mismatch.")
	require.Equal(t, clientConn.RemotePeer(), serverTpt.LocalPeer(), "Client Remote Peer ID mismatch.")
	require.Equal(t, clientConn.LocalPeer(), serverConn.RemotePeer(), "Server Local Peer ID mismatch.")
}

// Check the keys
func testKeys(t *testing.T, clientTpt, serverTpt *Transport, clientConn, serverConn sec.SecureConn) {
	t.Helper()
	require.True(t, clientConn.RemotePublicKey().Equals(serverTpt.key.GetPublic()), "client conn key mismatch")
	require.True(t, serverConn.RemotePublicKey().Equals(clientTpt.key.GetPublic()), "server conn key mismatch")
}

// Check sending and receiving messages
func testReadWrite(t *testing.T, clientConn, serverConn sec.SecureConn) {
	before := []byte("hello world")
	_, err := clientConn.Write(before)
	require.NoError(t, err)

	after := make([]byte, len(before))
	_, err = io.ReadFull(serverConn, after)
	require.NoError(t, err)
	require.Equal(t, before, after, "message mismatch")
}
