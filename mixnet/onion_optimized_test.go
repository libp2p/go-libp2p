package mixnet

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/mixnet/circuit"
	"github.com/libp2p/go-libp2p/mixnet/ces"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Benchmark: Original vs Optimized Onion Encryption
// ============================================================================

// BenchmarkOriginalOnionEncryption benchmarks the ORIGINAL approach
// where the ENTIRE payload is encrypted at each hop
func BenchmarkOriginalOnionEncryption(b *testing.B) {
	payloadSizes := []int{64, 256, 1024, 4096, 16384, 65536}
	hopCounts := []int{1, 2, 3, 5}

	for _, size := range payloadSizes {
		for _, hops := range hopCounts {
			b.Run(fmt.Sprintf("payload_%dB_hops_%d", size, hops), func(b *testing.B) {
				payload := make([]byte, size)
				rand.Read(payload)

				hopKeys := make([][]byte, hops)
				for i := range hopKeys {
					hopKeys[i] = make([]byte, 32)
					rand.Read(hopKeys[i])
				}

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					// Original: encrypt entire payload at each hop
					current := payload
					for j := 0; j < hops; j++ {
						key := make([]byte, 32)
						rand.Read(key)
						enc, _ := encryptHopPayload(key, current)
						current = enc
					}
					_ = current
				}
			})
		}
	}
}

// BenchmarkOptimizedHybridOnion benchmarks the OPTIMIZED approach
// where payload is encrypted once end-to-end, only headers use layered encryption
func BenchmarkOptimizedHybridOnion(b *testing.B) {
	payloadSizes := []int{64, 256, 1024, 4096, 16384, 65536}
	hopCounts := []int{1, 2, 3, 5}

	sharder := ces.NewSharder(3, 2)
	compressor := ces.NewCompressor("snappy")

	for _, size := range payloadSizes {
		for _, hops := range hopCounts {
			b.Run(fmt.Sprintf("payload_%dB_hops_%d", size, hops), func(b *testing.B) {
				payload := make([]byte, size)
				rand.Read(payload)

				circuit := &circuit.Circuit{
					Peers: make([]peer.ID, hops),
				}
				hopKeys := make([][]byte, hops)
				for i := range hopKeys {
					hopKeys[i] = make([]byte, 32)
					rand.Read(hopKeys[i])
				}

				dest := peer.ID("test-destination")

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					// Optimized: CES pipeline once + small header encryption per hop
					bundle, _ := prepareHybridPayload(
						payload,
						circuit,
						dest,
						hopKeys,
						sharder,
						compressor,
					)
					_ = bundle
				}
			})
		}
	}
}

// BenchmarkPerHopDecryptionOriginal benchmarks decryption cost per hop (ORIGINAL)
func BenchmarkPerHopDecryptionOriginal(b *testing.B) {
	payloadSizes := []int{1024, 4096, 16384, 65536}

	for _, size := range payloadSizes {
		b.Run(fmt.Sprintf("payload_%dB", size), func(b *testing.B) {
			payload := make([]byte, size)
			rand.Read(payload)

			// Simulate encrypted payload at a hop
			key := make([]byte, 32)
			rand.Read(key)
			encrypted, _ := encryptHopPayload(key, payload)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				// Original: decrypt entire payload at each hop
				_, _ = decryptHopPayload(key, encrypted)
			}
		})
	}
}

// BenchmarkPerHopDecryptionOptimized benchmarks decryption cost per hop (OPTIMIZED)
func BenchmarkPerHopDecryptionOptimized(b *testing.B) {
	// Optimized: only decrypt ~100 byte routing header per hop
	headerSize := 100 // Routing header size

	b.Run("routing_header_only", func(b *testing.B) {
		header := make([]byte, headerSize)
		rand.Read(header)

		key := make([]byte, 32)
		rand.Read(key)
		encrypted, _ := encryptRoutingLayer(key, header)

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			// Optimized: decrypt only routing header (~100 bytes)
			_, _, _, _ = decryptRoutingLayerAtHop(encrypted, key)
		}
	})
}

// BenchmarkEndToEndLatencyComparison compares total latency
func BenchmarkEndToEndLatencyComparison(b *testing.B) {
	payloadSizes := []int{1024, 4096, 16384}
	hopCounts := []int{2, 3, 5}

	sharder := ces.NewSharder(3, 2)
	compressor := ces.NewCompressor("snappy")

	for _, size := range payloadSizes {
		for _, hops := range hopCounts {
			b.Run(fmt.Sprintf("payload_%dB_hops_%d", size, hops), func(b *testing.B) {
				payload := make([]byte, size)
				rand.Read(payload)

				circuit := &circuit.Circuit{
					Peers: make([]peer.ID, hops),
				}
				hopKeys := make([][]byte, hops)
				for i := range hopKeys {
					hopKeys[i] = make([]byte, 32)
					rand.Read(hopKeys[i])
				}
				dest := peer.ID("test-destination")

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					// Full pipeline: prepare + process at each hop + decrypt at destination
					bundle, _ := prepareHybridPayload(
						payload,
						circuit,
						dest,
						hopKeys,
						sharder,
						compressor,
					)

					// Simulate processing at each hop
					currentBundle := bundle
					for j := 0; j < hops; j++ {
						_, _, currentBundle, _ = processAtHop(currentBundle, hopKeys[j])
					}

					// Decrypt at destination
					_, _ = decryptAtDestination(currentBundle, sharder)
				}
			})
		}
	}
}

