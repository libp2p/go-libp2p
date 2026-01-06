package libp2pquic

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestMigrationDisabledByDefault(t *testing.T) {
	serverID, serverKey := createPeer(t)
	_, clientKey := createPeer(t)

	serverTransport, err := NewTransport(serverKey, newConnManager(t), nil, nil, nil)
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()

	ln := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic-v1")
	defer ln.Close()

	// Client without migration enabled
	clientTransport, err := NewTransport(clientKey, newConnManager(t), nil, nil, nil)
	require.NoError(t, err)
	defer clientTransport.(io.Closer).Close()

	conn, err := clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.NoError(t, err)
	defer conn.Close()

	// Try to get the MigratableConn interface
	var migratable network.MigratableConn
	if conn.As(&migratable) {
		// Even if we can get the interface, migration should not be supported
		require.False(t, migratable.SupportsMigration(), "migration should not be supported by default")
	}
}

func TestMigrationEnabledWithOption(t *testing.T) {
	serverID, serverKey := createPeer(t)
	_, clientKey := createPeer(t)

	serverTransport, err := NewTransport(serverKey, newConnManager(t), nil, nil, nil)
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()

	ln := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic-v1")
	defer ln.Close()

	// Client with migration enabled
	clientTransport, err := NewTransport(clientKey, newConnManager(t), nil, nil, nil, EnableExperimentalConnectionMigration())
	require.NoError(t, err)
	defer clientTransport.(io.Closer).Close()

	conn, err := clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.NoError(t, err)
	defer conn.Close()

	// Try to get the MigratableConn interface
	var migratable network.MigratableConn
	require.True(t, conn.As(&migratable), "should be able to get MigratableConn interface")
	require.True(t, migratable.SupportsMigration(), "migration should be supported when enabled via option")

	// Check ActivePath returns valid info
	activePath := migratable.ActivePath()
	require.NotNil(t, activePath.LocalAddr, "active path should have a local address")
	require.NotNil(t, activePath.RemoteAddr, "active path should have a remote address")
	require.True(t, activePath.Active, "active path should be marked as active")
}

func TestServerConnectionDoesNotSupportMigration(t *testing.T) {
	serverID, serverKey := createPeer(t)
	clientID, clientKey := createPeer(t)

	// Server with migration enabled (but server connections still don't support migration)
	serverTransport, err := NewTransport(serverKey, newConnManager(t), nil, nil, nil, EnableExperimentalConnectionMigration())
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()

	ln := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic-v1")
	defer ln.Close()

	// Client with migration enabled
	clientTransport, err := NewTransport(clientKey, newConnManager(t), nil, nil, nil, EnableExperimentalConnectionMigration())
	require.NoError(t, err)
	defer clientTransport.(io.Closer).Close()

	// Connect
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		conn, err := clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.NoError(t, err)
		defer conn.Close()

		// Client connection should support migration
		var migratable network.MigratableConn
		require.True(t, conn.As(&migratable), "client should be able to get MigratableConn interface")
		require.True(t, migratable.SupportsMigration(), "client connection should support migration")

		time.Sleep(100 * time.Millisecond) // Give time for server to check
	}()

	// Accept on server
	serverConn, err := ln.Accept()
	require.NoError(t, err)
	defer serverConn.Close()

	require.Equal(t, clientID, serverConn.RemotePeer())

	// Server connection should NOT support migration (per QUIC spec)
	var migratable network.MigratableConn
	if serverConn.As(&migratable) {
		require.False(t, migratable.SupportsMigration(), "server connection should not support migration")
	}

	<-clientDone
}

func TestAddPathRequiresMigrationEnabled(t *testing.T) {
	serverID, serverKey := createPeer(t)
	_, clientKey := createPeer(t)

	serverTransport, err := NewTransport(serverKey, newConnManager(t), nil, nil, nil)
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()

	ln := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic-v1")
	defer ln.Close()

	// Client without migration enabled
	clientTransport, err := NewTransport(clientKey, newConnManager(t), nil, nil, nil)
	require.NoError(t, err)
	defer clientTransport.(io.Closer).Close()

	conn, err := clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.NoError(t, err)
	defer conn.Close()

	// Try to get the MigratableConn interface and call AddPath
	var migratable network.MigratableConn
	require.True(t, conn.As(&migratable), "should be able to get MigratableConn interface")

	// AddPath should fail because migration is not enabled
	newLocalAddr := ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")
	_, err = migratable.AddPath(context.Background(), newLocalAddr)
	require.ErrorIs(t, err, ErrMigrationNotEnabled, "AddPath should fail when migration is not enabled")
}

func TestAvailablePaths(t *testing.T) {
	serverID, serverKey := createPeer(t)
	_, clientKey := createPeer(t)

	serverTransport, err := NewTransport(serverKey, newConnManager(t), nil, nil, nil)
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()

	ln := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic-v1")
	defer ln.Close()

	// Client with migration enabled
	clientTransport, err := NewTransport(clientKey, newConnManager(t), nil, nil, nil, EnableExperimentalConnectionMigration())
	require.NoError(t, err)
	defer clientTransport.(io.Closer).Close()

	conn, err := clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.NoError(t, err)
	defer conn.Close()

	// Get the MigratableConn interface
	var migratable network.MigratableConn
	require.True(t, conn.As(&migratable), "should be able to get MigratableConn interface")

	// Initially should have one path (the active one)
	paths := migratable.AvailablePaths()
	require.Len(t, paths, 1, "should have exactly one path initially")
	require.True(t, paths[0].Active, "the initial path should be active")
}

func TestMigrationConnManagerTransportForMigration(t *testing.T) {
	cm, err := quicreuse.NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{})
	require.NoError(t, err)
	defer cm.Close()

	// Test that we can get a transport for migration
	localAddr := ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")
	tr, err := cm.TransportForMigration(localAddr)
	require.NoError(t, err)
	require.NotNil(t, tr)
	defer tr.DecreaseCount()

	// The transport should have a valid local address
	require.NotNil(t, tr.LocalAddr())

	// The underlying transport should be available
	underlyingTr := tr.UnderlyingTransport()
	require.NotNil(t, underlyingTr, "should be able to get underlying *quic.Transport")
}
