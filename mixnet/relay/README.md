# Relay Node

The relay package implements the functionality for a libp2p node to act as a relay in the Lib-Mix network.

## Functionality

- **Packet Forwarding**: Receives encrypted shards, strips one layer of encryption, and forwards them to the next hop.
- **Resource Management**: Limits the number of active circuits and bandwidth used by the relay to prevent exhaustion.
- **Zero Knowledge**: Relays do not know the Origin (except for the entry relay) or the Destination (except for the exit relay). They never see the unencrypted content.

## Operating a Relay

To operate a relay, a node simply needs to register the Lib-Mix protocol handler and advertise its capability in the DHT.

```go
// Example (Internal)
handler := relay.NewHandler(host, maxCircuits, bufferSize)
host.SetStreamHandler(mixnet.ProtocolID, handler.HandleStream)
```
