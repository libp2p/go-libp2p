package mixnet

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// MetricsCollector accumulates performance metrics for a Mixnet instance.
// It also implements libp2p's metrics.Reporter interface for seamless Prometheus integration.
type MetricsCollector struct {
	mu                     sync.RWMutex
	avgRTT                 time.Duration
	rttSamples             int
	circuitSuccess         uint64
	circuitFail            uint64
	recoveryEvents         uint64
	throughputBytes        uint64
	compressionRatio       float64
	activeCircuits         int
	resourceUtilization    float64 // CPU/memory utilization percentage (0-100)
	maxResourceUtilization float64 // Peak resource utilization observed
	relayActiveCircuits    int
	relayBandwidthPerSec   int64

	// Bandwidth tracking for metrics.Reporter interface
	statsLock     sync.RWMutex
	totalIn       int64
	totalOut      int64
	peerStats     map[peer.ID]metrics.Stats
	protocolStats map[protocol.ID]metrics.Stats
}

// Ensure MetricsCollector implements libp2p's metrics.Reporter interface
var _ metrics.Reporter = (*MetricsCollector)(nil)

// NewMetricsCollector creates a new instance of MetricsCollector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		peerStats:     make(map[peer.ID]metrics.Stats),
		protocolStats: make(map[protocol.ID]metrics.Stats),
	}
}

// RecordRTT records a new round-trip time measurement and updates the running average.
func (m *MetricsCollector) RecordRTT(rtt time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Running average
	if m.rttSamples == 0 {
		m.avgRTT = rtt
	} else {
		m.avgRTT = (m.avgRTT*time.Duration(m.rttSamples) + rtt) / time.Duration(m.rttSamples+1)
	}
	m.rttSamples++
}

// RecordCircuitSuccess increments the count of successfully established circuits.
func (m *MetricsCollector) RecordCircuitSuccess() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.circuitSuccess++
	m.activeCircuits++
}

// RecordCircuitFailure increments the count of failed circuit establishments.
func (m *MetricsCollector) RecordCircuitFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.circuitFail++
}

// RecordRecovery increments the count of circuit recovery events.
func (m *MetricsCollector) RecordRecovery() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoveryEvents++
}

// RecordThroughput adds the specified number of bytes to the total throughput.
// This also logs the message to the metrics.Reporter interface for Prometheus integration.
func (m *MetricsCollector) RecordThroughput(bytes uint64) {
	m.mu.Lock()
	m.throughputBytes += bytes
	m.mu.Unlock()

	// Also log to metrics.Reporter interface for Prometheus integration
	m.LogSentMessage(int64(bytes))
}

// RecordCompressionRatio updates the running average of the compression ratio.
func (m *MetricsCollector) RecordCompressionRatio(original, compressed int) {
	if original == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ratio := float64(compressed) / float64(original)
	// Running average
	if m.compressionRatio == 0 {
		m.compressionRatio = ratio
	} else {
		m.compressionRatio = (m.compressionRatio + ratio) / 2
	}
}

// CircuitClosed decrements the count of active circuits.
func (m *MetricsCollector) CircuitClosed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeCircuits > 0 {
		m.activeCircuits--
	}
}

// GetMetrics returns a map containing all collected metrics.
func (m *MetricsCollector) GetMetrics() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	successRate := 0.0
	total := m.circuitSuccess + m.circuitFail
	if total > 0 {
		successRate = float64(m.circuitSuccess) / float64(total)
	}

	return map[string]interface{}{
		"avg_rtt_ns":               m.avgRTT.Nanoseconds(),
		"circuit_success":          m.circuitSuccess,
		"circuit_fail":             m.circuitFail,
		"circuit_success_rate":     successRate,
		"recovery_events":          m.recoveryEvents,
		"throughput_bytes":         m.throughputBytes,
		"compression_ratio":        m.compressionRatio,
		"active_circuits":          m.activeCircuits,
		"relay_active_circuits":    m.relayActiveCircuits,
		"relay_bandwidth_bps":      m.relayBandwidthPerSec,
		"resource_utilization":     m.resourceUtilization,
		"resource_utilization_max": m.maxResourceUtilization,
	}
}

// AverageRTT returns the current running average of RTT measurements.
func (m *MetricsCollector) AverageRTT() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.avgRTT
}

// CircuitSuccessRate returns the ratio of successful to total circuit establishment attempts.
func (m *MetricsCollector) CircuitSuccessRate() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := m.circuitSuccess + m.circuitFail
	if total == 0 {
		return 0
	}
	return float64(m.circuitSuccess) / float64(total)
}

// CircuitSuccesses returns the total number of successful circuit establishments.
func (m *MetricsCollector) CircuitSuccesses() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.circuitSuccess
}

// RecoveryEvents returns the total number of circuit recovery events.
func (m *MetricsCollector) RecoveryEvents() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.recoveryEvents
}

// TotalThroughput returns the total number of bytes transmitted through the Mixnet.
func (m *MetricsCollector) TotalThroughput() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.throughputBytes
}

// CompressionRatio returns the current running average of the compression ratio.
func (m *MetricsCollector) CompressionRatio() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.compressionRatio
}

// ActiveCircuits returns the current number of active circuits.
func (m *MetricsCollector) ActiveCircuits() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeCircuits
}

// CircuitFailures returns the total number of failed circuit establishments.
func (m *MetricsCollector) CircuitFailures() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.circuitFail
}

// ThroughputPerCircuit returns the average throughput per active circuit in bytes.
// Returns 0 if there are no active circuits.
func (m *MetricsCollector) ThroughputPerCircuit() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.activeCircuits == 0 {
		return 0
	}
	return m.throughputBytes / uint64(m.activeCircuits)
}

