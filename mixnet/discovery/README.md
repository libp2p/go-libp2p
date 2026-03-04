# Relay Discovery

The discovery package is responsible for finding and selecting suitable relay nodes for Lib-Mix circuits.

## Mechanisms

- **DHT-based Discovery**: Uses the libp2p Kademlia DHT to find peers that have advertised themselves as Mixnet relays.
- **Relay Selection**: Implements various strategies for selecting relays from the discovered pool:
    - **RTT-based**: Selects relays with the lowest latency.
    - **Random**: Selects relays randomly to maximize anonymity.
    - **Hybrid**: A balanced approach combining latency and randomness.

## Requirements

Relay nodes must:
1. Support the Lib-Mix protocol.
2. Be publicly reachable or have a valid relay address.
3. Maintain acceptable uptime and performance.
