package libp2pquic

import (
	"context"
	"errors"
	"sync"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
)

// ErrMigrationNotSupported is returned when migration is attempted on a connection
// that does not support it (e.g., server-side connections).
var ErrMigrationNotSupported = errors.New("connection does not support migration")

// ErrMigrationNotEnabled is returned when migration is attempted but the
// experimental migration feature is not enabled.
var ErrMigrationNotEnabled = errors.New("connection migration is not enabled; use EnableExperimentalConnectionMigration option")

type conn struct {
	quicConn  *quic.Conn
	transport *transport
	scope     network.ConnManagementScope

	localPeer      peer.ID
	localMultiaddr ma.Multiaddr

	remotePeerID    peer.ID
	remotePubKey    ic.PubKey
	remoteMultiaddr ma.Multiaddr

	// isOutbound indicates if this connection was initiated by us (client-side).
	// Only outbound connections support migration per QUIC spec.
	isOutbound bool

	// migrationEnabled indicates if the experimental migration feature is enabled.
	migrationEnabled bool

	// Migration-related fields (protected by migrationMu)
	migrationMu sync.RWMutex
	paths       map[string]*connPath // keyed by local addr string
	activePath  *connPath
}

// connPath represents a potential path for connection migration.
type connPath struct {
	localAddr  ma.Multiaddr
	remoteAddr ma.Multiaddr
	rtt        time.Duration
	active     bool
	transport  refCountedTransport // for cleanup on close
}

// refCountedTransport is an interface for transports that support reference counting.
type refCountedTransport interface {
	DecreaseCount()
}

func (c *conn) As(target any) bool {
	if t, ok := target.(**quic.Conn); ok {
		*t = c.quicConn
		return true
	}
	if t, ok := target.(*network.MigratableConn); ok {
		*t = c
		return true
	}
	return false
}

var _ tpt.CapableConn = &conn{}

// Close closes the connection.
// It must be called even if the peer closed the connection in order for
// garbage collection to properly work in this package.
func (c *conn) Close() error {
	return c.closeWithError(0, "")
}

// CloseWithError closes the connection
// It must be called even if the peer closed the connection in order for
// garbage collection to properly work in this package.
func (c *conn) CloseWithError(errCode network.ConnErrorCode) error {
	return c.closeWithError(quic.ApplicationErrorCode(errCode), "")
}

func (c *conn) closeWithError(errCode quic.ApplicationErrorCode, errString string) error {
	// Clean up migration path resources before closing.
	c.cleanupMigrationPaths()

	c.transport.removeConn(c.quicConn)
	err := c.quicConn.CloseWithError(errCode, errString)
	c.scope.Done()
	return err
}

// cleanupMigrationPaths releases all transport references held by migration paths.
func (c *conn) cleanupMigrationPaths() {
	c.migrationMu.Lock()
	defer c.migrationMu.Unlock()

	for _, p := range c.paths {
		if p.transport != nil {
			p.transport.DecreaseCount()
			p.transport = nil
		}
	}
	c.paths = nil
	c.activePath = nil
}

// IsClosed returns whether a connection is fully closed.
func (c *conn) IsClosed() bool {
	return c.quicConn.Context().Err() != nil
}

func (c *conn) allowWindowIncrease(size uint64) bool {
	return c.scope.ReserveMemory(int(size), network.ReservationPriorityMedium) == nil
}

// OpenStream creates a new stream.
func (c *conn) OpenStream(ctx context.Context) (network.MuxedStream, error) {
	qstr, err := c.quicConn.OpenStreamSync(ctx)
	if err != nil {
		return nil, parseStreamError(err)
	}
	return &stream{Stream: qstr}, nil
}

// AcceptStream accepts a stream opened by the other side.
func (c *conn) AcceptStream() (network.MuxedStream, error) {
	qstr, err := c.quicConn.AcceptStream(context.Background())
	if err != nil {
		return nil, parseStreamError(err)
	}
	return &stream{Stream: qstr}, nil
}

// LocalPeer returns our peer ID
func (c *conn) LocalPeer() peer.ID { return c.localPeer }

// RemotePeer returns the peer ID of the remote peer.
func (c *conn) RemotePeer() peer.ID { return c.remotePeerID }

// RemotePublicKey returns the public key of the remote peer.
func (c *conn) RemotePublicKey() ic.PubKey { return c.remotePubKey }

// LocalMultiaddr returns the local Multiaddr associated
func (c *conn) LocalMultiaddr() ma.Multiaddr { return c.localMultiaddr }

// RemoteMultiaddr returns the remote Multiaddr associated
func (c *conn) RemoteMultiaddr() ma.Multiaddr { return c.remoteMultiaddr }

func (c *conn) Transport() tpt.Transport { return c.transport }

