package libp2pquic

import (
	"context"
	"errors"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/quic-go/quic-go"
)

// ErrActivePathClose is returned when attempting to close the active path.
var ErrActivePathClose = errors.New("cannot close active path; switch to a different path first")

// pathWrapper wraps a quic-go Path to implement network.Path.
type pathWrapper struct {
	conn     *conn
	quicPath *quic.Path
	connPath *connPath
}

// Compile-time check that pathWrapper implements network.Path.
var _ network.Path = &pathWrapper{}

// Probe tests connectivity on this path by sending PATH_CHALLENGE frames.
// Returns nil if the path is valid and can be switched to.
func (p *pathWrapper) Probe(ctx context.Context) error {
	start := time.Now()
	if err := p.quicPath.Probe(ctx); err != nil {
		return err
	}
	// Protect rtt write with lock to avoid data race with Info().
	p.conn.migrationMu.Lock()
	p.connPath.rtt = time.Since(start)
	p.conn.migrationMu.Unlock()
	return nil
}

// Switch migrates the connection to use this path for all future communication.
// Should only be called after a successful Probe().
//
// Note: After switching, use ActivePath().LocalAddr to get the current local address.
// The LocalMultiaddr() method returns the original address to avoid data races.
func (p *pathWrapper) Switch() error {
	if err := p.quicPath.Switch(); err != nil {
		return err
	}

	// Update path tracking after successful switch.
	p.conn.migrationMu.Lock()
	defer p.conn.migrationMu.Unlock()

	// Mark the old active path as inactive.
	if p.conn.activePath != nil {
		p.conn.activePath.active = false
	}

	// Mark this path as active.
	p.connPath.active = true
	p.conn.activePath = p.connPath

	// Note: We intentionally do NOT modify p.conn.localMultiaddr here.
	// That field may be read concurrently from other goroutines without
	// holding migrationMu (e.g., LocalMultiaddr()), so writing to it would
	// introduce a data race. Use ActivePath().LocalAddr to get the current
	// local address after migration.
	//
	// The remote address doesn't change during client-side migration.
	// We're only changing which local interface we use to reach the same peer.

	return nil
}

// Info returns information about this path.
func (p *pathWrapper) Info() network.PathInfo {
	p.conn.migrationMu.RLock()
	defer p.conn.migrationMu.RUnlock()

	return network.PathInfo{
		LocalAddr:  p.connPath.localAddr,
		RemoteAddr: p.connPath.remoteAddr,
		Active:     p.connPath.active,
		RTT:        p.connPath.rtt,
	}
}

// Close removes this path from the connection and releases its resources.
// Returns an error if this is the active path.
func (p *pathWrapper) Close() error {
	p.conn.migrationMu.Lock()
	defer p.conn.migrationMu.Unlock()

	if p.connPath.active {
		return ErrActivePathClose
	}

	delete(p.conn.paths, p.connPath.localAddr.String())

	// Close the underlying quic-go path.
	if p.quicPath != nil {
		_ = p.quicPath.Close()
	}

	// Release the transport reference to prevent resource leak.
	if p.connPath.transport != nil {
		p.connPath.transport.DecreaseCount()
		p.connPath.transport = nil
	}

	return nil
}
