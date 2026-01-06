package event

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

// EvtConnectionMigrationStarted is emitted when a connection migration begins.
// This event would be emitted when AddPath() completes successfully, before
// the user calls Probe() on the returned path.
//
// Note: Event emission is not yet implemented. These types are defined for
// future use and API stability.
//
// This is an EXPERIMENTAL event and may change in future versions.
type EvtConnectionMigrationStarted struct {
	// Peer is the remote peer ID of the connection being migrated.
	Peer peer.ID
	// ConnID is the unique identifier of the connection.
	ConnID string
	// FromLocalAddr is the current local address before migration.
	FromLocalAddr ma.Multiaddr
	// ToLocalAddr is the target local address to migrate to.
	ToLocalAddr ma.Multiaddr
}

// EvtConnectionMigrationCompleted is emitted when a connection migration succeeds.
// At this point, the connection is using the new path for all communication.
//
// Note: In client-side QUIC migration, only the local address changes. The remote
// address remains the same since we're changing which local interface we use to
// reach the same peer.
//
// This is an EXPERIMENTAL event and may change in future versions.
type EvtConnectionMigrationCompleted struct {
	// Peer is the remote peer ID of the migrated connection.
	Peer peer.ID
	// ConnID is the unique identifier of the connection.
	ConnID string
	// FromLocalAddr is the previous local address.
	FromLocalAddr ma.Multiaddr
	// ToLocalAddr is the new local address after migration.
	ToLocalAddr ma.Multiaddr
	// ProbeRTT is the round-trip time measured during path probing.
	ProbeRTT time.Duration
}

// EvtConnectionMigrationFailed is emitted when a connection migration fails.
// The connection may still be usable on the original path, or it may be closed
// if both the new path and rollback failed.
//
// This is an EXPERIMENTAL event and may change in future versions.
type EvtConnectionMigrationFailed struct {
	// Peer is the remote peer ID of the connection.
	Peer peer.ID
	// ConnID is the unique identifier of the connection.
	ConnID string
	// FromLocalAddr is the original local address.
	FromLocalAddr ma.Multiaddr
	// ToLocalAddr is the target local address that failed.
	ToLocalAddr ma.Multiaddr
	// Error describes why the migration failed.
	Error error
	// ConnectionClosed indicates whether the connection was closed due to
	// the failure (e.g., if rollback to the original path also failed).
	ConnectionClosed bool
}

// EvtPathAdded is emitted when a new path is added to a connection.
// This happens when MigratableConn.AddPath() is called successfully.
//
// This is an EXPERIMENTAL event and may change in future versions.
type EvtPathAdded struct {
	// Peer is the remote peer ID of the connection.
	Peer peer.ID
	// ConnID is the unique identifier of the connection.
	ConnID string
	// LocalAddr is the local address of the new path.
	LocalAddr ma.Multiaddr
}

// EvtPathRemoved is emitted when a path is removed from a connection.
// This happens when Path.Close() is called.
//
// This is an EXPERIMENTAL event and may change in future versions.
type EvtPathRemoved struct {
	// Peer is the remote peer ID of the connection.
	Peer peer.ID
	// ConnID is the unique identifier of the connection.
	ConnID string
	// LocalAddr is the local address of the removed path.
	LocalAddr ma.Multiaddr
}
