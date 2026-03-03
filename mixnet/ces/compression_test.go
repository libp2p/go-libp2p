package ces

import (
	"bytes"
	"testing"
)

func TestCompress_Gzip(t *testing.T) {
	compressor := NewCompressor("gzip")
	if compressor == nil {
		t.Fatal("expected non-nil compressor")
	}

	data := []byte("Hello, this is test data for compression! This is a longer string to test compression effectiveness.")
	compressed, err := compressor.Compress(data)

	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if len(compressed) >= len(data) {
		t.Errorf("compressed data should be smaller than original: original=%d, compressed=%d", len(data), len(compressed))
	}
}

func TestCompress_Snappy(t *testing.T) {
	compressor := NewCompressor("snappy")
	if compressor == nil {
		t.Fatal("expected non-nil compressor")
	}

	data := []byte("Hello, this is test data for compression! This is a longer string to test compression effectiveness.")
	compressed, err := compressor.Compress(data)

	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if len(compressed) >= len(data) {
		t.Errorf("compressed data should be smaller than original: original=%d, compressed=%d", len(data), len(compressed))
	}
}

func TestDecompress_Gzip(t *testing.T) {
	compressor := NewCompressor("gzip")

	original := []byte("Test data for gzip compression roundtrip! This is a comprehensive test.")
	compressed, err := compressor.Compress(original)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}

	decompressed, err := compressor.Decompress(compressed)
	if err != nil {
		t.Fatalf("Decompress() error = %v", err)
	}

	if !bytes.Equal(original, decompressed) {
		t.Errorf("roundtrip failed:\n  got:      %q\n  expected: %q", decompressed, original)
	}
}

func TestDecompress_Snappy(t *testing.T) {
	compressor := NewCompressor("snappy")

	original := []byte("Test data for snappy compression roundtrip! This is a comprehensive test.")
	compressed, err := compressor.Compress(original)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}

	decompressed, err := compressor.Decompress(compressed)
	if err != nil {
		t.Fatalf("Decompress() error = %v", err)
	}

	if !bytes.Equal(original, decompressed) {
		t.Errorf("roundtrip failed:\n  got:      %q\n  expected: %q", decompressed, original)
	}
}

func TestCompress_InvalidAlgorithm(t *testing.T) {
	compressor := NewCompressor("invalid")
	if compressor != nil {
		t.Error("expected nil compressor for invalid algorithm")
	}
}

func TestCompress_EmptyData(t *testing.T) {
	compressor := NewCompressor("gzip")

	compressed, err := compressor.Compress([]byte{})
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if len(compressed) != 0 {
		t.Errorf("expected empty compressed data, got %d bytes", len(compressed))
	}

	// Decompress empty
	decompressed, err := compressor.Decompress([]byte{})
	if err != nil {
		t.Fatalf("Decompress() error = %v", err)
	}
	if len(decompressed) != 0 {
		t.Errorf("expected empty decompressed data, got %d bytes", len(decompressed))
	}
}

func TestCompress_LargeData(t *testing.T) {
	compressor := NewCompressor("gzip")

	// Create 1MB of repeated data
	original := bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. "), 5000)

	compressed, err := compressor.Compress(original)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}

	decompressed, err := compressor.Decompress(compressed)
	if err != nil {
		t.Fatalf("Decompress() error = %v", err)
	}

	if !bytes.Equal(original, decompressed) {
		t.Error("large data roundtrip failed")
	}

	// Compression ratio should be significant for repeated data
	ratio := float64(len(compressed)) / float64(len(original))
	if ratio > 0.5 {
		t.Logf("Warning: compression ratio is %f (expected < 0.5 for repeated data)", ratio)
	}
}

func TestCompress_SnappyVsGzip(t *testing.T) {
	data := []byte("This is sample data for comparison between snappy and gzip compression algorithms.")

	gzipComp := NewCompressor("gzip")
	snappyComp := NewCompressor("snappy")

	gzipOut, _ := gzipComp.Compress(data)
	snappyOut, _ := snappyComp.Compress(data)

	t.Logf("Gzip:   %d bytes", len(gzipOut))
	t.Logf("Snappy: %d bytes", len(snappyOut))

	// Both should be smaller than original
	if len(gzipOut) >= len(data) {
		t.Error("gzip should compress")
	}
	if len(snappyOut) >= len(data) {
		t.Error("snappy should compress")
	}
}