func (c *conn) Scope() network.ConnScope { return c.scope }

// ConnState is the state of security connection.
func (c *conn) ConnState() network.ConnectionState {
	t := "quic-v1"
	if _, err := c.LocalMultiaddr().ValueForProtocol(ma.P_QUIC); err == nil {
		t = "quic"
	}
	return network.ConnectionState{Transport: t}
}

// Compile-time check that conn implements MigratableConn.
var _ network.MigratableConn = &conn{}

// SupportsMigration returns true if this connection supports path migration.
// Only outbound (client-initiated) QUIC connections with the experimental
// migration feature enabled support migration.
func (c *conn) SupportsMigration() bool {
	return c.isOutbound && c.migrationEnabled
}

// ErrPathAlreadyExists is returned when attempting to add a path with a local
// address that already has a path associated with it.
var ErrPathAlreadyExists = errors.New("path with this local address already exists")

// AddPath adds a new potential path using the given local address.
// This is an EXPERIMENTAL API.
func (c *conn) AddPath(ctx context.Context, localAddr ma.Multiaddr) (network.Path, error) {
	if !c.migrationEnabled {
		return nil, ErrMigrationNotEnabled
	}
	if !c.isOutbound {
		return nil, ErrMigrationNotSupported
	}

	// Get a QUIC transport for the local address from the transport's connection manager.
	refCountedTr, quicTr, err := c.transport.getTransportForMigration(localAddr)
	if err != nil {
		return nil, err
	}

	// Add the path using quic-go's API.
	// Note: quic-go's AddPath returns a quic.Path interface.
	quicPath, err := c.quicConn.AddPath(quicTr)
	if err != nil {
		// Release the transport reference on failure to prevent resource leak.
		refCountedTr.DecreaseCount()
		return nil, err
	}

	// Create the connPath with transport reference for cleanup.
	cp := &connPath{
		localAddr:  localAddr,
		remoteAddr: c.remoteMultiaddr,
		transport:  refCountedTr,
	}

	// Create our path wrapper.
	// The remote address doesn't change during client-side migration
	path := &pathWrapper{
		conn:     c,
		quicPath: quicPath,
		connPath: cp,
	}

	// Track the path under lock.
	// We check for duplicates here (not earlier) to avoid a TOCTOU race condition
	// where two goroutines could both pass an early check and then both attempt
	// to add paths with the same address.
	c.migrationMu.Lock()
	if c.paths == nil {
		c.paths = make(map[string]*connPath)
	}
	addrKey := localAddr.String()
	if _, exists := c.paths[addrKey]; exists {
		c.migrationMu.Unlock()
		// Clean up resources since we can't use this path.
		if quicPath != nil {
			_ = quicPath.Close()
		}
		refCountedTr.DecreaseCount()
		return nil, ErrPathAlreadyExists
	}
	c.paths[addrKey] = cp
	c.migrationMu.Unlock()

	return path, nil
}

// ActivePath returns information about the currently active path.
func (c *conn) ActivePath() network.PathInfo {
	c.migrationMu.RLock()
	defer c.migrationMu.RUnlock()

	if c.activePath != nil {
		return network.PathInfo{
			LocalAddr:  c.activePath.localAddr,
			RemoteAddr: c.activePath.remoteAddr,
			Active:     true,
			RTT:        c.activePath.rtt,
		}
	}

	// No explicit active path set, return current connection addresses.
	return network.PathInfo{
		LocalAddr:  c.localMultiaddr,
		RemoteAddr: c.remoteMultiaddr,
		Active:     true,
	}
}

// AvailablePaths returns information about all available paths.
func (c *conn) AvailablePaths() []network.PathInfo {
	c.migrationMu.RLock()
	defer c.migrationMu.RUnlock()

	paths := make([]network.PathInfo, 0, len(c.paths)+1)

	var activePathInfo network.PathInfo
	if c.activePath != nil {
		activePathInfo = network.PathInfo{
			LocalAddr:  c.activePath.localAddr,
			RemoteAddr: c.activePath.remoteAddr,
			Active:     true,
			RTT:        c.activePath.rtt,
		}
	} else {
		// No explicit active path set, use current connection addresses.
		activePathInfo = network.PathInfo{
			LocalAddr:  c.localMultiaddr,
			RemoteAddr: c.remoteMultiaddr,
			Active:     true,
		}
	}
	paths = append(paths, activePathInfo)

	// Add any additional paths.
	for _, p := range c.paths {
		if !p.active {
			paths = append(paths, network.PathInfo{
				LocalAddr:  p.localAddr,
				RemoteAddr: p.remoteAddr,
				Active:     false,
				RTT:        p.rtt,
			})
		}
	}

	return paths
}
