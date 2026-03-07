#!/bin/bash

# Docker-based Mixnet Test - Starts N nodes and tests real message transmission

set -e

echo "=============================================="
echo "  Mixnet Docker Network Test"
echo "  Starting mixnet nodes in Docker containers"
echo "=============================================="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
ENC_MODE="${MIXNET_ENCRYPTION_MODE:-}"
PORT_BASE="${MIXNET_PORT_BASE:-4001}"
HTTP_PORT_BASE="${MIXNET_HTTP_PORT_BASE:-18080}"
BENCH_MODE="${MIXNET_BENCH:-}"
BENCH_WAIT="${MIXNET_BENCH_WAIT:-180}"
NODE_COUNT="${MIXNET_NODE_COUNT:-10}"
LAST_NODE=$((NODE_COUNT - 1))

# Cleanup function
cleanup() {
    if [ -n "${MIXNET_KEEP_UP:-}" ]; then
        echo ""
        echo "Keeping Docker containers up (MIXNET_KEEP_UP=1)"
        return
    fi
    echo ""
    echo "Cleaning up Docker containers..."
    docker-compose -f docker-compose.test.yml down 2>/dev/null || true
}

trap cleanup EXIT

# Step 1: Generate identities for all nodes
echo ""
echo "Step 1: Generating peer identities..."
echo "-------------------------------------------"

declare -a PEER_IDS
declare -a PRIV_KEYS

IDENTITY_OUTPUT=$(go run gen-identities.go "$NODE_COUNT" 2>/dev/null || true)
for ((i=0; i<NODE_COUNT; i++)); do
    # Generate identity using the gen-identities tool
    OUTPUT=$(echo "$IDENTITY_OUTPUT" | grep "^Node $i:" || echo "")
    
    if [ -n "$OUTPUT" ]; then
        PEER_IDS[$i]=$(echo "$OUTPUT" | sed -n 's/.*ID=\([^ ]*\).*/\1/p')
        PRIV_KEYS[$i]=$(echo "$OUTPUT" | sed -n 's/.*PRIV=\([^ ]*\).*/\1/p')
        echo "  Node $i: ${PEER_IDS[$i]:0:30}..."
    else
        echo "  Node $i: Failed to generate identity"
        PEER_IDS[$i]="UNKNOWN"
        PRIV_KEYS[$i]=""
    fi
done

# Step 2: Generate docker-compose with actual peer IDs
echo ""
echo "Step 2: Generating Docker Compose configuration..."
echo "-------------------------------------------"

cat > docker-compose.test.yml << 'HEADER'
version: '3.7'

services:
HEADER

for ((i=0; i<NODE_COUNT; i++)); do
    PORT=$((PORT_BASE + i))
    HTTP_PORT=$((HTTP_PORT_BASE + i))
    IP=$((10 + i))
    
    # Build peer list (all other nodes)
    PEERS=""
    for ((j=0; j<NODE_COUNT; j++)); do
        if [ $i -ne $j ]; then
            PEER_IP=$((10 + j))
            PEERS="${PEERS}${PEER_IDS[$j]}@/ip4/172.25.0.$PEER_IP/tcp/4001,"
        fi
    done
    PEERS=${PEERS%,}
    
    cat >> docker-compose.test.yml << EOF
  node$i:
    build:
      context: ..
      dockerfile: mixnet-test/Dockerfile
    environment:
      - NODE_ID=$i
      - MIXNET_PORT=4001
      - MIXNET_PRIV_KEY=${PRIV_KEYS[$i]}
      - MIXNET_PEERS=$PEERS
EOF

    if [ -n "$ENC_MODE" ]; then
        cat >> docker-compose.test.yml << EOF
      - MIXNET_ENCRYPTION_MODE=$ENC_MODE
EOF
    fi

    if [ -n "$BENCH_MODE" ] && [ "$i" -eq 0 ]; then
        cat >> docker-compose.test.yml << EOF
      - MIXNET_BENCH=1
      - MIXNET_BENCH_SENDER=1
EOF
        for var in MIXNET_BENCH_SIZES MIXNET_BENCH_HOPS MIXNET_BENCH_CIRCUITS MIXNET_BENCH_MODES MIXNET_BENCH_ITER MIXNET_BENCH_AUDIO_BITRATES MIXNET_BENCH_VIDEO_BITRATES MIXNET_BENCH_STREAM_DURATION; do
            val="${!var}"
            if [ -n "$val" ]; then
                cat >> docker-compose.test.yml << EOF
      - $var=$val
EOF
            fi
        done
    fi

    cat >> docker-compose.test.yml << EOF
    networks:
      mixnet-network:
        ipv4_address: 172.25.0.$IP
    ports:
      - "$PORT:4001"
      - "$HTTP_PORT:8080"

EOF
done

cat >> docker-compose.test.yml << 'FOOTER'
networks:
  mixnet-network:
    driver: bridge
    ipam:
      config:
        - subnet: 172.25.0.0/16
FOOTER

echo "  Generated docker-compose.test.yml with ${#PEER_IDS[@]} nodes"

# Step 3: Build and start containers
echo ""
echo "Step 3: Building Docker images..."
echo "-------------------------------------------"
docker-compose -f docker-compose.test.yml build

echo ""
echo "Step 4: Starting $NODE_COUNT mixnet nodes..."
echo "-------------------------------------------"
docker-compose -f docker-compose.test.yml up -d

echo ""
echo "  Waiting for nodes to start and connect (45 seconds)..."
sleep 45

