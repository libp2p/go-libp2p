# Allowlist

Imagine you have a node that is getting overloaded by possibly malicious
incoming connections. This node won't be able to accept incoming connections
from peers it _knows_ to be good. This node would effectively be _eclipsed_ from
the network since no other nodes will be able to connect to it.

This is the problem that the Allowlist is designed to solve.

## Design Goals

- We should not fail to allocate a resource for an allowlisted peer because the
  normal transient and system scopes are at their limits. This is the minimum
  bar to avoid eclipse attacks.
- Minimal changes to resource manager and existing code (e.g. go-libp2p).
- The allowlist scope itself is limited to avoid giving an allowlisted peer the
  ability to DoS a node.
- PeerIDs can optionally be fed into the allowlist. This will give an extra
  step of verification before continuing to allow the peer to open streams.
  - A peer may be able to open a connection, but after the handshake, if it's
    not an expected peer id we move it to the normal system scope.
- We can have multiple PeerIDs for a given IP addr.
- No extra cost for the happy path when we are still below system and transient
  limits.

## Proposed change

Add a change to `ResourceManager.OpenConnection` so that it accepts a multiaddr
parameter of the endpoint the connection is for.

Add a change to `ResourceManager` to initialize it with a set of allowlisted
multiaddrs. This set can be modified at runtime as well for dynamic updating.

For example, an allowlist set could look like:
```
/ip4/1.1.1.1
/ip6/2345:0425:2CA1::0567:5673:23b5
/ip4/192.168.1.1/p2p/qmFoo
/ip4/192.168.1.1/p2p/qmBar
/ip4/1.2.3.0/ipcidr/24
```

When a new connection is opened, the resource manager tries to allocate with the
normal system and transient resource scopes. If that fails, it checks if the
multiaddr matches an item in the set of allowlisted multiaddrs. If so, it
creates the connection resource scope using the allowlisted specific system and
transient resource scopes. If it wasn't an allowlisted multiaddr it fails as
before.

When an allowlisted connection is tied to a peer id and transferred with
`ConnManagementScope.SetPeer`, we check if that peer id matches the expected
value in the allowlist (if it exists). If it does not match, we attempt to
transfer this resource to the normal system and peer scope. If that transfer
fails we close the connection.
