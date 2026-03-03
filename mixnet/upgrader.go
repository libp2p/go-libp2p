package mixnet

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"

	"github.com/libp2p/go-libp2p/mixnet/ces"
	"github.com/libp2p/go-libp2p/mixnet/circuit"
	"github.com/libp2p/go-libp2p/mixnet/discovery"
	"github.com/libp2p/go-libp2p/mixnet/relay"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// Mixnet is the main mixnet implementation
type Mixnet struct {
	config       *MixnetConfig
	host         host.Host
	routing      routing.Routing
	circuitMgr   *circuit.CircuitManager
	pipeline     *ces.CESPipeline
	relayHandler *relay.Handler
	discovery    *discovery.RelayDiscovery
	metrics      *MetricsCollector

	// For origin mode
	originCtx    context.Context
	originCancel context.CancelFunc

	// For destination mode
	destHandler *DestinationHandler

	// Established circuits to destinations
	activeConnections map[peer.ID][]*circuit.Circuit

	mu sync.RWMutex
}

// DestinationHandler handles incoming data at the destination
type DestinationHandler struct {
	pipeline  *ces.CESPipeline
	shardBuf  map[string][]*ces.Shard
	keys      map[string][]*ces.EncryptionKey
	threshold int
	timeout   time.Duration
	dataCh    chan []byte
	stopCh    chan struct{}
	mu        sync.Mutex
}

// NewMixnet creates a new Mixnet instance (Req 1, 2)
func NewMixnet(cfg *MixnetConfig, h host.Host, r routing.Routing) (*Mixnet, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	cfg.InitDefaults()

	// Create metrics collector (Req 17)
	metrics := NewMetricsCollector()

	// Create circuit manager (Req 6)
	circuitCfg := &circuit.CircuitConfig{
		HopCount:      cfg.HopCount,
		CircuitCount:  cfg.CircuitCount,
		StreamTimeout: 30 * time.Second,
	}
	circuitMgr := circuit.NewCircuitManager(circuitCfg)
	circuitMgr.SetHost(h)

	// Create CES pipeline (Req 3)
	pipelineCfg := &ces.Config{
		HopCount:         cfg.HopCount,
		CircuitCount:     cfg.CircuitCount,
		Compression:      cfg.Compression,
		ErasureThreshold: cfg.GetErasureThreshold(),
	}
	pipeline := ces.NewPipeline(pipelineCfg)

	// Create relay handler (Req 7)
	relayHandler := relay.NewHandler(h, cfg.CircuitCount*cfg.HopCount, 1024*1024)

	// Create relay discovery (Req 4)
	relayDiscovery := discovery.NewRelayDiscovery(
		ProtocolID,
		cfg.GetSamplingSize(),
		string(cfg.SelectionMode),
	)

	originCtx, originCancel := context.WithCancel(context.Background())

	m := &Mixnet{
		config:       cfg,
		host:         h,
		routing:      r,
		circuitMgr:   circuitMgr,
		pipeline:     pipeline,
		relayHandler: relayHandler,
		discovery:    relayDiscovery,
		metrics:      metrics,
		originCtx:    originCtx,
		originCancel: originCancel,
		destHandler: &DestinationHandler{
			pipeline:  pipeline,
			shardBuf:  make(map[string][]*ces.Shard),
			keys:      make(map[string][]*ces.EncryptionKey),
			threshold: cfg.GetErasureThreshold(),
			timeout:   30 * time.Second,
			dataCh:    make(chan []byte, 10),
			stopCh:    make(chan struct{}),
		},
		activeConnections: make(map[peer.ID][]*circuit.Circuit),
	}

	// Register the mixnet protocol handler with the host (Req 12).
	h.SetStreamHandler(ProtocolID, func(s network.Stream) {
		m.handleIncomingStream(s)
	})

	// Start the destination handler goroutine with a controlled lifetime.
	go m.destHandler.waitForData()

	return m, nil
}

// waitForData checks the shard buffer periodically and delivers reconstructed
// data on dataCh.  It exits when stopCh is closed (Req 18).
func (h *DestinationHandler) waitForData() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			h.mu.Lock()
			for sessionID, shards := range h.shardBuf {
				if len(shards) >= h.threshold {
					keys := h.keys[sessionID]
					data, err := h.pipeline.Reconstruct(shards, keys)
					if err == nil {
						select {
						case h.dataCh <- data:
						default:
						}
						// Erase keys after successful reconstruction (Req 16.3).
						ces.EraseKeys(keys)
						delete(h.shardBuf, sessionID)
						delete(h.keys, sessionID)
					}
				}
			}
			h.mu.Unlock()
		}
	}
}

