# Requirements Document

## Introduction

Lib-Mix is a high-performance, sharded, configurable-hop mixnet protocol for libp2p that provides metadata-private communication at near-wire speeds. The protocol addresses the fundamental trade-off between privacy and performance in decentralized applications by using multi-path sharding and configurable onion routing. It operates as a transport-agnostic stream upgrader that can be toggled on by libp2p developers when metadata privacy is required without sacrificing application performance.

## Glossary

- **Lib_Mix_Protocol**: The complete anonymity layer system that provides metadata-private communication over libp2p
- **Origin**: The libp2p peer initiating a private communication stream
- **Destination**: The libp2p peer receiving the private communication stream
- **Relay_Node**: A libp2p peer that forwards encrypted data without knowledge of the Origin or Destination
- **Entry_Relay**: The first relay in a circuit that sees the Origin PeerID but not the Destination
- **Exit_Relay**: The final relay in a circuit that sees the Destination but not the Origin PeerID
- **Circuit**: A complete path from Origin through relay nodes to Destination
- **Shard**: An independent fragment of erasure-coded encrypted data
- **CES_Pipeline**: The Compress-Encrypt-Shard processing pipeline applied to data at the Origin
- **DHT_Pool**: The set of potential relay nodes discovered via Kademlia DHT
- **Noise_Protocol**: The cryptographic framework used for layered encryption
- **Stream_Upgrader**: A libp2p component that wraps existing streams with additional protocol layers
- **RTT**: Round Trip Time, the latency measurement between two peers
- **Reed_Solomon_Coding**: The erasure coding algorithm used for data sharding
- **Multiaddr**: A libp2p multi-address format for identifying network endpoints
- **Protocol_ID**: The libp2p protocol identifier string

## Requirements

### Requirement 1: Configurable Multi-Hop Routing

**User Story:** As a libp2p developer, I want to configure the number of relay hops, so that I can balance privacy requirements against latency constraints for my specific application.

#### Acceptance Criteria

1. THE Lib_Mix_Protocol SHALL accept a configuration parameter specifying the number of hops per circuit
2. THE Lib_Mix_Protocol SHALL support a minimum of 1 hop and a maximum of 10 hops per circuit
3. THE Lib_Mix_Protocol SHALL default to 2 hops when no configuration is provided
4. WHEN the hop count is configured, THE Lib_Mix_Protocol SHALL construct circuits with exactly that number of Relay_Nodes
5. THE Lib_Mix_Protocol SHALL apply one Noise_Protocol encryption layer per configured hop

### Requirement 2: Configurable Multi-Circuit Sharding

**User Story:** As a libp2p developer, I want to configure the number of parallel circuits, so that I can adjust the redundancy and traffic distribution for my threat model.

#### Acceptance Criteria

1. THE Lib_Mix_Protocol SHALL accept a configuration parameter specifying the number of parallel circuits
2. THE Lib_Mix_Protocol SHALL support a minimum of 1 circuit and a maximum of 20 circuits
3. THE Lib_Mix_Protocol SHALL default to 3 circuits when no configuration is provided
4. WHEN the circuit count is configured, THE Lib_Mix_Protocol SHALL distribute shards across exactly that number of circuits
5. THE Lib_Mix_Protocol SHALL adjust Reed_Solomon_Coding parameters to match the configured circuit count

### Requirement 3: CES Pipeline Data Processing

**User Story:** As an Origin peer, I want my data compressed, encrypted, and sharded before transmission, so that observers cannot reconstruct content or timing from individual circuit traffic.

#### Acceptance Criteria

1. WHEN data is submitted for transmission, THE CES_Pipeline SHALL compress the data using a configurable compression algorithm
2. THE CES_Pipeline SHALL support Gzip and Snappy compression algorithms
3. WHEN compression completes, THE CES_Pipeline SHALL apply layered Noise_Protocol encryption based on the configured encryption mode (full-payload per hop or header-only per hop)
4. WHEN encryption completes, THE CES_Pipeline SHALL apply Reed_Solomon_Coding to generate shards equal to the configured circuit count
5. THE CES_Pipeline SHALL generate shards such that any subset of shards meeting the erasure coding threshold can reconstruct the original data
6. THE CES_Pipeline SHALL process data in sequential order without reordering operations

