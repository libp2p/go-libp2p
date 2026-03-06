#!/bin/bash

# Mixnet Localhost Network Test Script
# Starts 10 actual mixnet nodes and tests real message transmission

set -e

echo "=============================================="
echo "  Mixnet Localhost Network Test"
echo "  Starting 10 actual nodes on localhost"
echo "=============================================="

LOG_DIR="/tmp/mixnet-test-$(date +%s)"
mkdir -p "$LOG_DIR"

cleanup() {
    echo ""
    echo "Cleaning up..."
    pkill -f "mixnet-node-test" 2>/dev/null || true
    sleep 2
    echo "Logs saved in: $LOG_DIR"
}

trap cleanup EXIT

cd /Users/abhinavnehra/git/Libp2p/go-libp2p-mixnet-impl

echo ""
echo "Step 1: Building mixnet node binary..."
echo "-------------------------------------------"

cat > /tmp/mixnet-node-test.go << 'GOEOF'
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/mixnet"
	"github.com/libp2p/go-libp2p/mixnet/relay"
	ma "github.com/multiformats/go-multiaddr"
)

type simpleRouting struct {
	host   host.Host
	relays []peer.AddrInfo
}

func (m *simpleRouting) FindProvidersAsync(ctx context.Context, c cid.Cid, count int) <-chan peer.AddrInfo {
	ch := make(chan peer.AddrInfo, len(m.relays))
	for _, r := range m.relays {
		ch <- r
	}
	close(ch)
	return ch
}

func (m *simpleRouting) Provide(ctx context.Context, c cid.Cid, b bool) error {
	return nil
}

func (m *simpleRouting) Bootstrap(ctx context.Context) error {
	return nil
}

func (m *simpleRouting) FindPeer(ctx context.Context, p peer.ID) (peer.AddrInfo, error) {
	return peer.AddrInfo{}, fmt.Errorf("not found")
}

func (m *simpleRouting) GetClosestPeers(ctx context.Context, key string) ([]peer.ID, error) {
	ids := make([]peer.ID, len(m.relays))
	for i, r := range m.relays {
		ids[i] = r.ID
	}
	return ids, nil
}

func (m *simpleRouting) GetValue(ctx context.Context, key string, opts ...routing.Option) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *simpleRouting) SetValue(ctx context.Context, key string, val []byte, opts ...routing.Option) error {
	return nil
}

func (m *simpleRouting) PutValue(ctx context.Context, key string, val []byte, opts ...routing.Option) error {
	return nil
}

func (m *simpleRouting) SearchValue(ctx context.Context, key string, opts ...routing.Option) (<-chan []byte, error) {
	ch := make(chan []byte)
	close(ch)
	return ch, nil
}

