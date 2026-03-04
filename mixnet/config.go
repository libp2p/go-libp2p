package mixnet

import (
	"errors"
	"fmt"
	"time"
)

const (
	ProtocolID = "/lib-mix/1.0.0"
)

// SelectionMode defines how relays are selected
type SelectionMode string

const (
	SelectionModeRTT    SelectionMode = "rtt"
	SelectionModeRandom SelectionMode = "random"
	SelectionModeHybrid  SelectionMode = "hybrid"
)

type MixnetConfig struct {
	HopCount         int
	CircuitCount     int
	Compression      string
	ErasureThreshold int

	// Relay selection (Req 4)
	SelectionMode    SelectionMode // "rtt" | "random" | "hybrid", default "rtt"
	SamplingSize     int           // K candidates to sample, default 3 * hopCount * circuitCount
	RandomnessFactor float64      // [0.0, 1.0], default 0.3
}

func DefaultConfig() *MixnetConfig {
	return &MixnetConfig{
		HopCount:         2,
		CircuitCount:     3,
		Compression:      "gzip",
		ErasureThreshold: 0, // 0 means default to CircuitCount - 1

		// Relay selection defaults
		SelectionMode:    SelectionModeRTT,
		SamplingSize:     0, // 0 means auto-calculate
		RandomnessFactor: 0.3,
	}
}

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

func (c *MixnetConfig) SetHopCount(n int) {
	c.HopCount = n
}

func (c *MixnetConfig) SetCircuitCount(n int) {
	c.CircuitCount = n
}

func (c *MixnetConfig) SetCompression(algo string) {
	c.Compression = algo
}

func (c *MixnetConfig) SetErasureThreshold(n int) {
	c.ErasureThreshold = n
}

func (c *MixnetConfig) SetSelectionMode(mode SelectionMode) {
	c.SelectionMode = mode
}

func (c *MixnetConfig) SetSamplingSize(n int) {
	c.SamplingSize = n
}

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

// GetSamplingSize returns the effective sampling size
func (c *MixnetConfig) GetSamplingSize() int {
	if c.SamplingSize == 0 {
		return c.HopCount * c.CircuitCount * 3
	}
	return c.SamplingSize
}

// ErrConfigValidation is returned when config validation fails
var ErrConfigValidation = errors.New("config validation failed")

// Initialize defaults for unset fields
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

// EnsureReliableRTT marks relays below this threshold as unreliable (Req 5.2)
const RTTUnreliableThreshold = 5 * time.Second