### Requirement 3A: Header-Only Onion Optimization

**User Story:** As a libp2p developer, I want an optional header-only onion mode, so that I can reduce per-hop encryption overhead while keeping routing metadata private.

#### Acceptance Criteria

1. THE Lib_Mix_Protocol SHALL expose an `encryption_mode` configuration with values `full` and `header-only`
2. WHEN `encryption_mode` is `full`, THE Lib_Mix_Protocol SHALL apply layered encryption to the entire shard payload per hop
3. WHEN `encryption_mode` is `header-only`, THE Lib_Mix_Protocol SHALL apply layered encryption only to routing/control headers and forward the end-to-end encrypted shard payload unchanged across hops
4. WHEN `encryption_mode` is `header-only`, THE routing header SHALL include the encrypted destination and per-shard control metadata required by the destination to reconstruct data
5. THE Lib_Mix_Protocol SHALL default to `full` encryption mode unless explicitly configured otherwise

### Requirement 4: DHT-Based Relay Discovery

**User Story:** As an Origin peer, I want to discover potential relay nodes from the DHT, so that I can build circuits without relying on centralized directory services.

#### Acceptance Criteria

1. WHEN building circuits, THE Lib_Mix_Protocol SHALL query the Kademlia DHT for potential Relay_Nodes
2. THE Lib_Mix_Protocol SHALL retrieve a DHT_Pool containing at least 3 times the number of required Relay_Nodes
3. WHEN the DHT_Pool is insufficient, THE Lib_Mix_Protocol SHALL return an error indicating insufficient relay availability
4. THE Lib_Mix_Protocol SHALL filter the DHT_Pool to exclude the Origin and Destination peers
5. THE Lib_Mix_Protocol SHALL filter the DHT_Pool to exclude peers that do not advertise the Protocol_ID
6. THE Lib_Mix_Protocol SHALL support a `selection_mode` configuration with values `rtt`, `random`, and `hybrid`, defaulting to `rtt`.
7. WHEN `selection_mode` is `hybrid`, THE Lib_Mix_Protocol SHALL uniformly sample a configurable `sampling_size` K candidates from the filtered DHT_Pool (default K = 3 × required_relays) before performing RTT qualification.
8. WHEN `selection_mode` is `hybrid`, THE Lib_Mix_Protocol SHALL measure RTT for the sampled candidates (respecting the 5s timeout) and select relays per-circuit using an adjustable `randomness_factor` in [0.0, 1.0] that balances latency (RTT) and randomness.
9. WHEN `selection_mode` is `random`, THE Lib_Mix_Protocol SHALL select relays uniformly at random from the filtered DHT_Pool subject to the exclusion rules and optional RTT threshold.
10. THE Lib_Mix_Protocol SHALL expose `sampling_size` and `randomness_factor` as validated configuration parameters and reject invalid values.

### Requirement 5: Latency-Based Relay Selection

**User Story:** As an Origin peer, I want to select the lowest-latency relays from the DHT pool, so that the mixnet does not introduce unnecessary delays.

#### Acceptance Criteria

1. WHEN the DHT_Pool is populated, THE Lib_Mix_Protocol SHALL measure RTT to each peer in the pool
2. THE Lib_Mix_Protocol SHALL complete RTT measurements within 5 seconds or mark unresponsive peers as unavailable
3. WHEN RTT measurements complete, THE Lib_Mix_Protocol SHALL sort the DHT_Pool by ascending RTT
4. THE Lib_Mix_Protocol SHALL select Relay_Nodes from the sorted pool starting with the lowest RTT peers
5. THE Lib_Mix_Protocol SHALL ensure no Relay_Node appears in multiple positions within the same circuit

