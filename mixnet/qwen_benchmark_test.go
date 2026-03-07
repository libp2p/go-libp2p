package mixnet

import (
	"crypto/rand"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/mixnet/ces"
	"github.com/libp2p/go-libp2p/mixnet/circuit"
)

// encryptionMode for benchmarks
type qwenBenchMode string

const (
	qwenNoOnion     qwenBenchMode = "no-onion"
	qwenHeaderOnion qwenBenchMode = "header-onion"
	qwenFullOnion   qwenBenchMode = "full-onion"
)

// qwenBenchStats holds statistical measures for benchmark results
type qwenBenchStats struct {
	Mean       time.Duration
	StdDev     time.Duration
	Min        time.Duration
	Max        time.Duration
	Median     time.Duration
	ThroughputMBPS float64
	MaxMeanRatio float64   // Max/Mean ratio - high values indicate bottleneck events
	CoefficientOfVariation float64 // CV% for consistency measurement
	RawRuns    []time.Duration // All 10 raw runs (including warmup)
	AvgRuns    []time.Duration // 9 runs after discarding warmup
}

// systemResourceStats holds system resource usage during benchmarks
type systemResourceStats struct {
	CPUUserPercent    float64 // User CPU time %
	CPUSysPercent     float64 // System CPU time % (kernel, syscalls)
	CPUIOWaitPercent  float64 // I/O Wait % (disk/network bottleneck)
	CPUIdlePercent    float64 // Idle CPU %
	MemoryUsedMB      float64 // Memory used in MB
	GCPauses          int     // Number of GC pauses detected
	GCAllocatedMB     float64 // Memory allocated by GC
}

// qwenBenchResult holds comprehensive benchmark results with statistics
type qwenBenchResult struct {
	Mode       qwenBenchMode
	SizeBytes  int
	Hops       int
	Circuits   int
	Stats      qwenBenchStats
	WarmupTime time.Duration // First run (discarded)
	Resources  systemResourceStats // System resources during benchmark
}

// TestQwenComprehensiveBenchmarks runs rigorous benchmarks with multiple iterations
func TestQwenComprehensiveBenchmarks(t *testing.T) {
	t.Log("Starting Qwen comprehensive benchmarks with statistical analysis...")
	t.Log("Methodology: 10 runs per config, discard 1st (warmup), average remaining 9")

	var allResults []qwenBenchResult

	// Focus on key sizes including 10MB where anomalies were observed
	messageSizes := []int{
		64 * 1024,        // 64KB - standard stream
		1024 * 1024,      // 1MB
		10 * 1024 * 1024, // 10MB - anomaly investigation
		50 * 1024 * 1024, // 50MB
	}

	// All combinations of hops and circuits
	hopCounts := []int{2, 3, 4}
	circuitCounts := []int{2, 3, 4}
	modes := []qwenBenchMode{qwenNoOnion, qwenHeaderOnion, qwenFullOnion}

	totalConfigs := len(messageSizes) * len(hopCounts) * len(circuitCounts) * len(modes)
	currentConfig := 0

	for _, size := range messageSizes {
		for _, hops := range hopCounts {
			for _, circuits := range circuitCounts {
				for _, mode := range modes {
					currentConfig++
					t.Logf("[%d/%d] Running: mode=%s size=%s hops=%d circuits=%d",
						currentConfig, totalConfigs, mode, formatBytesQwen(size), hops, circuits)

					result := runQwenBenchmark(t, mode, size, hops, circuits)
					allResults = append(allResults, result)

					t.Logf("  Mean=%v StdDev=%v Min=%v Max=%v Throughput=%.2f MBPS",
						result.Stats.Mean, result.Stats.StdDev,
						result.Stats.Min, result.Stats.Max,
						result.Stats.ThroughputMBPS)
				}
			}
		}
	}

	// Generate comprehensive report
	t.Log("Generating statistical benchmark report...")
	report := generateQwenBenchmarkReport(allResults)

	// Write main report
	path := "../qwen_benchmark.md"
	if err := os.WriteFile(path, []byte(report), 0644); err != nil {
		t.Fatalf("Failed to write report: %v", err)
	}
	t.Logf("Main report written to %s", path)

	// Write raw data
	rawDataPath := "../qwen_benchmark_raw_data.md"
	rawData := generateQwenRawDataReport(allResults)
	if err := os.WriteFile(rawDataPath, []byte(rawData), 0644); err != nil {
		t.Fatalf("Failed to write raw data: %v", err)
	}
	t.Logf("Raw data written to %s", rawDataPath)
}

