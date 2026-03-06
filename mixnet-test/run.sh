#!/bin/bash

# Mixnet + PentAGI Automation Script
# This script starts 8 mixnet nodes and PentAGI for security testing.

set -e

# 1. Check for LLM Key
if [ -z "$OPEN_AI_KEY" ] && [ -z "$ANTHROPIC_API_KEY" ] && [ -z "$GEMINI_API_KEY" ]; then
    echo "⚠️ Warning: No LLM API key detected in environment (OPEN_AI_KEY, ANTHROPIC_API_KEY, or GEMINI_API_KEY)."
    echo "PentAGI requires an LLM to perform autonomous pentesting."
    echo "Please export one of them before running this script, or edit pentagi/.env later."
fi

# 2. Setup PentAGI Network
echo "Creating pentagi-network..."
docker network inspect pentagi-network >/dev/null 2>&1 || \
    docker network create --driver bridge --subnet=172.20.0.0/16 pentagi-network

# 3. Generate Mixnet Identities
echo "Generating node identities..."
IDS=()
KEYS=()
for i in {0..7}; do
    OUT=$(go run mixnet-test/gen-identities.go | grep "Node $i")
    ID=$(echo $OUT | sed -n 's/.*ID=\([^ ]*\).*/\1/p')
    KEY=$(echo $OUT | sed -n 's/.*PRIV=\([^ ]*\).*/\1/p')
    IDS+=($ID)
    KEYS+=($KEY)
done

# Create the peer list for nodes to find each other
PEER_LIST=""
for i in {0..7}; do
    IP="172.20.0.$((10 + i))"
    PEER_LIST="${PEER_LIST}${IDS[$i]}@/ip4/${IP}/tcp/4001,"
done
PEER_LIST=${PEER_LIST%,} # remove trailing comma

export MIXNET_PEERS=$PEER_LIST

# 4. Generate Docker Compose for Mixnet
echo "Generating mixnet-test/docker-compose.generated.yml..."
cat <<EOF > mixnet-test/docker-compose.generated.yml
version: '3.7'
services:
EOF

for i in {0..7}; do
    IP="172.20.0.$((10 + i))"
    cat <<EOF >> mixnet-test/docker-compose.generated.yml
  node$i:
    build:
      context: ..
      dockerfile: mixnet-test/Dockerfile
    environment:
      - MIXNET_PRIV_KEY=${KEYS[$i]}
      - MIXNET_PEERS=$PEER_LIST
      - MIXNET_PORT=4001
    networks:
      pentagi-network:
        ipv4_address: $IP
EOF
done

cat <<EOF >> mixnet-test/docker-compose.generated.yml
networks:
  pentagi-network:
    external: true
    name: pentagi-network
EOF

# 5. Start PentAGI
echo "Starting PentAGI..."
cd pentagi
if [ ! -f .env ]; then
    cp .env.example .env
    # Configure cross-platform sed in-place flag
    if sed -i'' --version /dev/null >/dev/null 2>&1; then
        # GNU sed (accepts --version and -i'' works without a backup suffix)
        SED_INPLACE=(sed -i'')
    else
        # BSD/macOS sed
        SED_INPLACE=(sed -i '')
    fi
    # Inject keys if provided in shell env
    [ ! -z "$OPEN_AI_KEY" ] && "${SED_INPLACE[@]}" "s/OPEN_AI_KEY=.*/OPEN_AI_KEY=$OPEN_AI_KEY/" .env || true
    [ ! -z "$ANTHROPIC_API_KEY" ] && "${SED_INPLACE[@]}" "s/ANTHROPIC_API_KEY=.*/ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY/" .env || true
    [ ! -z "$GEMINI_API_KEY" ] && "${SED_INPLACE[@]}" "s/GEMINI_API_KEY=.*/GEMINI_API_KEY=$GEMINI_API_KEY/" .env || true
fi
docker compose up -d
cd ..

# 6. Start Mixnet Nodes
echo "Starting Mixnet Nodes..."
docker compose -f mixnet-test/docker-compose.generated.yml up -d --build

echo "--------------------------------------------------------"
echo "🚀 ALL SYSTEMS STARTED!"
echo "--------------------------------------------------------"
echo "Mixnet Nodes: 172.20.0.10 - 172.20.0.17"
echo "PentAGI UI: https://localhost:8443"
echo "--------------------------------------------------------"
echo "Step 1: Open PentAGI in your browser."
echo "Step 2: Create a NEW FLOW."
echo "Step 3: Ask PentAGI to scan the nodes: 'Scan the mixnet relay nodes from 172.20.0.10 to 172.20.0.17 on port 4001. Check for anonymity leaks in the libp2p mixnet protocol.'"
echo "--------------------------------------------------------"
echo "To stop everything, run: ./mixnet-test/stop.sh"
