package ces

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"

	"github.com/golang/snappy"
)

// Compression algorithm identifiers used in the Mixnet protocol header.
const (
	// AlgoGzip indicates that the payload is compressed using Gzip.
	AlgoGzip   byte = 0x01
	// AlgoSnappy indicates that the payload is compressed using Snappy.
	AlgoSnappy byte = 0x02
)

// Compressor is an interface for algorithms that can compress and decompress data.
type Compressor interface {
	// Compress compresses the provided data and returns the result with an algorithm-specific header.
	Compress(data []byte) ([]byte, error)
	// Decompress removes the header and decompresses the provided data.
	Decompress(data []byte) ([]byte, error)
}

// gzipCompressor implements the Compressor interface using the Gzip algorithm.
type gzipCompressor struct {
	level int
}

// NewCompressor returns a new Compressor for the specified algorithm ("gzip" or "snappy").
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

// NewCompressorWithLevel returns a new Compressor for the specified algorithm and compression level.
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

// Compress compresses data using Gzip and prepends the AlgoGzip header.
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

// Decompress validates the header and decompresses data using Gzip.
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

// snappyCompressor implements the Compressor interface using the Snappy algorithm.
type snappyCompressor struct{}

// Compress compresses data using Snappy and prepends the AlgoSnappy header.
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

// Decompress validates the header and decompresses data using Snappy.
func (c *snappyCompressor) Decompress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{}, nil
	}

	if data[0] != AlgoSnappy {
		return nil, fmt.Errorf("unexpected algorithm ID: want %d, got %d", AlgoSnappy, data[0])
	}

	return snappy.Decode(nil, data[1:])
}

// ErrInvalidAlgorithm is returned when an unsupported compression algorithm is requested.
var ErrInvalidAlgorithm = fmt.Errorf("invalid compression algorithm")
