package mixnet

import (
	"sync"
	"time"
)

// MetricsCollector collects mixnet performance metrics (Req 17)
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
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{}
}

// RecordRTT records an RTT measurement (Req 5)
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

// RecordCircuitSuccess records a successful circuit establishment
func (m *MetricsCollector) RecordCircuitSuccess() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.circuitSuccess++
	m.activeCircuits++
}

// RecordCircuitFailure records a circuit failure
func (m *MetricsCollector) RecordCircuitFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.circuitFail++
}

// RecordRecovery records a circuit recovery event (Req 10)
func (m *MetricsCollector) RecordRecovery() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoveryEvents++
}

// RecordThroughput records data throughput
func (m *MetricsCollector) RecordThroughput(bytes uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.throughputBytes += bytes
}

// RecordCompressionRatio records compression ratio (Req 17)
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

// CircuitClosed decrements active circuit count
func (m *MetricsCollector) CircuitClosed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeCircuits > 0 {
		m.activeCircuits--
	}
}

// GetMetrics returns current metrics (Req 17)
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

// AverageRTT returns the average RTT
func (m *MetricsCollector) AverageRTT() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.avgRTT
}

// CircuitSuccessRate returns the circuit success rate
func (m *MetricsCollector) CircuitSuccessRate() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := m.circuitSuccess + m.circuitFail
	if total == 0 {
		return 0
	}
	return float64(m.circuitSuccess) / float64(total)
}

// RecoveryEvents returns the number of recovery events
func (m *MetricsCollector) RecoveryEvents() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.recoveryEvents
}

// TotalThroughput returns the total throughput in bytes
func (m *MetricsCollector) TotalThroughput() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.throughputBytes
}

// CompressionRatio returns the average compression ratio
func (m *MetricsCollector) CompressionRatio() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.compressionRatio
}

// ActiveCircuits returns the number of active circuits
func (m *MetricsCollector) ActiveCircuits() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeCircuits
}
