package ces

import (
	"fmt"

	"github.com/klauspost/reedsolomon"
)

// Shard represents a single erasure-coded shard
type Shard struct {
	Index int
	Data  []byte
}

type Sharder struct {
	totalShards int
	threshold   int
	encoder     reedsolomon.Encoder
}

func NewSharder(totalShards, threshold int) *Sharder {
	enc, err := reedsolomon.New(totalShards, threshold)
	if err != nil {
		// This should not happen with valid parameters
		panic(fmt.Sprintf("failed to create reed-solomon encoder: %v", err))
	}
	return &Sharder{
		totalShards: totalShards,
		threshold:   threshold,
		encoder:     enc,
	}
}

// Shard encodes data into N shards using Reed-Solomon coding
func (s *Sharder) Shard(data []byte) ([]*Shard, error) {
	// Calculate optimal shard size
	// We need to ensure we have enough data to fill all shards
	minSize := 1
	if len(data) > 0 {
		minSize = (len(data) + s.totalShards - 1) / s.totalShards
	}

	// Pad data to fill all shards
	paddedLen := minSize * s.totalShards
	padded := make([]byte, paddedLen)
	copy(padded, data)

	// Split into data shards
	shards := make([][]byte, s.totalShards)
	for i := 0; i < s.totalShards; i++ {
		start := i * minSize
		end := start + minSize
		if end > len(padded) {
			end = len(padded)
		}
		shards[i] = make([]byte, end-start)
		copy(shards[i], padded[start:end])
	}

	// Encode parity shards
	err := s.encoder.Encode(shards)
	if err != nil {
		return nil, fmt.Errorf("failed to encode: %w", err)
	}

	// Wrap in Shard structs
	result := make([]*Shard, s.totalShards)
	for i := 0; i < s.totalShards; i++ {
		result[i] = &Shard{
			Index: i,
			Data:  shards[i],
		}
	}

	return result, nil
}

// Reconstruct recovers the original data from shards
// Requires at least threshold number of shards
func (s *Sharder) Reconstruct(shards []*Shard) ([]byte, error) {
	if len(shards) < s.threshold {
		return nil, fmt.Errorf("insufficient shards: have %d, need %d", len(shards), s.threshold)
	}

	// Convert to slice format for reed-solomon
	shardData := make([][]byte, s.totalShards)
	for i := 0; i < s.totalShards; i++ {
		shardData[i] = nil // Missing by default
	}

	// Fill in provided shards
	for _, shard := range shards {
		if shard.Index >= 0 && shard.Index < s.totalShards {
			shardData[shard.Index] = shard.Data
		}
	}

	// Reconstruct missing shards
	err := s.encoder.Reconstruct(shardData)
	if err != nil {
		return nil, fmt.Errorf("failed to reconstruct: %w", err)
	}

	// Combine data shards (first threshold shards contain original data)
	// Find the actual data size from first available shard
	var dataSize int
	for i := 0; i < s.threshold; i++ {
		if shardData[i] != nil && len(shardData[i]) > 0 {
			dataSize = len(shardData[i])
			break
		}
	}

	if dataSize == 0 {
		return nil, fmt.Errorf("no valid data shards found")
	}

	// Combine only the data shards (not parity)
	result := make([]byte, 0, dataSize*s.threshold)
	for i := 0; i < s.threshold; i++ {
		if shardData[i] != nil {
			result = append(result, shardData[i]...)
		}
	}

	// Trim to original size if we have padding
	if len(result) > len(shards[0].Data) {
		// The original data was smaller than the shard size
		// We need to return exactly what was originally encoded
		// Calculate original data size
		originalSize := 0
		for _, shard := range shards {
			if shard != nil && shard.Data != nil {
				originalSize = len(shard.Data) * s.threshold
				break
			}
		}
		if originalSize > 0 && len(result) > originalSize {
			result = result[:originalSize]
		}
	}

	return result, nil
}

// Threshold returns the minimum number of shards needed for reconstruction
func (s *Sharder) Threshold() int {
	return s.threshold
}

// TotalShards returns the total number of shards
func (s *Sharder) TotalShards() int {
	return s.totalShards
}
