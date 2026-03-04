package mixnet

import (
	"testing"
)

func TestMixnetConfig_Defaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.HopCount != 2 {
		t.Errorf("expected default hop count 2, got %d", cfg.HopCount)
	}
	if cfg.CircuitCount != 3 {
		t.Errorf("expected default circuit count 3, got %d", cfg.CircuitCount)
	}
	if cfg.Compression != "gzip" {
		t.Errorf("expected default compression gzip, got %s", cfg.Compression)
	}
	if cfg.SelectionMode != SelectionModeRTT {
		t.Errorf("expected default selection mode rtt, got %s", cfg.SelectionMode)
	}
	if cfg.RandomnessFactor != 0.3 {
		t.Errorf("expected default randomness factor 0.3, got %f", cfg.RandomnessFactor)
	}
}

func TestMixnetConfig_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *MixnetConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid config",
			cfg:     &MixnetConfig{HopCount: 3, CircuitCount: 5, Compression: "gzip"},
			wantErr: false,
		},
		{
			name:    "valid with snappy",
			cfg:     &MixnetConfig{HopCount: 2, CircuitCount: 3, Compression: "snappy"},
			wantErr: false,
		},
		{
			name:    "hop count too low",
			cfg:     &MixnetConfig{HopCount: 0, CircuitCount: 3},
			wantErr: true,
			errMsg:  "hop count must be between 1 and 10",
		},
		{
			name:    "hop count too high",
			cfg:     &MixnetConfig{HopCount: 11, CircuitCount: 3},
			wantErr: true,
			errMsg:  "hop count must be between 1 and 10",
		},
		{
			name:    "circuit count too low",
			cfg:     &MixnetConfig{HopCount: 2, CircuitCount: 0},
			wantErr: true,
			errMsg:  "circuit count must be between 1 and 20",
		},
		{
			name:    "circuit count too high",
			cfg:     &MixnetConfig{HopCount: 2, CircuitCount: 21},
			wantErr: true,
			errMsg:  "circuit count must be between 1 and 20",
		},
		{
			name:    "invalid compression",
			cfg:     &MixnetConfig{HopCount: 2, CircuitCount: 3, Compression: "lz4"},
			wantErr: true,
			errMsg:  "compression must be gzip or snappy",
		},
		{
			name:    "threshold >= circuit count",
			cfg:     &MixnetConfig{HopCount: 2, CircuitCount: 3, Compression: "gzip", ErasureThreshold: 3},
			wantErr: true,
			errMsg:  "erasure threshold must be less than circuit count",
		},
		{
			name:    "invalid selection mode",
			cfg:     &MixnetConfig{HopCount: 2, CircuitCount: 3, Compression: "gzip", SelectionMode: "invalid"},
			wantErr: true,
			errMsg:  "selection mode must be rtt, random, or hybrid",
		},
		{
			name:    "randomness factor too low",
			cfg:     &MixnetConfig{HopCount: 2, CircuitCount: 3, Compression: "gzip", RandomnessFactor: -0.1},
			wantErr: true,
			errMsg:  "randomness factor must be between 0.0 and 1.0",
		},
		{
			name:    "randomness factor too high",
			cfg:     &MixnetConfig{HopCount: 2, CircuitCount: 3, Compression: "gzip", RandomnessFactor: 1.1},
			wantErr: true,
			errMsg:  "randomness factor must be between 0.0 and 1.0",
		},
		{
			name:    "sampling size too small",
			cfg:     &MixnetConfig{HopCount: 2, CircuitCount: 3, Compression: "gzip", SamplingSize: 2},
			wantErr: true,
			errMsg:  "sampling size must be at least",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && tt.errMsg != "" && err != nil {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, want contain %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestMixnetConfig_Setters(t *testing.T) {
	cfg := NewMixnetConfig()
	cfg.SetHopCount(5)
	cfg.SetCircuitCount(10)
	cfg.SetCompression("snappy")
	cfg.SetErasureThreshold(8)
	cfg.SetSelectionMode(SelectionModeHybrid)
	cfg.SetSamplingSize(20)
	cfg.SetRandomnessFactor(0.5)

	if cfg.HopCount != 5 {
		t.Errorf("expected hop count 5, got %d", cfg.HopCount)
	}
	if cfg.CircuitCount != 10 {
		t.Errorf("expected circuit count 10, got %d", cfg.CircuitCount)
	}
	if cfg.Compression != "snappy" {
		t.Errorf("expected compression snappy, got %s", cfg.Compression)
	}
	if cfg.ErasureThreshold != 8 {
		t.Errorf("expected threshold 8, got %d", cfg.ErasureThreshold)
	}
	if cfg.SelectionMode != SelectionModeHybrid {
		t.Errorf("expected selection mode hybrid, got %s", cfg.SelectionMode)
	}
	if cfg.SamplingSize != 20 {
		t.Errorf("expected sampling size 20, got %d", cfg.SamplingSize)
	}
	if cfg.RandomnessFactor != 0.5 {
		t.Errorf("expected randomness factor 0.5, got %f", cfg.RandomnessFactor)
	}
}

func TestMixnetConfig_GetErasureThreshold(t *testing.T) {
	tests := []struct {
		name         string
		threshold    int
		circuitCount int
		want         int
	}{
		{
			name:         "explicit threshold",
			threshold:    2,
			circuitCount: 5,
			want:         2,
		},
		{
			name:         "default threshold",
			threshold:    0,
			circuitCount: 5,
			want:         3, // ceil(5 * 0.6) = 3
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &MixnetConfig{
				ErasureThreshold: tt.threshold,
				CircuitCount:      tt.circuitCount,
			}
			if got := cfg.GetErasureThreshold(); got != tt.want {
				t.Errorf("GetErasureThreshold() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMixnetConfig_GetSamplingSize(t *testing.T) {
	tests := []struct {
		name         string
		samplingSize int
		hopCount     int
		circuitCount int
		want         int
	}{
		{
			name:         "explicit sampling size",
			samplingSize: 30,
			hopCount:     2,
			circuitCount: 3,
			want:         30,
		},
		{
			name:         "default sampling size",
			samplingSize: 0,
			hopCount:     2,
			circuitCount: 3,
			want:         18, // hopCount * circuitCount * 3
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &MixnetConfig{
				SamplingSize: tt.samplingSize,
				HopCount:     tt.hopCount,
				CircuitCount: tt.circuitCount,
			}
			if got := cfg.GetSamplingSize(); got != tt.want {
				t.Errorf("GetSamplingSize() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMixnetConfig_InitDefaults(t *testing.T) {
	cfg := NewMixnetConfig()
	cfg.InitDefaults()

	if cfg.HopCount != 2 {
		t.Errorf("expected hop count 2, got %d", cfg.HopCount)
	}
	if cfg.CircuitCount != 3 {
		t.Errorf("expected circuit count 3, got %d", cfg.CircuitCount)
	}
	if cfg.Compression != "gzip" {
		t.Errorf("expected compression gzip, got %s", cfg.Compression)
	}
	if cfg.SelectionMode != SelectionModeRTT {
		t.Errorf("expected selection mode rtt, got %s", cfg.SelectionMode)
	}
	if cfg.RandomnessFactor != 0.3 {
		t.Errorf("expected randomness factor 0.3, got %f", cfg.RandomnessFactor)
	}
}

func TestProtocolID(t *testing.T) {
	if ProtocolID != "/lib-mix/1.0.0" {
		t.Errorf("expected protocol ID /lib-mix/1.0.0, got %s", ProtocolID)
	}
}

func TestSelectionMode_Constants(t *testing.T) {
	if SelectionModeRTT != "rtt" {
		t.Errorf("expected rtt, got %s", SelectionModeRTT)
	}
	if SelectionModeRandom != "random" {
		t.Errorf("expected random, got %s", SelectionModeRandom)
	}
	if SelectionModeHybrid != "hybrid" {
		t.Errorf("expected hybrid, got %s", SelectionModeHybrid)
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
