package network

import (
	"context"
	"time"

	ma "github.com/multiformats/go-multiaddr"
)

// PathInfo contains information about a network path.
//
// This is an EXPERIMENTAL type and may change in future versions.
type PathInfo struct {
	// LocalAddr is the local address of this path.
	LocalAddr ma.Multiaddr
	// RemoteAddr is the remote address of this path.
	RemoteAddr ma.Multiaddr
	// Active indicates whether this is the currently active path.
	Active bool
	// RTT is the round-trip time measured for this path.
	RTT time.Duration
}

// Path represents a network path that can be used for connection migration.
// A path is created via MigratableConn.AddPath() and can be probed and
// switched to.
//
// This is an EXPERIMENTAL interface and may change in future versions.
type Path interface {
	// Probe tests the path connectivity by sending a probe packet and
	// waiting for acknowledgment. This validates that the path is usable
	// before switching to it.
	//
	// The context can be used to set a timeout for the probe operation.
	Probe(ctx context.Context) error

	// Switch makes this path the active path for the connection.
	// This should only be called after a successful Probe().
	// After switching, all subsequent packets will use this path.
	Switch() error

	// Info returns information about this path.
	Info() PathInfo

	// Close removes this path from the connection.
	// If this is the active path, Close will fail; switch to a different
	// path first.
	Close() error
}

// MigratableConn is implemented by connections that support path migration.
// This allows switching the underlying network path (e.g., from a primary
// interface to a failover interface) without disrupting active streams.
//
// Only client-initiated (outbound) QUIC connections support migration per
// the QUIC specification.
//
// This is an EXPERIMENTAL interface and may change in future versions.
type MigratableConn interface {
	// SupportsMigration returns true if this connection supports path migration.
	// Returns false for server-side connections or when migration is disabled.
	SupportsMigration() bool

	// AddPath adds a new potential path using the given local address.
	// The returned Path can be probed and then switched to.
	//
	// The context can be used to set a timeout for path creation.
	AddPath(ctx context.Context, localAddr ma.Multiaddr) (Path, error)

	// ActivePath returns information about the currently active path.
	ActivePath() PathInfo

	// AvailablePaths returns information about all available paths,
	// including the active path and any paths added via AddPath().
	AvailablePaths() []PathInfo
}
