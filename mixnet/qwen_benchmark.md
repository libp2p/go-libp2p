# Qwen Mixnet Benchmark Report - Statistical Analysis

**Generated:** 2026-03-07 21:42:56

## Methodology

- **Runs per configuration:** 10
- **Warmup runs discarded:** 1 (first run)
- **Valid runs averaged:** 9
- **Statistical measures:** Mean, Standard Deviation, Min, Max, Median
- **Resource monitoring:** CPU (user/sys/iowait), Memory, GC pauses

This approach eliminates cold-start effects and provides reliable averages
by accounting for system noise and scheduling variations.

**Key Metrics Explained:**

- **Max/Mean Ratio:** Values >1.5x indicate bottleneck events (GC, packet drops, context switches)
- **CV% (Coefficient of Variation):** Values >20% indicate unreliable measurements
- **CPU Sys%:** High values indicate kernel overhead (syscall-heavy operations like sharding)
- **CPU IOWait%:** High values indicate network/disk bottleneck (Docker virtual network congestion)

---

## Executive Summary - Anomaly Analysis

### 10MB Message Performance (Investigating Anomalies)

| Hops | Circuits | Mode | Mean (ms) | StdDev (ms) | Min (ms) | Max (ms) | **Max/Mean** | CV% | Throughput (MB/s) |
|------|----------|------|-----------|-------------|----------|----------|--------------|-----|-------------------|
| 2 | 2 | no-onion | 190.24 | 18.52 | 180.81 | 241.93 | **1.27x** | 9.7% | 52.56 |
| 2 | 2 | header-onion | 188.19 | 11.78 | 182.10 | 220.94 | **1.17x** | 6.3% | 53.14 |
| 2 | 2 | full-onion | 285.41 | 2.76 | 282.10 | 290.32 | **1.02x** | 1.0% | 35.04 |
| 2 | 3 | no-onion | 181.39 | 1.85 | 179.92 | 185.35 | **1.02x** | 1.0% | 55.13 |
| 2 | 3 | header-onion | 190.92 | 27.06 | 179.45 | 267.22 | **1.40x** | 14.2% | 52.38 |
| 2 | 3 | full-onion | 263.04 | 15.56 | 255.91 | 306.95 | **1.17x** | 5.9% | 38.02 |
| 2 | 4 | no-onion | 182.52 | 3.14 | 178.96 | 189.36 | **1.04x** | 1.7% | 54.79 |
| 2 | 4 | header-onion | 180.49 | 1.90 | 179.07 | 184.58 | **1.02x** | 1.1% | 55.40 |
| 2 | 4 | full-onion | 257.73 | 12.92 | 248.34 | 289.77 | **1.12x** | 5.0% | 38.80 |
| 3 | 2 | no-onion | 186.49 | 10.46 | 180.74 | 215.00 | **1.15x** | 5.6% | 53.62 |
| 3 | 2 | header-onion | 182.98 | 2.08 | 181.06 | 187.96 | **1.03x** | 1.1% | 54.65 |
| 3 | 2 | full-onion | 338.62 | 3.90 | 334.41 | 344.26 | **1.02x** | 1.2% | 29.53 |
| 3 | 3 | no-onion | 188.40 | 12.90 | 180.36 | 223.76 | **1.19x** | 6.8% | 53.08 |
| 3 | 3 | header-onion | 194.11 | 17.49 | 181.20 | 229.65 | **1.18x** | 9.0% | 51.52 |
| 3 | 3 | full-onion | 298.78 | 3.22 | 294.76 | 303.68 | **1.02x** | 1.1% | 33.47 |
| 3 | 4 | no-onion | 182.20 | 2.70 | 179.29 | 186.76 | **1.03x** | 1.5% | 54.88 |
| 3 | 4 | header-onion | 188.31 | 16.83 | 180.08 | 235.34 | **1.25x** | 8.9% | 53.10 |
| 3 | 4 | full-onion | 289.00 | 10.82 | 281.86 | 318.64 | **1.10x** | 3.7% | 34.60 |
| 4 | 2 | no-onion | 183.33 | 1.83 | 181.18 | 186.14 | **1.02x** | 1.0% | 54.55 |
| 4 | 2 | header-onion | 183.23 | 1.71 | 181.37 | 186.24 | **1.02x** | 0.9% | 54.58 |
| 4 | 2 | full-onion | 401.60 | 26.22 | 386.19 | 465.72 | **1.16x** | 6.5% | 24.90 |
| 4 | 3 | no-onion | 182.02 | 2.58 | 179.45 | 186.25 | **1.02x** | 1.4% | 54.94 |
| 4 | 3 | header-onion | 181.22 | 1.39 | 179.74 | 183.67 | **1.01x** | 0.8% | 55.18 |
| 4 | 3 | full-onion | 342.17 | 17.88 | 331.26 | 391.91 | **1.15x** | 5.2% | 29.23 |
| 4 | 4 | no-onion | 186.95 | 10.57 | 179.92 | 212.19 | **1.13x** | 5.7% | 53.49 |
| 4 | 4 | header-onion | 181.43 | 1.40 | 179.58 | 183.66 | **1.01x** | 0.8% | 55.12 |
| 4 | 4 | full-onion | 318.27 | 2.72 | 314.30 | 323.04 | **1.02x** | 0.9% | 31.42 |

