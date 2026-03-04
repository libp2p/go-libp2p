package ces

import (
	"bytes"
	"testing"
)

func TestPipeline_FullRoundtrip(t *testing.T) {
	cfg := &Config{
		HopCount:         3,
		CircuitCount:     5,
		Compression:      "gzip",
		ErasureThreshold: 0, // Will default to ceil(5*0.6)=3
	}

	pipeline := NewPipeline(cfg)

	originalData := []byte("This is a comprehensive test of the full CES pipeline! " +
		"We need enough data to test compression effectively. " +
		"Testing various data sizes and patterns.")

	destinations := []string{
		"/ip4/192.168.1.1/tcp/4001/p2p/QmRelay1",
		"/ip4/192.168.1.2/tcp/4002/p2p/QmRelay2",
		"/ip4/192.168.1.3/tcp/4003/p2p/QmRelay3",
	}

	shards, keys, err := pipeline.ProcessWithKeys(originalData, destinations)
	if err != nil {
		t.Fatalf("ProcessWithKeys() error = %v", err)
	}

	if len(shards) != 5 {
		t.Errorf("expected 5 shards, got %d", len(shards))
	}

	// Reconstruct from 3 of 5 shards (threshold = ceil(5*0.6) = 3)
	reconstructed, err := pipeline.Reconstruct(shards[:3], keys)
	if err != nil {
		t.Fatalf("Reconstruct() error = %v", err)
	}

	if !bytes.Equal(originalData, reconstructed) {
		t.Errorf("roundtrip failed:\n  got:      %q\n  expected: %q", reconstructed, originalData)
	}
}

func TestPipeline_WithSnappy(t *testing.T) {
	cfg := &Config{
		HopCount:         2,
		CircuitCount:     3,
		Compression:      "snappy",
		ErasureThreshold: 0,
	}

	pipeline := NewPipeline(cfg)

	originalData := []byte("Snappy compression test data!")

	destinations := []string{
		"/ip4/10.0.0.1/tcp/4001",
		"/ip4/10.0.0.2/tcp/4002",
	}

	shards, keys, err := pipeline.ProcessWithKeys(originalData, destinations)
	if err != nil {
		t.Fatalf("ProcessWithKeys() error = %v", err)
	}

	// threshold = ceil(3*0.6) = 2
	reconstructed, err := pipeline.Reconstruct(shards[:2], keys)
	if err != nil {
		t.Fatalf("Reconstruct() error = %v", err)
	}

	if !bytes.Equal(originalData, reconstructed) {
		t.Error("snappy roundtrip failed")
	}
}

func TestPipeline_AllShardsRecovery(t *testing.T) {
	cfg := &Config{
		HopCount:         2,
		CircuitCount:     3,
		Compression:      "gzip",
		ErasureThreshold: 0,
	}

	pipeline := NewPipeline(cfg)
	originalData := []byte("Test with all shards recovery - long enough for gzip to compress well")

	destinations := []string{"/ip4/1.1.1.1/tcp/1", "/ip4/2.2.2.2/tcp/2"}

	shards, keys, err := pipeline.ProcessWithKeys(originalData, destinations)
	if err != nil {
		t.Fatalf("ProcessWithKeys() error = %v", err)
	}

	// Use ALL shards for reconstruction
	reconstructed, err := pipeline.Reconstruct(shards, keys)
	if err != nil {
		t.Fatalf("Reconstruct() error = %v", err)
	}

	if !bytes.Equal(originalData, reconstructed) {
		t.Error("all shards recovery failed")
	}
}

func TestPipeline_LargeData(t *testing.T) {
	cfg := &Config{
		HopCount:         3,
		CircuitCount:     5,
		Compression:      "gzip",
		ErasureThreshold: 0,
	}

	pipeline := NewPipeline(cfg)

	originalData := bytes.Repeat([]byte("Lib-Mix is a high-performance metadata-private communication protocol. "), 2000)

	destinations := []string{
		"/ip4/192.168.1.1/tcp/4001",
		"/ip4/192.168.1.2/tcp/4002",
		"/ip4/192.168.1.3/tcp/4003",
	}

	shards, keys, err := pipeline.ProcessWithKeys(originalData, destinations)
	if err != nil {
		t.Fatalf("ProcessWithKeys() error = %v", err)
	}

	// threshold = ceil(5*0.6) = 3
	reconstructed, err := pipeline.Reconstruct(shards[:3], keys)
	if err != nil {
		t.Fatalf("Reconstruct() error = %v", err)
	}

	if !bytes.Equal(originalData, reconstructed) {
		t.Error("large data roundtrip failed")
	}

	t.Logf("Original: %d bytes, Shards: %d x %d bytes, Reconstructed: %d bytes",
		len(originalData), len(shards), len(shards[0].Data), len(reconstructed))
}

func TestPipeline_Config(t *testing.T) {
	cfg := &Config{
		HopCount:         3,
		CircuitCount:     5,
		Compression:      "snappy",
		ErasureThreshold: 0,
	}

	pipeline := NewPipeline(cfg)

	if pipeline.Config() != cfg {
		t.Error("Config() should return the same config")
	}

	if pipeline.Compressor() == nil {
		t.Error("Compressor() should not be nil")
	}

	if pipeline.Sharder() == nil {
		t.Error("Sharder() should not be nil")
	}

	if pipeline.Encrypter() == nil {
		t.Error("Encrypter() should not be nil")
	}
}

func TestPipeline_EmptyData(t *testing.T) {
	cfg := &Config{
		HopCount:         2,
		CircuitCount:     3,
		Compression:      "gzip",
		ErasureThreshold: 0,
	}

	pipeline := NewPipeline(cfg)

	_, err := pipeline.Process([]byte{}, []string{"/ip4/1.1.1.1/tcp/1", "/ip4/2.2.2.2/tcp/2"})
	if err == nil {
		t.Error("expected error for empty data")
	}
}

func TestPipeline_MismatchedDestinations(t *testing.T) {
	cfg := &Config{
		HopCount:         2,
		CircuitCount:     3,
		Compression:      "gzip",
		ErasureThreshold: 0,
	}

	pipeline := NewPipeline(cfg)

	// Wrong number of destinations
	_, err := pipeline.Process([]byte("test"), []string{"/ip4/1.1.1.1/tcp/1"})
	if err == nil {
		t.Error("expected error for mismatched destinations")
	}
}



