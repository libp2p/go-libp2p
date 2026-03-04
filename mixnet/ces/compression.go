package ces

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"

	"github.com/golang/snappy"
)

// Algorithm IDs for compression (Design Doc "Data Format")
const (
	AlgoGzip   byte = 0x01
	AlgoSnappy byte = 0x02
)

// Compressor handles data compression
type Compressor interface {
	Compress(data []byte) ([]byte, error)
	Decompress(data []byte) ([]byte, error)
}

// gzipCompressor implements gzip compression
type gzipCompressor struct {
	level int
}

func NewCompressor(algo string) Compressor {
	switch algo {
	case "gzip":
		return &gzipCompressor{level: gzip.DefaultCompression}
	case "snappy":
		return &snappyCompressor{}
	default:
		return nil
	}
}

// NewCompressorWithLevel creates a compressor with a configurable compression level (0-9 for gzip).
func NewCompressorWithLevel(algo string, level int) Compressor {
	switch algo {
	case "gzip":
		if level < gzip.HuffmanOnly || level > gzip.BestCompression {
			level = gzip.DefaultCompression
		}
		return &gzipCompressor{level: level}
	case "snappy":
		return &snappyCompressor{}
	default:
		return nil
	}
}

// Compress compresses data and prepends a 1-byte algorithm ID header.
// Format: [algorithm_id: 1 byte][compressed_payload]
func (c *gzipCompressor) Compress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{}, nil
	}

	var buf bytes.Buffer
	buf.WriteByte(AlgoGzip)

	gw, err := gzip.NewWriterLevel(&buf, c.level)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip writer: %w", err)
	}

	_, err = gw.Write(data)
	if err != nil {
		return nil, fmt.Errorf("failed to compress: %w", err)
	}

	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("failed to close gzip writer: %w", err)
	}

	return buf.Bytes(), nil
}

// Decompress removes the algorithm ID header and decompresses the payload.
func (c *gzipCompressor) Decompress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{}, nil
	}

	if data[0] != AlgoGzip {
		return nil, fmt.Errorf("unexpected algorithm ID: want %d, got %d", AlgoGzip, data[0])
	}

	gr, err := gzip.NewReader(bytes.NewReader(data[1:]))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	return io.ReadAll(gr)
}

// snappyCompressor implements snappy compression
type snappyCompressor struct{}

// Compress compresses data and prepends a 1-byte algorithm ID header.
// Format: [algorithm_id: 1 byte][compressed_payload]
func (c *snappyCompressor) Compress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{}, nil
	}
	compressed := snappy.Encode(nil, data)
	out := make([]byte, 1+len(compressed))
	out[0] = AlgoSnappy
	copy(out[1:], compressed)
	return out, nil
}

// Decompress removes the algorithm ID header and decompresses the payload.
func (c *snappyCompressor) Decompress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{}, nil
	}

	if data[0] != AlgoSnappy {
		return nil, fmt.Errorf("unexpected algorithm ID: want %d, got %d", AlgoSnappy, data[0])
	}

	return snappy.Decode(nil, data[1:])
}

// ErrInvalidAlgorithm is returned when an invalid compression algorithm is specified
var ErrInvalidAlgorithm = fmt.Errorf("invalid compression algorithm")
