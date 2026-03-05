package mixnet

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
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
	config          *MixnetConfig
	host            host.Host
	routing         routing.Routing
	circuitMgr      *circuit.CircuitManager
	pipeline        *ces.CESPipeline
	relayHandler    *relay.Handler
	discovery       *discovery.RelayDiscovery
	metrics         *MetricsCollector
	metricsExporter *MetricsExporter
	resourceMgr     *ResourceManager
	circuitKeys     map[string][][]byte
	failureNotifier *CircuitFailureNotifier
	heartbeatStart  map[string]struct{}

	// For origin mode
	originCtx    context.Context
	originCancel context.CancelFunc

	// For destination mode
	destHandler *DestinationHandler

	// Established circuits to destinations
	activeConnections map[peer.ID][]*circuit.Circuit
	pendingShards     map[peer.ID]*PendingTransmission

	mu sync.RWMutex
}

// PendingTransmission tracks shards that need re-scheduling after circuit recovery.
type PendingTransmission struct {
	SessionID string
	KeyData   []byte
	Shards    []*ces.Shard
	CreatedAt time.Time
}

// DestinationHandler handles the reception and reconstruction of incoming shards at the destination.
type DestinationHandler struct {
	pipeline    *ces.CESPipeline
	shardBuf    map[string][]*ces.Shard
	totalShards map[string]int
	timers      map[string]*time.Timer
	sessions    map[string]chan []byte
	keys        map[string]sessionKey
	inboundCh   chan string
	threshold   int
	timeout     time.Duration
	dataCh      chan []byte
	stopCh      chan struct{}
	mu          sync.Mutex
}

const (
	msgTypeData     byte = 0x00
	msgTypeCloseReq byte = 0x01
	msgTypeCloseAck byte = 0x02
)

