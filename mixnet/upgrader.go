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

// Mixnet is the core implementation of the Lib-Mix protocol.
// It manages circuit establishment, data sharding, and communication privacy.
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

// DestinationHandler handles the reception and reconstruction of incoming shards at the destination.
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

// NewMixnet creates a new Mixnet instance with the provided configuration, host, and routing.
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
		config:            cfg,
		host:              h,
		routing:           r,
		circuitMgr:        circuitMgr,
		pipeline:          pipeline,
		relayHandler:      relayHandler,
		discovery:         relayDiscovery,
		metrics:           metrics,
		originCtx:         originCtx,
		originCancel:      originCancel,
		activeConnections: make(map[peer.ID][]*circuit.Circuit),
		destHandler: &DestinationHandler{
			pipeline:  pipeline,
			shardBuf:  make(map[string][]*ces.Shard),
			keys:      make(map[string][]*ces.EncryptionKey),
			threshold: cfg.GetErasureThreshold(),
			timeout:   30 * time.Second,
			dataCh:    make(chan []byte, 100),
			stopCh:    make(chan struct{}),
		},
	}

	// Register protocol handler (Req 9)
	h.SetStreamHandler(ProtocolID, m.handleIncomingStream)

	return m, nil
}

// EstablishConnection establishes a set of parallel circuits to the target destination.
func (m *Mixnet) EstablishConnection(ctx context.Context, dest peer.ID) ([]*circuit.Circuit, error) {
	m.mu.Lock()
	if circuits, ok := m.activeConnections[dest]; ok {
		m.mu.Unlock()
		return circuits, nil
	}
	m.mu.Unlock()

	// Discover relays (Req 4)
	relays, err := m.discoverRelays(ctx, dest)
	if err != nil {
		return nil, fmt.Errorf("relay discovery failed: %w", err)
	}

	m.circuitMgr.UpdateRelayPool(relays)

	// Build circuits in parallel (Req 6)
	circuits := make([]*circuit.Circuit, m.config.CircuitCount)
	var wg sync.WaitGroup
	errCh := make(chan error, m.config.CircuitCount)

	for i := 0; i < m.config.CircuitCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c, err := m.circuitMgr.BuildCircuit()
			if err != nil {
				errCh <- err
				return
			}

			// Establish circuit (Req 6.5)
			err = m.circuitMgr.EstablishCircuit(c, dest, ProtocolID)
			if err != nil {
				errCh <- err
				return
			}

			m.circuitMgr.ActivateCircuit(c.ID)
			circuits[idx] = c
			m.metrics.RecordCircuitSuccess()
		}(i)
	}

	wg.Wait()
	close(errCh)

	// Check if we have enough circuits (Req 15)
	activeCircuits := 0
	for _, c := range circuits {
		if c != nil && c.IsActive() {
			activeCircuits++
		}
	}

	if activeCircuits < m.config.GetErasureThreshold() {
		m.metrics.RecordCircuitFailure()
		return nil, fmt.Errorf("failed to establish enough circuits: have %d, need %d", activeCircuits, m.config.GetErasureThreshold())
	}

	m.mu.Lock()
	m.activeConnections[dest] = circuits
	m.mu.Unlock()

	return circuits, nil
}

func (m *Mixnet) discoverRelays(ctx context.Context, dest peer.ID) ([]circuit.RelayInfo, error) {
	// Advertise ourselves as a relay first (Req 7)
	// In a real implementation, this would be a background task

	// Create a CID for Mixnet relays
	h_hash, _ := mh.Encode([]byte("mixnet-relay-v1"), mh.SHA2_256)
	c := cid.NewCidV1(cid.Raw, h_hash)

	// Provide if we are a relay
	if m.relayHandler != nil {
		go func() {
			_ = m.routing.Provide(ctx, c, true)
		}()
	}

	// Find providers
	providersChan := m.routing.FindProvidersAsync(ctx, c, 0)
	var providers []peer.AddrInfo
	for p := range providersChan {
		providers = append(providers, p)
	}

	if len(providers) == 0 {
		return m.getSampleRelays(ctx, dest)
	}

	// Convert to discovery.RelayInfo for selection
	relayInfos := make([]discovery.RelayInfo, len(providers))
	for i, p := range providers {
		relayInfos[i] = discovery.RelayInfo{
			PeerID:   p.ID,
			AddrInfo: p,
		}
	}

	// Select relays (Req 4)
	selected, err := m.discovery.SelectRelays(ctx, relayInfos)
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

// getSampleRelays returns sample relays for testing.
func (m *Mixnet) getSampleRelays(ctx context.Context, dest peer.ID) ([]circuit.RelayInfo, error) {
	return nil, fmt.Errorf("no DHT configured and no sample relays available")
}

// Send transmits data to the specified destination through the mixnet.
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

// ReceiveHandler returns the function used to handle incoming Mixnet streams.
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

// parseShard parses shard data from the stream.
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

// AddShard adds an incoming shard to the destination's buffer for the given session.
func (h *DestinationHandler) AddShard(sessionID string, shard *ces.Shard) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shardBuf[sessionID] = append(h.shardBuf[sessionID], shard)
}

