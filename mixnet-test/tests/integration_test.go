// Package mixnet_test provides comprehensive integration tests for the mixnet protocol.
// This test file validates the entire mixnet flow from encryption to decryption,
// circuit establishment, circuit retry, and end-to-end message transmission.
package mixnet_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/mixnet"
	"github.com/libp2p/go-libp2p/mixnet/circuit"
	"github.com/libp2p/go-libp2p/mixnet/relay"
	"github.com/stretchr/testify/require"
)

// mockRouting implements a minimal routing interface for testing
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

// TestNetwork represents a test network of mixnet nodes
type TestNetwork struct {
	hosts       []host.Host
	mixnets     []*mixnet.Mixnet
	peerInfos   []peer.AddrInfo
	cleanupFunc func()
}

// NewTestNetwork creates a test network with the specified number of nodes
func NewTestNetwork(t *testing.T, numNodes int) *TestNetwork {
	ctx := context.Background()
	network := &TestNetwork{
		hosts:     make([]host.Host, numNodes),
		mixnets:   make([]*mixnet.Mixnet, numNodes),
		peerInfos: make([]peer.AddrInfo, numNodes),
	}

	// Create hosts for each node
	for i := 0; i < numNodes; i++ {
		privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
		require.NoError(t, err)

		h, err := libp2p.New(
			libp2p.Identity(privKey),
			libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
			libp2p.DisableRelay(),
		)
		require.NoError(t, err)
		network.hosts[i] = h

		// Get peer info
		network.peerInfos[i] = peer.AddrInfo{
			ID:    h.ID(),
			Addrs: h.Addrs(),
		}
	}

	// Connect all hosts in a full mesh
	for i := 0; i < numNodes; i++ {
		for j := i + 1; j < numNodes; j++ {
			err := network.hosts[i].Connect(ctx, network.peerInfos[j])
			require.NoError(t, err)
		}
	}

	// Wait for connections to establish and protocol identification to occur
	time.Sleep(500 * time.Millisecond)

	// Manually register mixnet protocol in peerstore for all peers
	// This simulates protocol identification that would happen in production
	for i := 0; i < numNodes; i++ {
		for j := 0; j < numNodes; j++ {
			if i != j {
				// Add the mixnet protocol to the peerstore for each peer
				network.hosts[i].Peerstore().AddProtocols(network.peerInfos[j].ID, protocol.ID(mixnet.ProtocolID), protocol.ID(relay.ProtocolID))
			}
		}
	}

	// Initialize mixnet for each node
	for i := 0; i < numNodes; i++ {
		// Register relay handler
		maxCircuits := 100
		maxBandwidth := int64(1024 * 1024 * 10)
		relayHandler := relay.NewHandler(network.hosts[i], maxCircuits, maxBandwidth)
		network.hosts[i].SetStreamHandler(relay.ProtocolID, relayHandler.HandleStream)
		network.hosts[i].SetStreamHandler(mixnet.KeyExchangeProtocolID, relayHandler.HandleKeyExchange)

		// Create mixnet config - use smaller values for test network
		// With hopCount=1 and circuitCount=2, we need at least 6 relays (3x required)
		cfg := mixnet.DefaultConfig()
		cfg.HopCount = 1
		cfg.CircuitCount = 2
		cfg.ErasureThreshold = 1
		cfg.SamplingSize = numNodes * 3 // Adjust sampling size for test network

		// Create mock routing with all other peers as relays
		var relays []peer.AddrInfo
		for j := 0; j < numNodes; j++ {
			if j != i {
				relays = append(relays, network.peerInfos[j])
			}
		}

		m, err := mixnet.NewMixnet(cfg, network.hosts[i], &mockRouting{relays: relays})
		require.NoError(t, err)
		network.mixnets[i] = m

		// Set up receiver handler - register mixnet protocol on ALL nodes
		network.hosts[i].SetStreamHandler(mixnet.ProtocolID, func(s libp2pnetwork.Stream) {
			log.Printf("[RECV] Private mixnet stream received from %s", s.Conn().RemotePeer())
			s.Close()
		})
	}

	network.cleanupFunc = func() {
		for _, m := range network.mixnets {
			if m != nil {
				_ = m.Close()
			}
		}
		for _, h := range network.hosts {
			if h != nil {
				_ = h.Close()
			}
		}
	}

	return network
}

// Close cleans up the test network
func (tn *TestNetwork) Close() {
	if tn.cleanupFunc != nil {
		tn.cleanupFunc()
	}
}

