package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
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
			if len(parts) != 2 {
				continue
			}
			pid, _ := peer.Decode(parts[0])
			maddr, _ := ma.NewMultiaddr(parts[1])
			h.Peerstore().AddAddr(pid, maddr, time.Hour)
			h.Peerstore().AddProtocols(pid, mixnet.ProtocolID)
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
	cfg.SamplingSize = 0
	if mode := os.Getenv("MIXNET_ENCRYPTION_MODE"); mode != "" {
		cfg.EncryptionMode = mixnet.EncryptionMode(mode)
	}

	m, err := mixnet.NewMixnet(cfg, h, &mockRouting{relays: relayInfos})
	if err != nil {
		log.Fatal(err)
	}

	// 4. Handle Incoming Private Messages (real decrypt path)
	recvHandler := m.ReceiveHandler()
	h.SetStreamHandler(mixnet.ProtocolID, func(s network.Stream) {
		log.Printf("[RECV] Private mixnet stream received from %s", s.Conn().RemotePeer())
		recvHandler(s)
	})

	// 5. Periodic Random Pings or Benchmarks
	if os.Getenv("MIXNET_BENCH") == "1" && os.Getenv("MIXNET_BENCH_SENDER") == "1" {
		go runBenchmarks(h, relayInfos)
	} else {
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
				startNs := time.Now().UnixNano()
				log.Printf("[BENCH_SEND_START] epoch_ns=%d dest=%s", startNs, target.ID)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := m.Send(ctx, target.ID, []byte(fmt.Sprintf("Ping from %s at %s", h.ID(), time.Now().Format(time.RFC3339))))
				if err != nil {
					log.Printf("[ERR] Send failed: %v", err)
					log.Printf("[BENCH_SEND_FAIL] epoch_ns=%d dest=%s err=%v", time.Now().UnixNano(), target.ID, err)
				} else {
					log.Printf("[SUCCESS] Private message sent to %s", target.ID)
					log.Printf("[BENCH_SEND_SUCCESS] epoch_ns=%d dest=%s", time.Now().UnixNano(), target.ID)
				}
				cancel()
				time.Sleep(20 * time.Second)
			}
		}()
	}

	// HTTP Status and Trigger API
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "PeerID: %s\nRelays: %d\n", h.ID(), len(relayInfos))
	})

	log.Println("HTTP Status running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func runBenchmarks(h host.Host, relayInfos []peer.AddrInfo) {
	time.Sleep(20 * time.Second) // Initial delay for network bootstrap
	if len(relayInfos) == 0 {
		log.Printf("[BENCH] no relays discovered; skipping benchmark")
		return
	}
	ensurePeerConnectivity(h, relayInfos, 3*time.Minute)

	sizes := parseSizeList("MIXNET_BENCH_SIZES", []int{
		1024,
		16 * 1024,
		64 * 1024,
		256 * 1024,
		1024 * 1024,
		5 * 1024 * 1024,
		10 * 1024 * 1024,
		25 * 1024 * 1024,
		50 * 1024 * 1024,
	})
	hops := parseIntList("MIXNET_BENCH_HOPS", []int{3, 5, 7, 10})
	circuits := parseIntList("MIXNET_BENCH_CIRCUITS", []int{3, 5, 8})
	modes := parseStringList("MIXNET_BENCH_MODES", []string{"full", "header-only"})
	iterations := parseInt("MIXNET_BENCH_ITER", 1)
	streamDuration := parseInt("MIXNET_BENCH_STREAM_DURATION", 5)

	for _, mode := range modes {
		for _, hopCount := range hops {
			for _, circuitCount := range circuits {
				requiredRelays := relayPoolRequirement(hopCount, circuitCount)
				if requiredRelays > len(relayInfos)-1 {
					log.Printf("[BENCH_SKIP] mode=%s hops=%d circuits=%d reason=insufficient_relays have=%d need=%d",
						mode, hopCount, circuitCount, len(relayInfos)-1, requiredRelays)
					continue
				}
				for _, size := range sizes {
					for i := 0; i < iterations; i++ {
						target := relayInfos[(size+hopCount+circuitCount+i)%len(relayInfos)]
						cfg := mixnet.DefaultConfig()
						cfg.HopCount = hopCount
						cfg.CircuitCount = circuitCount
						cfg.ErasureThreshold = benchmarkThreshold(circuitCount)
						cfg.SamplingSize = requiredRelays
						cfg.EncryptionMode = mixnet.EncryptionMode(mode)
						m, err := mixnet.NewMixnet(cfg, h, &mockRouting{relays: relayInfos})
						if err != nil {
							log.Printf("[BENCH_ERR] create mixnet: %v", err)
							continue
						}

						payload := make([]byte, size)
						if _, err := rand.Read(payload); err != nil {
							log.Printf("[BENCH_ERR] rand: %v", err)
							_ = m.Close()
							continue
						}

						start := time.Now()
						startNs := start.UnixNano()
						log.Printf("[BENCH_E2E_START] epoch_ns=%d mode=%s size=%d hops=%d circuits=%d iter=%d dest=%s",
							startNs, mode, size, hopCount, circuitCount, i, target.ID)
						ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
						err = m.Send(ctx, target.ID, payload)
						cancel()
						if err != nil {
							log.Printf("[BENCH_E2E_FAIL] epoch_ns=%d mode=%s size=%d hops=%d circuits=%d iter=%d dest=%s err=%v",
								time.Now().UnixNano(), mode, size, hopCount, circuitCount, i, target.ID, err)
						} else {
							elapsed := time.Since(start)
							log.Printf("[BENCH_E2E_SUCCESS] epoch_ns=%d mode=%s size=%d hops=%d circuits=%d iter=%d dest=%s elapsed_ns=%d",
								time.Now().UnixNano(), mode, size, hopCount, circuitCount, i, target.ID, elapsed.Nanoseconds())
						}
						_ = m.Close()
					}
				}
			}
		}
		runStreamingBenchmarks(h, relayInfos, mode, hops, circuits, streamDuration)
	}
	log.Printf("[BENCH_DONE] epoch_ns=%d", time.Now().UnixNano())
}

