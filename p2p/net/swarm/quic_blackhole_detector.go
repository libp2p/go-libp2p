package swarm

import (
	"fmt"
	"sync"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	// QUICBlackHoleTimeout is the time we wait for a QUIC connection attempt before considering it potentially blackholed
	QUICBlackHoleTimeout = 250 * time.Millisecond

	// QUICBlackHoleMinAttempts is the minimum number of failed attempts before we consider UDP/QUIC to be blackholed
	QUICBlackHoleMinAttempts = 5

	// QUICBlackHoleResetAfter is the number of successful connections after which we reset the blackhole state
	QUICBlackHoleResetAfter = 2
)

// QUICBlackHoleDetector provides specialized detection for QUIC/UDP blackholes.
// It extends the basic blackhole detection with QUIC-specific timing and behavior analysis.
type QUICBlackHoleDetector struct {
	mu sync.RWMutex

	// Track consecutive failures for each remote IP
	failures map[string]int
	// Track successful connections
	successes map[string]int
	// Track when we last attempted a connection
	lastAttempt map[string]time.Time
	// Track blackholed IPs
	blackholed map[string]bool

	// Metrics
	metricsTracer MetricsTracer
}

// NewQUICBlackHoleDetector creates a new QUIC-specific blackhole detector
func NewQUICBlackHoleDetector(mt MetricsTracer) *QUICBlackHoleDetector {
	return &QUICBlackHoleDetector{
		failures:    make(map[string]int),
		successes:   make(map[string]int),
		lastAttempt: make(map[string]time.Time),
		blackholed:  make(map[string]bool),
		metricsTracer: mt,
	}
}

// IsBlackholed checks if a given multiaddr is likely in a UDP/QUIC blackhole
func (d *QUICBlackHoleDetector) IsBlackholed(addr ma.Multiaddr) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Only check QUIC addresses
	if !isQUICAddress(addr) {
		return false
	}

	// Extract IP from multiaddr
	ip, err := manet.ToIP(addr)
	if err != nil {
		return false
	}

	return d.blackholed[ip.String()]
}

// RecordAttempt records the start of a QUIC connection attempt
func (d *QUICBlackHoleDetector) RecordAttempt(addr ma.Multiaddr) {
	if !isQUICAddress(addr) {
		return
	}

	ip, err := manet.ToIP(addr)
	if err != nil {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	ipStr := ip.String()
	d.lastAttempt[ipStr] = time.Now()
}

// RecordResult records the result of a QUIC connection attempt
func (d *QUICBlackHoleDetector) RecordResult(addr ma.Multiaddr, success bool, duration time.Duration) {
	if !isQUICAddress(addr) {
		return
	}

	ip, err := manet.ToIP(addr)
	if err != nil {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	ipStr := ip.String()

	if success {
		// Reset failures on success
		delete(d.failures, ipStr)
		d.successes[ipStr]++

		// If we've had enough successes, remove from blackhole list
		if d.successes[ipStr] >= QUICBlackHoleResetAfter {
			delete(d.blackholed, ipStr)
			d.successes[ipStr] = 0
			if d.metricsTracer != nil {
				d.metricsTracer.UpdatedBlackHoleSuccessCounter("QUIC", blackHoleStateAllowed, 0, 1.0)
			}
		}
		return
	}

	// Handle failure
	d.failures[ipStr]++
	delete(d.successes, ipStr)

	// Check if this failure indicates a blackhole
	isTimeout := duration >= QUICBlackHoleTimeout
	failureCount := d.failures[ipStr]

	if isTimeout && failureCount >= QUICBlackHoleMinAttempts {
		d.blackholed[ipStr] = true
		if d.metricsTracer != nil {
			d.metricsTracer.UpdatedBlackHoleSuccessCounter("QUIC", blackHoleStateBlocked, QUICBlackHoleMinAttempts-failureCount, 0.0)
		}
	}
}

// FilterAddrs filters out addresses that are likely in a UDP/QUIC blackhole
func (d *QUICBlackHoleDetector) FilterAddrs(addrs []ma.Multiaddr) ([]ma.Multiaddr, []ma.Multiaddr) {
	var (
		filtered   []ma.Multiaddr
		blackholed []ma.Multiaddr
	)

	for _, addr := range addrs {
		if d.IsBlackholed(addr) {
			blackholed = append(blackholed, addr)
		} else {
			filtered = append(filtered, addr)
		}
	}

	return filtered, blackholed
}

// isQUICAddress checks if a multiaddr uses QUIC
func isQUICAddress(addr ma.Multiaddr) bool {
	protos := addr.Protocols()
	for _, proto := range protos {
		switch proto.Name {
		case "quic", "quic-v1":
			return true
		}
	}
	return false
}

// Reset clears all blackhole detection state
func (d *QUICBlackHoleDetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.failures = make(map[string]int)
	d.successes = make(map[string]int)
	d.lastAttempt = make(map[string]time.Time)
	d.blackholed = make(map[string]bool)
}