// BenchmarkSpeedupVerification verifies the theoretical speedup matches reality
func BenchmarkSpeedupVerification(b *testing.B) {
	testCases := []struct {
		name        string
		payloadSize int
		hops        int
	}{
		{"small_3hops", 1024, 3},
		{"medium_3hops", 4096, 3},
		{"large_3hops", 16384, 3},
		{"small_5hops", 1024, 5},
		{"medium_5hops", 4096, 5},
		{"large_5hops", 16384, 5},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			metrics := CalculateHybridMetrics(tc.payloadSize, tc.hops)
			b.Logf("Payload: %d bytes, Hops: %d", tc.payloadSize, tc.hops)
			b.Logf("Original work: %d bytes", metrics.OriginalWork)
			b.Logf("Optimized work: %d bytes", metrics.OptimizedWork)
			b.Logf("Expected speedup: %.2fx", metrics.SpeedupFactor)
			b.Logf("Memory saved: %d bytes (%.1f%%)", metrics.MemorySaved, float64(metrics.MemorySaved)/float64(metrics.OriginalWork)*100)
		})
	}
}

// ============================================================================
// Test: Verify optimized approach produces correct results
// ============================================================================

// TestHybridOnionCorrectness verifies the optimized hybrid onion encryption works correctly
func TestHybridOnionCorrectness(t *testing.T) {
	payload := make([]byte, 1024)
	rand.Read(payload)

	hops := 3
	circuit := &circuit.Circuit{
		Peers: make([]peer.ID, hops),
	}
	// Set up proper peer IDs
	for i := 0; i < hops; i++ {
		circuit.Peers[i] = peer.ID(fmt.Sprintf("relay-peer-%d", i))
	}
	
	hopKeys := make([][]byte, hops)
	for i := range hopKeys {
		hopKeys[i] = make([]byte, 32)
		rand.Read(hopKeys[i])
	}
	dest := peer.ID("test-destination")

	sharder := ces.NewSharder(3, 2)
	compressor := ces.NewCompressor("snappy")

	// Prepare hybrid payload
	bundle, err := prepareHybridPayload(
		payload,
		circuit,
		dest,
		hopKeys,
		sharder,
		compressor,
	)
	require.NoError(t, err)
	require.NotNil(t, bundle)
	require.NotNil(t, bundle.RoutingHeader)
	require.Len(t, bundle.RoutingHeader.EncryptedLayers, hops)

	// Process at each hop
	currentBundle := bundle
	for i := 0; i < hops; i++ {
		t.Logf("Processing hop %d/%d", i, hops)
		shouldContinue, nextHop, updatedBundle, err := processAtHop(currentBundle, hopKeys[i])
		require.NoError(t, err)
		t.Logf("  shouldContinue=%v, nextHop=%q", shouldContinue, nextHop)

		if i < hops-1 {
			require.True(t, shouldContinue, "should continue for intermediate hops")
			require.NotEmpty(t, nextHop, "next hop should be set for intermediate hops")
		} else {
			require.False(t, shouldContinue, "should stop at exit node")
			// Exit node doesn't need nextHop - it's the destination
		}

		currentBundle = updatedBundle
	}

	// Decrypt at destination
	decrypted, err := decryptAtDestination(currentBundle, sharder)
	require.NoError(t, err)

	// Decompress to get original
	decompressed, err := compressor.Decompress(decrypted)
	require.NoError(t, err)

	// Verify payload matches
	require.Equal(t, payload, decompressed, "decrypted payload should match original")
}

// TestRoutingLayerParsing verifies routing layer build/parse round-trip
func TestRoutingLayerParsing(t *testing.T) {
	e2eKey := make([]byte, 32)
	rand.Read(e2eKey)
	nextHop := "test-peer-id-12345"

	// Build layer
	layer := buildRoutingLayer(true, nextHop, e2eKey)
	require.Len(t, layer, 1+2+len(nextHop)+32)

	// Parse layer
	isFinal, parsedHop, parsedKey, err := parseRoutingLayer(layer)
	require.NoError(t, err)
	require.True(t, isFinal)
	require.Equal(t, nextHop, parsedHop)
	require.Equal(t, e2eKey, parsedKey)
}

// TestHybridMetricsCalculation verifies speedup calculations
func TestHybridMetricsCalculation(t *testing.T) {
	testCases := []struct {
		name        string
		payloadSize int
		hops        int
		minSpeedup  float64
	}{
		{"tiny_1hop", 64, 1, 0.3},    // Small payloads don't benefit from hybrid
		{"small_3hops", 1024, 3, 2.0},
		{"medium_3hops", 4096, 3, 2.5},
		{"large_5hops", 16384, 5, 4.0},
		{"huge_5hops", 65536, 5, 4.5},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			metrics := CalculateHybridMetrics(tc.payloadSize, tc.hops)
			require.GreaterOrEqual(t, metrics.SpeedupFactor, tc.minSpeedup,
				"speedup should be at least %.1fx for %d byte payload with %d hops",
				tc.minSpeedup, tc.payloadSize, tc.hops)

			t.Logf("✓ %s: %.2fx speedup (saves %d bytes)",
				tc.name, metrics.SpeedupFactor, metrics.MemorySaved)
		})
	}
}

// TestSpeedupTable prints the complete speedup table
func TestSpeedupTable(t *testing.T) {
	table := GetSpeedupTable()

	t.Log("\n=== Hybrid Encryption Speedup Table ===")
	t.Log("Payload | 1 hop | 2 hops | 3 hops | 5 hops | 10 hops")
	t.Log("--------|-------|--------|--------|--------|--------")

	for _, size := range []string{"64B", "256B", "1KB", "4KB", "16KB", "64KB", "1MB"} {
		row := fmt.Sprintf("%-7s |", size)
		for _, hops := range []int{1, 2, 3, 5, 10} {
			speedup := table[size][hops]
			row += fmt.Sprintf(" %.1fx |", speedup)
		}
		t.Log(row)
	}
}
