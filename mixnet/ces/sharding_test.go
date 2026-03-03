package ces

import (
	"bytes"
	"testing"
)

func TestSharding_CreateAndReconstruct(t *testing.T) {
	sharder := NewSharder(5, 3) // 5 shards, need 3 to reconstruct

	data := []byte("This is test data for erasure coding!")
	shards, err := sharder.Shard(data)

	if err != nil {
		t.Fatalf("Shard() error = %v", err)
	}
	if len(shards) != 5 {
		t.Errorf("expected 5 shards, got %d", len(shards))
	}

	// Reconstruct from 3 shards (threshold)
	reconstructed, err := sharder.Reconstruct(shards[:3])
	if err != nil {
		t.Fatalf("Reconstruct() error = %v", err)
	}

	if !bytes.Equal(data, reconstructed) {
		t.Errorf("reconstruction failed:\n  got:      %q\n  expected: %q", reconstructed, data)
	}
}

func TestSharding_ReconstructFromDifferentSubsets(t *testing.T) {
	sharder := NewSharder(5, 3)

	data := []byte("Test reconstruction from different shard subsets!")
	shards, err := sharder.Shard(data)
	if err != nil {
		t.Fatalf("Shard() error = %v", err)
	}

	// Test with shards 0, 2, 4 (non-contiguous)
	subset := []*Shard{shards[0], shards[2], shards[4]}
	reconstructed, err := sharder.Reconstruct(subset)
	if err != nil {
		t.Fatalf("Reconstruct() error = %v", err)
	}

	if !bytes.Equal(data, reconstructed) {
		t.Error("reconstruction from non-contiguous subset failed")
	}
}

func TestSharding_InsufficientShards(t *testing.T) {
	sharder := NewSharder(5, 3)

	data := []byte("Test data")
	shards, err := sharder.Shard(data)
	if err != nil {
		t.Fatalf("Shard() error = %v", err)
	}

	// Try to reconstruct with only 2 shards (below threshold)
	_, err = sharder.Reconstruct(shards[:2])
	if err == nil {
		t.Error("expected error with insufficient shards")
	}
}

func TestSharding_EmptyData(t *testing.T) {
	sharder := NewSharder(5, 3)

	shards, err := sharder.Shard([]byte{})
	if err != nil {
		t.Fatalf("Shard() error = %v", err)
	}

	// Should be able to reconstruct empty data
	reconstructed, err := sharder.Reconstruct(shards[:3])
	if err != nil {
		t.Fatalf("Reconstruct() error = %v", err)
	}

	if len(reconstructed) != 0 {
		t.Errorf("expected empty data, got %d bytes", len(reconstructed))
	}
}

func TestShard_Index(t *testing.T) {
	sharder := NewSharder(5, 3)

	data := []byte("Test")
	shards, _ := sharder.Shard(data)

	for i, shard := range shards {
		if shard.Index != i {
			t.Errorf("expected shard index %d, got %d", i, shard.Index)
		}
	}
}

func TestSharding_LargeData(t *testing.T) {
	sharder := NewSharder(10, 6) // 10 shards, need 6

	// Create 10KB of data
	original := bytes.Repeat([]byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"), 425)

	shards, err := sharder.Shard(original)
	if err != nil {
		t.Fatalf("Shard() error = %v", err)
	}

	// Reconstruct with exactly threshold (6)
	reconstructed, err := sharder.Reconstruct(shards[:6])
	if err != nil {
		t.Fatalf("Reconstruct() error = %v", err)
	}

	if !bytes.Equal(original, reconstructed) {
		t.Error("large data reconstruction failed")
	}
}

func TestSharding_MinimumShards(t *testing.T) {
	// Test with minimum: 1 data shard + parity
	sharder := NewSharder(2, 1)

	data := []byte("Minimal")
	shards, err := sharder.Shard(data)
	if err != nil {
		t.Fatalf("Shard() error = %v", err)
	}

	reconstructed, err := sharder.Reconstruct(shards[:1])
	if err != nil {
		t.Fatalf("Reconstruct() error = %v", err)
	}

	if !bytes.Equal(data, reconstructed) {
		t.Error("minimal reconstruction failed")
	}
}

func TestSharding_AllShards(t *testing.T) {
	sharder := NewSharder(5, 3)

	data := []byte("Test with all shards")
	shards, err := sharder.Shard(data)
	if err != nil {
		t.Fatalf("Shard() error = %v", err)
	}

	// Reconstruct with ALL shards (more than threshold)
	reconstructed, err := sharder.Reconstruct(shards)
	if err != nil {
		t.Fatalf("Reconstruct() error = %v", err)
	}

	if !bytes.Equal(data, reconstructed) {
		t.Error("reconstruction with all shards failed")
	}
}

func TestSharder_Threshold(t *testing.T) {
	sharder := NewSharder(5, 3)

	if sharder.Threshold() != 3 {
		t.Errorf("expected threshold 3, got %d", sharder.Threshold())
	}
	if sharder.TotalShards() != 5 {
		t.Errorf("expected total shards 5, got %d", sharder.TotalShards())
	}
}