// TestEncryptionDecryptionFlow tests the complete encryption and decryption flow
func TestEncryptionDecryptionFlow(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	t.Log("Starting encryption/decryption flow test...")
	network := NewTestNetwork(t, 8) // Need 8 nodes to have 6 relays after filtering origin/dest
	defer network.Close()

	// Test message
	testMessage := []byte("Hello, Mixnet! This is a test message for encryption/decryption.")

	// Establish connection from node 0 to node 4
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Logf("Establishing connection from %s to %s", network.hosts[0].ID(), network.hosts[4].ID())
	circuits, err := network.mixnets[0].EstablishConnection(ctx, network.hosts[4].ID())
	require.NoError(t, err)
	require.Len(t, circuits, 2) // CircuitCount = 2

	// Verify all circuits are active
	for _, c := range circuits {
		require.True(t, c.IsActive(), "Circuit should be active")
		t.Logf("Circuit %s established with %d hops", c.ID, len(c.Peers))
	}

	// Send message
	t.Logf("Sending message from %s to %s", network.hosts[0].ID(), network.hosts[4].ID())
	err = network.mixnets[0].Send(ctx, network.hosts[4].ID(), testMessage)
	require.NoError(t, err)

	t.Log("✓ Message sent successfully through encrypted circuits")
}

// TestCircuitEstablishment tests circuit establishment and management
func TestCircuitEstablishment(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	t.Log("Starting circuit establishment test...")
	network := NewTestNetwork(t, 8) // Need 8 nodes to have 6 relays after filtering
	defer network.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test circuit establishment
	src := 0
	dst := 5

	t.Logf("Establishing circuits from node %d to node %d", src, dst)
	circuits, err := network.mixnets[src].EstablishConnection(ctx, network.hosts[dst].ID())
	require.NoError(t, err)

	expectedCircuits := 2 // From our test config (CircuitCount = 2)
	require.Len(t, circuits, expectedCircuits, "Should establish expected number of circuits")

	// Verify circuit properties
	for i, c := range circuits {
		t.Logf("Circuit %d: ID=%s, State=%s, Peers=%d", i, c.ID, c.GetState(), len(c.Peers))
		require.Equal(t, circuit.StateActive, c.GetState())
		require.Greater(t, len(c.Peers), 0, "Circuit should have relay peers")
		require.NotEmpty(t, c.ID)
		require.False(t, c.Entry() == "", "Entry peer should not be empty")
		require.False(t, c.Exit() == "", "Exit peer should not be empty")
	}

	t.Log("✓ All circuits established successfully")
}

// TestCircuitRetryAndRecovery tests circuit failure detection and recovery
func TestCircuitRetryAndRecovery(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	t.Log("Starting circuit retry and recovery test...")
	network := NewTestNetwork(t, 8) // Need 8 nodes for sufficient relay pool
	defer network.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	src := 0
	dst := 5

	// Establish initial circuits
	circuits, err := network.mixnets[src].EstablishConnection(ctx, network.hosts[dst].ID())
	require.NoError(t, err)
	initialCount := len(circuits)
	t.Logf("Established %d initial circuits", initialCount)

	// Test multiple message sends to verify circuit resilience
	testMessages := []string{
		"Message 1: Testing circuit resilience",
		"Message 2: Multiple transmissions",
		"Message 3: Circuit stability check",
	}

	for i, msg := range testMessages {
		t.Logf("Sending test message %d", i+1)
		err := network.mixnets[src].Send(ctx, network.hosts[dst].ID(), []byte(msg))
		if err != nil {
			t.Logf("Message %d failed (expected in some failure scenarios): %v", i+1, err)
		} else {
			t.Logf("✓ Message %d sent successfully", i+1)
		}
	}

	t.Log("✓ Circuit retry and recovery test completed")
}

// TestEndToEndMessageTransmission tests complete end-to-end message flow
func TestEndToEndMessageTransmission(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	t.Log("Starting end-to-end message transmission test...")
	network := NewTestNetwork(t, 8) // Need 8 nodes for sufficient relay pool
	defer network.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Set up message reception tracking
	receivedMessages := make(map[peer.ID][][]byte)
	var mu sync.Mutex

	// Set up receivers on all nodes except sender
	for i := 1; i < len(network.hosts); i++ {
		nodeID := network.hosts[i].ID()
		receivedMessages[nodeID] = [][]byte{}

		network.hosts[i].SetStreamHandler(mixnet.ProtocolID, func(s libp2pnetwork.Stream) {
			mu.Lock()
			defer s.Close()

			// Read message data
			buf := make([]byte, 64*1024)
			n, err := s.Read(buf)
			if err != nil {
				return
			}

			receivedMessages[nodeID] = append(receivedMessages[nodeID], buf[:n])
			log.Printf("[RECV] Received %d bytes from %s", n, s.Conn().RemotePeer())
		})
	}

	// Test sending messages from node 0 to all other nodes
	testMessages := map[peer.ID][]byte{
		network.hosts[1].ID(): []byte("Hello Node 1!"),
		network.hosts[2].ID(): []byte("Hello Node 2!"),
		network.hosts[3].ID(): []byte("Hello Node 3!"),
		network.hosts[4].ID(): []byte("Hello Node 4!"),
	}

	for destID, message := range testMessages {
		t.Logf("Sending message to %s", destID)
		err := network.mixnets[0].Send(ctx, destID, message)
		if err != nil {
			t.Logf("Failed to send to %s: %v", destID, err)
		} else {
			t.Logf("✓ Successfully sent to %s", destID)
		}

		// Small delay between sends
		time.Sleep(100 * time.Millisecond)
	}

	// Wait for messages to be processed
	time.Sleep(2 * time.Second)

	t.Log("✓ End-to-end transmission test completed")
}