### Requirement 6: Circuit Construction

**User Story:** As an Origin peer, I want to construct multiple independent circuits to the Destination, so that I can distribute shards across diverse network paths.

#### Acceptance Criteria

1. WHEN Relay_Nodes are selected, THE Lib_Mix_Protocol SHALL construct circuits equal to the configured circuit count
2. THE Lib_Mix_Protocol SHALL ensure each circuit uses a distinct set of Relay_Nodes
3. WHEN constructing a circuit, THE Lib_Mix_Protocol SHALL establish a libp2p stream to the first Relay_Node
4. THE Lib_Mix_Protocol SHALL negotiate Noise_Protocol encryption with each Relay_Node in sequence
5. WHEN all circuits are established, THE Lib_Mix_Protocol SHALL mark the connection as ready for data transmission
6. IF circuit construction fails for any circuit, THEN THE Lib_Mix_Protocol SHALL tear down all circuits and return an error

### Requirement 7: Stream-Based Relay Forwarding

**User Story:** As a Relay_Node, I want to forward encrypted data without reading or storing it, so that I operate as a zero-knowledge intermediary.

#### Acceptance Criteria

1. WHEN a Relay_Node receives encrypted data, THE Relay_Node SHALL decrypt only the outermost Noise_Protocol layer
2. THE Relay_Node SHALL extract the next-hop Multiaddr from the decrypted header
3. THE Relay_Node SHALL establish a libp2p stream to the next-hop peer if not already connected
4. THE Relay_Node SHALL forward the remaining encrypted payload to the next-hop peer without buffering
5. THE Relay_Node SHALL maintain the stream connection until the Origin closes the circuit
6. THE Relay_Node SHALL NOT log, store, or inspect the payload content beyond the outer header

### Requirement 8: Shard Transmission

**User Story:** As an Origin peer, I want to transmit each shard through its assigned circuit, so that no single observer can reconstruct the complete message.

#### Acceptance Criteria

1. WHEN shards are generated, THE Lib_Mix_Protocol SHALL assign each shard to a distinct circuit
2. THE Lib_Mix_Protocol SHALL transmit all shards in parallel across their assigned circuits
3. WHEN transmitting a shard, THE Lib_Mix_Protocol SHALL prepend routing headers for each hop in the circuit
4. THE Lib_Mix_Protocol SHALL encrypt each shard with layered Noise_Protocol encryption in reverse hop order
5. THE Lib_Mix_Protocol SHALL stream shard data without waiting for acknowledgment from intermediate Relay_Nodes

### Requirement 9: Shard Reception and Reconstruction

**User Story:** As a Destination peer, I want to receive shards from multiple circuits and reconstruct the original data, so that I can process the private communication.

#### Acceptance Criteria

1. WHEN the Destination receives a shard, THE Lib_Mix_Protocol SHALL buffer the shard until the erasure coding threshold is met
2. THE Lib_Mix_Protocol SHALL apply Reed_Solomon_Coding to reconstruct the encrypted compressed data from available shards
3. WHEN reconstruction succeeds, THE Lib_Mix_Protocol SHALL decrypt the data using the negotiated Noise_Protocol keys
4. THE Lib_Mix_Protocol SHALL decompress the decrypted data using the algorithm specified in the data header
5. THE Lib_Mix_Protocol SHALL deliver the reconstructed data to the application layer in the original order
6. IF the erasure coding threshold is not met within 30 seconds, THEN THE Lib_Mix_Protocol SHALL return a timeout error

### Requirement 10: Circuit Failure Recovery

**User Story:** As an Origin peer, I want to recover from relay node failures without dropping the connection, so that temporary network issues do not interrupt my communication.

#### Acceptance Criteria

