#!/bin/bash

# Mixnet + PentAGI Cleanup Script

echo "Stopping Mixnet nodes..."
docker compose -f mixnet-test/docker-compose.generated.yml down || true

echo "Stopping PentAGI..."
cd pentagi && docker compose down && cd ..

echo "Cleaning up pentagi-network..."
docker network rm pentagi-network || true

echo "Cleanup complete!"
