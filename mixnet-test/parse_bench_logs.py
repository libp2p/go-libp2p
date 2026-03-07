#!/usr/bin/env python3
import argparse
import statistics
from collections import defaultdict


def parse_kv(line: str):
    kv = {}
    for token in line.strip().split():
        if "=" not in token:
            continue
        key, val = token.split("=", 1)
        kv[key.strip()] = val.strip()
    return kv


def parse_int(val, default=0):
    try:
        return int(val)
    except Exception:
        return default


def parse_logs(lines):
    e2e = []
    e2e_fail = []
    stream = []
    stream_fail = []
    skipped = []
    for line in lines:
        if "BENCH_SKIP" in line:
            skipped.append(line.strip())
        if "BENCH_E2E_SUCCESS" in line:
            kv = parse_kv(line)
            e2e.append({
                "mode": kv.get("mode", ""),
                "size": parse_int(kv.get("size", "0")),
                "hops": parse_int(kv.get("hops", "0")),
                "circuits": parse_int(kv.get("circuits", "0")),
                "iter": parse_int(kv.get("iter", "0")),
                "elapsed_ns": parse_int(kv.get("elapsed_ns", "0")),
            })
        if "BENCH_E2E_FAIL" in line:
            kv = parse_kv(line)
            e2e_fail.append({
                "mode": kv.get("mode", ""),
                "size": parse_int(kv.get("size", "0")),
                "hops": parse_int(kv.get("hops", "0")),
                "circuits": parse_int(kv.get("circuits", "0")),
                "iter": parse_int(kv.get("iter", "0")),
            })
        if "BENCH_STREAM_SUCCESS" in line:
            kv = parse_kv(line)
            stream.append({
                "mode": kv.get("mode", ""),
                "kind": kv.get("kind", ""),
                "bitrate_kbps": parse_int(kv.get("bitrate_kbps", "0")),
                "duration_s": parse_int(kv.get("duration_s", "0")),
                "hops": parse_int(kv.get("hops", "0")),
                "circuits": parse_int(kv.get("circuits", "0")),
                "elapsed_ns": parse_int(kv.get("elapsed_ns", "0")),
            })
        if "BENCH_STREAM_FAIL" in line:
            kv = parse_kv(line)
            stream_fail.append({
                "mode": kv.get("mode", ""),
                "kind": kv.get("kind", ""),
                "bitrate_kbps": parse_int(kv.get("bitrate_kbps", "0")),
                "duration_s": parse_int(kv.get("duration_s", "0")),
                "hops": parse_int(kv.get("hops", "0")),
                "circuits": parse_int(kv.get("circuits", "0")),
            })
    return e2e, e2e_fail, stream, stream_fail, skipped


def format_bytes(n):
    for unit in ["B", "KB", "MB", "GB"]:
        if n < 1024 or unit == "GB":
            return f"{n:.0f}{unit}"
        n /= 1024
    return f"{n:.0f}B"


def build_report(e2e, e2e_fail, stream, stream_fail, skipped):
    out = []
    out.append("# Mixnet Benchmark Report (Docker E2E)\n")
    out.append("This report is derived from real end-to-end Docker runs. All timings are actual send durations over the mixnet.\n\n")
    out.append("## Run Health\n\n")
    out.append(f"- E2E success rows: {len(e2e)}\n")
    out.append(f"- E2E fail rows: {len(e2e_fail)}\n")
    out.append(f"- Stream success rows: {len(stream)}\n")
    out.append(f"- Stream fail rows: {len(stream_fail)}\n")
    out.append(f"- Skipped scenarios: {len(skipped)}\n\n")

    if e2e:
        out.append("## Summary (Average Seconds per Scenario)\n\n")
        grouped = defaultdict(list)
        for row in e2e:
            key = (row["mode"], row["size"], row["hops"], row["circuits"])
            grouped[key].append(row["elapsed_ns"] / 1e9)
        out.append("| Mode | Size | Hops | Circuits | Samples | Avg (s) |\n")
        out.append("| --- | --- | --- | --- | --- | --- |\n")
        for key in sorted(grouped.keys()):
            vals = grouped[key]
            out.append(
                f"| {key[0]} | {format_bytes(key[1])} | {key[2]} | {key[3]} | {len(vals)} | {statistics.mean(vals):.6f} |\n"
            )
        out.append("\n")

        out.append("## Detailed Results (Seconds per Send)\n\n")
        out.append("| Mode | Size | Hops | Circuits | Iter | Elapsed (s) |\n")
        out.append("| --- | --- | --- | --- | --- | --- |\n")
        for row in e2e:
            out.append(
                f"| {row['mode']} | {format_bytes(row['size'])} | {row['hops']} | {row['circuits']} | {row['iter']} | {row['elapsed_ns']/1e9:.6f} |\n"
            )
        out.append("\n")

    if stream:
        out.append("## Streaming Results (Average Seconds per Scenario)\n\n")
        grouped = defaultdict(list)
        for row in stream:
            key = (row["mode"], row["kind"], row["bitrate_kbps"], row["duration_s"], row["hops"], row["circuits"])
            grouped[key].append(row["elapsed_ns"] / 1e9)
        out.append("| Mode | Kind | Bitrate (kbps) | Duration (s) | Hops | Circuits | Samples | Avg (s) |\n")
        out.append("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
        for key in sorted(grouped.keys()):
            vals = grouped[key]
            out.append(
                f"| {key[0]} | {key[1]} | {key[2]} | {key[3]} | {key[4]} | {key[5]} | {len(vals)} | {statistics.mean(vals):.6f} |\n"
            )
        out.append("\n")

    if e2e_fail:
        out.append("## E2E Failure Counts\n\n")
        grouped = defaultdict(int)
        for row in e2e_fail:
            key = (row["mode"], row["size"], row["hops"], row["circuits"])
            grouped[key] += 1
        out.append("| Mode | Size | Hops | Circuits | Fails |\n")
        out.append("| --- | --- | --- | --- | --- |\n")
        for key in sorted(grouped.keys()):
            out.append(
                f"| {key[0]} | {format_bytes(key[1])} | {key[2]} | {key[3]} | {grouped[key]} |\n"
            )
        out.append("\n")

    if stream_fail:
        out.append("## Streaming Failure Counts\n\n")
        grouped = defaultdict(int)
        for row in stream_fail:
            key = (row["mode"], row["kind"], row["bitrate_kbps"], row["duration_s"], row["hops"], row["circuits"])
            grouped[key] += 1
        out.append("| Mode | Kind | Bitrate (kbps) | Duration (s) | Hops | Circuits | Fails |\n")
        out.append("| --- | --- | --- | --- | --- | --- | --- |\n")
        for key in sorted(grouped.keys()):
            out.append(
                f"| {key[0]} | {key[1]} | {key[2]} | {key[3]} | {key[4]} | {key[5]} | {grouped[key]} |\n"
            )
        out.append("\n")

    if not e2e and not stream and not e2e_fail and not stream_fail:
        out.append("No benchmark lines found in logs.\n")

    return "".join(out)


def main():
    parser = argparse.ArgumentParser(description="Parse mixnet benchmark logs and produce a report")
    parser.add_argument("--log", required=True, help="Path to log file")
    parser.add_argument("--out", required=True, help="Output report path")
    args = parser.parse_args()

    with open(args.log, "r", encoding="utf-8") as f:
        lines = f.readlines()

    e2e, e2e_fail, stream, stream_fail, skipped = parse_logs(lines)
    report = build_report(e2e, e2e_fail, stream, stream_fail, skipped)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(report)


if __name__ == "__main__":
    main()