// NewMixnet creates a new Mixnet instance with the provided configuration, host, and routing.
func NewMixnet(cfg *MixnetConfig, h host.Host, r routing.Routing) (*Mixnet, error) {
	if err := cfg.Validate(); err != nil {
		return nil, ErrConfigInvalid("invalid config").WithCause(err)
	}

	cfg.InitDefaults()

	// Create metrics collector (Req 17)
	metrics := NewMetricsCollector()
	metricsExporter := NewMetricsExporter(metrics)

	// Create resource manager (Req 20).
	resourceMgr := NewLibp2pResourceManager(h, nil)

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

	// CRITICAL FIX: Register relay handler's stream handler for actual relay forwarding
	// This fixes the issue where HandleStream() was never called
	h.SetStreamHandler(relay.ProtocolID, relayHandler.HandleStream)

	// Create relay discovery (Req 4)
	relayDiscovery := discovery.NewRelayDiscoveryWithHost(
		h,
		ProtocolID,
		cfg.GetSamplingSize(),
		string(cfg.SelectionMode),
		cfg.RandomnessFactor,
	)

	originCtx, originCancel := context.WithCancel(context.Background())
	resourceMgr.StartCleanup(originCtx)

	m := &Mixnet{
		config:            cfg,
		host:              h,
		routing:           r,
		circuitMgr:        circuitMgr,
		pipeline:          pipeline,
		relayHandler:      relayHandler,
		discovery:         relayDiscovery,
		metrics:           metrics,
		metricsExporter:   metricsExporter,
		resourceMgr:       resourceMgr,
		circuitKeys:       make(map[string][][]byte),
		heartbeatStart:    make(map[string]struct{}),
		originCtx:         originCtx,
		originCancel:      originCancel,
		activeConnections: make(map[peer.ID][]*circuit.Circuit),
		pendingShards:     make(map[peer.ID]*PendingTransmission),
		destHandler: &DestinationHandler{
			pipeline:    pipeline,
			shardBuf:    make(map[string][]*ces.Shard),
			totalShards: make(map[string]int),
			timers:      make(map[string]*time.Timer),
			sessions:    make(map[string]chan []byte),
			keys:        make(map[string]sessionKey),
			inboundCh:   make(chan string, 100),
			threshold:   cfg.GetErasureThreshold(),
			timeout:     30 * time.Second,
			dataCh:      make(chan []byte, 100),
			stopCh:      make(chan struct{}),
		},
	}

	// Wire relay resource/backpressure hooks (Req 20.4, 20.5).
	relayHandler.SetBandwidthBackpressure(func(ctx context.Context, bytes int64) error {
		return resourceMgr.WaitForBandwidth(ctx, bytes)
	})
	relayHandler.SetBandwidthRecorder(func(direction string, bytes int64) {
		resourceMgr.RecordBandwidth(bytes, direction)
		m.metrics.RecordRelayResourceUsage(resourceMgr.ActiveCircuitCount(), resourceMgr.BandwidthPerSec())
		m.metrics.RecordResourceUtilization(resourceMgr.UtilizationPercent())
	})
	relayHandler.SetUtilizationReporter(func(activeCircuits int) {
		resourceMgr.SetActiveCircuitCount(activeCircuits)
		m.metrics.RecordRelayResourceUsage(activeCircuits, resourceMgr.BandwidthPerSec())
		m.metrics.RecordResourceUtilization(resourceMgr.UtilizationPercent())
	})

	// Register protocol handler (Req 9)
	h.SetStreamHandler(ProtocolID, m.handleIncomingStream)
	h.SetStreamHandler(KeyExchangeProtocolID, relayHandler.HandleKeyExchange)

	// Start active failure detection (Req 10.1-10.4).
	m.failureNotifier = NewCircuitFailureNotifier(m, h)
	if err := m.failureNotifier.Start(originCtx); err != nil {
		return nil, ErrCircuitFailed("failed to start failure notifier").WithCause(err)
	}

	if addr := os.Getenv("LIBP2P_MIXNET_METRICS_ADDR"); addr != "" {
		go func() {
			if err := m.StartMetricsEndpoint(addr); err != nil {
				log.Printf("[mixnet] metrics endpoint failed: %v", err)
			}
		}()
	}

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

	// Req 11/16: use transport capability detection if address data is available.
	if info, err := DetectTransportCapabilities(m.host, dest); err == nil && len(info.Multiaddrs) > 0 {
		if !SupportsStandardTransport(info) {
			return nil, ErrTransportFailed(fmt.Sprintf("destination %s does not advertise tcp/quic/webrtc transport", dest))
		}
	}

	// Req 12.3: reject destination peers that do not advertise the mixnet ProtocolID.
	supported, err := VerifyProtocolSupport(m.host, dest, protocol.ID(ProtocolID))
	if err != nil || !supported {
		// Peerstore protocol lists can be stale/empty until we connect and identify.
		pi := m.host.Peerstore().PeerInfo(dest)
		if len(pi.Addrs) > 0 {
			connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_ = m.host.Connect(connectCtx, peer.AddrInfo{ID: dest, Addrs: pi.Addrs})
			cancel()
		}
		supported, err = VerifyProtocolSupport(m.host, dest, protocol.ID(ProtocolID))
		if err != nil {
			return nil, ErrProtocolError(fmt.Sprintf("failed to verify destination protocol support for %s", dest)).WithCause(err)
		}
		if !supported {
			return nil, ErrProtocolError(fmt.Sprintf("destination %s does not advertise protocol %s", dest, ProtocolID))
		}
	}

	// Discover relays (Req 4)
	relays, err := m.discoverRelays(ctx, dest)
	if err != nil {
		return nil, ErrDiscoveryFailed("relay discovery failed").WithCause(err)
	}

	// Build circuits with unique relay sets (Req 6.2).
	built, err := m.circuitMgr.BuildCircuits(ctx, dest, relays)
	if err != nil {
		return nil, ErrCircuitFailed("failed to build circuits").WithCause(err)
	}
	if len(built) != m.config.CircuitCount {
		return nil, ErrCircuitFailed(fmt.Sprintf("failed to build required circuits: have %d, need %d", len(built), m.config.CircuitCount))
	}

	// Establish circuits in parallel (Req 6.3-6.5).
	circuits := make([]*circuit.Circuit, len(built))
	copy(circuits, built)
	var wg sync.WaitGroup
	errCh := make(chan error, len(circuits))

	for i := 0; i < len(circuits); i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Establish circuit to entry relay (Req 6.3-6.5).
			err := m.circuitMgr.EstablishCircuit(circuits[idx], dest, relay.ProtocolID)
			if err != nil {
				errCh <- ErrCircuitFailed("failed to establish circuit").WithCause(err)
				return
			}

			m.circuitMgr.ActivateCircuit(circuits[idx].ID)
			m.metrics.RecordCircuitSuccess()
		}(i)
	}

	wg.Wait()
	close(errCh)

	var establishErr error
	for err := range errCh {
		if err != nil {
			establishErr = err
			break
		}
	}

	// Req 6.6: if any circuit establishment fails, tear down all circuits.
	if establishErr != nil {
		for _, c := range circuits {
			if c == nil {
				continue
			}
			_ = m.circuitMgr.CloseCircuit(c.ID)
		}
		m.metrics.RecordCircuitFailure()
		return nil, ErrCircuitFailed("failed to establish circuits").WithCause(establishErr)
	}

	// Check if we have enough circuits (Req 15)
	activeCircuits := 0
	for _, c := range circuits {
		if c != nil && c.IsActive() {
			activeCircuits++
		}
	}

	if activeCircuits < m.config.GetErasureThreshold() {
		for _, c := range circuits {
			if c == nil {
				continue
			}
			_ = m.circuitMgr.CloseCircuit(c.ID)
		}
		m.metrics.RecordCircuitFailure()
		return nil, ErrCircuitFailed(fmt.Sprintf("failed to establish enough circuits: have %d, need %d", activeCircuits, m.config.GetErasureThreshold()))
	}

	m.mu.Lock()
	m.activeConnections[dest] = circuits
	m.mu.Unlock()

	// Prevent config mutation while circuits are active (Req 15.5).
	m.config.Lock()
	m.StartHeartbeatMonitoring(defaultHeartbeatInterval)

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

	// CRITICAL FIX (Req 12): Verify protocol support using Peerstore
	// After getting providers, verify each peer actually advertises /lib-mix/1.0.0
	var validRelays []peer.AddrInfo
	for _, p := range providers {
		supported, err := m.host.Peerstore().SupportsProtocols(p.ID, protocol.ID(ProtocolID))
		if err == nil && len(supported) > 0 {
			validRelays = append(validRelays, p)
		}
	}

	// Use only verified relays
	providers = validRelays

	if len(providers) == 0 {
		return m.getSampleRelays(ctx, dest)
	}

	// AC 4.4: filter out origin and destination peers
	providers = discovery.FilterByExclusion(providers, dest, m.host.ID())

	// AC 4.2: DHT pool must be at least 3x required relay count
	required := m.config.HopCount * m.config.CircuitCount
	if len(providers) < required*3 {
		return nil, ErrDiscoveryFailed(fmt.Sprintf("insufficient relay pool: have %d, need %d", len(providers), required*3))
	}

	// Select relays using configured mode and RTT measurements (Req 4, 5)
	selected, err := m.discovery.FindRelays(ctx, providers, m.config.HopCount, m.config.CircuitCount)
	if err != nil {
		return nil, ErrDiscoveryFailed("relay selection failed").WithCause(err)
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
	return nil, ErrDiscoveryFailed("no DHT configured and no sample relays available")
}