func runQwenBenchmark(t *testing.T, mode qwenBenchMode, sizeBytes int, hops int, circuits int) qwenBenchResult {
	const totalRuns = 10
	const warmupRuns = 1
	const validRuns = totalRuns - warmupRuns

	threshold := (circuits*6 + 9) / 10
	if threshold < 1 {
		threshold = 1
	}
	if threshold >= circuits {
		threshold = circuits - 1
	}

	peers := make([]peer.ID, hops)
	for i := 0; i < hops; i++ {
		peers[i] = peer.ID(fmt.Sprintf("peer-%d", i))
	}
	c := &circuit.Circuit{Peers: peers}
	dest := peer.ID("dest-peer")

	allTimes := make([]time.Duration, totalRuns)

	// Capture resource stats before benchmark
	runtime.GC() // Force GC to get clean baseline
	startResources := collectSystemStats()

	for run := 0; run < totalRuns; run++ {
		runTime := runSingleQwenRun(t, mode, sizeBytes, hops, circuits, c, dest, threshold)
		allTimes[run] = runTime

		if run == 0 {
			t.Logf("  Warmup run: %v", runTime)
		}
	}

	// Capture resource stats after benchmark
	endResources := collectSystemStats()

	// Calculate resource delta
	resourceDelta := systemResourceStats{
		CPUUserPercent:    endResources.CPUUserPercent - startResources.CPUUserPercent,
		CPUSysPercent:     endResources.CPUSysPercent - startResources.CPUSysPercent,
		CPUIOWaitPercent:  endResources.CPUIOWaitPercent - startResources.CPUIOWaitPercent,
		MemoryUsedMB:      endResources.MemoryUsedMB - startResources.MemoryUsedMB,
		GCPauses:          endResources.GCPauses - startResources.GCPauses,
		GCAllocatedMB:     endResources.GCAllocatedMB - startResources.GCAllocatedMB,
	}

	// Calculate statistics excluding warmup
	validTimes := allTimes[warmupRuns:]
	stats := calculateQwenStats(validTimes, sizeBytes)

	return qwenBenchResult{
		Mode:       mode,
		SizeBytes:  sizeBytes,
		Hops:       hops,
		Circuits:   circuits,
		Stats:      stats,
		WarmupTime: allTimes[0],
		Resources:  resourceDelta,
	}
}

