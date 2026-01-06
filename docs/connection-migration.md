# QUIC Connection Migration

**Status: EXPERIMENTAL**

This document describes the client-side QUIC connection migration feature in go-libp2p, which allows seamlessly switching network paths without disrupting active streams.

## Overview

Connection migration is a QUIC protocol feature that enables a connection to continue operating when the client's network path changes. This is useful for scenarios such as:
- Switching from a primary network interface to a failover interface
- Migrating connections when network conditions change
- Maintaining connectivity during network transitions

**Key Points:**
- Only client-initiated (outbound) connections support migration (per QUIC specification)
- Server-side connections cannot initiate migration
- Requires explicit opt-in via the `EnableExperimentalConnectionMigration()` option
- Each alternate path requires a separate network interface

## Enabling Connection Migration

To enable connection migration, use the `EnableExperimentalConnectionMigration()` option when creating a QUIC transport:

```go
import (
    "github.com/libp2p/go-libp2p"
    quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
)

// When using the transport directly
transport, err := quic.NewTransport(
    privateKey,
    connManager,
    nil, // psk
    nil, // gater
    nil, // rcmgr
    quic.EnableExperimentalConnectionMigration(),
)
```

## Using the Migration API

### Checking Migration Support

```go
import "github.com/libp2p/go-libp2p/core/network"

// Check if a connection supports migration
var migratable network.MigratableConn
if conn.As(&migratable) && migratable.SupportsMigration() {
    // Connection supports migration
}
```

### Adding and Switching Paths

The typical migration flow is:
1. **AddPath** - Add a new path using a different local address
2. **Probe** - Test connectivity on the new path
3. **Switch** - Migrate the connection to the new path

```go
import (
    "context"
    "time"

    "github.com/libp2p/go-libp2p/core/network"
    ma "github.com/multiformats/go-multiaddr"
)

// Get the MigratableConn interface
var migratable network.MigratableConn
if !conn.As(&migratable) || !migratable.SupportsMigration() {
    return errors.New("connection does not support migration")
}

// Define the new local address (e.g., secondary/failover interface)
newLocalAddr, _ := ma.NewMultiaddr("/ip4/10.0.0.1/udp/0/quic-v1")

// Add a new path
path, err := migratable.AddPath(context.Background(), newLocalAddr)
if err != nil {
    return fmt.Errorf("failed to add path: %w", err)
}

// Probe the new path (with timeout)
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

if err := path.Probe(ctx); err != nil {
    // Probe failed, path is not viable
    path.Close()
    return fmt.Errorf("path probe failed: %w", err)
}

// Get path info (includes RTT from probe)
info := path.Info()
fmt.Printf("Path RTT: %v\n", info.RTT)

// Switch to the new path
if err := path.Switch(); err != nil {
    return fmt.Errorf("failed to switch path: %w", err)
}

// Connection is now using the new path
fmt.Printf("Migrated to: %s\n", path.Info().LocalAddr)
```

### Querying Available Paths

```go
// Get the currently active path
activePath := migratable.ActivePath()
fmt.Printf("Active path: %s -> %s\n", activePath.LocalAddr, activePath.RemoteAddr)

// Get all available paths
paths := migratable.AvailablePaths()
for _, p := range paths {
    status := "inactive"
    if p.Active {
        status = "active"
    }
    fmt.Printf("Path [%s]: %s -> %s (RTT: %v)\n", status, p.LocalAddr, p.RemoteAddr, p.RTT)
}
```

### Closing a Path

```go
import (
    "errors"
    "fmt"

    libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
)

// Close a non-active path
if err := path.Close(); err != nil {
    // Note: Cannot close the active path
    if errors.Is(err, libp2pquic.ErrActivePathClose) {
        fmt.Println("Switch to another path before closing this one")
    }
}
```

## Event Types

The `core/event` package defines events for monitoring migration:

```go
import "github.com/libp2p/go-libp2p/core/event"

// Subscribe to migration events
sub, _ := eventBus.Subscribe([]interface{}{
    new(event.EvtConnectionMigrationStarted),
    new(event.EvtConnectionMigrationCompleted),
    new(event.EvtConnectionMigrationFailed),
    new(event.EvtPathAdded),
    new(event.EvtPathRemoved),
})

for evt := range sub.Out() {
    switch e := evt.(type) {
    case event.EvtConnectionMigrationCompleted:
        fmt.Printf("Migration completed: %s -> %s (RTT: %v)\n",
            e.FromLocalAddr, e.ToLocalAddr, e.ProbeRTT)
    case event.EvtConnectionMigrationFailed:
        fmt.Printf("Migration failed: %v (closed: %v)\n", e.Error, e.ConnectionClosed)
    }
}
```

## Constraints and Limitations

1. **Client-only migration**: Only connections initiated by your node (outbound/dial) support migration. Incoming connections cannot be migrated.

2. **Single active path**: Only one path can be active at a time. The active path is used for all communication.

3. **Separate network interfaces**: Each path requires a different local address, typically from a different network interface.

4. **Experimental API**: This API is experimental and may change in future versions.

## Resource Management

Paths hold references to network transports that must be properly cleaned up:

1. **After switching**: The old path remains available but holds transport resources. Close it if you don't need rollback capability:
   ```go
   oldPath := currentPath // save reference before switching
   if err := newPath.Switch(); err == nil {
       oldPath.Close() // release old path resources
   }
   ```

2. **On failure**: If `AddPath` fails, resources are automatically cleaned up. If `Probe` or `Switch` fails, close the path:
   ```go
   path, err := migratable.AddPath(ctx, newAddr)
   if err != nil {
       return err // no cleanup needed
   }
   if err := path.Probe(ctx); err != nil {
       path.Close() // clean up failed path
       return err
   }
   ```

3. **On connection close**: All path resources are automatically cleaned up when the connection is closed.

4. **Getting current local address**: After switching paths, use `ActivePath().LocalAddr` to get the current local address. The `LocalMultiaddr()` method returns the original address.

## Error Handling

Common errors you may encounter:

| Error | Cause | Resolution |
|-------|-------|------------|
| `ErrMigrationNotEnabled` | Migration option not set | Use `EnableExperimentalConnectionMigration()` |
| `ErrMigrationNotSupported` | Server-side connection | Only client connections support migration |
| `ErrPathAlreadyExists` | Path with same local address exists | Use a different local address or close the existing path |
| `ErrActivePathClose` | Attempted to close active path | Switch to another path first |
| Context deadline exceeded | Probe timed out | Network path may be unavailable |

## Example: Failover to Secondary Network

```go
func migrateToFailoverNetwork(conn network.Conn, failoverAddr ma.Multiaddr) error {
    var migratable network.MigratableConn
    if !conn.As(&migratable) || !migratable.SupportsMigration() {
        return errors.New("migration not supported")
    }

    // Add the failover path
    path, err := migratable.AddPath(context.Background(), failoverAddr)
    if err != nil {
        return err
    }

    // Probe with timeout
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    if err := path.Probe(ctx); err != nil {
        path.Close()
        return fmt.Errorf("failover network unreachable: %w", err)
    }

    // Switch to failover
    if err := path.Switch(); err != nil {
        path.Close()
        return err
    }

    log.Printf("Successfully migrated to failover network")
    return nil
}
```

## See Also

- [quic-go Connection Migration](https://quic-go.net/docs/quic/connection-migration/) - Upstream documentation
- [RFC 9000 Section 9](https://datatracker.ietf.org/doc/html/rfc9000#section-9) - QUIC Connection Migration specification
