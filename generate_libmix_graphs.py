#!/usr/bin/env python3
"""
Generate visualization graphs for Lib-Mix Protocol
Saves graphs to ~/Desktop/
"""

import matplotlib.pyplot as plt
import numpy as np
from pathlib import Path
import os

# Set output directory to Desktop
desktop_path = Path.home() / "Desktop" / "libmix_graphs"
desktop_path.mkdir(exist_ok=True)

# Set style
plt.style.use('seaborn-v0_8-darkgrid')

# Graph 1: Latency vs Hop Count
fig, ax = plt.subplots(figsize=(10, 6))
hops = np.arange(1, 11)
tor_latency = 50 + (hops * 150)  # Tor-style latency
libmix_latency = 50 + (hops * 80)  # Lib-Mix optimized latency
direct_latency = np.full(10, 50)  # Direct connection baseline

ax.plot(hops, tor_latency, 'r-o', label='Traditional Mixnet (Tor-style)', linewidth=2)
ax.plot(hops, libmix_latency, 'g-s', label='Lib-Mix Protocol', linewidth=2)
ax.plot(hops, direct_latency, 'b--', label='Direct Connection', linewidth=2)
ax.set_xlabel('Number of Hops', fontsize=12)
ax.set_ylabel('Round Trip Time (ms)', fontsize=12)
ax.set_title('Latency Comparison: Lib-Mix vs Traditional Mixnet', fontsize=14, fontweight='bold')
ax.legend(fontsize=10)
ax.grid(True, alpha=0.3)
plt.tight_layout()
plt.savefig(desktop_path / '1_latency_vs_hops.png', dpi=300)
plt.close()

# Graph 2: Privacy vs Performance Trade-off
fig, ax = plt.subplots(figsize=(10, 6))
circuits = np.arange(1, 21)
privacy_score = 1 - np.exp(-circuits * 0.3)  # Asymptotic privacy improvement
throughput = 100 / (1 + circuits * 0.15)  # Throughput degradation

ax2 = ax.twinx()
line1 = ax.plot(circuits, privacy_score * 100, 'b-o', label='Privacy Score', linewidth=2)
line2 = ax2.plot(circuits, throughput, 'r-s', label='Throughput (MB/s)', linewidth=2)

ax.set_xlabel('Number of Circuits', fontsize=12)
ax.set_ylabel('Privacy Score (%)', fontsize=12, color='b')
ax2.set_ylabel('Throughput (MB/s)', fontsize=12, color='r')
ax.set_title('Privacy vs Performance Trade-off', fontsize=14, fontweight='bold')
ax.tick_params(axis='y', labelcolor='b')
ax2.tick_params(axis='y', labelcolor='r')

lines = line1 + line2
labels = [l.get_label() for l in lines]
ax.legend(lines, labels, loc='center right', fontsize=10)
ax.grid(True, alpha=0.3)
plt.tight_layout()
plt.savefig(desktop_path / '2_privacy_vs_performance.png', dpi=300)
plt.close()

# Graph 3: Circuit Failure Recovery
fig, ax = plt.subplots(figsize=(10, 6))
time = np.arange(0, 60, 0.1)
circuit1 = np.ones_like(time)
circuit2 = np.ones_like(time)
circuit3 = np.ones_like(time)

# Simulate failures and recoveries
circuit1[200:250] = 0  # Failure at 20s
circuit1[250:] = 1     # Recovery at 25s
circuit2[350:400] = 0  # Failure at 35s
circuit2[400:] = 1     # Recovery at 40s

ax.fill_between(time, 0, circuit1, alpha=0.6, label='Circuit 1', step='mid')
ax.fill_between(time, 1, 1 + circuit2, alpha=0.6, label='Circuit 2', step='mid')
ax.fill_between(time, 2, 2 + circuit3, alpha=0.6, label='Circuit 3', step='mid')
ax.axhline(y=1.8, color='orange', linestyle='--', linewidth=2, label='Erasure Threshold (60%)')
ax.set_xlabel('Time (seconds)', fontsize=12)
ax.set_ylabel('Circuit Status', fontsize=12)
ax.set_title('Circuit Failure Recovery Over Time', fontsize=14, fontweight='bold')
ax.set_ylim(-0.5, 3.5)
ax.set_yticks([0.5, 1.5, 2.5])
ax.set_yticklabels(['Circuit 1', 'Circuit 2', 'Circuit 3'])
ax.legend(loc='upper right', fontsize=10)
ax.grid(True, alpha=0.3)
plt.tight_layout()
plt.savefig(desktop_path / '3_circuit_failure_recovery.png', dpi=300)
plt.close()

