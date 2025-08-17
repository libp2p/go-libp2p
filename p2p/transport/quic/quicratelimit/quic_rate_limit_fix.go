// Package quicratelimit provides rate limiting for QUIC connections in go-libp2p
// This addresses the need for DoS protection against connection flooding

package quicratelimit

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	// DefaultConnectionRateLimit is the default number of connections per second allowed
	DefaultConnectionRateLimit = 10

	// DefaultBurstSize is the default burst size for connection rate limiting
	DefaultBurstSize = 20

	// DefaultCleanupInterval is how often to clean up expired rate limiters
	DefaultCleanupInterval = 5 * time.Minute

	// DefaultLimiterTTL is how long to keep a rate limiter for an IP after last use
	DefaultLimiterTTL = 10 * time.Minute
)

// RateLimitConfig configures the rate limiting behavior
type RateLimitConfig struct {
	// GlobalLimit is the global rate limit for all connections
	GlobalLimit rate.Limit

	// GlobalBurst is the global burst size
	GlobalBurst int

	// PerIPLimit is the rate limit per IP address
	PerIPLimit rate.Limit

	// PerIPBurst is the burst size per IP address
	PerIPBurst int

	// CleanupInterval is how often to clean up expired limiters
	CleanupInterval time.Duration

	// LimiterTTL is how long to keep unused limiters
	LimiterTTL time.Duration

	// Whitelist contains IP addresses/subnets that bypass rate limiting
	Whitelist []*net.IPNet
}

// DefaultRateLimitConfig returns a sensible default configuration
func DefaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		GlobalLimit:     rate.Limit(DefaultConnectionRateLimit * 5), // 5x per-IP limit globally
		GlobalBurst:     DefaultBurstSize * 5,
		PerIPLimit:      rate.Limit(DefaultConnectionRateLimit),
		PerIPBurst:      DefaultBurstSize,
		CleanupInterval: DefaultCleanupInterval,
		LimiterTTL:      DefaultLimiterTTL,
		Whitelist:       make([]*net.IPNet, 0),
	}
}

// ipLimiter tracks rate limiting for a specific IP
type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
	mutex    sync.RWMutex
}

// QUICRateLimiter implements rate limiting for QUIC connections
type QUICRateLimiter struct {
	config        *RateLimitConfig
	globalLimiter *rate.Limiter
	ipLimiters    map[string]*ipLimiter
	mutex         sync.RWMutex
	cleanup       *time.Ticker
	stopCleanup   chan struct{}
	closeOnce     sync.Once
}

// NewQUICRateLimiter creates a new QUIC rate limiter
func NewQUICRateLimiter(config *RateLimitConfig) *QUICRateLimiter {
	if config == nil {
		config = DefaultRateLimitConfig()
	}

	rl := &QUICRateLimiter{
		config:        config,
		globalLimiter: rate.NewLimiter(config.GlobalLimit, config.GlobalBurst),
		ipLimiters:    make(map[string]*ipLimiter),
		cleanup:       time.NewTicker(config.CleanupInterval),
		stopCleanup:   make(chan struct{}),
	}

	// Start cleanup goroutine
	go rl.cleanupWorker()

	return rl
}

// AllowConnection checks if a connection from the given address should be allowed
func (rl *QUICRateLimiter) AllowConnection(remoteAddr ma.Multiaddr) (bool, error) {
	// Extract IP address from multiaddr
	ip, err := rl.extractIP(remoteAddr)
	if err != nil {
		return false, fmt.Errorf("failed to extract IP: %w", err)
	}

	// Check if IP is whitelisted
	if rl.isWhitelisted(ip) {
		return true, nil
	}

	// Check global rate limit first
	if !rl.globalLimiter.Allow() {
		return false, nil
	}

	// Check per-IP rate limit
	limiter := rl.getLimiterForIP(ip.String())
	return limiter.Allow(), nil
}

// AllowConnectionWithContext checks if a connection should be allowed with context
func (rl *QUICRateLimiter) AllowConnectionWithContext(ctx context.Context, remoteAddr ma.Multiaddr) (bool, error) {
	// Extract IP address from multiaddr
	ip, err := rl.extractIP(remoteAddr)
	if err != nil {
		return false, fmt.Errorf("failed to extract IP: %w", err)
	}

	// Check if IP is whitelisted
	if rl.isWhitelisted(ip) {
		return true, nil
	}

	// Check global rate limit with context
	err = rl.globalLimiter.Wait(ctx)
	if err != nil {
		return false, err
	}

	// Check per-IP rate limit with context
	limiter := rl.getLimiterForIP(ip.String())
	err = limiter.Wait(ctx)
	return err == nil, err
}

// extractIP extracts an IP address from a multiaddr
func (rl *QUICRateLimiter) extractIP(addr ma.Multiaddr) (net.IP, error) {
	netAddr, err := manet.ToNetAddr(addr)
	if err != nil {
		return nil, err
	}

	switch v := netAddr.(type) {
	case *net.UDPAddr:
		return v.IP, nil
	case *net.TCPAddr:
		return v.IP, nil
	default:
		return nil, fmt.Errorf("unsupported address type: %T", netAddr)
	}
}