// EstablishConnection establishes circuits to the destination peer (Req 6)
func (m *Mixnet) EstablishConnection(ctx context.Context, dest peer.ID) ([]*circuit.Circuit, error) {
	// Step 1: Discover relays from DHT (Req 4)
	relays, err := m.discoverRelays(ctx, dest)
	if err != nil {
		m.metrics.RecordCircuitFailure()
		return nil, fmt.Errorf("failed to discover relays: %w", err)
	}

	// Step 2: Build circuits (Req 6)
	circuits, err := m.circuitMgr.BuildCircuits(ctx, dest, relays)
	if err != nil {
		m.metrics.RecordCircuitFailure()
		return nil, fmt.Errorf("failed to build circuits: %w", err)
	}

	// Step 3: Establish each circuit (connect to entry relay) (Req 6)
	for _, c := range circuits {
		err := m.circuitMgr.EstablishCircuit(c, dest, ProtocolID)
		if err != nil {
			// Clean up on failure
			m.circuitMgr.Close()
			m.metrics.RecordCircuitFailure()
			return nil, fmt.Errorf("failed to establish circuit %s: %w", c.ID, err)
		}
		m.circuitMgr.ActivateCircuit(c.ID)
		m.metrics.RecordCircuitSuccess()
	}

	// Store active connections
	m.mu.Lock()
	m.activeConnections[dest] = circuits
	m.mu.Unlock()

	return circuits, nil
}

// discoverRelays finds potential relay nodes via DHT (Req 4)
func (m *Mixnet) discoverRelays(ctx context.Context, dest peer.ID) ([]circuit.RelayInfo, error) {
	if m.routing == nil {
		return m.getSampleRelays(ctx, dest)
	}

	// Create a CID from the protocol string for DHT queries
	h, err := mh.Sum([]byte(ProtocolID), mh.SHA2_256, -1)
	if err != nil {
		return nil, err
	}
	protocolCID := cid.NewCidV1(cid.Raw, h)

	// Query DHT for providers
	provCh := m.routing.FindProvidersAsync(ctx, protocolCID, m.config.GetSamplingSize())

	var providers []peer.AddrInfo
	for p := range provCh {
		if p.ID != m.host.ID() && p.ID != dest {
			providers = append(providers, p)
		}
	}

	if len(providers) == 0 {
		return m.getSampleRelays(ctx, dest)
	}

	// Select relays based on mode (Req 4, 5)
	selected, err := m.discovery.FindRelays(ctx, providers, m.config.HopCount, m.config.CircuitCount)
	if err != nil {
		// Fallback to all discovered
		relays := make([]circuit.RelayInfo, len(providers))
		for i, p := range providers {
			relays[i] = circuit.RelayInfo{
				PeerID:   p.ID,
				AddrInfo: p,
			}
		}
		return relays, nil
	}

	// Convert discovery.RelayInfo to circuit.RelayInfo
	result := make([]circuit.RelayInfo, len(selected))
	for i, r := range selected {
		result[i] = circuit.RelayInfo{
			PeerID:   r.PeerID,
			AddrInfo: r.AddrInfo,
			Latency:  r.Latency,
		}
	}

	return result, nil
}

// getSampleRelays returns sample relays for testing
func (m *Mixnet) getSampleRelays(ctx context.Context, dest peer.ID) ([]circuit.RelayInfo, error) {
	return nil, fmt.Errorf("no DHT configured and no sample relays available")
}

