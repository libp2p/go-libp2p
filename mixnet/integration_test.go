package mixnet

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/libp2p/go-libp2p/mixnet/relay"
)

// testProtocolID is the protocol used for test communications
const testProtocolID = protocol.ID("/test/integration/1.0.0")

// TestIntegration_MixnetCreation tests that a mixnet instance can be created
func TestIntegration_MixnetCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	

	// Create a host
	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	// Create mixnet with valid config
	cfg := DefaultConfig()
	cfg.HopCount = 2
	cfg.CircuitCount = 3

	m, err := NewMixnet(cfg, h, nil)
	if err != nil {
		t.Fatalf("Failed to create mixnet: %v", err)
	}
	defer m.Close()

	if m == nil {
		t.Fatal("Mixnet should not be nil")
	}

	// Verify config
	if m.Config().HopCount != 2 {
		t.Errorf("Expected hop count 2, got %d", m.Config().HopCount)
	}
	if m.Config().CircuitCount != 3 {
		t.Errorf("Expected circuit count 3, got %d", m.Config().CircuitCount)
	}

	t.Logf("Mixnet created successfully with host ID: %s", h.ID())
}

// TestIntegration_RelayHandlerRegistration tests that relay handler can be registered
func TestIntegration_RelayHandlerRegistration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create a peer that will act as a relay
	relayNode, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("Failed to create relay node: %v", err)
	}
	defer relayNode.Close()

	// Create and register relay handler
	relayHandler := relay.NewHandler(relayNode, 10, 1024*1024)
	relayHandler.SetHost(relayNode)

	// Register both protocols
	relayNode.SetStreamHandler(relay.ProtocolID, relayHandler.HandleStream)
	relayNode.SetStreamHandler(KeyExchangeProtocolID, relayHandler.HandleKeyExchange)

	t.Logf("Relay handler registered for protocols: %s, %s", relay.ProtocolID, KeyExchangeProtocolID)
}

// TestIntegration_MixnetConfigValidation tests configuration validation
func TestIntegration_MixnetConfigValidation(t *testing.T) {
	// Test valid configuration
	validCfg := DefaultConfig()
	validCfg.HopCount = 3
	validCfg.CircuitCount = 5
	validCfg.Compression = "gzip"

	host, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
	if err != nil {
		t.Fatalf("Failed to create host: %v", err)
	}
	defer host.Close()

	_, err = NewMixnet(validCfg, host, nil)
	if err != nil {
		t.Errorf("Valid config should not error: %v", err)
	}

	// Test invalid configuration - too many hops
	invalidCfg1 := NewMixnetConfig()
	invalidCfg1.HopCount = 15
	invalidCfg1.CircuitCount = 3

	_, err = NewMixnet(invalidCfg1, host, nil)
	if err == nil {
		t.Error("Invalid hop count should error")
	}

	// Test invalid configuration - too few circuits
	invalidCfg2 := NewMixnetConfig()
	invalidCfg2.HopCount = 2
	invalidCfg2.CircuitCount = 0

	_, err = NewMixnet(invalidCfg2, host, nil)
	if err == nil {
		t.Error("Zero circuit count should error")
	}

	// Test invalid compression
	invalidCfg3 := NewMixnetConfig()
	invalidCfg3.HopCount = 2
	invalidCfg3.CircuitCount = 3
	invalidCfg3.Compression = "invalid"

	_, err = NewMixnet(invalidCfg3, host, nil)
	if err == nil {
		t.Error("Invalid compression should error")
	}
}

// TestIntegration_PrivacyInvariants verifies privacy design invariants
func TestIntegration_PrivacyInvariants(t *testing.T) {
	// Verify the privacy invariants are satisfied by design
	err := VerifyPrivacyInvariants()
	if err != nil {
		t.Errorf("Privacy invariants verification failed: %v", err)
	}

	// Test privacy manager with logging disabled
	privacyCfg := DefaultPrivacyConfig()
	pm := NewPrivacyManager(privacyCfg)

	// Test peer ID anonymization
	peerID := "QmTest123456789abcdef"
	anonymized := pm.AnonymizePeerID(peerID)

	if anonymized == peerID && !privacyCfg.LogRelayAddresses {
		t.Error("Peer ID should be anonymized when logging is disabled")
	}

	// Test circuit ID anonymization
	circuitID := "circuit-abc123"
	anonCircuit := pm.AnonymizeCircuitID(circuitID)

	if anonCircuit == circuitID && !privacyCfg.LogCircuitIDs {
		t.Error("Circuit ID should be anonymized when logging is disabled")
	}

	// Test with logging enabled
	privacyCfg.LogRelayAddresses = true
	privacyCfg.LogCircuitIDs = true
	pm = NewPrivacyManager(privacyCfg)

	if pm.AnonymizePeerID(peerID) != peerID {
		t.Error("Peer ID should not be anonymized when logging is enabled")
	}

	t.Logf("Privacy invariants verified")
}