// Send transmits data to the specified destination through the mixnet.
func (m *Mixnet) Send(ctx context.Context, dest peer.ID, data []byte) error {
	sessionID := fmt.Sprintf("%s-%d", dest.String(), time.Now().UnixNano())
	return m.SendWithSession(ctx, dest, data, sessionID)
}

// SendWithSession sends data using a caller-provided session ID (used by MixnetStream).
func (m *Mixnet) SendWithSession(ctx context.Context, dest peer.ID, data []byte, sessionID string) error {
	m.mu.RLock()
	circuits := m.activeConnections[dest]
	m.mu.RUnlock()
	if len(circuits) == 0 {
		var err error
		circuits, err = m.EstablishConnection(ctx, dest)
		if err != nil {
			return err
		}
	}

	// Record original size for compression metrics
	originalSize := len(data)

	// Process through CES pipeline: compress first.
	compressed, err := m.pipeline.Compressor().Compress(data)
	if err != nil {
		return ErrCompressionFailed("compression failed").WithCause(err)
	}

	// Encrypt before sharding (Req 3.3).
	encryptedPayload, keyData, err := encryptSessionPayload(compressed)
	if err != nil {
		return ErrEncryptionFailed("session encryption failed").WithCause(err)
	}

	shards, err := m.pipeline.Sharder().Shard(encryptedPayload)
	if err != nil {
		return ErrShardingFailed("sharding failed").WithCause(err)
	}

	// Record compression ratio
	m.metrics.RecordCompressionRatio(originalSize, len(data))

	sessionIDBytes := []byte(sessionID)

	// Ensure hop keys are established for all circuits.
	if err := m.ensureCircuitKeys(ctx, circuits); err != nil {
		return ErrEncryptionFailed("failed to establish hop keys").WithCause(err)
	}

	// Enforce 1:1 shard-to-circuit mapping (Req 2.4, 8.1).
	if len(shards) != len(circuits) {
		return ErrShardingFailed(fmt.Sprintf("shard count mismatch: have %d shards, %d circuits", len(shards), len(circuits)))
	}
	m.setPendingTransmission(dest, sessionID, keyData, shards)
	if err := m.sendShardsAcrossCircuits(ctx, dest, sessionIDBytes, keyData, shards, circuits); err != nil {
		return err
	}
	m.clearPendingTransmission(dest, sessionID)
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

	if len(shardData) < 1 {
		return
	}
	switch shardData[0] {
	case msgTypeCloseReq:
		_, _ = stream.Write([]byte{msgTypeCloseAck})
		return
	case msgTypeData:
		// continue
	default:
		return
	}

	// Parse data payload (msgType already stripped by relay).
	sessionID, shard, keyData, totalShards, err := m.parseShardPayload(shardData[1:])
	if err != nil {
		return
	}

	// Ensure session registration for inbound consumers.
	m.destHandler.ensureSession(sessionID)

	// Add to buffer with correct session ID (not hardcoded "default")
	m.destHandler.AddShard(sessionID, shard, keyData, totalShards)

	// Check if we can reconstruct using the correct session ID
	data, err := m.destHandler.TryReconstruct(sessionID)
	if err != nil {
		return
	}

	// Successfully got data
	m.destHandler.deliverSessionData(sessionID, data)
	select {
	case m.destHandler.dataCh <- data:
	default:
	}
}