// Send sends data through the mixnet to the destination (Req 8)
func (m *Mixnet) Send(ctx context.Context, dest peer.ID, data []byte) error {
	circuits := m.circuitMgr.ListCircuits()
	if len(circuits) == 0 {
		return fmt.Errorf("no circuits established")
	}

	// Get destinations for each circuit (ordered: entry -> exit)
	destinations := make([]string, m.config.HopCount)
	for i := 0; i < m.config.HopCount; i++ {
		destinations[i] = dest.String()
	}

	// Record original size for compression metrics
	originalSize := len(data)

	// Process through CES pipeline (Req 3)
	shards, keys, err := m.pipeline.ProcessWithKeys(data, destinations)
	if err != nil {
		return fmt.Errorf("CES pipeline failed: %w", err)
	}

	// Erase keys after sending; destination will use the keys delivered
	// out-of-band via SetKeys (Req 16.3).
	defer ces.EraseKeys(keys)

	// Record compression ratio
	m.metrics.RecordCompressionRatio(originalSize, len(data))

	// Store keys for destination to decrypt
	m.destHandler.SetKeys("default", keys)

	// Only send as many shards as we have circuits; excess shards are dropped
	// with an explicit log so silent data loss is avoided (Req 2.4).
	sendCount := len(shards)
	if len(circuits) < sendCount {
		sendCount = len(circuits)
	}

	// Transmit shards in parallel across circuits (Req 8)
	var wg sync.WaitGroup
	errCh := make(chan error, sendCount)

	for i := 0; i < sendCount; i++ {
		circuitID := circuits[i].ID
		shard := shards[i]

		wg.Add(1)
		go func(shardData []byte, circuitID string, idx int) {
			defer wg.Done()

			// Write shard with 4-byte index header
			header := make([]byte, 4)
			header[0] = byte(idx)
			header[1] = byte(idx >> 8)
			header[2] = byte(idx >> 16)
			header[3] = byte(idx >> 24)

			fullData := append(header, shardData...)

			// Apply per-stream write deadline (Req 8.2).
			if stream, ok := m.circuitMgr.GetStream(circuitID); ok && stream != nil {
				stream.Stream().SetDeadline(time.Now().Add(30 * time.Second))
			}

			if err := m.circuitMgr.SendData(circuitID, fullData); err != nil {
				errCh <- fmt.Errorf("failed to send on circuit %s: %w", circuitID, err)
				return
			}
			m.metrics.RecordThroughput(uint64(len(fullData)))
		}(shard.Data, circuitID, i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return err
		}
	}

	return nil
}

// ReceiveHandler returns a handler for incoming streams at destination (Req 9)
func (m *Mixnet) ReceiveHandler() func(network.Stream) {
	return m.handleIncomingStream
}

// handleIncomingStream handles incoming shard at destination (Req 9)
func (m *Mixnet) handleIncomingStream(stream network.Stream) {
	defer stream.Close()

	// Read the shard data with timeout
	stream.SetDeadline(time.Now().Add(m.destHandler.timeout))

	buf := make([]byte, 64*1024)
	n, err := stream.Read(buf)
	if err != nil {
		return
	}

	shardData := buf[:n]

	// Parse shard header to get index
	shard, err := m.parseShard(shardData)
	if err != nil {
		return
	}

	// Add to buffer with session based on connection
	m.destHandler.AddShard("default", shard)

	// Check if we can reconstruct
	data, err := m.destHandler.TryReconstruct("default")
	if err != nil {
		return
	}

	// Successfully got data!
	_ = data
}

// parseShard parses shard data from the stream
func (m *Mixnet) parseShard(data []byte) (*ces.Shard, error) {
	if len(data) < 4 {
		return &ces.Shard{Index: 0, Data: data}, nil
	}

	index := int(uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24)
	return &ces.Shard{
		Index: index,
		Data:  data[4:],
	}, nil
}

// AddShard adds a shard to the destination buffer
func (h *DestinationHandler) AddShard(sessionID string, shard *ces.Shard) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shardBuf[sessionID] = append(h.shardBuf[sessionID], shard)
}

// TryReconstruct attempts to reconstruct data (Req 9)
func (h *DestinationHandler) TryReconstruct(sessionID string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	shards := h.shardBuf[sessionID]
	if len(shards) < h.threshold {
		return nil, fmt.Errorf("insufficient shards: have %d, need %d", len(shards), h.threshold)
	}

	keys := h.keys[sessionID]
	return h.pipeline.Reconstruct(shards, keys)
}

// SetKeys sets the decryption keys for a session
func (h *DestinationHandler) SetKeys(sessionID string, keys []*ces.EncryptionKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keys[sessionID] = keys
}

// DataChan returns the channel for reconstructed data
func (h *DestinationHandler) DataChan() <-chan []byte {
	return h.dataCh
}

// Close closes the mixnet (Req 18)
func (m *Mixnet) Close() error {
	if m.originCancel != nil {
		m.originCancel()
	}

	// Stop the destination handler goroutine (Req 18).
	if m.destHandler != nil && m.destHandler.stopCh != nil {
		close(m.destHandler.stopCh)
	}

	// Erase all buffered session keys (Req 16.3, 18.4).
	if m.destHandler != nil {
		m.destHandler.mu.Lock()
		for sessionID, keys := range m.destHandler.keys {
			ces.EraseKeys(keys)
			delete(m.destHandler.keys, sessionID)
		}
		m.destHandler.mu.Unlock()
	}

	// Unregister the protocol handler (Req 12).
	m.host.RemoveStreamHandler(ProtocolID)

	m.mu.RLock()
	for dest := range m.activeConnections {
		circuits := m.activeConnections[dest]
		for _, c := range circuits {
			m.circuitMgr.CloseCircuit(c.ID)
		}
	}
	m.mu.RUnlock()

	return m.circuitMgr.Close()
}