1. WHEN a Relay_Node disconnects, THE Lib_Mix_Protocol SHALL detect the failure within 5 seconds
2. IF the number of remaining functional circuits meets the erasure coding threshold, THEN THE Lib_Mix_Protocol SHALL continue transmission using the remaining circuits
3. WHEN a circuit fails, THE Lib_Mix_Protocol SHALL select a new Relay_Node from the DHT_Pool
4. THE Lib_Mix_Protocol SHALL construct a replacement circuit using the new Relay_Node
5. THE Lib_Mix_Protocol SHALL resume shard transmission on the replacement circuit without data loss
6. IF the number of functional circuits falls below the erasure coding threshold, THEN THE Lib_Mix_Protocol SHALL return an error and close the connection

### Requirement 11: Transport Agnostic Operation

**User Story:** As a libp2p developer, I want the protocol to work over any libp2p transport, so that I can use it in diverse network environments including browsers.

#### Acceptance Criteria

1. THE Lib_Mix_Protocol SHALL operate over libp2p QUIC transport
2. THE Lib_Mix_Protocol SHALL operate over libp2p TCP transport
3. THE Lib_Mix_Protocol SHALL operate over libp2p WebRTC transport
4. THE Lib_Mix_Protocol SHALL negotiate transport selection using standard libp2p multiaddr resolution
5. THE Lib_Mix_Protocol SHALL NOT depend on transport-specific features beyond standard stream semantics

### Requirement 12: Protocol Identification

**User Story:** As a libp2p peer, I want to advertise and discover the Lib-Mix protocol capability, so that I can identify compatible peers in the network.

#### Acceptance Criteria

1. THE Lib_Mix_Protocol SHALL register the Protocol_ID "/lib-mix/1.0.0" with the libp2p host
2. WHEN querying peer capabilities, THE Lib_Mix_Protocol SHALL check for the Protocol_ID in the peer's protocol list
3. THE Lib_Mix_Protocol SHALL reject connection attempts from peers that do not advertise the Protocol_ID
4. THE Lib_Mix_Protocol SHALL include the Protocol_ID in all stream negotiations

### Requirement 13: Stream Upgrader Integration

**User Story:** As a libp2p developer, I want to enable Lib-Mix as a stream upgrader, so that I can add metadata privacy to existing applications with minimal code changes.

#### Acceptance Criteria

1. THE Lib_Mix_Protocol SHALL implement the libp2p Stream_Upgrader interface
2. WHEN enabled, THE Stream_Upgrader SHALL intercept outbound stream creation requests
3. THE Stream_Upgrader SHALL wrap the requested stream with Lib-Mix circuit establishment
4. THE Stream_Upgrader SHALL present a standard libp2p stream interface to the application layer
5. THE Stream_Upgrader SHALL allow applications to read and write data without awareness of the underlying circuit topology

### Requirement 14: Metadata Privacy Guarantees

**User Story:** As a privacy-conscious user, I want the protocol to hide communication patterns, so that observers cannot determine who is communicating with whom.

#### Acceptance Criteria

1. THE Lib_Mix_Protocol SHALL ensure Entry_Relay nodes cannot determine the Destination peer
2. THE Lib_Mix_Protocol SHALL ensure Exit_Relay nodes cannot determine the Origin peer
3. THE Lib_Mix_Protocol SHALL ensure intermediate Relay_Nodes cannot determine both Origin and Destination
4. THE Lib_Mix_Protocol SHALL ensure observers of a single circuit cannot reconstruct message content from shard data alone
5. THE Lib_Mix_Protocol SHALL use ephemeral Noise_Protocol keys that are not linkable across sessions

### Requirement 15: Configuration Validation

**User Story:** As a libp2p developer, I want the protocol to validate my configuration parameters, so that I receive clear feedback when settings are incompatible.

#### Acceptance Criteria

1. WHEN configuration is provided, THE Lib_Mix_Protocol SHALL validate that hop count is between 1 and 10 inclusive
2. WHEN configuration is provided, THE Lib_Mix_Protocol SHALL validate that circuit count is between 1 and 20 inclusive
3. WHEN configuration is provided, THE Lib_Mix_Protocol SHALL validate that the erasure coding threshold is less than the circuit count
4. IF configuration validation fails, THEN THE Lib_Mix_Protocol SHALL return an error describing the invalid parameter
5. THE Lib_Mix_Protocol SHALL reject configuration changes while circuits are active