func main() {
	nodeID := os.Getenv("NODE_ID")
	listenPort := os.Getenv("MIXNET_PORT")
	if listenPort == "" {
		listenPort = "4001"
	}

	privKeyHex := os.Getenv("MIXNET_PRIV_KEY")
	var opts []libp2p.Option
	opts = append(opts, libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/127.0.0.1/tcp/%s", listenPort)))
	
	if privKeyHex != "" && len(privKeyHex) >= 64 {
		privKeyBytes, err := hex.DecodeString(privKeyHex)
		if err == nil {
			privKey, err := crypto.UnmarshalPrivateKey(privKeyBytes)
			if err == nil {
				opts = append(opts, libp2p.Identity(privKey))
			}
		}
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("[NODE %s] Started on port %s. PeerID: %s", nodeID, listenPort, h.ID())

	maxCircuits := 100
	maxBandwidth := int64(1024 * 1024 * 10)
	relayHandler := relay.NewHandler(h, maxCircuits, maxBandwidth)
	h.SetStreamHandler(relay.ProtocolID, relayHandler.HandleStream)
	h.SetStreamHandler("/lib-mix/key-exchange/1.0.0", relayHandler.HandleKeyExchange)

	// Parse peers from environment - format: PEERID@/ip4/ADDR/tcp/PORT
	peersStr := os.Getenv("MIXNET_PEERS")
	var relayInfos []peer.AddrInfo
	if peersStr != "" {
		for _, s := range strings.Split(peersStr, ",") {
			parts := strings.SplitN(s, "@", 2)
			if len(parts) != 2 { continue }
			pid, err := peer.Decode(parts[0])
			if err != nil { continue }
			maddr, err := ma.NewMultiaddr(parts[1])
			if err != nil { continue }
			if pid != h.ID() {
				fullAddr := maddr.Encapsulate(ma.StringCast("/p2p/" + pid.String()))
				h.Peerstore().AddAddr(pid, fullAddr, time.Hour)
				relayInfos = append(relayInfos, peer.AddrInfo{ID: pid, Addrs: []ma.Multiaddr{fullAddr}})
			}
		}
	}

	// Connect to all known peers
	log.Printf("[NODE %s] Connecting to %d peers...", nodeID, len(relayInfos))
	var wg sync.WaitGroup
	for _, p := range relayInfos {
		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			for attempt := 0; attempt < 10; attempt++ {
				if err := h.Connect(context.Background(), pi); err == nil {
					log.Printf("[NODE %s] ✓ Connected to %s", nodeID, pi.ID)
					return
				}
				time.Sleep(1 * time.Second)
			}
			log.Printf("[NODE %s] ✗ Failed to connect to %s", nodeID, pi.ID)
		}(p)
	}
	wg.Wait()

	cfg := mixnet.DefaultConfig()
	cfg.HopCount = 1
	cfg.CircuitCount = 2
	cfg.ErasureThreshold = 1
	cfg.SamplingSize = len(relayInfos)

	m, err := mixnet.NewMixnet(cfg, h, &simpleRouting{host: h, relays: relayInfos})
	if err != nil {
		log.Fatal(err)
	}

	messageCount := 0
	h.SetStreamHandler(mixnet.ProtocolID, func(s network.Stream) {
		messageCount++
		buf := make([]byte, 65536)
		n, err := s.Read(buf)
		if err != nil {
			log.Printf("[NODE %s] Error reading stream: %v", nodeID, err)
			return
		}
		log.Printf("[NODE %s] RECEIVED message #%d from %s: %s", nodeID, messageCount, s.Conn().RemotePeer(), string(buf[:n]))
		s.Close()
	})

	// Wait for network to stabilize
	time.Sleep(15 * time.Second)

	// Node 0 sends test messages
	if nodeID == "0" && len(relayInfos) >= 10 {
		time.Sleep(5 * time.Second)
		
		log.Printf("[NODE %s] Starting test message transmission...", nodeID)
		
		// Send to nodes 5, 7, 9
		targets := []struct{idx int; name string}{
			{5, "Node 5"},
			{7, "Node 7"},
			{9, "Node 9"},
		}
		
		for _, t := range targets {
			if t.idx < len(relayInfos) {
				target := relayInfos[t.idx].ID
				log.Printf("[NODE %s] >>> SENDING test message to %s (%s) via Mixnet", nodeID, target, t.name)
				
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				testMsg := fmt.Sprintf("Hello from Node 0! Test message to %s at %s", t.name, time.Now().Format("15:04:05"))
				err := m.Send(ctx, target, []byte(testMsg))
				cancel()
				
				if err != nil {
					log.Printf("[NODE %s] XXX SEND FAILED to %s: %v", nodeID, target, err)
				} else {
					log.Printf("[NODE %s] ✓✓✓ SEND SUCCESS to %s", nodeID, target)
				}
				time.Sleep(3 * time.Second)
			}
		}
		
		// Large message test
		log.Printf("[NODE %s] >>> SENDING large message (10KB) to Node 5", nodeID)
		largeMsg := make([]byte, 10240)
		for i := range largeMsg {
			largeMsg[i] = byte(i % 256)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = m.Send(ctx, relayInfos[5].ID, largeMsg)
		cancel()
		if err != nil {
			log.Printf("[NODE %s] XXX LARGE MESSAGE FAILED: %v", nodeID, err)
		} else {
			log.Printf("[NODE %s] ✓✓✓ LARGE MESSAGE (10KB) SUCCESS", nodeID)
		}
	}

	log.Printf("[NODE %s] Running... Press Ctrl+C to stop", nodeID)
	select {}
}
GOEOF

go build -o /tmp/mixnet-node-test /tmp/mixnet-node-test.go 2>&1 | head -20

if [ ! -f /tmp/mixnet-node-test ]; then
    echo "  Build failed!"
    exit 1
fi
echo "  Build successful!"

echo ""
echo "Step 2: Generating fixed peer identities..."
echo "-------------------------------------------"

# Generate fixed private keys for all nodes so peer IDs stay consistent
declare -a NODE_KEYS
declare -a NODE_IDS

for i in {0..9}; do
    # Generate a deterministic key based on node number
    NODE_KEYS[$i]=$(printf "%064d" $((i + 1)))
done

echo "  Generated fixed identities for 10 nodes"

echo ""
echo "Step 3: Starting all 10 nodes simultaneously..."
echo "-------------------------------------------"

# Start all nodes at once with pre-configured peer list
for i in {0..9}; do
    PORT=$((4001 + i))
    
    # Build peer list (all other nodes)
    PEERS=""
    for j in {0..9}; do
        if [ $i -ne $j ]; then
            PPORT=$((4001 + j))
            PEERS="${PEERS}${NODE_KEYS[$j]}@/ip4/127.0.0.1/tcp/${PPORT},"
        fi
    done
    PEERS=${PEERS%,}
    
    echo "  Starting Node $i on port $PORT..."
    
    (NODE_ID="$i" \
     MIXNET_PORT="$PORT" \
     MIXNET_PRIV_KEY="${NODE_KEYS[$i]}" \
     MIXNET_PEERS="$PEERS" \
     /tmp/mixnet-node-test 2>&1 | tee "$LOG_DIR/node$i.log") &
    
    sleep 0.2
done

echo ""
echo "  Waiting for connections and test messages (60 seconds)..."
sleep 60

echo ""
echo "Step 4: Checking results..."
echo "=============================================="

echo ""
echo "Connection Status:"
echo "-------------------------------------------"
for i in {0..9}; do
    LOG="$LOG_DIR/node$i.log"
    if [ -f "$LOG" ]; then
        CONNECTED=$(grep -c "✓ Connected to" "$LOG" 2>/dev/null || echo "0")
        STARTED=$(grep -c "Started on port" "$LOG" 2>/dev/null || echo "0")
        echo "  Node $i: Started=$STARTED, Successful Connections=$CONNECTED"
    fi
done

echo ""
echo "Messages SENT (Node 0):"
echo "-------------------------------------------"
grep -h "SENDING\|SEND SUCCESS\|SEND FAILED\|LARGE MESSAGE" "$LOG_DIR/node0.log" 2>/dev/null || echo "  No send logs found"

echo ""
echo "Messages RECEIVED:"
echo "-------------------------------------------"
for i in 5 7 9; do
    LOG="$LOG_DIR/node$i.log"
    if [ -f "$LOG" ]; then
        RECEIVED=$(grep "RECEIVED message" "$LOG" 2>/dev/null || true)
        if [ -n "$RECEIVED" ]; then
            echo "  Node $i:"
            echo "$RECEIVED" | sed 's/^/    /'
        else
            echo "  Node $i: No messages received"
        fi
    fi
done

echo ""
echo "Large Message Test:"
echo "-------------------------------------------"
grep "LARGE MESSAGE" "$LOG_DIR/node0.log" 2>/dev/null || echo "  No large message send logs"
grep "RECEIVED.*10240\|RECEIVED message" "$LOG_DIR/node5.log" 2>/dev/null | grep "10240\|bytes" | head -1 | sed 's/^/    /' || echo "  No large message receive logs"

echo ""
echo "=============================================="
echo "  Test Complete!"
echo "=============================================="
echo "Logs: $LOG_DIR"