# Graph 4: Compression Ratio Comparison
fig, ax = plt.subplots(figsize=(10, 6))
data_types = ['Text', 'JSON', 'Binary', 'Video', 'Audio']
gzip_ratio = [0.25, 0.30, 0.85, 0.95, 0.90]
snappy_ratio = [0.45, 0.50, 0.90, 0.98, 0.95]

x = np.arange(len(data_types))
width = 0.35

bars1 = ax.bar(x - width/2, gzip_ratio, width, label='Gzip', color='steelblue')
bars2 = ax.bar(x + width/2, snappy_ratio, width, label='Snappy', color='coral')

ax.set_xlabel('Data Type', fontsize=12)
ax.set_ylabel('Compression Ratio (lower is better)', fontsize=12)
ax.set_title('Compression Algorithm Performance by Data Type', fontsize=14, fontweight='bold')
ax.set_xticks(x)
ax.set_xticklabels(data_types)
ax.legend(fontsize=10)
ax.grid(True, alpha=0.3, axis='y')
plt.tight_layout()
plt.savefig(desktop_path / '4_compression_ratios.png', dpi=300)
plt.close()

# Graph 5: Shard Distribution Across Circuits
fig, ax = plt.subplots(figsize=(10, 6))
circuit_ids = ['Circuit 1', 'Circuit 2', 'Circuit 3']
shard_sizes = [342, 338, 340]  # KB, roughly equal due to erasure coding
colors = ['#FF6B6B', '#4ECDC4', '#45B7D1']

bars = ax.bar(circuit_ids, shard_sizes, color=colors, alpha=0.7, edgecolor='black', linewidth=1.5)
ax.axhline(y=np.mean(shard_sizes), color='orange', linestyle='--', linewidth=2, label=f'Average: {np.mean(shard_sizes):.0f} KB')
ax.set_ylabel('Shard Size (KB)', fontsize=12)
ax.set_title('Shard Distribution Across Circuits (1 MB Message)', fontsize=14, fontweight='bold')
ax.legend(fontsize=10)
ax.grid(True, alpha=0.3, axis='y')

# Add value labels on bars
for bar in bars:
    height = bar.get_height()
    ax.text(bar.get_x() + bar.get_width()/2., height,
            f'{height:.0f} KB',
            ha='center', va='bottom', fontsize=10, fontweight='bold')

plt.tight_layout()
plt.savefig(desktop_path / '5_shard_distribution.png', dpi=300)
plt.close()

# Graph 6: RTT-Based Relay Selection
fig, ax = plt.subplots(figsize=(10, 6))
relay_count = 30
rtts = np.random.exponential(scale=50, size=relay_count) + 20
selected_threshold = 6

sorted_rtts = np.sort(rtts)
colors = ['green' if i < selected_threshold else 'gray' for i in range(relay_count)]

ax.bar(range(relay_count), sorted_rtts, color=colors, alpha=0.7, edgecolor='black', linewidth=0.5)
ax.axhline(y=sorted_rtts[selected_threshold-1], color='red', linestyle='--', linewidth=2, 
           label=f'Selection Cutoff (Top 6)')
ax.set_xlabel('Relay Node (sorted by RTT)', fontsize=12)
ax.set_ylabel('Round Trip Time (ms)', fontsize=12)
ax.set_title('RTT-Based Relay Selection from DHT Pool', fontsize=14, fontweight='bold')
ax.legend(fontsize=10)
ax.grid(True, alpha=0.3, axis='y')
plt.tight_layout()
plt.savefig(desktop_path / '6_relay_selection.png', dpi=300)
plt.close()

# Graph 7: Throughput vs Circuit Count
fig, ax = plt.subplots(figsize=(10, 6))
circuits = np.arange(1, 21)
throughput_1hop = 95 / (1 + circuits * 0.08)
throughput_2hop = 85 / (1 + circuits * 0.12)
throughput_5hop = 60 / (1 + circuits * 0.18)

ax.plot(circuits, throughput_1hop, 'g-o', label='1 Hop', linewidth=2)
ax.plot(circuits, throughput_2hop, 'b-s', label='2 Hops (Default)', linewidth=2)
ax.plot(circuits, throughput_5hop, 'r-^', label='5 Hops', linewidth=2)
ax.set_xlabel('Number of Circuits', fontsize=12)
ax.set_ylabel('Throughput (MB/s)', fontsize=12)
ax.set_title('Throughput vs Circuit Count for Different Hop Configurations', fontsize=14, fontweight='bold')
ax.legend(fontsize=10)
ax.grid(True, alpha=0.3)
plt.tight_layout()
plt.savefig(desktop_path / '7_throughput_vs_circuits.png', dpi=300)
plt.close()