// parseShardPayload parses shard data including session ID.
func (m *Mixnet) parseShardPayload(data []byte) (string, *ces.Shard, []byte, int, error) {
	header, payload, err := DecodePrivacyShard(data)
	if err != nil {
		return "", nil, nil, 0, err
	}
	sessionID := string(header.SessionID)
	idx := int(header.ShardIndex)
	return sessionID, &ces.Shard{
		Index: idx,
		Data:  payload,
	}, header.KeyData, int(header.TotalShards), nil
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
func (h *DestinationHandler) AddShard(sessionID string, shard *ces.Shard, keyData []byte, totalShards int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if totalShards > 0 {
		h.totalShards[sessionID] = totalShards
	}
	if _, exists := h.timers[sessionID]; !exists {
		h.timers[sessionID] = time.AfterFunc(h.timeout, func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.shardBuf, sessionID)
			delete(h.totalShards, sessionID)
			if t, ok := h.timers[sessionID]; ok {
				t.Stop()
				delete(h.timers, sessionID)
			}
			delete(h.keys, sessionID)
			delete(h.sessions, sessionID)
		})
	}
	if len(keyData) > 0 {
		if key, err := decodeSessionKeyData(keyData); err == nil {
			h.keys[sessionID] = key
		}
	}
	h.shardBuf[sessionID] = append(h.shardBuf[sessionID], shard)
}