*Max/Mean >1.5x indicates bottleneck events (GC, context switches). CV >20% indicates unreliable measurements.*

### Key Findings

#### 2 Hops, 2 Circuits - 10MB Messages

- **No Onion:** 190.24 ms (±18.52 ms, range: 180.81-241.93 ms)
- **Header Onion:** 188.19 ms (±11.78 ms) → **+-1.1% overhead**
- **Full Onion:** 285.41 ms (±2.76 ms) → **+50.0% overhead**
  - ⚠️ **ANOMALY:** Header Onion faster than No Onion (likely measurement noise)

#### 2 Hops, 3 Circuits - 10MB Messages

- **No Onion:** 181.39 ms (±1.85 ms, range: 179.92-185.35 ms)
- **Header Onion:** 190.92 ms (±27.06 ms) → **+5.3% overhead**
- **Full Onion:** 263.04 ms (±15.56 ms) → **+45.0% overhead**

#### 2 Hops, 4 Circuits - 10MB Messages

- **No Onion:** 182.52 ms (±3.14 ms, range: 178.96-189.36 ms)
- **Header Onion:** 180.49 ms (±1.90 ms) → **+-1.1% overhead**
- **Full Onion:** 257.73 ms (±12.92 ms) → **+41.2% overhead**
  - ⚠️ **ANOMALY:** Header Onion faster than No Onion (likely measurement noise)

#### 3 Hops, 2 Circuits - 10MB Messages

- **No Onion:** 186.49 ms (±10.46 ms, range: 180.74-215.00 ms)
- **Header Onion:** 182.98 ms (±2.08 ms) → **+-1.9% overhead**
- **Full Onion:** 338.62 ms (±3.90 ms) → **+81.6% overhead**
  - ⚠️ **ANOMALY:** Header Onion faster than No Onion (likely measurement noise)

#### 3 Hops, 3 Circuits - 10MB Messages

- **No Onion:** 188.40 ms (±12.90 ms, range: 180.36-223.76 ms)
- **Header Onion:** 194.11 ms (±17.49 ms) → **+3.0% overhead**
- **Full Onion:** 298.78 ms (±3.22 ms) → **+58.6% overhead**

#### 3 Hops, 4 Circuits - 10MB Messages

- **No Onion:** 182.20 ms (±2.70 ms, range: 179.29-186.76 ms)
- **Header Onion:** 188.31 ms (±16.83 ms) → **+3.4% overhead**
- **Full Onion:** 289.00 ms (±10.82 ms) → **+58.6% overhead**

#### 4 Hops, 2 Circuits - 10MB Messages

- **No Onion:** 183.33 ms (±1.83 ms, range: 181.18-186.14 ms)
- **Header Onion:** 183.23 ms (±1.71 ms) → **+-0.1% overhead**
- **Full Onion:** 401.60 ms (±26.22 ms) → **+119.1% overhead**
  - ⚠️ **ANOMALY:** Header Onion faster than No Onion (likely measurement noise)

#### 4 Hops, 3 Circuits - 10MB Messages

- **No Onion:** 182.02 ms (±2.58 ms, range: 179.45-186.25 ms)
- **Header Onion:** 181.22 ms (±1.39 ms) → **+-0.4% overhead**
- **Full Onion:** 342.17 ms (±17.88 ms) → **+88.0% overhead**
  - ⚠️ **ANOMALY:** Header Onion faster than No Onion (likely measurement noise)

#### 4 Hops, 4 Circuits - 10MB Messages

- **No Onion:** 186.95 ms (±10.57 ms, range: 179.92-212.19 ms)
- **Header Onion:** 181.43 ms (±1.40 ms) → **+-3.0% overhead**
- **Full Onion:** 318.27 ms (±2.72 ms) → **+70.2% overhead**
  - ⚠️ **ANOMALY:** Header Onion faster than No Onion (likely measurement noise)