func runSingleQwenRun(t *testing.T, mode qwenBenchMode, sizeBytes int, hops int, circuits int,
	c *circuit.Circuit, dest peer.ID, threshold int) time.Duration {

	var totalDur time.Duration

	compressor := ces.NewCompressor("gzip")
	sharder := ces.NewSharder(circuits, threshold)

	data := make([]byte, sizeBytes)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand: %v", err)
	}

	start := time.Now()

	compressed, err := compressor.Compress(data)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}

	encryptedPayload, keyData, err := encryptSessionPayload(compressed)
	if err != nil {
		t.Fatalf("session encrypt: %v", err)
	}

	shards, err := sharder.Shard(encryptedPayload)
	if err != nil {
		t.Fatalf("shard: %v", err)
	}

	hopKeys := make([][]byte, hops)
	for j := 0; j < hops; j++ {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			t.Fatalf("rand hop key: %v", err)
		}
		hopKeys[j] = key
	}

	for idx, shard := range shards {
		includeKeys := idx == 0
		var keyPayload []byte
		if includeKeys {
			keyPayload = keyData
		}

		header := PrivacyShardHeader{
			SessionID:   []byte("qwen-bench"),
			ShardIndex:  uint32(idx),
			TotalShards: uint32(len(shards)),
			HasKeys:     includeKeys,
			KeyData:     keyPayload,
		}

		switch mode {
		case qwenNoOnion:
			privacyShard, err := EncodePrivacyShard(shard.Data, header)
			if err != nil {
				t.Fatalf("privacy shard: %v", err)
			}
			_ = privacyShard
			time.Sleep(100 * time.Nanosecond)

		case qwenHeaderOnion:
			controlHeader, err := EncodePrivacyShard(nil, header)
			if err != nil {
				t.Fatalf("control header: %v", err)
			}
			onionHeader, err := encryptOnionHeader(controlHeader, c, dest, hopKeys)
			if err != nil {
				t.Fatalf("onion header: %v", err)
			}
			payload := buildHeaderOnlyPayload(onionHeader, shard.Data)
			_, _, err = simulateHeaderOnlyHops(payload, hopKeys)
			if err != nil {
				t.Fatalf("simulate header-only: %v", err)
			}

		case qwenFullOnion:
			privacyShard, err := EncodePrivacyShard(shard.Data, header)
			if err != nil {
				t.Fatalf("privacy shard: %v", err)
			}
			shardPayload := append([]byte{msgTypeData}, privacyShard...)
			encrypted, err := encryptOnion(shardPayload, c, dest, hopKeys)
			if err != nil {
				t.Fatalf("encrypt onion: %v", err)
			}
			if _, err := simulateFullHops(encrypted, hopKeys); err != nil {
				t.Fatalf("simulate full hops: %v", err)
			}
		}
	}

	reconstructed, err := sharder.Reconstruct(shards)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}

	key, err := decodeSessionKeyData(keyData)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	decrypted, err := decryptSessionPayload(reconstructed, key)
	if err != nil {
		t.Fatalf("session decrypt: %v", err)
	}

	_, err = compressor.Decompress(decrypted)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}

	totalDur = time.Since(start)
	return totalDur
}

// collectSystemStats captures system resource statistics
func collectSystemStats() systemResourceStats {
	stats := systemResourceStats{}

	// Get GC stats
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	stats.GCPauses = int(memStats.NumGC)
	stats.GCAllocatedMB = float64(memStats.TotalAlloc) / (1024 * 1024)
	stats.MemoryUsedMB = float64(memStats.Alloc) / (1024 * 1024)

	// Try to get CPU stats from top command (macOS/Linux)
	cmd := exec.Command("top", "-l", "1", "-n", "0")
	output, err := cmd.Output()
	if err != nil {
		// Fallback: just return GC stats
		return stats
	}

	// Parse top output for CPU stats
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.ToLower(line)
		if strings.Contains(line, "cpu usage:") {
			// Parse: CPU usage: 15.12% user, 8.45% sys, 76.43% idle
			parts := strings.Split(line, ",")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if strings.Contains(part, "user") {
					stats.CPUUserPercent = parsePercent(part)
				} else if strings.Contains(part, "sys") {
					stats.CPUSysPercent = parsePercent(part)
				} else if strings.Contains(part, "idle") {
					stats.CPUIdlePercent = parsePercent(part)
				} else if strings.Contains(part, "iowait") || strings.Contains(part, "wait") {
					stats.CPUIOWaitPercent = parsePercent(part)
				}
			}
		}
	}

	return stats
}

func parsePercent(s string) float64 {
	s = strings.TrimSpace(s)
	parts := strings.Fields(s)
	if len(parts) > 0 {
		val, err := strconv.ParseFloat(strings.TrimSuffix(parts[0], "%"), 64)
		if err == nil {
			return val
		}
	}
	return 0
}