// TryReconstruct attempts to reconstruct the original data from buffered shards.
func (h *DestinationHandler) TryReconstruct(sessionID string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	shards := h.shardBuf[sessionID]
	total := h.totalShards[sessionID]
	if total <= 0 {
		total = h.pipeline.Sharder().TotalShards()
	}
	unique := dedupeShards(shards)
	missing := missingShardIDs(unique, total)

	if len(unique) < h.threshold {
		return nil, ErrReconstructionMissingShards(sessionID, len(unique), h.threshold, missing)
	}

	encrypted, err := h.pipeline.Sharder().Reconstruct(unique)
	if err != nil {
		return nil, ErrShardingFailed(fmt.Sprintf("reconstruction failed for session %s missing_shard_ids=%v", sessionID, missing)).WithCause(err)
	}
	key, ok := h.keys[sessionID]
	if !ok {
		missingKey := false
		for _, id := range missing {
			if id == 0 {
				missingKey = true
				break
			}
		}
		if missingKey {
			return nil, ErrReconstructionMissingShards(sessionID, len(unique), h.threshold, missing)
		}
		return nil, ErrEncryptionFailed(fmt.Sprintf("missing session key for %s", sessionID))
	}
	decrypted, err := decryptSessionPayload(encrypted, key)
	if err != nil {
		return nil, ErrEncryptionFailed("session decrypt failed").WithCause(err)
	}
	data, err := h.pipeline.Compressor().Decompress(decrypted)
	if err != nil {
		return nil, err
	}

	if t, ok := h.timers[sessionID]; ok {
		t.Stop()
		delete(h.timers, sessionID)
	}
	delete(h.shardBuf, sessionID)
	delete(h.keys, sessionID)
	delete(h.totalShards, sessionID)
	return data, nil
}

func (h *DestinationHandler) registerSession(sessionID string) chan []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.sessions[sessionID]; ok {
		return ch
	}
	ch := make(chan []byte, 10)
	h.sessions[sessionID] = ch
	return ch
}

func (h *DestinationHandler) ensureSession(sessionID string) {
	h.mu.Lock()
	if _, ok := h.sessions[sessionID]; !ok {
		ch := make(chan []byte, 10)
		h.sessions[sessionID] = ch
		select {
		case h.inboundCh <- sessionID:
		default:
		}
	}
	h.mu.Unlock()
}

func (h *DestinationHandler) unregisterSession(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.sessions[sessionID]; ok {
		close(ch)
		delete(h.sessions, sessionID)
	}
}

func (h *DestinationHandler) deliverSessionData(sessionID string, data []byte) {
	h.mu.Lock()
	ch, ok := h.sessions[sessionID]
	h.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- data:
	default:
	}
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

	if m.failureNotifier != nil {
		if err := m.failureNotifier.Stop(); err != nil {
			log.Printf("[mixnet] failure notifier stop error: %v", err)
		}
	}

	// Stop the destination handler goroutine (Req 18).
	if m.destHandler != nil && m.destHandler.stopCh != nil {
		close(m.destHandler.stopCh)
	}
	if m.resourceMgr != nil {
		m.resourceMgr.Stop()
	}

	// Clear buffered shards.
	if m.destHandler != nil {
		m.destHandler.mu.Lock()
		for sessionID := range m.destHandler.shardBuf {
			delete(m.destHandler.shardBuf, sessionID)
		}
		for sessionID := range m.destHandler.totalShards {
			delete(m.destHandler.totalShards, sessionID)
		}
		for sessionID, timer := range m.destHandler.timers {
			if timer != nil {
				timer.Stop()
			}
			delete(m.destHandler.timers, sessionID)
		}
		for sessionID := range m.destHandler.keys {
			delete(m.destHandler.keys, sessionID)
		}
		for sessionID, ch := range m.destHandler.sessions {
			close(ch)
			delete(m.destHandler.sessions, sessionID)
		}
		m.destHandler.mu.Unlock()
	}

	// Unregister the protocol handler (Req 12).
	m.host.RemoveStreamHandler(ProtocolID)

	// Send close signal through all active circuits and wait for acknowledgment (Req 18)
	m.mu.RLock()
	var closeSignals []string
	for dest := range m.activeConnections {
		circuits := m.activeConnections[dest]
		for _, c := range circuits {
			closeSignals = append(closeSignals, c.ID)
		}
	}
	m.mu.RUnlock()

	// Wait for acknowledgments with timeout (Req 18.2)
	// Use libp2p's stream close semantics properly - close each circuit and wait for completion
	ackTimeout := 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), ackTimeout)
	defer cancel()

	// Close all circuits and collect errors
	var closeErrors []error
	var wg sync.WaitGroup

	for _, circuitID := range closeSignals {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := m.sendCloseAndWait(ctx, id); err != nil {
				closeErrors = append(closeErrors, ErrProtocolError(fmt.Sprintf("close ack failed for circuit %s", id)).WithCause(err))
			}
			// Close the circuit and wait for completion
			if err := m.circuitMgr.CloseCircuitWithContext(ctx, id); err != nil {
				closeErrors = append(closeErrors, ErrCircuitFailed(fmt.Sprintf("failed to close circuit %s", id)).WithCause(err))
			}
		}(circuitID)
	}

	wg.Wait()

	// Check for context timeout (Req 18.5)
	if ctx.Err() == context.DeadlineExceeded {
		// Log timeout but don't fail - circuits will be cleaned up eventually
		fmt.Printf("Warning: close acknowledgment timeout after %v\n", ackTimeout)
	}

	// Close underlying circuit manager
	err := m.circuitMgr.Close()

	// Securely erase all cryptographic material (Req 18.4)
	m.pipeline.Encrypter().SecureErase()
	m.clearCircuitKeys()

	// Mark metrics
	for range m.activeConnections {
		m.metrics.CircuitClosed()
	}

	if err != nil {
		return ErrCircuitFailed("failed to close circuit manager").WithCause(err)
	}
	return nil
}

