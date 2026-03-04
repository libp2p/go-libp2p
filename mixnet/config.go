package mixnet

import (
	"errors"
	"fmt"
	"time"
)

const (
	// ProtocolID is the libp2p protocol identifier for the Mixnet protocol.
	ProtocolID = "/lib-mix/1.0.0"
)

// SelectionMode defines how relays are selected from the available pool.
type SelectionMode string

const (
	// SelectionModeRTT selects relays based on their round-trip time.
	SelectionModeRTT SelectionMode = "rtt"
	// SelectionModeRandom selects relays randomly.
	SelectionModeRandom SelectionMode = "random"
	// SelectionModeHybrid selects relays using a combination of RTT and randomness.
	SelectionModeHybrid SelectionMode = "hybrid"
)

// MixnetConfig holds the configuration parameters for a Mixnet instance.
type MixnetConfig struct {
	// HopCount is the number of relay nodes in each circuit.
	HopCount int
	// CircuitCount is the number of parallel circuits to establish.
	CircuitCount int
	// Compression is the algorithm used for data compression ("gzip" or "snappy").
	Compression string
	// ErasureThreshold is the minimum number of shards required to reconstruct the data.
	ErasureThreshold int

	// SelectionMode defines the relay selection strategy.
	SelectionMode SelectionMode
	// SamplingSize is the number of relay candidates to sample from the DHT.
	SamplingSize int
	// RandomnessFactor is a value between 0.0 and 1.0 used in hybrid selection.
	RandomnessFactor float64
}

// DefaultConfig returns a MixnetConfig with recommended default values.
func DefaultConfig() *MixnetConfig {
	return &MixnetConfig{
		HopCount:         2,
		CircuitCount:     3,
		Compression:      "gzip",
		ErasureThreshold: 0, // 0 means default to 60% of CircuitCount

		// Relay selection defaults
		SelectionMode:    SelectionModeRTT,
		SamplingSize:     0, // 0 means auto-calculate
		RandomnessFactor: 0.3,
	}
}

// NewMixnetConfig creates a new MixnetConfig with all fields initialized to zero values.
// It is intended to be used when full manual configuration is desired.
func NewMixnetConfig() *MixnetConfig {
	cfg := DefaultConfig()
	// Reset to allow custom configuration
	cfg.HopCount = 0
	cfg.CircuitCount = 0
	cfg.Compression = ""
	cfg.ErasureThreshold = 0
	cfg.SelectionMode = ""
	cfg.SamplingSize = 0
	cfg.RandomnessFactor = 0
	return cfg
}

// Validate checks if the configuration parameters are within acceptable ranges.
func (c *MixnetConfig) Validate() error {
	// Hop count validation (Req 1.2)
	if c.HopCount < 1 || c.HopCount > 10 {
		return fmt.Errorf("hop count must be between 1 and 10, got %d", c.HopCount)
	}

	// Circuit count validation (Req 2.2)
	if c.CircuitCount < 1 || c.CircuitCount > 20 {
		return fmt.Errorf("circuit count must be between 1 and 20, got %d", c.CircuitCount)
	}

	// Compression algorithm validation (Req 3.2)
	if c.Compression != "gzip" && c.Compression != "snappy" {
		return fmt.Errorf("compression must be gzip or snappy, got %s", c.Compression)
	}

	// Erasure threshold validation (Req 15.3)
	threshold := c.ErasureThreshold
	if threshold == 0 {
		// ceil(N * 0.6)
		threshold = (c.CircuitCount*6 + 9) / 10
		if threshold < 1 {
			threshold = 1
		}
	}
	if threshold >= c.CircuitCount {
		return fmt.Errorf("erasure threshold must be less than circuit count, got %d >= %d", threshold, c.CircuitCount)
	}

	// Selection mode validation (Req 4.6)
	if c.SelectionMode == "" {
		c.SelectionMode = SelectionModeRTT
	}
	if c.SelectionMode != SelectionModeRTT && c.SelectionMode != SelectionModeRandom && c.SelectionMode != SelectionModeHybrid {
		return fmt.Errorf("selection mode must be rtt, random, or hybrid, got %s", c.SelectionMode)
	}

	// Sampling size validation (Req 4.10)
	required := c.HopCount * c.CircuitCount
	if c.SamplingSize == 0 {
		c.SamplingSize = required * 3 // Default to 3x required
	}
	if c.SamplingSize < required {
		return fmt.Errorf("sampling size must be at least %d, got %d", required, c.SamplingSize)
	}

	// Randomness factor validation (Req 4.10)
	if c.RandomnessFactor < 0.0 || c.RandomnessFactor > 1.0 {
		return fmt.Errorf("randomness factor must be between 0.0 and 1.0, got %f", c.RandomnessFactor)
	}

	return nil
}

// SetHopCount sets the number of relays per circuit.
func (c *MixnetConfig) SetHopCount(n int) {
	c.HopCount = n
}

// SetCircuitCount sets the number of parallel circuits.
func (c *MixnetConfig) SetCircuitCount(n int) {
	c.CircuitCount = n
}

// SetCompression sets the compression algorithm.
func (c *MixnetConfig) SetCompression(algo string) {
	c.Compression = algo
}

// SetErasureThreshold sets the reconstruction threshold.
func (c *MixnetConfig) SetErasureThreshold(n int) {
	c.ErasureThreshold = n
}

// SetSelectionMode sets the relay selection strategy.
func (c *MixnetConfig) SetSelectionMode(mode SelectionMode) {
	c.SelectionMode = mode
}

// SetSamplingSize sets the number of relays to sample from the DHT.
func (c *MixnetConfig) SetSamplingSize(n int) {
	c.SamplingSize = n
}

// SetRandomnessFactor sets the randomness factor for relay selection.
func (c *MixnetConfig) SetRandomnessFactor(f float64) {
	c.RandomnessFactor = f
}

// GetErasureThreshold returns the effective threshold.
// When ErasureThreshold is zero the default is ceil(CircuitCount * 0.6) per the
// design document "Data Models" section (60% reconstruction threshold).
func (c *MixnetConfig) GetErasureThreshold() int {
	if c.ErasureThreshold != 0 {
		return c.ErasureThreshold
	}
	// ceil(N * 0.6)
	threshold := (c.CircuitCount*6 + 9) / 10
	if threshold < 1 {
		threshold = 1
	}
	return threshold
}

// GetSamplingSize returns the effective sampling size.
func (c *MixnetConfig) GetSamplingSize() int {
	if c.SamplingSize == 0 {
		return c.HopCount * c.CircuitCount * 3
	}
	return c.SamplingSize
}

// ErrConfigValidation is returned when config validation fails.
var ErrConfigValidation = errors.New("config validation failed")

// InitDefaults initializes unset fields with default values.
func (c *MixnetConfig) InitDefaults() {
	if c.HopCount == 0 {
		c.HopCount = 2
	}
	if c.CircuitCount == 0 {
		c.CircuitCount = 3
	}
	if c.Compression == "" {
		c.Compression = "gzip"
	}
	if c.SelectionMode == "" {
		c.SelectionMode = SelectionModeRTT
	}
	if c.RandomnessFactor == 0 {
		c.RandomnessFactor = 0.3
	}
}

// RTTUnreliableThreshold defines the latency above which a relay is considered unreliable.
const RTTUnreliableThreshold = 5 * time.Second