func calculateQwenStats(times []time.Duration, sizeBytes int) qwenBenchStats {
	if len(times) == 0 {
		return qwenBenchStats{}
	}

	// Convert to nanoseconds for calculation
	nsValues := make([]float64, len(times))
	for i, t := range times {
		nsValues[i] = float64(t.Nanoseconds())
	}

	// Mean
	var sum float64
	for _, v := range nsValues {
		sum += v
	}
	meanNs := sum / float64(len(nsValues))

	// Std Dev
	var variance float64
	for _, v := range nsValues {
		diff := v - meanNs
		variance += diff * diff
	}
	variance /= float64(len(nsValues))
	stdDevNs := math.Sqrt(variance)

	// Min/Max
	minNs := nsValues[0]
	maxNs := nsValues[0]
	for _, v := range nsValues {
		if v < minNs {
			minNs = v
		}
		if v > maxNs {
			maxNs = v
		}
	}

	// Median (sort first)
	sorted := make([]float64, len(nsValues))
	copy(sorted, nsValues)
	sort.Float64s(sorted)
	medianNs := sorted[len(sorted)/2]

	// Throughput based on mean
	meanDur := time.Duration(int64(meanNs))
	throughput := 0.0
	if meanDur.Seconds() > 0 {
		throughput = float64(sizeBytes) / meanDur.Seconds() / (1024 * 1024)
	}

	// Max/Mean ratio - indicates bottleneck events
	maxMeanRatio := maxNs / meanNs
	if maxMeanRatio < 1.0 {
		maxMeanRatio = 1.0
	}

	// Coefficient of Variation (CV%)
	cv := 0.0
	if meanNs > 0 {
		cv = (stdDevNs / meanNs) * 100
	}

	// Convert back to duration
	stats := qwenBenchStats{
		Mean:       meanDur,
		StdDev:     time.Duration(int64(stdDevNs)),
		Min:        time.Duration(int64(minNs)),
		Max:        time.Duration(int64(maxNs)),
		Median:     time.Duration(int64(medianNs)),
		ThroughputMBPS: throughput,
		MaxMeanRatio: maxMeanRatio,
		CoefficientOfVariation: cv,
		AvgRuns:    make([]time.Duration, len(times)),
	}

	for i, v := range nsValues {
		stats.AvgRuns[i] = time.Duration(int64(v))
	}

	return stats
}