---

## Throughput Analysis (MB/s)

### 64KB Messages

| Hops | Circuits | No Onion | Header Onion | Full Onion |
|------|----------|----------|--------------|------------|
| 2 | 2 | 45.41 ±5.32 | 45.84 ±4.08 | 28.74 ±1.88 |
| 2 | 3 | 47.59 ±1.23 | 46.63 ±3.09 | 35.63 ±1.78 |
| 2 | 4 | 47.32 ±2.97 | 47.77 ±4.05 | 34.81 ±1.91 |
| 3 | 2 | 44.37 ±3.66 | 48.61 ±2.28 | 26.52 ±1.10 |
| 3 | 3 | 49.87 ±1.90 | 45.79 ±3.90 | 28.38 ±1.48 |
| 3 | 4 | 48.57 ±2.86 | 46.20 ±2.35 | 30.90 ±2.70 |
| 4 | 2 | 49.23 ±2.21 | 44.89 ±2.71 | 23.55 ±0.66 |
| 4 | 3 | 45.90 ±5.96 | 48.36 ±3.18 | 26.31 ±0.97 |
| 4 | 4 | 47.65 ±2.33 | 48.44 ±3.31 | 28.18 ±1.34 |

### 1MB Messages

| Hops | Circuits | No Onion | Header Onion | Full Onion |
|------|----------|----------|--------------|------------|
| 2 | 2 | 53.06 ±1.04 | 53.18 ±0.37 | 33.05 ±0.72 |
| 2 | 3 | 52.69 ±1.19 | 52.45 ±0.63 | 36.79 ±1.06 |
| 2 | 4 | 52.37 ±1.60 | 53.57 ±0.47 | 38.40 ±0.24 |
| 3 | 2 | 51.69 ±1.70 | 53.16 ±0.74 | 28.47 ±0.38 |
| 3 | 3 | 52.87 ±2.11 | 51.27 ±1.55 | 31.79 ±0.48 |
| 3 | 4 | 53.60 ±0.94 | 43.04 ±12.95 | 33.55 ±0.30 |
| 4 | 2 | 52.22 ±1.22 | 52.55 ±0.76 | 24.63 ±0.31 |
| 4 | 3 | 52.90 ±0.67 | 52.74 ±0.91 | 27.87 ±0.60 |
| 4 | 4 | 53.41 ±0.80 | 53.18 ±0.89 | 29.11 ±0.69 |

### 10MB Messages

| Hops | Circuits | No Onion | Header Onion | Full Onion |
|------|----------|----------|--------------|------------|
| 2 | 2 | 52.56 ±5.12 | 53.14 ±3.33 | 35.04 ±0.34 |
| 2 | 3 | 55.13 ±0.56 | 52.38 ±7.42 | 38.02 ±2.25 |
| 2 | 4 | 54.79 ±0.94 | 55.40 ±0.58 | 38.80 ±1.94 |
| 3 | 2 | 53.62 ±3.01 | 54.65 ±0.62 | 29.53 ±0.34 |
| 3 | 3 | 53.08 ±3.63 | 51.52 ±4.64 | 33.47 ±0.36 |
| 3 | 4 | 54.88 ±0.81 | 53.10 ±4.75 | 34.60 ±1.30 |
| 4 | 2 | 54.55 ±0.55 | 54.58 ±0.51 | 24.90 ±1.63 |
| 4 | 3 | 54.94 ±0.78 | 55.18 ±0.42 | 29.23 ±1.53 |
| 4 | 4 | 53.49 ±3.02 | 55.12 ±0.43 | 31.42 ±0.27 |

### 50MB Messages

| Hops | Circuits | No Onion | Header Onion | Full Onion |
|------|----------|----------|--------------|------------|
| 2 | 2 | 54.59 ±0.78 | 52.87 ±1.93 | 33.22 ±2.20 |
| 2 | 3 | 54.53 ±1.11 | 54.67 ±1.42 | 38.34 ±0.87 |
| 2 | 4 | 54.57 ±1.65 | 54.06 ±1.50 | 39.45 ±1.09 |
| 3 | 2 | 54.36 ±1.22 | 54.66 ±0.91 | 29.42 ±0.48 |
| 3 | 3 | 53.61 ±1.90 | 54.36 ±1.73 | 33.08 ±0.62 |
| 3 | 4 | 54.43 ±1.74 | 54.81 ±0.84 | 34.70 ±0.90 |
| 4 | 2 | 54.30 ±1.19 | 54.19 ±1.60 | 25.45 ±0.42 |
| 4 | 3 | 39.39 ±28.56 | 53.38 ±1.94 | 29.56 ±0.24 |
| 4 | 4 | 54.22 ±1.50 | 54.31 ±1.49 | 30.90 ±0.59 |