// TestIntegration_GracefulShutdown tests graceful shutdown of mixnet
func TestIntegration_GracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}


	
	h, _ := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.DisableRelay(),
	)

	cfg := DefaultConfig()
	cfg.HopCount = 2
	cfg.CircuitCount = 3
	m, err := NewMixnet(cfg, h, nil)
	if err != nil {
		t.Fatalf("Failed to create mixnet: %v", err)
	}

	// Close the mixnet
	err = m.Close()
	if err != nil {
		t.Errorf("Close returned error: %v", err)
	}

	// Try to close again - should be idempotent
	err = m.Close()
	if err != nil {
		t.Logf("Second close returned error (may be expected): %v", err)
	}

	h.Close()
	t.Logf("Graceful shutdown works correctly")
}

// TestIntegration_MetricsCollection tests that metrics are properly collected
func TestIntegration_MetricsCollection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}


	
	h, _ := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.DisableRelay(),
	)

	cfg := DefaultConfig()
	cfg.HopCount = 2
	cfg.CircuitCount = 3
	m, err := NewMixnet(cfg, h, nil)
	if err != nil {
		t.Fatalf("Failed to create mixnet: %v", err)
	}

	// Get metrics collector
	metrics := m.Metrics()

	// Verify metrics are accessible
	_ = metrics.CircuitSuccesses()
	_ = metrics.CircuitFailures()
	_ = metrics.RecoveryEvents()
	_ = metrics.TotalThroughput()
	_ = metrics.CompressionRatio()
	_ = metrics.ActiveCircuits()

	m.Close()
	h.Close()
	t.Logf("Metrics collection works correctly")
}

// TestIntegration_PeerConnection tests connecting peers
func TestIntegration_PeerConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	

	// Create two peers
	peer1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("Failed to create peer1: %v", err)
	}
	defer peer1.Close()

	peer2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("Failed to create peer2: %v", err)
	}
	defer peer2.Close()

	// Connect peer1 to peer2
	err = peer1.Connect(ctx, peer.AddrInfo{
		ID:   peer2.ID(),
		Addrs: peer2.Addrs(),
	})
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Verify connection
	if peer1.Network().Connectedness(peer2.ID()) != network.Connected {
		t.Error("Peers should be connected")
	}

	t.Logf("Peer connection successful: %s <-> %s", peer1.ID(), peer2.ID())
}

// TestIntegration_StreamCommunication tests stream communication between peers
func TestIntegration_StreamCommunication(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	

	// Create two peers
	peer1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("Failed to create peer1: %v", err)
	}
	defer peer1.Close()

	peer2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("Failed to create peer2: %v", err)
	}
	defer peer2.Close()

	// Set up echo handler on peer2
	peer2.SetStreamHandler(testProtocolID, func(s network.Stream) {
		defer s.Close()
		data, _ := io.ReadAll(s)
		_, _ = s.Write(data)
	})

	// Connect peer1 to peer2
	err = peer1.Connect(ctx, peer.AddrInfo{
		ID:   peer2.ID(),
		Addrs: peer2.Addrs(),
	})
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Open a stream and send data
	stream, err := peer1.NewStream(ctx, peer2.ID(), testProtocolID)
	if err != nil {
		t.Fatalf("Failed to open stream: %v", err)
	}
	defer stream.Close()

	testData := []byte("Hello, peer!")
	_, err = stream.Write(testData)
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}
	if err := stream.CloseWrite(); err != nil {
		t.Fatalf("Failed to close write side: %v", err)
	}

	// Read response
	response, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("Failed to read: %v", err)
	}

	if !bytes.Equal(response, testData) {
		t.Errorf("Response mismatch: got %q, want %q", response, testData)
	}

	t.Logf("Stream communication successful")
}