// RecordResourceUtilization records the current resource utilization (CPU/memory).
// The utilization parameter should be a percentage value between 0 and 100.
func (m *MetricsCollector) RecordResourceUtilization(utilization float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.resourceUtilization = utilization
	if utilization > m.maxResourceUtilization {
		m.maxResourceUtilization = utilization
	}
}

// RecordRelayResourceUsage records relay-specific resource usage snapshots.
func (m *MetricsCollector) RecordRelayResourceUsage(activeCircuits int, bandwidthPerSec int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.relayActiveCircuits = activeCircuits
	m.relayBandwidthPerSec = bandwidthPerSec
}

// RelayActiveCircuits returns the number of active relay circuits on this node.
func (m *MetricsCollector) RelayActiveCircuits() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.relayActiveCircuits
}

// RelayBandwidthPerSec returns observed relay bandwidth in bytes per second.
func (m *MetricsCollector) RelayBandwidthPerSec() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.relayBandwidthPerSec
}

// CurrentResourceUtilization returns the current resource utilization percentage.
func (m *MetricsCollector) CurrentResourceUtilization() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.resourceUtilization
}

// MaxResourceUtilization returns the peak resource utilization percentage observed.
func (m *MetricsCollector) MaxResourceUtilization() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maxResourceUtilization
}

// ============ metrics.Reporter interface implementation ============
// These methods integrate with libp2p's built-in Prometheus metrics system.

// LogSentMessage logs a message sent event for bandwidth tracking.
func (m *MetricsCollector) LogSentMessage(size int64) {
	m.statsLock.Lock()
	defer m.statsLock.Unlock()
	m.totalOut += size
}

// LogRecvMessage logs a message received event for bandwidth tracking.
func (m *MetricsCollector) LogRecvMessage(size int64) {
	m.statsLock.Lock()
	defer m.statsLock.Unlock()
	m.totalIn += size
}

// LogSentMessageStream logs a message sent on a specific stream.
func (m *MetricsCollector) LogSentMessageStream(size int64, proto protocol.ID, p peer.ID) {
	m.statsLock.Lock()
	defer m.statsLock.Unlock()
	m.totalOut += size

	// Update protocol stats
	if m.protocolStats == nil {
		m.protocolStats = make(map[protocol.ID]metrics.Stats)
	}
	stats := m.protocolStats[proto]
	stats.TotalOut += size
	m.protocolStats[proto] = stats

	// Update peer stats
	if m.peerStats == nil {
		m.peerStats = make(map[peer.ID]metrics.Stats)
	}
	peerStats := m.peerStats[p]
	peerStats.TotalOut += size
	m.peerStats[p] = peerStats
}

// LogRecvMessageStream logs a message received on a specific stream.
func (m *MetricsCollector) LogRecvMessageStream(size int64, proto protocol.ID, p peer.ID) {
	m.statsLock.Lock()
	defer m.statsLock.Unlock()
	m.totalIn += size

	// Update protocol stats
	if m.protocolStats == nil {
		m.protocolStats = make(map[protocol.ID]metrics.Stats)
	}
	stats := m.protocolStats[proto]
	stats.TotalIn += size
	m.protocolStats[proto] = stats

	// Update peer stats
	if m.peerStats == nil {
		m.peerStats = make(map[peer.ID]metrics.Stats)
	}
	peerStats := m.peerStats[p]
	peerStats.TotalIn += size
	m.peerStats[p] = peerStats
}

// GetBandwidthForPeer returns bandwidth statistics for a specific peer.
func (m *MetricsCollector) GetBandwidthForPeer(p peer.ID) metrics.Stats {
	m.statsLock.RLock()
	defer m.statsLock.RUnlock()

	if m.peerStats == nil {
		return metrics.Stats{}
	}
	return m.peerStats[p]
}

// GetBandwidthForProtocol returns bandwidth statistics for a specific protocol.
func (m *MetricsCollector) GetBandwidthForProtocol(proto protocol.ID) metrics.Stats {
	m.statsLock.RLock()
	defer m.statsLock.RUnlock()

	if m.protocolStats == nil {
		return metrics.Stats{}
	}
	return m.protocolStats[proto]
}

// GetBandwidthTotals returns total bandwidth statistics.
func (m *MetricsCollector) GetBandwidthTotals() metrics.Stats {
	m.statsLock.RLock()
	defer m.statsLock.RUnlock()

	return metrics.Stats{
		TotalIn:  m.totalIn,
		TotalOut: m.totalOut,
	}
}

// GetBandwidthByPeer returns bandwidth statistics grouped by peer.
func (m *MetricsCollector) GetBandwidthByPeer() map[peer.ID]metrics.Stats {
	m.statsLock.RLock()
	defer m.statsLock.RUnlock()

	if m.peerStats == nil {
		return make(map[peer.ID]metrics.Stats)
	}
	return m.peerStats
}

// GetBandwidthByProtocol returns bandwidth statistics grouped by protocol.
func (m *MetricsCollector) GetBandwidthByProtocol() map[protocol.ID]metrics.Stats {
	m.statsLock.RLock()
	defer m.statsLock.RUnlock()

	if m.protocolStats == nil {
		return make(map[protocol.ID]metrics.Stats)
	}
	return m.protocolStats
}

// Reset resets all bandwidth statistics.
func (m *MetricsCollector) Reset() {
	m.statsLock.Lock()
	defer m.statsLock.Unlock()

	m.totalIn = 0
	m.totalOut = 0
	m.peerStats = make(map[peer.ID]metrics.Stats)
	m.protocolStats = make(map[protocol.ID]metrics.Stats)
}

// TrimIdle removes statistics for idle peers.
func (m *MetricsCollector) TrimIdle(since time.Time) {
	// No-op for now - could implement cleanup of old peer stats if needed
	_ = since
}