### Requirement 16: Cryptographic Key Management

**User Story:** As an Origin peer, I want ephemeral encryption keys for each circuit, so that compromise of one circuit does not affect others.

#### Acceptance Criteria

1. WHEN establishing a circuit, THE Lib_Mix_Protocol SHALL generate ephemeral Noise_Protocol key pairs for each hop
2. THE Lib_Mix_Protocol SHALL use the XX handshake pattern for Noise_Protocol negotiation
3. WHEN a circuit is closed, THE Lib_Mix_Protocol SHALL securely erase all ephemeral keys associated with that circuit
4. THE Lib_Mix_Protocol SHALL NOT reuse ephemeral keys across different circuits
5. THE Lib_Mix_Protocol SHALL derive independent encryption keys for each layer of the onion encryption

### Requirement 17: Performance Monitoring

**User Story:** As a libp2p developer, I want to monitor protocol performance metrics, so that I can tune configuration parameters for my application.

#### Acceptance Criteria

1. THE Lib_Mix_Protocol SHALL expose metrics for average RTT across all active circuits
2. THE Lib_Mix_Protocol SHALL expose metrics for circuit construction success rate
3. THE Lib_Mix_Protocol SHALL expose metrics for circuit failure and recovery events
4. THE Lib_Mix_Protocol SHALL expose metrics for data throughput per circuit
5. THE Lib_Mix_Protocol SHALL expose metrics for compression ratio achieved by the CES_Pipeline
6. THE Lib_Mix_Protocol SHALL update metrics in real-time without blocking data transmission

### Requirement 18: Graceful Shutdown

**User Story:** As an Origin peer, I want to cleanly close circuits when communication completes, so that relay nodes can release resources.

#### Acceptance Criteria

1. WHEN the application closes a stream, THE Lib_Mix_Protocol SHALL send a close signal through all active circuits
2. THE Lib_Mix_Protocol SHALL wait for acknowledgment from all Relay_Nodes with a timeout of 10 seconds
3. WHEN acknowledgments are received, THE Lib_Mix_Protocol SHALL close all underlying libp2p streams
4. THE Lib_Mix_Protocol SHALL securely erase all circuit-specific cryptographic material
5. IF acknowledgment timeout occurs, THEN THE Lib_Mix_Protocol SHALL forcibly close streams and log the timeout event

### Requirement 19: Error Handling and Reporting

**User Story:** As a libp2p developer, I want descriptive error messages when protocol operations fail, so that I can diagnose and resolve issues quickly.

#### Acceptance Criteria

1. WHEN DHT relay discovery fails, THE Lib_Mix_Protocol SHALL return an error indicating insufficient peers available
2. WHEN circuit construction fails, THE Lib_Mix_Protocol SHALL return an error identifying which relay connection failed
3. WHEN shard reconstruction fails, THE Lib_Mix_Protocol SHALL return an error indicating which shards were not received
4. WHEN encryption negotiation fails, THE Lib_Mix_Protocol SHALL return an error identifying the incompatible peer
5. THE Lib_Mix_Protocol SHALL include relevant context in all error messages without exposing sensitive routing information

### Requirement 20: Relay Node Resource Limits

**User Story:** As a Relay_Node operator, I want to limit the resources consumed by forwarding traffic, so that my node remains stable under load.

#### Acceptance Criteria

1. THE Relay_Node SHALL accept a configuration parameter for maximum concurrent circuits
2. THE Relay_Node SHALL accept a configuration parameter for maximum bandwidth per circuit
3. WHEN resource limits are reached, THE Relay_Node SHALL reject new circuit establishment requests
4. THE Relay_Node SHALL enforce bandwidth limits by applying backpressure to incoming streams
5. THE Relay_Node SHALL expose metrics for current resource utilization