func (m *Mixnet) ensureCircuitKeys(ctx context.Context, circuits []*circuit.Circuit) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(circuits))

	for _, c := range circuits {
		c := c
		if c == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys, err := m.establishCircuitKeys(ctx, c)
			if err != nil {
				errCh <- err
				return
			}
			m.setCircuitKeys(m.circuitKeyID(c), keys)
		}()
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

func (m *Mixnet) establishCircuitKeys(ctx context.Context, c *circuit.Circuit) ([][]byte, error) {
	if c == nil || len(c.Peers) == 0 {
		return nil, fmt.Errorf("invalid circuit")
	}
	keyID := m.circuitKeyID(c)
	keys := make([][]byte, len(c.Peers))
	for i, p := range c.Peers {
		key, err := m.exchangeHopKey(ctx, p, keyID)
		if err != nil {
			return nil, fmt.Errorf("key exchange failed for %s: %w", p, err)
		}
		keys[i] = key
	}
	return keys, nil
}

func (m *Mixnet) setCircuitKeys(circuitID string, keys [][]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.circuitKeys[circuitID] = keys
}

func (m *Mixnet) getCircuitKeys(circuitID string) ([][]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys, ok := m.circuitKeys[circuitID]
	return keys, ok
}

func (m *Mixnet) clearCircuitKeys() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, keys := range m.circuitKeys {
		for i := range keys {
			ces.SecureEraseBytes(keys[i])
			keys[i] = nil
		}
		delete(m.circuitKeys, id)
	}
}

func (m *Mixnet) circuitKeyID(c *circuit.Circuit) string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s", m.host.ID(), c.ID)
}

func (m *Mixnet) sendCloseAndWait(ctx context.Context, circuitID string) error {
	c, ok := m.circuitMgr.GetCircuit(circuitID)
	if !ok {
		return fmt.Errorf("circuit not found: %s", circuitID)
	}
	dest, ok := m.destinationForCircuit(circuitID)
	if !ok {
		return fmt.Errorf("destination not found for circuit %s", circuitID)
	}
	stream, ok := m.circuitMgr.GetStream(circuitID)
	if !ok || stream == nil {
		return fmt.Errorf("stream not found for circuit %s", circuitID)
	}

	keyID := m.circuitKeyID(c)
	keys, ok := m.getCircuitKeys(keyID)
	if !ok {
		var err error
		keys, err = m.establishCircuitKeys(ctx, c)
		if err != nil {
			return fmt.Errorf("missing hop keys for circuit %s: %w", circuitID, err)
		}
		m.setCircuitKeys(keyID, keys)
	}
	encryptedPayload, err := encryptOnion([]byte{msgTypeCloseReq}, c, dest, keys)
	if err != nil {
		return err
	}
	fullData, err := encodeEncryptedFrame(keyID, encryptedPayload)
	if err != nil {
		return err
	}

	if err := m.circuitMgr.SendData(circuitID, fullData); err != nil {
		return err
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.Stream().SetReadDeadline(deadline)
	} else {
		_ = stream.Stream().SetReadDeadline(time.Now().Add(10 * time.Second))
	}

	buf := make([]byte, 1)
	if _, err := stream.Stream().Read(buf); err != nil {
		return err
	}
	if buf[0] != msgTypeCloseAck {
		return fmt.Errorf("unexpected close ack: %x", buf[0])
	}
	return nil
}

