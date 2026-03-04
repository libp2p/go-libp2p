# Circuit Management

The circuit package manages the establishment and maintenance of multi-hop paths (circuits) through the mixnet.

## Overview

A circuit is a series of relay nodes between the Origin and the Destination. Lib-Mix uses multiple parallel circuits for each connection to provide high throughput and fault tolerance.

## Key Features

- **Circuit Building**: Orchestrates the multi-hop handshake with relay nodes.
- **Onion Routing**: Implements the logic for forwarding data through multiple layers of encryption.
- **Failover & Recovery**: Automatically detects circuit failures and rebuilds them using fresh relays.
- **Stream Management**: Manages the underlying libp2p streams for each circuit.

## Circuit Establishment

1.  **Relay Selection**: Relays are selected from the pool provided by the discovery package.
2.  **Layered Handshake**: The Origin performs an authenticated handshake with each relay in the path sequentially.
3.  **Activation**: Once all hops are established, the circuit is marked as active and ready for data transmission.
