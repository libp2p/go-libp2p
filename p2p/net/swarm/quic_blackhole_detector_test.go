package swarm

import (
	"testing"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestQUICBlackHoleDetector(t *testing.T) {
	detector := NewQUICBlackHoleDetector(nil)

	// Test non-QUIC address
	tcpAddr := ma.StringCast("/ip4/1.2.3.4/tcp/1234")
	require.False(t, detector.IsBlackholed(tcpAddr))

	// Test QUIC address
	quicAddr := ma.StringCast("/ip4/1.2.3.4/udp/1234/quic-v1")
	require.False(t, detector.IsBlackholed(quicAddr))

	// Test blackhole detection with timeouts
	detector.RecordAttempt(quicAddr)
	for i := 0; i < QUICBlackHoleMinAttempts; i++ {
		detector.RecordResult(quicAddr, false, QUICBlackHoleTimeout+time.Millisecond)
	}
	require.True(t, detector.IsBlackholed(quicAddr))

	// Test recovery after successful connections
	for i := 0; i < QUICBlackHoleResetAfter; i++ {
		detector.RecordResult(quicAddr, true, 100*time.Millisecond)
	}
	require.False(t, detector.IsBlackholed(quicAddr))
}

func TestQUICBlackHoleDetectorFiltering(t *testing.T) {
	detector := NewQUICBlackHoleDetector(nil)

	quicAddr1 := ma.StringCast("/ip4/1.2.3.4/udp/1234/quic-v1")
	quicAddr2 := ma.StringCast("/ip4/1.2.3.5/udp/1234/quic-v1")
	tcpAddr := ma.StringCast("/ip4/1.2.3.4/tcp/1234")

	// Blackhole first QUIC address
	detector.RecordAttempt(quicAddr1)
	for i := 0; i < QUICBlackHoleMinAttempts; i++ {
		detector.RecordResult(quicAddr1, false, QUICBlackHoleTimeout+time.Millisecond)
	}

	addrs := []ma.Multiaddr{quicAddr1, quicAddr2, tcpAddr}
	filtered, blackholed := detector.FilterAddrs(addrs)

	require.Len(t, filtered, 2)
	require.Contains(t, filtered, quicAddr2)
	require.Contains(t, filtered, tcpAddr)

	require.Len(t, blackholed, 1)
	require.Contains(t, blackholed, quicAddr1)
}

func TestQUICBlackHoleDetectorReset(t *testing.T) {
	detector := NewQUICBlackHoleDetector(nil)

	quicAddr := ma.StringCast("/ip4/1.2.3.4/udp/1234/quic-v1")

	// Blackhole the address
	detector.RecordAttempt(quicAddr)
	for i := 0; i < QUICBlackHoleMinAttempts; i++ {
		detector.RecordResult(quicAddr, false, QUICBlackHoleTimeout+time.Millisecond)
	}
	require.True(t, detector.IsBlackholed(quicAddr))

	// Reset the detector
	detector.Reset()
	require.False(t, detector.IsBlackholed(quicAddr))

	// Verify we can detect blackholes again
	detector.RecordAttempt(quicAddr)
	for i := 0; i < QUICBlackHoleMinAttempts; i++ {
		detector.RecordResult(quicAddr, false, QUICBlackHoleTimeout+time.Millisecond)
	}
	require.True(t, detector.IsBlackholed(quicAddr))
}

func TestQUICBlackHoleDetectorMetrics(t *testing.T) {
	mt := &mockMetricsTracer{}
	detector := NewQUICBlackHoleDetector(mt)

	quicAddr := ma.StringCast("/ip4/1.2.3.4/udp/1234/quic-v1")

	// Test metrics for blackhole detection
	detector.RecordAttempt(quicAddr)
	for i := 0; i < QUICBlackHoleMinAttempts; i++ {
		detector.RecordResult(quicAddr, false, QUICBlackHoleTimeout+time.Millisecond)
	}

	require.Equal(t, "QUIC", mt.lastName)
	require.Equal(t, blackHoleStateBlocked, mt.lastState)
	require.Equal(t, 0.0, mt.lastSuccessFraction)

	// Test metrics for recovery
	for i := 0; i < QUICBlackHoleResetAfter; i++ {
		detector.RecordResult(quicAddr, true, 100*time.Millisecond)
	}

	require.Equal(t, "QUIC", mt.lastName)
	require.Equal(t, blackHoleStateAllowed, mt.lastState)
	require.Equal(t, 1.0, mt.lastSuccessFraction)
}

// Mock metrics tracer for testing
type mockMetricsTracer struct {
	lastName            string
	lastState          BlackHoleState
	lastNextProbeAfter int
	lastSuccessFraction float64
}

func (m *mockMetricsTracer) UpdatedBlackHoleSuccessCounter(name string, state BlackHoleState, nextProbeAfter int, successFraction float64) {
	m.lastName = name
	m.lastState = state
	m.lastNextProbeAfter = nextProbeAfter
	m.lastSuccessFraction = successFraction
}