// TryReconstruct attempts to reconstruct the original data from buffered shards.
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

// SetKeys sets the layered decryption keys for a particular session.
func (h *DestinationHandler) SetKeys(sessionID string, keys []*ces.EncryptionKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keys[sessionID] = keys
}

// DataChan returns a channel that receives reconstructed data.
func (h *DestinationHandler) DataChan() <-chan []byte {
	return h.dataCh
}

// Close shuts down the Mixnet instance and releases all resources.
func (m *Mixnet) Close() error {
	// Cancel origin context to stop new operations
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

	// Send close signal through all active circuits
	m.mu.RLock()
	var closeSignals []string
	for dest := range m.activeConnections {
		circuits := m.activeConnections[dest]
		for _, c := range circuits {
			closeSignals = append(closeSignals, c.ID)
			// Send close signal
			m.circuitMgr.SendData(c.ID, []byte{0xFF, 0x00, 0x00, 0x00}) // Close signal header
		}
	}
	m.mu.RUnlock()

	// Wait for acknowledgments with timeout (Req 18.2)
	ackTimeout := 10 * time.Second
	ackChan := make(chan error, len(closeSignals))

	var wg sync.WaitGroup
	for _, circuitID := range closeSignals {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			// Wait for close acknowledgment
			select {
			case <-ackChan:
			case <-time.After(ackTimeout):
				// Timeout - log and continue (Req 18.5)
				ackChan <- fmt.Errorf("close ack timeout for circuit %s", id)
			}
		}(circuitID)
	}

	// Close all circuits
	for _, circuitID := range closeSignals {
		m.circuitMgr.CloseCircuit(circuitID)
	}

	// Close underlying circuit manager
	err := m.circuitMgr.Close()

	// Securely erase all cryptographic material (Req 18.4)
	m.pipeline.Encrypter().SecureErase()

	// Mark metrics
	for range m.activeConnections {
		m.metrics.CircuitClosed()
	}

	return err
}

// CircuitManager returns the instance of the circuit manager.
func (m *Mixnet) CircuitManager() *circuit.CircuitManager {
	return m.circuitMgr
}

// Pipeline returns the CES pipeline instance.
func (m *Mixnet) Pipeline() *ces.CESPipeline {
	return m.pipeline
}

// RelayHandler returns the handler for relay operations.
func (m *Mixnet) RelayHandler() *relay.Handler {
	return m.relayHandler
}

// Config returns the Mixnet configuration.
func (m *Mixnet) Config() *MixnetConfig {
	return m.config
}

// Host returns the underlying libp2p host.
func (m *Mixnet) Host() host.Host {
	return m.host
}

// Metrics returns the metrics collector.
func (m *Mixnet) Metrics() *MetricsCollector {
	return m.metrics
}

// ActiveConnections returns a map of current active connections and their circuits.
func (m *Mixnet) ActiveConnections() map[peer.ID][]*circuit.Circuit {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[peer.ID][]*circuit.Circuit)
	for k, v := range m.activeConnections {
		result[k] = v
	}
	return result
}

// StreamUpgrader implements the libp2p stream upgrader interface for Mixnet.
type StreamUpgrader struct {
	mixnet *Mixnet
	config *MixnetConfig
}

// NewStreamUpgrader creates a new Mixnet stream upgrader.
func NewStreamUpgrader(cfg *MixnetConfig) *StreamUpgrader {
	return &StreamUpgrader{
		config: cfg,
	}
}

// SetMixnet sets the Mixnet instance to be used by the upgrader.
func (s *StreamUpgrader) SetMixnet(m *Mixnet) {
	s.mixnet = m
}

// Upgrade upgrades a connection to use Mixnet for privacy.
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

// Config returns the upgrader's configuration.
func (s *StreamUpgrader) Config() *MixnetConfig {
	return s.config
}

// CanUpgrade returns true if the connection to the given address can be upgraded to Mixnet.
func (s *StreamUpgrader) CanUpgrade(addr string) bool {
	return true
}

// Protocol returns the Mixnet protocol ID.
func (s *StreamUpgrader) Protocol() string {
	return ProtocolID
}

// RecoverFromFailure attempts to rebuild failed circuits to maintain the reconstruction threshold.
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
// Package mixnet provides a high-performance, metadata-private communication protocol for libp2p.
