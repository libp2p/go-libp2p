package connmgr

import (
	"errors"
	"time"

	"github.com/filecoin-project/go-clock"
)

// config is the configuration struct for the basic connection manager.
type config struct {
	highWater     int
	lowWater      int
	gracePeriod   time.Duration
	silencePeriod time.Duration
	decayer       *DecayerCfg
	clock         clock.Clock
}

// Option represents an option for the basic connection manager.
type Option func(*config) error

// DecayerConfig applies a configuration for the decayer.
func DecayerConfig(opts *DecayerCfg) Option {
	return func(cfg *config) error {
		cfg.decayer = opts
		return nil
	}
}

// WithClock sets the internal clock impl
func WithClock(c clock.Clock) Option {
	return func(cfg *config) error {
		cfg.clock = c
		return nil
	}
}

// WithGracePeriod sets the grace period.
// The grace period is the time a newly opened connection is given before it becomes
// subject to pruning.
func WithGracePeriod(p time.Duration) Option {
	return func(cfg *config) error {
		if p < 0 {
			return errors.New("grace period must be non-negative")
		}
		cfg.gracePeriod = p
		return nil
	}
}

// WithSilencePeriod sets the silence period.
// The connection manager will perform a cleanup once per silence period
// if the number of connections surpasses the high watermark.
func WithSilencePeriod(p time.Duration) Option {
	return func(cfg *config) error {
		if p <= 0 {
			return errors.New("silence period must be non-zero")
		}
		cfg.silencePeriod = p
		return nil
	}
}