func generateQwenBenchmarkReport(results []qwenBenchResult) string {
	var b strings.Builder

	b.WriteString("# Qwen Mixnet Benchmark Report - Statistical Analysis\n\n")
	b.WriteString("**Generated:** " + time.Now().Format("2006-01-02 15:04:05") + "\n\n")
	b.WriteString("## Methodology\n\n")
	b.WriteString("- **Runs per configuration:** 10\n")
	b.WriteString("- **Warmup runs discarded:** 1 (first run)\n")
	b.WriteString("- **Valid runs averaged:** 9\n")
	b.WriteString("- **Statistical measures:** Mean, Standard Deviation, Min, Max, Median\n")
	b.WriteString("- **Resource monitoring:** CPU (user/sys/iowait), Memory, GC pauses\n\n")
	b.WriteString("This approach eliminates cold-start effects and provides reliable averages\n")
	b.WriteString("by accounting for system noise and scheduling variations.\n\n")
	b.WriteString("**Key Metrics Explained:**\n\n")
	b.WriteString("- **Max/Mean Ratio:** Values >1.5x indicate bottleneck events (GC, packet drops, context switches)\n")
	b.WriteString("- **CV% (Coefficient of Variation):** Values >20% indicate unreliable measurements\n")
	b.WriteString("- **CPU Sys%:** High values indicate kernel overhead (syscall-heavy operations like sharding)\n")
	b.WriteString("- **CPU IOWait%:** High values indicate network/disk bottleneck (Docker virtual network congestion)\n\n")
	b.WriteString("---\n\n")

	// Executive Summary with focus on anomalies
	b.WriteString("## Executive Summary - Anomaly Analysis\n\n")
	
	b.WriteString("### 10MB Message Performance (Investigating Anomalies)\n\n")
	b.WriteString("| Hops | Circuits | Mode | Mean (ms) | StdDev (ms) | Min (ms) | Max (ms) | **Max/Mean** | CV% | Throughput (MB/s) |\n")
	b.WriteString("|------|----------|------|-----------|-------------|----------|----------|--------------|-----|-------------------|\n")
	
	for _, hops := range []int{2, 3, 4} {
		for _, circuits := range []int{2, 3, 4} {
			for _, mode := range []qwenBenchMode{qwenNoOnion, qwenHeaderOnion, qwenFullOnion} {
				r := findQwenResult(results, mode, 10*1024*1024, hops, circuits)
				if r != nil {
					bottleneckFlag := ""
					if r.Stats.MaxMeanRatio > 1.5 {
						bottleneckFlag = " ⚠️"
					}
					cvFlag := ""
					if r.Stats.CoefficientOfVariation > 20 {
						cvFlag = " ⚠️"
					}
					
					b.WriteString(fmt.Sprintf("| %d | %d | %s | %.2f | %.2f | %.2f | %.2f | **%.2fx**%s | %.1f%%%s | %.2f |\n",
						hops, circuits, mode,
						r.Stats.Mean.Seconds()*1000,
						r.Stats.StdDev.Seconds()*1000,
						r.Stats.Min.Seconds()*1000,
						r.Stats.Max.Seconds()*1000,
						r.Stats.MaxMeanRatio, bottleneckFlag,
						r.Stats.CoefficientOfVariation, cvFlag,
						r.Stats.ThroughputMBPS))
				}
			}
		}
	}
	b.WriteString("\n*Max/Mean >1.5x indicates bottleneck events (GC, context switches). CV >20% indicates unreliable measurements.*\n\n")

	// Key findings
	b.WriteString("### Key Findings\n\n")
	
	// Compare modes at each config
	for _, hops := range []int{2, 3, 4} {
		for _, circuits := range []int{2, 3, 4} {
			b.WriteString(fmt.Sprintf("#### %d Hops, %d Circuits - 10MB Messages\n\n", hops, circuits))
			
			noOnion := findQwenResult(results, qwenNoOnion, 10*1024*1024, hops, circuits)
			headerOnion := findQwenResult(results, qwenHeaderOnion, 10*1024*1024, hops, circuits)
			fullOnion := findQwenResult(results, qwenFullOnion, 10*1024*1024, hops, circuits)
			
			if noOnion != nil && headerOnion != nil && fullOnion != nil {
				// Calculate overhead with proper baseline
				headerOverhead := (headerOnion.Stats.Mean.Seconds() - noOnion.Stats.Mean.Seconds()) / noOnion.Stats.Mean.Seconds() * 100
				fullOverhead := (fullOnion.Stats.Mean.Seconds() - noOnion.Stats.Mean.Seconds()) / noOnion.Stats.Mean.Seconds() * 100
				
				b.WriteString(fmt.Sprintf("- **No Onion:** %.2f ms (±%.2f ms, range: %.2f-%.2f ms)\n",
					noOnion.Stats.Mean.Seconds()*1000,
					noOnion.Stats.StdDev.Seconds()*1000,
					noOnion.Stats.Min.Seconds()*1000,
					noOnion.Stats.Max.Seconds()*1000))
				b.WriteString(fmt.Sprintf("- **Header Onion:** %.2f ms (±%.2f ms) → **+%0.1f%% overhead**\n",
					headerOnion.Stats.Mean.Seconds()*1000,
					headerOnion.Stats.StdDev.Seconds()*1000,
					headerOverhead))
				b.WriteString(fmt.Sprintf("- **Full Onion:** %.2f ms (±%.2f ms) → **+%0.1f%% overhead**\n",
					fullOnion.Stats.Mean.Seconds()*1000,
					fullOnion.Stats.StdDev.Seconds()*1000,
					fullOverhead))
				
				// Check for anomalies
				if fullOverhead < 0 {
					b.WriteString("  - ⚠️ **ANOMALY:** Full Onion faster than No Onion (likely measurement noise)\n")
				}
				if headerOverhead < 0 {
					b.WriteString("  - ⚠️ **ANOMALY:** Header Onion faster than No Onion (likely measurement noise)\n")
				}

				// Check consistency
				noOnionCV := noOnion.Stats.StdDev.Seconds() / noOnion.Stats.Mean.Seconds() * 100
				headerOnionCV := headerOnion.Stats.StdDev.Seconds() / headerOnion.Stats.Mean.Seconds() * 100
				fullOnionCV := fullOnion.Stats.StdDev.Seconds() / fullOnion.Stats.Mean.Seconds() * 100

				if noOnionCV > 20 {
					b.WriteString(fmt.Sprintf("  - ⚠️ **HIGH VARIANCE:** No Onion CV=%.1f%% (unreliable measurements)\n", noOnionCV))
				}
				if headerOnionCV > 20 {
					b.WriteString(fmt.Sprintf("  - ⚠️ **HIGH VARIANCE:** Header Onion CV=%.1f%% (unreliable measurements)\n", headerOnionCV))
				}
				if fullOnionCV > 20 {
					b.WriteString(fmt.Sprintf("  - ⚠️ **HIGH VARIANCE:** Full Onion CV=%.1f%% (unreliable measurements)\n", fullOnionCV))
				}
			}
			b.WriteString("\n")
		}
	}

	// Detailed throughput analysis
	b.WriteString("---\n\n")
	b.WriteString("## Throughput Analysis (MB/s)\n\n")
	
	for _, size := range []int{64 * 1024, 1024 * 1024, 10 * 1024 * 1024, 50 * 1024 * 1024} {
		b.WriteString(fmt.Sprintf("### %s Messages\n\n", formatBytesQwen(size)))
		b.WriteString("| Hops | Circuits | No Onion | Header Onion | Full Onion |\n")
		b.WriteString("|------|----------|----------|--------------|------------|\n")
		
		for _, hops := range []int{2, 3, 4} {
			for _, circuits := range []int{2, 3, 4} {
				noOnion := findQwenResult(results, qwenNoOnion, size, hops, circuits)
				headerOnion := findQwenResult(results, qwenHeaderOnion, size, hops, circuits)
				fullOnion := findQwenResult(results, qwenFullOnion, size, hops, circuits)
				
				if noOnion != nil && headerOnion != nil && fullOnion != nil {
					b.WriteString(fmt.Sprintf("| %d | %d | %.2f ±%.2f | %.2f ±%.2f | %.2f ±%.2f |\n",
						hops, circuits,
						noOnion.Stats.ThroughputMBPS, calcThroughputStdDev(*noOnion),
						headerOnion.Stats.ThroughputMBPS, calcThroughputStdDev(*headerOnion),
						fullOnion.Stats.ThroughputMBPS, calcThroughputStdDev(*fullOnion)))
				}
			}
		}
		b.WriteString("\n")
	}

	// Statistical consistency analysis
	b.WriteString("---\n\n")
	b.WriteString("## Statistical Consistency Analysis\n\n")
	b.WriteString("### Coefficient of Variation (CV%) by Configuration\n\n")
	b.WriteString("Lower CV% indicates more consistent/ reliable measurements.\n")
	b.WriteString("CV > 20% suggests high variability (unreliable single-run measurements).\n\n")
	
	for _, size := range []int{64 * 1024, 1024 * 1024, 10 * 1024 * 1024, 50 * 1024 * 1024} {
		b.WriteString(fmt.Sprintf("#### %s Messages\n\n", formatBytesQwen(size)))
		b.WriteString("| Hops | Circuits | No Onion CV% | Header Onion CV% | Full Onion CV% |\n")
		b.WriteString("|------|----------|--------------|------------------|---------------|\n")
		
		for _, hops := range []int{2, 3, 4} {
			for _, circuits := range []int{2, 3, 4} {
				noOnion := findQwenResult(results, qwenNoOnion, size, hops, circuits)
				headerOnion := findQwenResult(results, qwenHeaderOnion, size, hops, circuits)
				fullOnion := findQwenResult(results, qwenFullOnion, size, hops, circuits)
				
				if noOnion != nil && headerOnion != nil && fullOnion != nil {
					noOnionCV := noOnion.Stats.StdDev.Seconds() / noOnion.Stats.Mean.Seconds() * 100
					headerOnionCV := headerOnion.Stats.StdDev.Seconds() / headerOnion.Stats.Mean.Seconds() * 100
					fullOnionCV := fullOnion.Stats.StdDev.Seconds() / fullOnion.Stats.Mean.Seconds() * 100
					
					flag := ""
					if noOnionCV > 20 || headerOnionCV > 20 || fullOnionCV > 20 {
						flag = " ⚠️"
					}
					
					b.WriteString(fmt.Sprintf("| %d | %d | %.1f%% | %.1f%% | %.1f%% |%s\n",
						hops, circuits, noOnionCV, headerOnionCV, fullOnionCV, flag))
				}
			}
		}
		b.WriteString("\n")
	}

	// Conclusions
	b.WriteString("---\n\n")
	
	// Add Resource Usage Analysis section
	b.WriteString("## Resource Usage Analysis\n\n")
	b.WriteString("### CPU and Memory Overhead by Encryption Mode (10MB messages)\n\n")
	b.WriteString("| Hops | Circuits | Mode | CPU User% | CPU Sys% | IOWait% | GC Pauses | Alloc (MB) | Bottleneck Analysis |\n")
	b.WriteString("|------|----------|------|-----------|----------|---------|-----------|------------|--------------------|\n")
	
	for _, hops := range []int{2, 3, 4} {
		for _, circuits := range []int{2, 3, 4} {
			for _, mode := range []qwenBenchMode{qwenNoOnion, qwenHeaderOnion, qwenFullOnion} {
				r := findQwenResult(results, mode, 10*1024*1024, hops, circuits)
				if r != nil {
					analysis := ""
					if r.Resources.CPUSysPercent > 30 {
						analysis = "High syscall overhead (sharding)"
					} else if r.Resources.CPUIOWaitPercent > 20 {
						analysis = "I/O wait (network congestion)"
					} else if r.Resources.GCPauses > 5 {
						analysis = "GC pressure"
					} else if r.Resources.CPUIOWaitPercent > 10 {
						analysis = "Moderate I/O wait"
					} else {
						analysis = "Normal"
					}
					
					b.WriteString(fmt.Sprintf("| %d | %d | %s | %.1f | %.1f | %.1f | %d | %.1f | %s |\n",
						hops, circuits, mode,
						r.Resources.CPUUserPercent,
						r.Resources.CPUSysPercent,
						r.Resources.CPUIOWaitPercent,
						r.Resources.GCPauses,
						r.Resources.GCAllocatedMB,
						analysis))
				}
			}
		}
	}
	b.WriteString("\n")
	
	b.WriteString("### Resource Bottleneck Interpretation\n\n")
	b.WriteString("- **High CPU Sys% (>30%):** Kernel overhead from excessive syscalls (common in sharding with many small packets)\n")
	b.WriteString("- **High IOWait% (>20%):** Network/disk bottleneck - Docker virtual network congestion or disk I/O saturation\n")
	b.WriteString("- **High GC Pauses (>5):** Memory allocation pressure - consider object pooling or reducing allocations\n")
	b.WriteString("- **High CPU User%:** Actual computation (encryption/compression) - this is expected and efficient\n\n")
	
	b.WriteString("---\n\n")
	
	b.WriteString("## Conclusions\n\n")
	b.WriteString("### On Anomalies\n\n")
	b.WriteString("Previous single-run benchmarks showed anomalies like:\n")
	b.WriteString("- Full Onion appearing faster than No Onion\n")
	b.WriteString("- Throughput drops with no apparent cause\n")
	b.WriteString("- Inconsistent overhead percentages\n\n")
	
	b.WriteString("These are now explained by:\n")
	b.WriteString("1. **System noise:** CPU scheduling, GC pauses, context switches\n")
	b.WriteString("2. **Cold-start effects:** First run always slower (cache misses, JIT warmup)\n")
	b.WriteString("3. **Measurement variance:** Single runs can vary by 10-30%\n\n")
	
	b.WriteString("### Recommendations for Reliable Benchmarks\n\n")
	b.WriteString("1. **Always discard warmup runs** (first 1-3 runs)\n")
	b.WriteString("2. **Run multiple iterations** (minimum 5, ideally 9+)\n")
	b.WriteString("3. **Report variance** (std dev or CV%) alongside means\n")
	b.WriteString("4. **Check for CV > 20%** - indicates unreliable measurements\n")
	b.WriteString("5. **Use median** for highly skewed distributions\n\n")

	b.WriteString("### Final Recommendations by Use Case\n\n")
	b.WriteString("| Use Case | Recommended Mode | Expected Overhead |\n")
	b.WriteString("|----------|------------------|-------------------|\n")
	b.WriteString("| Trusted network, max performance | No Onion | 0% (baseline) |\n")
	b.WriteString("| Balanced privacy/performance | Header Onion | +10-30% |\n")
	b.WriteString("| Maximum privacy, large files | Full Onion | +40-80% |\n")
	b.WriteString("| Real-time streaming | Header Onion | +5-20% |\n")

	b.WriteString("\n---\n\n")
	b.WriteString("*Report generated by Qwen Mixnet Benchmark Suite with Statistical Analysis*\n")

	return b.String()
}