func (m *Mixnet) destinationForCircuit(circuitID string) (peer.ID, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for dest, circuits := range m.activeConnections {
		for _, c := range circuits {
			if c != nil && c.ID == circuitID {
				return dest, true
			}
		}
	}
	return "", false
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

// RecoverFromFailure attempts to rebuild failed circuits to maintain the reconstruction threshold.
func (m *Mixnet) RecoverFromFailure(ctx context.Context, dest peer.ID) error {
	m.mu.RLock()
	circuits, ok := m.activeConnections[dest]
	m.mu.RUnlock()

	if !ok {
		return ErrCircuitFailed(fmt.Sprintf("no active connection to %s", dest))
	}

	activeCount := 0
	for _, c := range circuits {
		if c.IsActive() {
			activeCount++
		}
	}

	threshold := m.config.GetErasureThreshold()
	m.metrics.RecordRecovery()

	// Discover fresh relays so we don't rebuild with stale/failed ones (Req 10.3).
	newRelays, err := m.discoverRelays(ctx, dest)
	if err != nil {
		return ErrDiscoveryFailed("failed to discover relays for recovery").WithCause(err)
	}

	// Update the circuit manager relay pool with freshly discovered relays.
	m.circuitMgr.UpdateRelayPool(newRelays)

	for i, c := range circuits {
		if c == nil {
			continue
		}
		if !c.IsActive() {
			newCircuit, err := m.circuitMgr.RebuildCircuit(c.ID)
			if err != nil {
				continue
			}

			err = m.circuitMgr.EstablishCircuit(newCircuit, dest, relay.ProtocolID)
			if err != nil {
				continue
			}

			m.circuitMgr.ActivateCircuit(newCircuit.ID)
			circuits[i] = newCircuit
			m.metrics.RecordCircuitSuccess()
		}
	}
	m.mu.Lock()
	m.activeConnections[dest] = circuits
	m.mu.Unlock()
	m.StartHeartbeatMonitoring(defaultHeartbeatInterval)

	if !m.circuitMgr.CanRecover() {
		m.metrics.RecordCircuitFailure()
		return ErrCircuitFailed(fmt.Sprintf("insufficient circuits after recovery: have %d, need %d", m.circuitMgr.ActiveCircuitCount(), threshold))
	}

	if err := m.reschedulePendingShards(ctx, dest); err != nil {
		return ErrCircuitFailed("failed to reschedule shards after recovery").WithCause(err)
	}

	return nil
}

func dedupeShards(shards []*ces.Shard) []*ces.Shard {
	seen := make(map[int]*ces.Shard)
	for _, sh := range shards {
		if sh == nil {
			continue
		}
		seen[sh.Index] = sh
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]*ces.Shard, 0, len(ids))
	for _, id := range ids {
		out = append(out, seen[id])
	}
	return out
}

func missingShardIDs(shards []*ces.Shard, total int) []int {
	if total <= 0 {
		return nil
	}
	present := make(map[int]struct{}, len(shards))
	for _, sh := range shards {
		if sh == nil {
			continue
		}
		present[sh.Index] = struct{}{}
	}
	missing := make([]int, 0)
	for i := 0; i < total; i++ {
		if _, ok := present[i]; !ok {
			missing = append(missing, i)
		}
	}
	return missing
}

func cloneShards(shards []*ces.Shard) []*ces.Shard {
	out := make([]*ces.Shard, 0, len(shards))
	for _, sh := range shards {
		if sh == nil {
			continue
		}
		cp := make([]byte, len(sh.Data))
		copy(cp, sh.Data)
		out = append(out, &ces.Shard{Index: sh.Index, Data: cp})
	}
	return out
}

func (m *Mixnet) setPendingTransmission(dest peer.ID, sessionID string, keyData []byte, shards []*ces.Shard) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keyCopy := make([]byte, len(keyData))
	copy(keyCopy, keyData)
	m.pendingShards[dest] = &PendingTransmission{
		SessionID: sessionID,
		KeyData:   keyCopy,
		Shards:    cloneShards(shards),
		CreatedAt: time.Now(),
	}
}

func (m *Mixnet) clearPendingTransmission(dest peer.ID, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pt, ok := m.pendingShards[dest]
	if !ok || pt.SessionID != sessionID {
		return
	}
	delete(m.pendingShards, dest)
}