# Step 5: Check status
echo ""
echo "Step 5: Checking node status..."
echo "-------------------------------------------"

for ((i=0; i<NODE_COUNT; i++)); do
    LOGS=$(docker-compose -f docker-compose.test.yml logs --tail=20 node$i 2>/dev/null || echo "No logs")
    CONNECTED=$(echo "$LOGS" | grep -c "Connected to" || echo "0")
    STARTED=$(echo "$LOGS" | grep -c "Started on port" || echo "0")
    echo "  Node $i: Started=$STARTED, Connections=$CONNECTED"
done

# Step 5B: Optional fault injection and recovery timing
if [ -n "${MIXNET_FAULT_INJECT:-}" ]; then
    FAULT_NODE="${MIXNET_FAULT_NODE:-node3}"
    echo ""
    echo "Step 5B: Fault Injection (${FAULT_NODE})"
    echo "-------------------------------------------"
    FAIL_TS=$(date +%s%N)
    echo "  Stopping $FAULT_NODE at epoch_ns=$FAIL_TS"
    docker-compose -f docker-compose.test.yml stop "$FAULT_NODE" >/dev/null 2>&1 || true

    echo "  Waiting for first successful send after failure..."
    RECOVERED=0
    for i in {1..24}; do
        LOG_LINE=$(docker-compose -f docker-compose.test.yml logs --tail=200 node0 2>/dev/null | grep -E "BENCH_SEND_SUCCESS|BENCH_E2E_SUCCESS" | tail -1 || true)
        if [ -n "$LOG_LINE" ]; then
            TS=$(echo "$LOG_LINE" | sed -n 's/.*epoch_ns=\([0-9]*\).*/\1/p')
            if [ -n "$TS" ] && [ "$TS" -ge "$FAIL_TS" ]; then
                DELTA_NS=$((TS - FAIL_TS))
                DELTA_SEC=$(awk "BEGIN {printf \"%.6f\", $DELTA_NS/1000000000}")
                echo "  Recovery observed after ${DELTA_SEC}s (epoch_ns=$TS)"
                RECOVERED=1
                break
            fi
        fi
        sleep 5
    done
    if [ $RECOVERED -eq 0 ]; then
        echo "  Recovery not observed within timeout window"
    fi
fi

# Step 6: Wait for test messages or benchmarks
echo ""
if [ -n "$BENCH_MODE" ]; then
    echo "Step 6: Waiting for benchmark completion (timeout ${BENCH_WAIT} seconds)..."
    WAITED=0
    STEP=5
    while [ "$WAITED" -lt "$BENCH_WAIT" ]; do
        if docker-compose -f docker-compose.test.yml logs --tail=200 node0 2>/dev/null | grep -q "BENCH_DONE"; then
            echo "  Benchmark run completed (BENCH_DONE found)"
            break
        fi
        sleep "$STEP"
        WAITED=$((WAITED + STEP))
    done
    if [ "$WAITED" -ge "$BENCH_WAIT" ]; then
        echo "  Timeout reached before BENCH_DONE; collecting partial benchmark logs"
    fi
else
    echo "Step 6: Waiting for test messages (30 seconds)..."
    sleep 30
fi

# Step 7: Show results
echo ""
echo "Step 7: Test Results"
echo "=============================================="

if [ -n "$BENCH_MODE" ]; then
    echo ""
    echo "Node 0 Benchmark Logs (Sender):"
    echo "-------------------------------------------"
    docker-compose -f docker-compose.test.yml logs --tail=2000 node0 2>/dev/null | grep -E "BENCH_E2E_|BENCH_STREAM_" || echo "  No benchmark logs found"

    echo ""
    echo "Generating benchmark report from Node 0 logs..."
    LOG_PATH="/tmp/mixnet-bench.log"
    REPORT_PATH="/Users/abhinavnehra/git/Libp2p/go-libp2p-mixnet-impl/mixnet/BENCHMARK_REPORT_2026-03-07.md"
    docker-compose -f docker-compose.test.yml logs node0 2>/dev/null > "$LOG_PATH"
    python3 /Users/abhinavnehra/git/Libp2p/go-libp2p-mixnet-impl/mixnet-test/parse_bench_logs.py --log "$LOG_PATH" --out "$REPORT_PATH"
    echo "Report written to: $REPORT_PATH"
else
echo ""
echo "Node 0 Logs (Sender):"
echo "-------------------------------------------"
docker-compose -f docker-compose.test.yml logs --tail=50 node0 2>/dev/null | grep -E "SENDING|SEND SUCCESS|SEND FAILED|LARGE MESSAGE" || echo "  No send logs found"

echo ""
echo "Sample Receiver Logs:"
echo "-------------------------------------------"
for i in 5 7 "$LAST_NODE"; do
    if [ "$i" -gt "$LAST_NODE" ]; then
        continue
    fi
    echo "  Node $i:"
    docker-compose -f docker-compose.test.yml logs --tail=50 node$i 2>/dev/null | grep "RECEIVED" | head -5 | sed 's/^/    /' || echo "    No messages received"
done

echo ""
echo "Full Node 0 Logs:"
echo "-------------------------------------------"
docker-compose -f docker-compose.test.yml logs --tail=100 node0 2>/dev/null | tail -50
fi

echo ""
echo "=============================================="
echo "  Test Complete!"
echo "=============================================="
echo ""
echo "To view live logs: docker-compose -f docker-compose.test.yml logs -f"
echo "To stop: docker-compose -f docker-compose.test.yml down"
