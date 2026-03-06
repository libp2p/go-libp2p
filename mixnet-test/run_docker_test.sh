#!/bin/bash

# Docker-based Mixnet Test - Starts 10 nodes and tests real message transmission

set -e

echo "=============================================="
echo "  Mixnet Docker Network Test"
echo "  Starting 10 nodes in Docker containers"
echo "=============================================="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Cleanup function
cleanup() {
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

for i in {0..9}; do
    # Generate identity using the gen-identities tool
    OUTPUT=$(go run gen-identities.go 2>/dev/null | grep "Node $i" || echo "")
    
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

for i in {0..9}; do
    PORT=$((4001 + i))
    HTTP_PORT=$((8080 + i))
    IP=$((10 + i))
    
    # Build peer list (all other nodes)
    PEERS=""
    for j in {0..9}; do
        if [ $i -ne $j ]; then
            PEERS="${PEERS}${PEER_IDS[$j]}@/ip4/node$j/tcp/4001,"
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
echo "Step 4: Starting 10 mixnet nodes..."
echo "-------------------------------------------"
docker-compose -f docker-compose.test.yml up -d

echo ""
echo "  Waiting for nodes to start and connect (45 seconds)..."
sleep 45

# Step 5: Check status
echo ""
echo "Step 5: Checking node status..."
echo "-------------------------------------------"

for i in {0..9}; do
    LOGS=$(docker-compose -f docker-compose.test.yml logs --tail=20 node$i 2>/dev/null || echo "No logs")
    CONNECTED=$(echo "$LOGS" | grep -c "Connected to" || echo "0")
    STARTED=$(echo "$LOGS" | grep -c "Started on port" || echo "0")
    echo "  Node $i: Started=$STARTED, Connections=$CONNECTED"
done

# Step 6: Wait for test messages
echo ""
echo "Step 6: Waiting for test messages (30 seconds)..."
sleep 30

# Step 7: Show results
echo ""
echo "Step 7: Test Results"
echo "=============================================="

echo ""
echo "Node 0 Logs (Sender):"
echo "-------------------------------------------"
docker-compose -f docker-compose.test.yml logs --tail=50 node0 2>/dev/null | grep -E "SENDING|SEND SUCCESS|SEND FAILED|LARGE MESSAGE" || echo "  No send logs found"

echo ""
echo "Node 5, 7, 9 Logs (Receivers):"
echo "-------------------------------------------"
for i in 5 7 9; do
    echo "  Node $i:"
    docker-compose -f docker-compose.test.yml logs --tail=50 node$i 2>/dev/null | grep "RECEIVED" | head -5 | sed 's/^/    /' || echo "    No messages received"
done

echo ""
echo "Full Node 0 Logs:"
echo "-------------------------------------------"
docker-compose -f docker-compose.test.yml logs --tail=100 node0 2>/dev/null | tail -50

echo ""
echo "=============================================="
echo "  Test Complete!"
echo "=============================================="
echo ""
echo "To view live logs: docker-compose -f docker-compose.test.yml logs -f"
echo "To stop: docker-compose -f docker-compose.test.yml down"
