# libp2p metrics server

This is an experimental libp2p service that exposes a libp2p node's metrics to
a set of allowlisted peers (by their Peer ID).

Useful for:
- Help debugging a node you do not control.
- Ingesting a node's metrics over libp2p when exposing the standard HTTP metrics
  server is difficult.
- Allowing only authenticated peers to ingest metrics.
