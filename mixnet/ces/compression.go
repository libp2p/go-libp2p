package ces

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"

	"github.com/golang/snappy"
)

// Compressor handles data compression
type Compressor interface {
	Compress(data []byte) ([]byte, error)
	Decompress(data []byte) ([]byte, error)
}

// gzipCompressor implements gzip compression
type gzipCompressor struct{}

func NewCompressor(algo string) Compressor {
	switch algo {
	case "gzip":
		return &gzipCompressor{}
	case "snappy":
		return &snappyCompressor{}
	default:
		return nil
	}
}

func (c *gzipCompressor) Compress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{}, nil
	}

	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
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

func (c *gzipCompressor) Decompress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{}, nil
	}

	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	return io.ReadAll(gr)
}

// snappyCompressor implements snappy compression
type snappyCompressor struct{}

func (c *snappyCompressor) Compress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{}, nil
	}
	return snappy.Encode(nil, data), nil
}

func (c *snappyCompressor) Decompress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{}, nil
	}
	return snappy.Decode(nil, data)
}

// ErrInvalidAlgorithm is returned when an invalid compression algorithm is specified
var ErrInvalidAlgorithm = fmt.Errorf("invalid compression algorithm")