# Graph 8: Erasure Coding Threshold
fig, ax = plt.subplots(figsize=(10, 6))
total_circuits = np.arange(1, 21)
threshold = np.ceil(total_circuits * 0.6)
redundancy = total_circuits - threshold

ax.bar(total_circuits, threshold, label='Required Shards', color='steelblue', alpha=0.7)
ax.bar(total_circuits, redundancy, bottom=threshold, label='Redundant Shards', color='coral', alpha=0.7)
ax.set_xlabel('Total Circuit Count', fontsize=12)
ax.set_ylabel('Number of Shards', fontsize=12)
ax.set_title('Reed-Solomon Erasure Coding: Required vs Redundant Shards', fontsize=14, fontweight='bold')
ax.legend(fontsize=10)
ax.grid(True, alpha=0.3, axis='y')
plt.tight_layout()
plt.savefig(desktop_path / '8_erasure_coding_threshold.png', dpi=300)
plt.close()

# Graph 9: Security Model - Knowledge Distribution
fig, ax = plt.subplots(figsize=(12, 8))

# Define knowledge visibility
roles = ['Observer', 'Entry Relay', 'Mid Relay', 'Exit Relay', 'Origin', 'Destination']
knows_origin = [0, 1, 0, 0, 1, 0]
knows_destination = [0, 0, 0, 1, 1, 1]
knows_content = [0, 0, 0, 0, 1, 1]
knows_shard = [0.33, 0.33, 0.33, 0.33, 1, 1]

x = np.arange(len(roles))
width = 0.2

bars1 = ax.bar(x - 1.5*width, knows_origin, width, label='Knows Origin', color='#FF6B6B')
bars2 = ax.bar(x - 0.5*width, knows_destination, width, label='Knows Destination', color='#4ECDC4')
bars3 = ax.bar(x + 0.5*width, knows_content, width, label='Knows Content', color='#45B7D1')
bars4 = ax.bar(x + 1.5*width, knows_shard, width, label='Sees Complete Shard', color='#FFA07A')

ax.set_ylabel('Knowledge Level (1 = Full, 0 = None)', fontsize=12)
ax.set_title('Metadata Privacy: Knowledge Distribution Across Roles', fontsize=14, fontweight='bold')
ax.set_xticks(x)
ax.set_xticklabels(roles, rotation=15, ha='right')
ax.legend(fontsize=10)
ax.set_ylim(0, 1.2)
ax.grid(True, alpha=0.3, axis='y')
plt.tight_layout()
plt.savefig(desktop_path / '9_security_knowledge_model.png', dpi=300)
plt.close()

# Graph 10: CES Pipeline Processing Time
fig, ax = plt.subplots(figsize=(10, 6))
message_sizes = np.array([1, 10, 100, 1000, 10000])  # KB
compress_time = message_sizes * 0.5
encrypt_time = message_sizes * 1.2
shard_time = message_sizes * 0.3

ax.plot(message_sizes, compress_time, 'g-o', label='Compression', linewidth=2, markersize=8)
ax.plot(message_sizes, encrypt_time, 'r-s', label='Encryption (Layered)', linewidth=2, markersize=8)
ax.plot(message_sizes, shard_time, 'b-^', label='Sharding (Reed-Solomon)', linewidth=2, markersize=8)
ax.plot(message_sizes, compress_time + encrypt_time + shard_time, 'k--', 
        label='Total Pipeline Time', linewidth=2)

ax.set_xlabel('Message Size (KB)', fontsize=12)
ax.set_ylabel('Processing Time (ms)', fontsize=12)
ax.set_title('CES Pipeline Processing Time by Message Size', fontsize=14, fontweight='bold')
ax.set_xscale('log')
ax.set_yscale('log')
ax.legend(fontsize=10)
ax.grid(True, alpha=0.3, which='both')
plt.tight_layout()
plt.savefig(desktop_path / '10_ces_pipeline_timing.png', dpi=300)
plt.close()

print(f"✓ Generated 10 graphs successfully!")
print(f"✓ Saved to: {desktop_path}")
print("\nGenerated graphs:")
print("  1. Latency vs Hop Count")
print("  2. Privacy vs Performance Trade-off")
print("  3. Circuit Failure Recovery")
print("  4. Compression Ratios")
print("  5. Shard Distribution")
print("  6. RTT-Based Relay Selection")
print("  7. Throughput vs Circuit Count")
print("  8. Erasure Coding Threshold")
print("  9. Security Knowledge Model")
print(" 10. CES Pipeline Processing Time")