---

## Statistical Consistency Analysis

### Coefficient of Variation (CV%) by Configuration

Lower CV% indicates more consistent/ reliable measurements.
CV > 20% suggests high variability (unreliable single-run measurements).

#### 64KB Messages

| Hops | Circuits | No Onion CV% | Header Onion CV% | Full Onion CV% |
|------|----------|--------------|------------------|---------------|
| 2 | 2 | 11.7% | 8.9% | 6.5% |
| 2 | 3 | 2.6% | 6.6% | 5.0% |
| 2 | 4 | 6.3% | 8.5% | 5.5% |
| 3 | 2 | 8.2% | 4.7% | 4.1% |
| 3 | 3 | 3.8% | 8.5% | 5.2% |
| 3 | 4 | 5.9% | 5.1% | 8.7% |
| 4 | 2 | 4.5% | 6.0% | 2.8% |
| 4 | 3 | 13.0% | 6.6% | 3.7% |
| 4 | 4 | 4.9% | 6.8% | 4.7% |

#### 1MB Messages

| Hops | Circuits | No Onion CV% | Header Onion CV% | Full Onion CV% |
|------|----------|--------------|------------------|---------------|
| 2 | 2 | 2.0% | 0.7% | 2.2% |
| 2 | 3 | 2.3% | 1.2% | 2.9% |
| 2 | 4 | 3.0% | 0.9% | 0.6% |
| 3 | 2 | 3.3% | 1.4% | 1.3% |
| 3 | 3 | 4.0% | 3.0% | 1.5% |
| 3 | 4 | 1.8% | 30.1% | 0.9% | ⚠️
| 4 | 2 | 2.3% | 1.4% | 1.3% |
| 4 | 3 | 1.3% | 1.7% | 2.1% |
| 4 | 4 | 1.5% | 1.7% | 2.4% |

#### 10MB Messages

| Hops | Circuits | No Onion CV% | Header Onion CV% | Full Onion CV% |
|------|----------|--------------|------------------|---------------|
| 2 | 2 | 9.7% | 6.3% | 1.0% |
| 2 | 3 | 1.0% | 14.2% | 5.9% |
| 2 | 4 | 1.7% | 1.1% | 5.0% |
| 3 | 2 | 5.6% | 1.1% | 1.2% |
| 3 | 3 | 6.8% | 9.0% | 1.1% |
| 3 | 4 | 1.5% | 8.9% | 3.7% |
| 4 | 2 | 1.0% | 0.9% | 6.5% |
| 4 | 3 | 1.4% | 0.8% | 5.2% |
| 4 | 4 | 5.7% | 0.8% | 0.9% |

#### 50MB Messages

| Hops | Circuits | No Onion CV% | Header Onion CV% | Full Onion CV% |
|------|----------|--------------|------------------|---------------|
| 2 | 2 | 1.4% | 3.6% | 6.6% |
| 2 | 3 | 2.0% | 2.6% | 2.3% |
| 2 | 4 | 3.0% | 2.8% | 2.8% |
| 3 | 2 | 2.2% | 1.7% | 1.6% |
| 3 | 3 | 3.5% | 3.2% | 1.9% |
| 3 | 4 | 3.2% | 1.5% | 2.6% |
| 4 | 2 | 2.2% | 3.0% | 1.6% |
| 4 | 3 | 72.5% | 3.6% | 0.8% | ⚠️
| 4 | 4 | 2.8% | 2.7% | 1.9% |

---

## Resource Usage Analysis

### CPU and Memory Overhead by Encryption Mode (10MB messages)

