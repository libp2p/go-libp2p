package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/mixnet"
	"github.com/libp2p/go-libp2p/mixnet/relay"
	ma "github.com/multiformats/go-multiaddr"
)

type mockRouting struct {
	routing.Routing
	relays []peer.AddrInfo
}

func (m *mockRouting) FindProvidersAsync(ctx context.Context, c cid.Cid, count int) <-chan peer.AddrInfo {
	ch := make(chan peer.AddrInfo, len(m.relays))
	for _, r := range m.relays {
		ch <- r
	}
	close(ch)
	return ch
}

func (m *mockRouting) Provide(ctx context.Context, c cid.Cid, b bool) error {
	return nil
}

func main() {
	listenPort := os.Getenv("MIXNET_PORT")
	if listenPort == "" {
		listenPort = "4001"
	}

	var opts []libp2p.Option
	opts = append(opts, libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%s", listenPort)))

	privKeyHex := os.Getenv("MIXNET_PRIV_KEY")
	if privKeyHex != "" {
		privKeyBytes, err := hex.DecodeString(privKeyHex)
		if err != nil {
			log.Fatal(err)
		}
		privKey, err := crypto.UnmarshalPrivateKey(privKeyBytes)
		if err != nil {
			log.Fatal(err)
		}
		opts = append(opts, libp2p.Identity(privKey))
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Mixnet Universal Node Started. PeerID: %s", h.ID())

	// 1. Register Relay Handler (Every node is a relay)
	maxCircuits := 100
	maxBandwidth := int64(1024 * 1024 * 10)
	relayHandler := relay.NewHandler(h, maxCircuits, maxBandwidth)
	h.SetStreamHandler(relay.ProtocolID, relayHandler.HandleStream)
	h.SetStreamHandler(mixnet.KeyExchangeProtocolID, relayHandler.HandleKeyExchange)

	// 2. Parse Peers from environment (supports both MIXNET_PEERS and MIXNET_RELAYS)
	peersStr := os.Getenv("MIXNET_PEERS")
	if peersStr == "" {
		peersStr = os.Getenv("MIXNET_RELAYS")
	}
	var relayInfos []peer.AddrInfo
	if peersStr != "" {
		for _, s := range strings.Split(peersStr, ",") {
			parts := strings.Split(s, "@")
			if len(parts) != 2 { continue }
			pid, _ := peer.Decode(parts[0])
			maddr, _ := ma.NewMultiaddr(parts[1])
			h.Peerstore().AddAddr(pid, maddr, time.Hour)
			relayInfos = append(relayInfos, peer.AddrInfo{ID: pid, Addrs: []ma.Multiaddr{maddr}})
			
			// Auto-connect to neighbors
			go func(p peer.AddrInfo) {
				for {
					if err := h.Connect(context.Background(), p); err == nil {
						break
					}
					time.Sleep(2 * time.Second)
				}
			}(peer.AddrInfo{ID: pid, Addrs: []ma.Multiaddr{maddr}})
		}
	}

	// 3. Initialize Mixnet (Sender/Receiver logic)
	cfg := mixnet.DefaultConfig()
	cfg.HopCount = 1
	cfg.CircuitCount = 2
	cfg.ErasureThreshold = 1
	cfg.SamplingSize = 8
	
	m, err := mixnet.NewMixnet(cfg, h, &mockRouting{relays: relayInfos})
	if err != nil {
		log.Fatal(err)
	}

	// 4. Handle Incoming Private Messages
	h.SetStreamHandler(mixnet.ProtocolID, func(s network.Stream) {
		log.Printf("[RECV] Private mixnet stream received from %s", s.Conn().RemotePeer())
		s.Close()
	})

	// 5. Periodic Random Pings to other nodes via Mixnet
	go func() {
		time.Sleep(15 * time.Second) // Wait for network to stabilize
		for {
			if len(relayInfos) == 0 { 
				time.Sleep(5 * time.Second)
				continue 
			}
			
			// Pick a random target
			target := relayInfos[time.Now().UnixNano()%int64(len(relayInfos))]
			
			log.Printf("[SEND] Sending private ping to %s via Mixnet", target.ID)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := m.Send(ctx, target.ID, []byte(fmt.Sprintf("Ping from %s at %s", h.ID(), time.Now().Format(time.RFC3339))))
			if err != nil {
				log.Printf("[ERR] Send failed: %v", err)
			} else {
				log.Printf("[SUCCESS] Private message sent to %s", target.ID)
			}
			cancel()
			time.Sleep(20 * time.Second)
		}
	}()

	// HTTP Status and Trigger API
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "PeerID: %s\nRelays: %d\n", h.ID(), len(relayInfos))
	})
	
	log.Println("HTTP Status running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