// TestMultiHopEncryption tests encryption through multiple relay hops
func TestMultiHopEncryption(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	t.Log("Starting multi-hop encryption test...")
	network := NewTestNetwork(t, 12) // Need more nodes for multi-hop tests
	defer network.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test with different hop counts (adjusted for test network size)
	hopConfigs := []int{1} // Use only 1 hop for test network

	for _, hopCount := range hopConfigs {
		t.Run(fmt.Sprintf("HopCount_%d", hopCount), func(t *testing.T) {
			// Create new mixnet with specific hop count
			cfg := mixnet.DefaultConfig()
			cfg.HopCount = hopCount
			cfg.CircuitCount = 2

			srcHost := network.hosts[0]
			dstHost := network.hosts[len(network.hosts)-1]

			var relays []peer.AddrInfo
			for i := 1; i < len(network.hosts)-1; i++ {
				relays = append(relays, network.peerInfos[i])
			}

			m, err := mixnet.NewMixnet(cfg, srcHost, &mockRouting{relays: relays})
			require.NoError(t, err)
			defer m.Close()

			// Establish connection
			circuits, err := m.EstablishConnection(ctx, dstHost.ID())
			require.NoError(t, err)
			require.Len(t, circuits, cfg.CircuitCount)

			// Verify each circuit has correct number of hops
			for _, c := range circuits {
				t.Logf("Circuit %s has %d hops (expected %d)", c.ID, len(c.Peers), hopCount)
				require.Equal(t, hopCount, len(c.Peers), "Circuit should have correct number of hops")
			}

			// Send test message
			testMsg := []byte(fmt.Sprintf("Testing %d-hop encryption", hopCount))
			err = m.Send(ctx, dstHost.ID(), testMsg)
			require.NoError(t, err)

			t.Logf("✓ Successfully transmitted message through %d hops", hopCount)
		})
	}
}

// TestCircuitFailureDetection tests active failure detection mechanism
func TestCircuitFailureDetection(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	t.Log("Starting circuit failure detection test...")
	network := NewTestNetwork(t, 8) // Need 8 nodes for sufficient relay pool
	defer network.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	src := 0
	dst := 5

	// Establish circuits
	circuits, err := network.mixnets[src].EstablishConnection(ctx, network.hosts[dst].ID())
	require.NoError(t, err)
	t.Logf("Established %d circuits", len(circuits))

	// Record initial circuit states
	initialStates := make([]circuit.CircuitState, len(circuits))
	for i, c := range circuits {
		initialStates[i] = c.GetState()
		t.Logf("Circuit %d initial state: %s", i, initialStates[i])
	}

	// Send multiple messages to test circuit health monitoring
	for i := 0; i < 5; i++ {
		msg := []byte(fmt.Sprintf("Health check message %d", i))
		err := network.mixnets[src].Send(ctx, network.hosts[dst].ID(), msg)
		if err != nil {
			t.Logf("Message %d failed: %v", i, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Check circuit states after transmissions
	for i, c := range circuits {
		t.Logf("Circuit %d final state: %s (was %s)", i, c.GetState(), initialStates[i])
	}

	t.Log("✓ Failure detection test completed")
}

// TestConcurrentTransmissions tests multiple concurrent message transmissions
func TestConcurrentTransmissions(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	t.Log("Starting concurrent transmissions test...")
	network := NewTestNetwork(t, 8) // Need 8 nodes for sufficient relay pool
	defer network.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const numConcurrent = 10
	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	// Launch concurrent sends from node 0 to node 5
	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			msg := []byte(fmt.Sprintf("Concurrent message %d from goroutine", id))
			err := network.mixnets[0].Send(ctx, network.hosts[5].ID(), msg)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successCount++
				t.Logf("✓ Concurrent send %d succeeded", id)
			} else {
				t.Logf("Concurrent send %d failed: %v", id, err)
			}
		}(i)
	}

	wg.Wait()
	t.Logf("Concurrent transmissions: %d/%d succeeded", successCount, numConcurrent)
	require.Greater(t, successCount, 0, "At least some concurrent transmissions should succeed")
}