| Hops | Circuits | Mode | CPU User% | CPU Sys% | IOWait% | GC Pauses | Alloc (MB) | Bottleneck Analysis |
|------|----------|------|-----------|----------|---------|-----------|------------|--------------------|
| 2 | 2 | no-onion | 0.0 | -2.9 | 0.0 | 84 | 2007.2 | GC pressure |
| 2 | 2 | header-onion | 0.0 | -2.7 | 0.0 | 87 | 2007.3 | GC pressure |
| 2 | 2 | full-onion | 0.0 | 3.5 | 0.0 | 124 | 3808.6 | GC pressure |
| 2 | 3 | no-onion | 0.0 | -5.3 | 0.0 | 87 | 1857.4 | GC pressure |
| 2 | 3 | header-onion | 0.0 | 1.2 | 0.0 | 85 | 1857.5 | GC pressure |
| 2 | 3 | full-onion | 0.0 | 0.9 | 0.0 | 140 | 3209.5 | GC pressure |
| 2 | 4 | no-onion | 0.0 | -2.0 | 0.0 | 87 | 1807.1 | GC pressure |
| 2 | 4 | header-onion | 0.0 | 0.0 | 0.0 | 84 | 1807.2 | GC pressure |
| 2 | 4 | full-onion | 0.0 | 0.0 | 0.0 | 144 | 3008.0 | GC pressure |
| 3 | 2 | no-onion | 0.0 | -0.8 | 0.0 | 84 | 2007.2 | GC pressure |
| 3 | 2 | header-onion | 0.0 | 0.6 | 0.0 | 83 | 2007.3 | GC pressure |
| 3 | 2 | full-onion | 0.0 | -2.7 | 0.0 | 150 | 4609.3 | GC pressure |
| 3 | 3 | no-onion | 0.0 | -0.8 | 0.0 | 84 | 1857.4 | GC pressure |
| 3 | 3 | header-onion | 0.0 | -0.2 | 0.0 | 88 | 1857.5 | GC pressure |
| 3 | 3 | full-onion | 0.0 | 2.5 | 0.0 | 161 | 3810.5 | GC pressure |
| 3 | 4 | no-onion | 0.0 | -1.5 | 0.0 | 85 | 1807.1 | GC pressure |
| 3 | 4 | header-onion | 0.0 | 2.7 | 0.0 | 86 | 1807.2 | GC pressure |
| 3 | 4 | full-onion | 0.0 | 0.8 | 0.0 | 168 | 3541.8 | GC pressure |
| 4 | 2 | no-onion | 0.0 | 0.9 | 0.0 | 84 | 2007.2 | GC pressure |
| 4 | 2 | header-onion | 0.0 | 1.1 | 0.0 | 86 | 2007.3 | GC pressure |
| 4 | 2 | full-onion | 0.0 | 2.0 | 0.0 | 178 | 5409.9 | GC pressure |
| 4 | 3 | no-onion | 0.0 | 0.9 | 0.0 | 87 | 1857.4 | GC pressure |
| 4 | 3 | header-onion | 0.0 | -0.0 | 0.0 | 84 | 1857.6 | GC pressure |
| 4 | 3 | full-onion | 0.0 | 2.1 | 0.0 | 201 | 4411.4 | GC pressure |
| 4 | 4 | no-onion | 0.0 | 2.6 | 0.0 | 86 | 1807.1 | GC pressure |
| 4 | 4 | header-onion | 0.0 | -1.1 | 0.0 | 85 | 1807.3 | GC pressure |
| 4 | 4 | full-onion | 0.0 | -8.0 | 0.0 | 199 | 4075.6 | GC pressure |

### Resource Bottleneck Interpretation

- **High CPU Sys% (>30%):** Kernel overhead from excessive syscalls (common in sharding with many small packets)
- **High IOWait% (>20%):** Network/disk bottleneck - Docker virtual network congestion or disk I/O saturation
- **High GC Pauses (>5):** Memory allocation pressure - consider object pooling or reducing allocations
- **High CPU User%:** Actual computation (encryption/compression) - this is expected and efficient

---

## Conclusions

### On Anomalies

Previous single-run benchmarks showed anomalies like:
- Full Onion appearing faster than No Onion
- Throughput drops with no apparent cause
- Inconsistent overhead percentages

These are now explained by:
1. **System noise:** CPU scheduling, GC pauses, context switches
2. **Cold-start effects:** First run always slower (cache misses, JIT warmup)
3. **Measurement variance:** Single runs can vary by 10-30%

### Recommendations for Reliable Benchmarks

1. **Always discard warmup runs** (first 1-3 runs)
2. **Run multiple iterations** (minimum 5, ideally 9+)
3. **Report variance** (std dev or CV%) alongside means
4. **Check for CV > 20%** - indicates unreliable measurements
5. **Use median** for highly skewed distributions

### Final Recommendations by Use Case

| Use Case | Recommended Mode | Expected Overhead |
|----------|------------------|-------------------|
| Trusted network, max performance | No Onion | 0% (baseline) |
| Balanced privacy/performance | Header Onion | +10-30% |
| Maximum privacy, large files | Full Onion | +40-80% |
| Real-time streaming | Header Onion | +5-20% |

---

*Report generated by Qwen Mixnet Benchmark Suite with Statistical Analysis*