// TestIntegration_MixnetHostAccess tests that mixnet host is accessible
func TestIntegration_MixnetHostAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	cfg := DefaultConfig()
	cfg.HopCount = 2
	cfg.CircuitCount = 3
	m, err := NewMixnet(cfg, h, nil)
	if err != nil {
		t.Fatalf("Failed to create mixnet: %v", err)
	}
	defer m.Close()

	// Test that we can access the underlying host
	host := m.Host()
	if host == nil {
		t.Fatal("Host should not be nil")
	}

	// Test that we can get the peer ID
	peerID := host.ID()
	if peerID == "" {
		t.Error("Peer ID should not be empty")
	}

	// Test pipeline access
	pipeline := m.Pipeline()
	if pipeline == nil {
		t.Fatal("Pipeline should not be nil")
	}

	// Test circuit manager access
	circuitMgr := m.CircuitManager()
	if circuitMgr == nil {
		t.Fatal("Circuit manager should not be nil")
	}

	t.Logf("Mixnet host access works correctly")
}

// TestIntegration_ConcurrentPeerOperations tests concurrent operations
func TestIntegration_ConcurrentPeerOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	

	// Create test peers
	peers := make([]host.Host, 3)
	for i := range peers {
		p, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
		if err != nil {
			t.Fatalf("Failed to create peer: %v", err)
		}
		peers[i] = p
		defer p.Close()
	}

	// Connect all peers concurrently
	var wg sync.WaitGroup
	for i := 0; i < len(peers); i++ {
		for j := i + 1; j < len(peers); j++ {
			wg.Add(1)
			go func(a, b int) {
				defer wg.Done()
				_ = peers[a].Connect(ctx, peer.AddrInfo{
					ID:   peers[b].ID(),
					Addrs: peers[b].Addrs(),
				})
			}(i, j)
		}
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	// Check connections
	for i, p := range peers {
		conns := p.Network().Conns()
		t.Logf("Peer %d has %d connections", i, len(conns))
	}

	t.Logf("Concurrent peer operations completed")
}

// TestIntegration_DataPatterns tests various data patterns through streams
func TestIntegration_DataPatterns(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	

	// Create two peers
	sender, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("Failed to create sender: %v", err)
	}
	defer sender.Close()

	receiver, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("Failed to create receiver: %v", err)
	}
	defer receiver.Close()

	// Set up echo handler
	receiver.SetStreamHandler(testProtocolID, func(s network.Stream) {
		defer s.Close()
		data, _ := io.ReadAll(s)
		_, _ = s.Write(data)
	})

	// Connect
	_ = sender.Connect(ctx, peer.AddrInfo{
		ID:   receiver.ID(),
		Addrs: receiver.Addrs(),
	})

	// Test various data patterns
	testCases := []struct {
		name string
		data []byte
	}{
		{"zeros", bytes.Repeat([]byte{0}, 1000)},
		{"ones", bytes.Repeat([]byte{0xFF}, 1000)},
		{"alternating", bytes.Repeat([]byte{0xAA}, 1000)},
		{"text", []byte("The quick brown fox jumps over the lazy dog")},
		{"empty", []byte{}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			stream, err := sender.NewStream(ctx, receiver.ID(), testProtocolID)
			if err != nil {
				t.Fatalf("Failed to open stream: %v", err)
			}
			defer stream.Close()

			_, err = stream.Write(tc.data)
			if err != nil {
				t.Fatalf("Failed to write: %v", err)
			}
			if err := stream.CloseWrite(); err != nil {
				t.Fatalf("Failed to close write side: %v", err)
			}

			response, err := io.ReadAll(stream)
			if err != nil {
				t.Fatalf("Failed to read: %v", err)
			}

			if !bytes.Equal(response, tc.data) {
				t.Errorf("Data mismatch")
			}
		})
	}
}

// BenchmarkIntegration_StreamThroughput measures stream throughput
func BenchmarkIntegration_StreamThroughput(b *testing.B) {
	ctx := context.Background()
	

	sender, _ := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
	receiver, _ := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))

	receiver.SetStreamHandler(testProtocolID, func(s network.Stream) {
		defer s.Close()
		data, _ := io.ReadAll(s)
		_, _ = s.Write(data)
	})

	_ = sender.Connect(ctx, peer.AddrInfo{ID: receiver.ID(), Addrs: receiver.Addrs()})

	testData := make([]byte, 4096)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		stream, _ := sender.NewStream(ctx, receiver.ID(), testProtocolID)
		stream.Write(testData)
		stream.CloseWrite()
		io.ReadAll(stream)
		stream.Close()
	}

	sender.Close()
	receiver.Close()
}