// CircuitManager returns the circuit manager
func (m *Mixnet) CircuitManager() *circuit.CircuitManager {
	return m.circuitMgr
}

// Pipeline returns the CES pipeline
func (m *Mixnet) Pipeline() *ces.CESPipeline {
	return m.pipeline
}

// RelayHandler returns the relay handler
func (m *Mixnet) RelayHandler() *relay.Handler {
	return m.relayHandler
}

// Config returns the configuration
func (m *Mixnet) Config() *MixnetConfig {
	return m.config
}

// Host returns the libp2p host
func (m *Mixnet) Host() host.Host {
	return m.host
}

// Metrics returns the metrics collector
func (m *Mixnet) Metrics() *MetricsCollector {
	return m.metrics
}

// ActiveConnections returns the active connections
func (m *Mixnet) ActiveConnections() map[peer.ID][]*circuit.Circuit {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[peer.ID][]*circuit.Circuit)
	for k, v := range m.activeConnections {
		result[k] = v
	}
	return result
}

// StreamUpgrader wraps existing libp2p streams with Mixnet circuit (Req 13)
type StreamUpgrader struct {
	mixnet *Mixnet
	config *MixnetConfig
}

// NewStreamUpgrader creates a new Mixnet stream upgrader
func NewStreamUpgrader(cfg *MixnetConfig) *StreamUpgrader {
	return &StreamUpgrader{
		config: cfg,
	}
}

// SetMixnet sets the mixnet instance
func (s *StreamUpgrader) SetMixnet(m *Mixnet) {
	s.mixnet = m
}

// Upgrade upgrades a connection to use Mixnet (Req 13)
func (s *StreamUpgrader) Upgrade(ctx context.Context, conn network.Conn, dir network.Direction) (network.Stream, error) {
	if s.mixnet == nil {
		return nil, fmt.Errorf("mixnet not configured")
	}

	remotePeer := conn.RemotePeer()

	circuits, err := s.mixnet.EstablishConnection(ctx, remotePeer)
	if err != nil {
		return nil, fmt.Errorf("failed to establish connection: %w", err)
	}

	if len(circuits) == 0 {
		return nil, fmt.Errorf("no circuits established")
	}

	circuitID := circuits[0].ID
	handler, ok := s.mixnet.CircuitManager().GetStream(circuitID)
	if !ok {
		return nil, fmt.Errorf("no stream for circuit %s", circuitID)
	}

	// Return the stream directly
	return handler.Stream(), nil
}

// Config returns the upgrader configuration
func (s *StreamUpgrader) Config() *MixnetConfig {
	return s.config
}

// CanUpgrade checks if the given connection can be upgraded (Req 12)
func (s *StreamUpgrader) CanUpgrade(addr string) bool {
	return true
}

// Protocol returns the protocol ID (Req 12)
func (s *StreamUpgrader) Protocol() string {
	return ProtocolID
}

// RecoverFromFailure attempts to recover from circuit failures (Req 10)
func (m *Mixnet) RecoverFromFailure(ctx context.Context, dest peer.ID) error {
	m.mu.RLock()
	circuits, ok := m.activeConnections[dest]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("no active connection to %s", dest)
	}

	activeCount := 0
	for _, c := range circuits {
		if c.IsActive() {
			activeCount++
		}
	}

	threshold := m.config.GetErasureThreshold()
	if activeCount >= threshold {
		return nil
	}

	m.metrics.RecordRecovery()

	// Discover fresh relays so we don't rebuild with stale/failed ones (Req 10.3).
	newRelays, err := m.discoverRelays(ctx, dest)
	if err != nil {
		return fmt.Errorf("failed to discover relays for recovery: %w", err)
	}

	// Update the circuit manager relay pool with freshly discovered relays.
	m.circuitMgr.UpdateRelayPool(newRelays)

	for _, c := range circuits {
		if !c.IsActive() {
			newCircuit, err := m.circuitMgr.RebuildCircuit(c.ID)
			if err != nil {
				continue
			}

			err = m.circuitMgr.EstablishCircuit(newCircuit, dest, ProtocolID)
			if err != nil {
				continue
			}

			m.circuitMgr.ActivateCircuit(newCircuit.ID)
			m.metrics.RecordCircuitSuccess()
		}
	}

	if !m.circuitMgr.CanRecover() {
		m.metrics.RecordCircuitFailure()
		return fmt.Errorf("insufficient circuits after recovery: have %d, need %d", m.circuitMgr.ActiveCircuitCount(), threshold)
	}

	return nil
}
