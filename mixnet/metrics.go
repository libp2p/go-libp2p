package mixnet

import (
	"sync"
	"time"
)

// MetricsCollector accumulates performance metrics for a Mixnet instance.
type MetricsCollector struct {
	mu               sync.RWMutex
	avgRTT           time.Duration
	rttSamples       int
	circuitSuccess   uint64
	circuitFail      uint64
	recoveryEvents   uint64
	throughputBytes  uint64
	compressionRatio float64
	activeCircuits   int
	resourceUtilization float64 // CPU/memory utilization percentage (0-100)
	maxResourceUtilization float64 // Peak resource utilization observed
}

// NewMetricsCollector creates a new instance of MetricsCollector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{}
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
func (m *MetricsCollector) RecordThroughput(bytes uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.throughputBytes += bytes
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
		"avg_rtt_ns":            m.avgRTT.Nanoseconds(),
		"circuit_success":       m.circuitSuccess,
		"circuit_fail":          m.circuitFail,
		"circuit_success_rate":  successRate,
		"recovery_events":       m.recoveryEvents,
		"throughput_bytes":     m.throughputBytes,
		"compression_ratio":    m.compressionRatio,
		"active_circuits":      m.activeCircuits,
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