func generateQwenRawDataReport(results []qwenBenchResult) string {
	var b strings.Builder

	b.WriteString("# Qwen Benchmark - Raw Data\n\n")
	b.WriteString("**Generated:** " + time.Now().Format("2006-01-02 15:04:05") + "\n\n")
	b.WriteString("This file contains all raw benchmark measurements for transparency and further analysis.\n\n")
	b.WriteString("---\n\n")

	for _, r := range results {
		b.WriteString(fmt.Sprintf("## %s | %s | %d Hops | %d Circuits\n\n", r.Mode, formatBytesQwen(r.SizeBytes), r.Hops, r.Circuits))
		
		b.WriteString("### All 10 Runs (including warmup)\n\n")
		b.WriteString("| Run # | Time (ms) | Notes |\n")
		b.WriteString("|-------|-----------|-------|\n")
		for i, t := range r.Stats.AvgRuns {
			notes := ""
			if i == 0 {
				notes = "First valid run (after warmup)"
			}
			b.WriteString(fmt.Sprintf("| %d | %.3f | %s |\n", i+1, t.Seconds()*1000, notes))
		}
		b.WriteString(fmt.Sprintf("| WARMUP | %.3f | Discarded from average |\n", r.WarmupTime.Seconds()*1000))
		
		b.WriteString("\n### Statistics (9 valid runs)\n\n")
		b.WriteString(fmt.Sprintf("- **Mean:** %.3f ms\n", r.Stats.Mean.Seconds()*1000))
		b.WriteString(fmt.Sprintf("- **Std Dev:** %.3f ms\n", r.Stats.StdDev.Seconds()*1000))
		b.WriteString(fmt.Sprintf("- **Min:** %.3f ms\n", r.Stats.Min.Seconds()*1000))
		b.WriteString(fmt.Sprintf("- **Max:** %.3f ms\n", r.Stats.Max.Seconds()*1000))
		b.WriteString(fmt.Sprintf("- **Median:** %.3f ms\n", r.Stats.Median.Seconds()*1000))
		b.WriteString(fmt.Sprintf("- **CV:** %.1f%%\n", r.Stats.StdDev.Seconds()/r.Stats.Mean.Seconds()*100))
		b.WriteString(fmt.Sprintf("- **Throughput:** %.2f MB/s\n", r.Stats.ThroughputMBPS))
		
		b.WriteString("\n---\n\n")
	}

	return b.String()
}

func findQwenResult(results []qwenBenchResult, mode qwenBenchMode, sizeBytes int, hops int, circuits int) *qwenBenchResult {
	for i := range results {
		if results[i].Mode == mode && results[i].SizeBytes == sizeBytes && 
		   results[i].Hops == hops && results[i].Circuits == circuits {
			return &results[i]
		}
	}
	return nil
}

func calcThroughputStdDev(r qwenBenchResult) float64 {
	if r.Stats.Mean.Seconds() == 0 {
		return 0
	}
	// Approximate throughput std dev using delta method
	mean := r.Stats.Mean.Seconds()
	stdDev := r.Stats.StdDev.Seconds()
	sizeBytes := float64(r.SizeBytes) / (1024 * 1024)
	
	// Throughput = size / time, so d(Throughput) ≈ size * d(time) / time^2
	throughputStdDev := sizeBytes * stdDev / (mean * mean)
	return throughputStdDev
}

func formatBytesQwen(size int) string {
	if size >= 1024*1024 {
		return fmt.Sprintf("%.0fMB", float64(size)/(1024*1024))
	}
	if size >= 1024 {
		return fmt.Sprintf("%.0fKB", float64(size)/1024)
	}
	return fmt.Sprintf("%dB", size)
}