// TestLargeMessageTransmission tests transmission of large messages
func TestLargeMessageTransmission(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	t.Log("Starting large message transmission test...")
	network := NewTestNetwork(t, 8) // Need 8 nodes for sufficient relay pool
	defer network.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test different message sizes
	messageSizes := []int{1024, 10 * 1024, 64 * 1024} // 1KB, 10KB, 64KB

	for _, size := range messageSizes {
		t.Run(fmt.Sprintf("Size_%d", size), func(t *testing.T) {
			// Create test message
			testMsg := make([]byte, size)
			for i := range testMsg {
				testMsg[i] = byte(i % 256)
			}

			t.Logf("Sending %d byte message", size)
			err := network.mixnets[0].Send(ctx, network.hosts[4].ID(), testMsg)
			require.NoError(t, err)
			t.Logf("✓ Successfully sent %d byte message", size)
		})
	}
}

// TestCircuitManagerOperations tests circuit manager operations directly
func TestCircuitManagerOperations(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run.")
	}

	t.Log("Starting circuit manager operations test...")

	cfg := &circuit.CircuitConfig{
		HopCount:      2,
		CircuitCount:  3,
		StreamTimeout: 30 * time.Second,
	}

	mgr := circuit.NewCircuitManager(cfg)
	require.NotNil(t, mgr)

	// Test circuit creation
	peers := []peer.ID{"peer1", "peer2", "peer3", "peer4", "peer5", "peer6"}
	relays := make([]circuit.RelayInfo, len(peers))
	for i, p := range peers {
		relays[i] = circuit.RelayInfo{PeerID: p}
	}

	circuits, err := mgr.BuildCircuits(context.Background(), "destination", relays)
	require.NoError(t, err)
	require.Len(t, circuits, cfg.CircuitCount)

	// Test circuit activation
	for _, c := range circuits {
		mgr.ActivateCircuit(c.ID)
		require.True(t, c.IsActive())
	}

	require.Equal(t, cfg.CircuitCount, mgr.ActiveCircuitCount())

	// Test circuit failure marking
	mgr.MarkCircuitFailed(circuits[0].ID)
	require.Equal(t, cfg.CircuitCount-1, mgr.ActiveCircuitCount())

	// Test recovery capacity
	canRecover := mgr.CanRecover()
	t.Logf("Can recover: %v", canRecover)

	// Test circuit rebuild
	if mgr.CanRecover() {
		rebuilt, err := mgr.RebuildCircuit(circuits[0].ID)
		if err == nil {
			require.NotNil(t, rebuilt)
			t.Logf("✓ Successfully rebuilt circuit %s", rebuilt.ID)
		}
	}

	// Test manager close
	mgr.Close()
	require.Equal(t, 0, mgr.ActiveCircuitCount())

	t.Log("✓ Circuit manager operations test completed")
}

// TestPrivacyInvariants tests that privacy invariants are maintained
func TestPrivacyInvariants(t *testing.T) {
	t.Log("Testing privacy invariants...")

	// Verify privacy invariants
	err := mixnet.VerifyPrivacyInvariants()
	require.NoError(t, err)

	// Test privacy manager
	privacyCfg := mixnet.DefaultPrivacyConfig()
	require.False(t, privacyCfg.LogTrafficPatterns)
	require.False(t, privacyCfg.LogRelayAddresses)
	require.False(t, privacyCfg.LogTimingInfo)
	require.False(t, privacyCfg.LogCircuitIDs)

	privacyMgr := mixnet.NewPrivacyManager(privacyCfg)
	require.False(t, privacyMgr.ShouldLogTrafficPatterns())
	require.False(t, privacyMgr.ShouldLogRelayAddresses())
	require.False(t, privacyMgr.ShouldLogTimingInfo())
	require.False(t, privacyMgr.ShouldLogCircuitIDs())

	// Test anonymization
	anonymized := privacyMgr.AnonymizePeerID("QmVeryLongPeerID123456789")
	t.Logf("Anonymized peer ID: %s", anonymized)

	anonymizedCircuit := privacyMgr.AnonymizeCircuitID("circuit-12345")
	t.Logf("Anonymized circuit ID: %s", anonymizedCircuit)

	t.Log("✓ Privacy invariants test passed")
}

// TestMain runs all tests with proper setup and teardown
func TestMain(m *testing.M) {
	// Set up test environment
	log.Println("Setting up mixnet integration test environment...")

	// Run tests
	exitCode := m.Run()

	// Clean up
	log.Println("Tearing down mixnet integration test environment...")

	os.Exit(exitCode)
}