func (m *Mixnet) pendingTransmission(dest peer.ID) (*PendingTransmission, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pt, ok := m.pendingShards[dest]
	if !ok {
		return nil, false
	}
	return &PendingTransmission{
		SessionID: pt.SessionID,
		KeyData:   append([]byte(nil), pt.KeyData...),
		Shards:    cloneShards(pt.Shards),
		CreatedAt: pt.CreatedAt,
	}, true
}

func (m *Mixnet) activeCircuitsForDest(dest peer.ID) []*circuit.Circuit {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := m.activeConnections[dest]
	active := make([]*circuit.Circuit, 0, len(all))
	for _, c := range all {
		if c != nil && c.IsActive() {
			active = append(active, c)
		}
	}
	return active
}

func (m *Mixnet) reschedulePendingShards(ctx context.Context, dest peer.ID) error {
	pt, ok := m.pendingTransmission(dest)
	if !ok {
		return nil
	}
	circuits := m.activeCircuitsForDest(dest)
	if len(circuits) == 0 {
		return ErrCircuitFailed("no active circuits available for shard rescheduling")
	}
	if len(pt.Shards) != len(circuits) {
		return ErrShardingFailed(fmt.Sprintf("cannot reschedule shards: shards=%d active_circuits=%d", len(pt.Shards), len(circuits)))
	}
	if err := m.sendShardsAcrossCircuits(ctx, dest, []byte(pt.SessionID), pt.KeyData, pt.Shards, circuits); err != nil {
		return err
	}
	m.clearPendingTransmission(dest, pt.SessionID)
	return nil
}

func (m *Mixnet) sendShardsAcrossCircuits(ctx context.Context, dest peer.ID, sessionIDBytes []byte, keyData []byte, shards []*ces.Shard, circuits []*circuit.Circuit) error {
	_ = ctx
	sendCount := len(shards)
	var wg sync.WaitGroup
	errCh := make(chan error, sendCount)

	for i := 0; i < sendCount; i++ {
		circuitID := circuits[i].ID
		shard := shards[i]

		wg.Add(1)
		go func(shardData []byte, circuitID string, idx int) {
			defer wg.Done()

			shardIndex := shard.Index
			if shardIndex < 0 {
				shardIndex = idx
			}
			includeKeys := len(keyData) > 0 && shardIndex == 0
			var keyPayload []byte
			if includeKeys {
				keyPayload = keyData
			}
			privacyShard, err := EncodePrivacyShard(shardData, PrivacyShardHeader{
				SessionID:   sessionIDBytes,
				ShardIndex:  uint32(shardIndex),
				TotalShards: uint32(sendCount),
				HasKeys:     includeKeys,
				KeyData:     keyPayload,
			})
			if err != nil {
				errCh <- ErrProtocolError("failed to encode privacy shard").WithCause(err)
				return
			}
			shardPayload := append([]byte{msgTypeData}, privacyShard...)

			keyID := m.circuitKeyID(circuits[idx])
			hopKeys, ok := m.getCircuitKeys(keyID)
			if !ok {
				errCh <- ErrEncryptionFailed(fmt.Sprintf("missing hop keys for circuit %s", circuitID))
				return
			}
			encryptedPayload, err := encryptOnion(shardPayload, circuits[idx], dest, hopKeys)
			if err != nil {
				errCh <- ErrEncryptionFailed(fmt.Sprintf("failed to encrypt shard for circuit %s", circuitID)).WithCause(err)
				return
			}
			fullData, err := encodeEncryptedFrame(keyID, encryptedPayload)
			if err != nil {
				errCh <- ErrProtocolError("failed to frame encrypted shard").WithCause(err)
				return
			}

			// Apply per-stream write deadline (Req 8.2).
			if stream, ok := m.circuitMgr.GetStream(circuitID); ok && stream != nil {
				stream.Stream().SetDeadline(time.Now().Add(30 * time.Second))
			}

			if err := m.circuitMgr.SendData(circuitID, fullData); err != nil {
				errCh <- ErrTransportFailed(fmt.Sprintf("failed to send on circuit %s", circuitID)).WithCause(err)
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

// Package mixnet provides a high-performance, metadata-private communication protocol for libp2p.
