package ces

import (
	"encoding/binary"
	"fmt"

	"github.com/klauspost/reedsolomon"
)

// Shard represents a single erasure-coded shard
type Shard struct {
	Index int
	Data  []byte
}

// Sharder encodes data into N shards using Reed-Solomon coding.
// totalShards is the total number of shards produced (data + parity).
// threshold is the minimum number of shards required for reconstruction
// (equals the number of data shards).
type Sharder struct {
	totalShards int
	threshold   int // = dataShards
	encoder     reedsolomon.Encoder
}

// NewSharder creates a new Sharder.
// totalShards  = dataShards + parityShards
// threshold    = dataShards (minimum shards needed to reconstruct)
func NewSharder(totalShards, threshold int) *Sharder {
	if threshold < 1 || threshold >= totalShards {
		panic(fmt.Sprintf("invalid sharder params: totalShards=%d threshold=%d", totalShards, threshold))
	}
	parityShards := totalShards - threshold
	enc, err := reedsolomon.New(threshold, parityShards)
	if err != nil {
		panic(fmt.Sprintf("failed to create reed-solomon encoder: %v", err))
	}
	return &Sharder{
		totalShards: totalShards,
		threshold:   threshold,
		encoder:     enc,
	}
}

// Shard encodes data into totalShards shards using Reed-Solomon coding.
// A 8-byte little-endian original length prefix is prepended so Reconstruct
// can accurately trim padding from the recovered data.
func (s *Sharder) Shard(data []byte) ([]*Shard, error) {
	dataShards := s.threshold

	// Prepend 8-byte original data length so we can trim padding on reconstruction.
	origLen := uint64(len(data))
	payload := make([]byte, 8+len(data))
	binary.LittleEndian.PutUint64(payload[:8], origLen)
	copy(payload[8:], data)

	// Calculate per-shard size; pad payload to fill all data shards evenly.
	shardSize := (len(payload) + dataShards - 1) / dataShards
	if shardSize == 0 {
		shardSize = 1
	}
	paddedLen := shardSize * dataShards
	padded := make([]byte, paddedLen)
	copy(padded, payload)

	// Allocate data + parity shard slices.
	shards := make([][]byte, s.totalShards)
	for i := 0; i < dataShards; i++ {
		shards[i] = padded[i*shardSize : (i+1)*shardSize]
	}
	for i := dataShards; i < s.totalShards; i++ {
		shards[i] = make([]byte, shardSize)
	}

	if err := s.encoder.Encode(shards); err != nil {
		return nil, fmt.Errorf("failed to encode: %w", err)
	}

	result := make([]*Shard, s.totalShards)
	for i := 0; i < s.totalShards; i++ {
		cp := make([]byte, len(shards[i]))
		copy(cp, shards[i])
		result[i] = &Shard{Index: i, Data: cp}
	}
	return result, nil
}

// Reconstruct recovers the original data from at least threshold shards.
func (s *Sharder) Reconstruct(shards []*Shard) ([]byte, error) {
	if len(shards) < s.threshold {
		return nil, fmt.Errorf("insufficient shards: have %d, need %d", len(shards), s.threshold)
	}

	// Place provided shards into a full-length slice (nil = missing).
	shardData := make([][]byte, s.totalShards)
	for _, sh := range shards {
		if sh.Index >= 0 && sh.Index < s.totalShards {
			shardData[sh.Index] = sh.Data
		}
	}

	if err := s.encoder.Reconstruct(shardData); err != nil {
		return nil, fmt.Errorf("failed to reconstruct: %w", err)
	}

	// Join only the data shards (first threshold shards).
	dataShards := s.threshold
	shardSize := len(shardData[0])
	combined := make([]byte, 0, dataShards*shardSize)
	for i := 0; i < dataShards; i++ {
		combined = append(combined, shardData[i]...)
	}

	// Extract original length from the 8-byte prefix.
	if len(combined) < 8 {
		return nil, fmt.Errorf("reconstructed data too short to contain length prefix")
	}
	origLen := binary.LittleEndian.Uint64(combined[:8])
	payload := combined[8:]
	if uint64(len(payload)) < origLen {
		return nil, fmt.Errorf("reconstructed payload shorter than expected: got %d, want %d", len(payload), origLen)
	}
	return payload[:origLen], nil
}

// Threshold returns the minimum number of shards needed for reconstruction.
func (s *Sharder) Threshold() int {
	return s.threshold
}

// TotalShards returns the total number of shards produced by Shard.
func (s *Sharder) TotalShards() int {
	return s.totalShards
}