func ensurePeerConnectivity(h host.Host, peers []peer.AddrInfo, timeout time.Duration) {
	required := len(peers) / 2
	if required < 8 {
		required = 8
	}
	if required > len(peers) {
		required = len(peers)
	}
	deadline := time.Now().Add(timeout)
	for {
		connected := 0
		for _, p := range peers {
			if p.ID == h.ID() {
				continue
			}
			if h.Network().Connectedness(p.ID) == network.Connected {
				connected++
				continue
			}
			_ = h.Connect(context.Background(), p)
			if h.Network().Connectedness(p.ID) == network.Connected {
				connected++
			}
		}
		log.Printf("[BENCH_CONNECTIVITY] connected=%d required=%d total=%d", connected, required, len(peers))
		if connected >= required {
			return
		}
		if time.Now().After(deadline) {
			log.Printf("[BENCH_CONNECTIVITY] timeout reached; continuing with partial connectivity")
			return
		}
		time.Sleep(3 * time.Second)
	}
}

func runStreamingBenchmarks(h host.Host, relayInfos []peer.AddrInfo, mode string, hops []int, circuits []int, durationSeconds int) {
	audioBitrates := parseIntList("MIXNET_BENCH_AUDIO_BITRATES", []int{64, 128, 256, 320})
	videoBitrates := parseIntList("MIXNET_BENCH_VIDEO_BITRATES", []int{500, 1000, 3000, 6000})

	runStream := func(kind string, bitrateKbps int, hopCount int, circuitCount int) {
		requiredRelays := relayPoolRequirement(hopCount, circuitCount)
		cfg := mixnet.DefaultConfig()
		cfg.HopCount = hopCount
		cfg.CircuitCount = circuitCount
		cfg.ErasureThreshold = benchmarkThreshold(circuitCount)
		cfg.SamplingSize = requiredRelays
		cfg.EncryptionMode = mixnet.EncryptionMode(mode)
		m, err := mixnet.NewMixnet(cfg, h, &mockRouting{relays: relayInfos})
		if err != nil {
			log.Printf("[BENCH_ERR] create mixnet: %v", err)
			return
		}
		defer m.Close()

		bytesPerSecond := (bitrateKbps * 1000) / 8
		start := time.Now()
		startNs := start.UnixNano()
		target := relayInfos[(bitrateKbps+hopCount+circuitCount)%len(relayInfos)]
		log.Printf("[BENCH_STREAM_START] epoch_ns=%d mode=%s kind=%s bitrate_kbps=%d duration_s=%d hops=%d circuits=%d dest=%s",
			startNs, mode, kind, bitrateKbps, durationSeconds, hopCount, circuitCount, target.ID)
		for i := 0; i < durationSeconds; i++ {
			chunk := make([]byte, bytesPerSecond)
			if _, err := rand.Read(chunk); err != nil {
				log.Printf("[BENCH_STREAM_FAIL] epoch_ns=%d mode=%s kind=%s bitrate_kbps=%d duration_s=%d hops=%d circuits=%d dest=%s err=%v",
					time.Now().UnixNano(), mode, kind, bitrateKbps, durationSeconds, hopCount, circuitCount, target.ID, err)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			if err := m.Send(ctx, target.ID, chunk); err != nil {
				cancel()
				log.Printf("[BENCH_STREAM_FAIL] epoch_ns=%d mode=%s kind=%s bitrate_kbps=%d duration_s=%d hops=%d circuits=%d dest=%s err=%v",
					time.Now().UnixNano(), mode, kind, bitrateKbps, durationSeconds, hopCount, circuitCount, target.ID, err)
				return
			}
			cancel()
		}
		elapsed := time.Since(start)
		log.Printf("[BENCH_STREAM_SUCCESS] epoch_ns=%d mode=%s kind=%s bitrate_kbps=%d duration_s=%d hops=%d circuits=%d dest=%s elapsed_ns=%d",
			time.Now().UnixNano(), mode, kind, bitrateKbps, durationSeconds, hopCount, circuitCount, target.ID, elapsed.Nanoseconds())
	}

	for _, hopCount := range hops {
		for _, circuitCount := range circuits {
			requiredRelays := relayPoolRequirement(hopCount, circuitCount)
			if requiredRelays > len(relayInfos)-1 {
				log.Printf("[BENCH_SKIP] mode=%s kind=stream hops=%d circuits=%d reason=insufficient_relays have=%d need=%d",
					mode, hopCount, circuitCount, len(relayInfos)-1, requiredRelays)
				continue
			}
			for _, bitrate := range audioBitrates {
				runStream("audio", bitrate, hopCount, circuitCount)
			}
			for _, bitrate := range videoBitrates {
				runStream("video", bitrate, hopCount, circuitCount)
			}
		}
	}
}

func relayPoolRequirement(hopCount int, circuitCount int) int {
	// Keep benchmark feasibility checks aligned with production discovery,
	// which requires a 3x candidate relay pool.
	return hopCount * circuitCount * 3
}

func benchmarkThreshold(circuitCount int) int {
	threshold := (circuitCount*6 + 9) / 10
	if threshold >= circuitCount {
		threshold = circuitCount - 1
	}
	if threshold < 1 {
		threshold = 1
	}
	return threshold
}

func parseIntList(env string, def []int) []int {
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func parseStringList(env string, def []string) []string {
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		val := strings.TrimSpace(p)
		if val == "" {
			continue
		}
		out = append(out, val)
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func parseInt(env string, def int) int {
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		return def
	}
	val, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return val
}

func parseSizeList(env string, def []int) []int {
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		val := strings.TrimSpace(p)
		if val == "" {
			continue
		}
		n, err := parseSize(val)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func parseSize(raw string) (int, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	multiplier := 1
	switch {
	case strings.HasSuffix(s, "KB"):
		multiplier = 1024
		s = strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "MB"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "GB"):
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return n * multiplier, nil
}