// isWhitelisted checks if an IP is in the whitelist
func (rl *QUICRateLimiter) isWhitelisted(ip net.IP) bool {
	for _, subnet := range rl.config.Whitelist {
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}

// getLimiterForIP gets or creates a rate limiter for the given IP
func (rl *QUICRateLimiter) getLimiterForIP(ip string) *rate.Limiter {
	rl.mutex.RLock()
	limiter, exists := rl.ipLimiters[ip]
	rl.mutex.RUnlock()

	if exists {
		limiter.mutex.Lock()
		limiter.lastSeen = time.Now()
		limiter.mutex.Unlock()
		return limiter.limiter
	}

	// Create new limiter
	rl.mutex.Lock()
	defer rl.mutex.Unlock()

	// Double-check after acquiring write lock
	if limiter, exists := rl.ipLimiters[ip]; exists {
		limiter.mutex.Lock()
		limiter.lastSeen = time.Now()
		limiter.mutex.Unlock()
		return limiter.limiter
	}

	newLimiter := &ipLimiter{
		limiter:  rate.NewLimiter(rl.config.PerIPLimit, rl.config.PerIPBurst),
		lastSeen: time.Now(),
	}

	rl.ipLimiters[ip] = newLimiter
	return newLimiter.limiter
}

// cleanupWorker periodically removes expired rate limiters
func (rl *QUICRateLimiter) cleanupWorker() {
	for {
		select {
		case <-rl.cleanup.C:
			rl.performCleanup()
		case <-rl.stopCleanup:
			return
		}
	}
}

// performCleanup removes expired rate limiters
func (rl *QUICRateLimiter) performCleanup() {
	rl.mutex.Lock()
	defer rl.mutex.Unlock()

	now := time.Now()
	for ip, limiter := range rl.ipLimiters {
		limiter.mutex.RLock()
		expired := now.Sub(limiter.lastSeen) > rl.config.LimiterTTL
		limiter.mutex.RUnlock()

		if expired {
			delete(rl.ipLimiters, ip)
		}
	}
}

// GetStats returns current rate limiting statistics
func (rl *QUICRateLimiter) GetStats() RateLimitStats {
	rl.mutex.RLock()
	defer rl.mutex.RUnlock()

	return RateLimitStats{
		ActiveIPLimiters: len(rl.ipLimiters),
		GlobalLimit:      float64(rl.config.GlobalLimit),
		PerIPLimit:       float64(rl.config.PerIPLimit),
	}
}

// RateLimitStats contains rate limiting statistics
type RateLimitStats struct {
	ActiveIPLimiters int
	GlobalLimit      float64
	PerIPLimit       float64
}

// Close shuts down the rate limiter
func (rl *QUICRateLimiter) Close() {
	rl.closeOnce.Do(func() {
		close(rl.stopCleanup)
		rl.cleanup.Stop()
	})
}

// AddToWhitelist adds an IP address or subnet to the whitelist
func (rl *QUICRateLimiter) AddToWhitelist(cidr string) error {
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		// Try parsing as a single IP
		ip := net.ParseIP(cidr)
		if ip == nil {
			return fmt.Errorf("invalid IP or CIDR: %s", cidr)
		}

		// Convert single IP to CIDR
		if ip.To4() != nil {
			subnet = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
		} else {
			subnet = &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
		}
	}

	rl.config.Whitelist = append(rl.config.Whitelist, subnet)
	return nil
}

// RateLimitedQUICTransport wraps a QUIC transport with rate limiting
type RateLimitedQUICTransport struct {
	transport.Transport
	rateLimiter *QUICRateLimiter
}

// NewRateLimitedQUICTransport creates a new rate-limited QUIC transport
func NewRateLimitedQUICTransport(baseTransport transport.Transport, config *RateLimitConfig) *RateLimitedQUICTransport {
	return &RateLimitedQUICTransport{
		Transport:   baseTransport,
		rateLimiter: NewQUICRateLimiter(config),
	}
}

// Listen wraps the base transport's Listen method with rate limiting
func (rt *RateLimitedQUICTransport) Listen(laddr ma.Multiaddr) (transport.Listener, error) {
	baseListener, err := rt.Transport.Listen(laddr)
	if err != nil {
		return nil, err
	}

	return &rateLimitedListener{
		Listener:    baseListener,
		rateLimiter: rt.rateLimiter,
	}, nil
}

// rateLimitedListener wraps a transport listener with rate limiting
type rateLimitedListener struct {
	transport.Listener
	rateLimiter *QUICRateLimiter
}

// Accept wraps the base listener's Accept method with rate limiting
func (rl *rateLimitedListener) Accept() (transport.CapableConn, error) {
	for {
		conn, err := rl.Listener.Accept()
		if err != nil {
			return nil, err
		}

		// Check rate limit
		allowed, err := rl.rateLimiter.AllowConnection(conn.RemoteMultiaddr())
		if err != nil {
			conn.Close()
			continue
		}

		if !allowed {
			conn.Close()
			continue
		}

		return conn, nil
	}
}

// Close closes the rate limiter along with the listener
func (rl *rateLimitedListener) Close() error {
	rl.rateLimiter.Close()
	return rl.Listener.Close()
}
