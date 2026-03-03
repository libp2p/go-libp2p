package circuit

import (
	"testing"
)

func TestCircuit_NewCircuit(t *testing.T) {
	peers := []peer.ID{"peer1", "peer2", "peer3"}
	circuit := NewCircuit("test-circuit", peers)

	if circuit.ID != "test-circuit" {
		t.Errorf("expected ID test-circuit, got %s", circuit.ID)
	}
	if len(circuit.Peers) != 3 {
		t.Errorf("expected 3 peers, got %d", len(circuit.Peers))
	}
	if circuit.GetState() != StatePending {
		t.Errorf("expected state pending, got %s", circuit.GetState())
	}
}

func TestCircuit_SetState(t *testing.T) {
	circuit := NewCircuit("test", []peer.ID{"a"})

	circuit.SetState(StateBuilding)
	if circuit.GetState() != StateBuilding {
		t.Errorf("expected state building, got %s", circuit.GetState())
	}

	circuit.SetState(StateActive)
	if circuit.GetState() != StateActive {
		t.Errorf("expected state active, got %s", circuit.GetState())
	}
}

func TestCircuit_MarkFailed(t *testing.T) {
	circuit := NewCircuit("test", []peer.ID{"a"})

	circuit.MarkFailed()
	if circuit.GetState() != StateFailed {
		t.Errorf("expected state failed, got %s", circuit.GetState())
	}
	if circuit.FailureCount != 1 {
		t.Errorf("expected failure count 1, got %d", circuit.FailureCount)
	}
}

func TestCircuit_EntryExit(t *testing.T) {
	peers := []peer.ID{"entry", "middle", "exit"}
	circuit := NewCircuit("test", peers)

	if circuit.Entry() != "entry" {
		t.Errorf("expected entry peer entry, got %s", circuit.Entry())
	}
	if circuit.Exit() != "exit" {
		t.Errorf("expected exit peer exit, got %s", circuit.Exit())
	}
}

func TestCircuit_IsActive(t *testing.T) {
	circuit := NewCircuit("test", []peer.ID{"a"})

	if circuit.IsActive() {
		t.Error("expected inactive initially")
	}

	circuit.SetState(StateActive)
	if !circuit.IsActive() {
		t.Error("expected active after setting state")
	}
}

func TestCircuitManager_NewCircuitManager(t *testing.T) {
	cfg := &CircuitConfig{
		HopCount:     2,
		CircuitCount: 3,
	}
	mgr := NewCircuitManager(cfg)

	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	if mgr.Config().HopCount != 2 {
		t.Errorf("expected hop count 2, got %d", mgr.Config().HopCount)
	}
}

func TestCircuitManager_BuildCircuits(t *testing.T) {
	cfg := &CircuitConfig{
		HopCount:     2,
		CircuitCount: 3,
	}
	mgr := NewCircuitManager(cfg)

	// Create mock relay pool (6 relays for 2 hops * 3 circuits)
	relays := []RelayInfo{
		{PeerID: "r1"}, {PeerID: "r2"}, {PeerID: "r3"},
		{PeerID: "r4"}, {PeerID: "r5"}, {PeerID: "r6"},
	}

	circuits, err := mgr.BuildCircuits(nil, "dest", relays)
	if err != nil {
		t.Fatalf("BuildCircuits() error = %v", err)
	}

	if len(circuits) != 3 {
		t.Errorf("expected 3 circuits, got %d", len(circuits))
	}

	// Check each circuit has correct number of peers
	for i, c := range circuits {
		if len(c.Peers) != 2 {
			t.Errorf("circuit %d: expected 2 peers, got %d", i, len(c.Peers))
		}
	}
}

func TestCircuitManager_BuildCircuits_InsufficientRelays(t *testing.T) {
	cfg := &CircuitConfig{
		HopCount:     2,
		CircuitCount: 3,
	}
	mgr := NewCircuitManager(cfg)

	// Only 4 relays, need 6
	relays := []RelayInfo{
		{PeerID: "r1"}, {PeerID: "r2"}, {PeerID: "r3"}, {PeerID: "r4"},
	}

	_, err := mgr.BuildCircuits(nil, "dest", relays)
	if err == nil {
		t.Error("expected error with insufficient relays")
	}
}

func TestCircuitManager_FilterDestination(t *testing.T) {
	cfg := &CircuitConfig{
		HopCount:     2,
		CircuitCount: 2,
	}
	mgr := NewCircuitManager(cfg)

	relays := []RelayInfo{
		{PeerID: "r1"}, {PeerID: "r2"}, {PeerID: "r3"}, {PeerID: "r4"},
	}

	// Build with r3 as destination (should be filtered out)
	circuits, err := mgr.BuildCircuits(nil, "r3", relays)
	if err != nil {
		t.Fatalf("BuildCircuits() error = %v", err)
	}

	// Check destination is not in any circuit
	for _, c := range circuits {
		for _, p := range c.Peers {
			if p == "r3" {
				t.Error("destination should not be in circuit")
			}
		}
	}
}

func TestCircuitManager_ActiveCircuitCount(t *testing.T) {
	cfg := &CircuitConfig{
		HopCount:     1,
		CircuitCount: 3,
	}
	mgr := NewCircuitManager(cfg)

	relays := []RelayInfo{
		{PeerID: "r1"}, {PeerID: "r2"}, {PeerID: "r3"},
	}

	circuits, _ := mgr.BuildCircuits(nil, "dest", relays)

	if mgr.ActiveCircuitCount() != 0 {
		t.Error("expected 0 active circuits initially")
	}

	// Activate all circuits
	for _, c := range circuits {
		mgr.ActivateCircuit(c.ID)
	}

	if mgr.ActiveCircuitCount() != 3 {
		t.Errorf("expected 3 active circuits, got %d", mgr.ActiveCircuitCount())
	}
}

func TestCircuitManager_CanRecover(t *testing.T) {
	cfg := &CircuitConfig{
		HopCount:     1,
		CircuitCount: 3, // threshold = 2
	}
	mgr := NewCircuitManager(cfg)

	relays := []RelayInfo{
		{PeerID: "r1"}, {PeerID: "r2"}, {PeerID: "r3"},
	}

	circuits, _ := mgr.BuildCircuits(nil, "dest", relays)

	// Activate all
	for _, c := range circuits {
		mgr.ActivateCircuit(c.ID)
	}

	if !mgr.CanRecover() {
		t.Error("should be able to recover with all circuits active")
	}

	// Fail one circuit (threshold is 2, we have 3)
	mgr.MarkCircuitFailed(circuits[0].ID)

	if !mgr.CanRecover() {
		t.Error("should still be able to recover (2 > threshold)")
	}

	// Fail another (now 2 active, threshold is 2)
	mgr.MarkCircuitFailed(circuits[1].ID)

	if mgr.CanRecover() {
		t.Error("should NOT be able to recover (2 == threshold)")
	}
}

func TestCircuitManager_RecoveryCapacity(t *testing.T) {
	cfg := &CircuitConfig{
		HopCount:     1,
		CircuitCount: 5, // threshold = 4
	}
	mgr := NewCircuitManager(cfg)

	relays := []RelayInfo{
		{PeerID: "r1"}, {PeerID: "r2"}, {PeerID: "r3"}, {PeerID: "r4"}, {PeerID: "r5"},
	}

	circuits, _ := mgr.BuildCircuits(nil, "dest", relays)
	for _, c := range circuits {
		mgr.ActivateCircuit(c.ID)
	}

	capacity := mgr.RecoveryCapacity()
	if capacity != 1 {
		t.Errorf("expected recovery capacity 1, got %d", capacity)
	}

	// Fail one
	mgr.MarkCircuitFailed(circuits[0].ID)
	capacity = mgr.RecoveryCapacity()
	if capacity != 0 {
		t.Errorf("expected recovery capacity 0, got %d", capacity)
	}
}

func TestCircuitManager_RebuildCircuit(t *testing.T) {
	cfg := &CircuitConfig{
		HopCount:     1,
		CircuitCount: 3, // need 3 relays
	}
	mgr := NewCircuitManager(cfg)

	relays := []RelayInfo{
		{PeerID: "r1"}, {PeerID: "r2"}, {PeerID: "r3"},
	}

	circuits, _ := mgr.BuildCircuits(nil, "dest", relays)
	for _, c := range circuits {
		mgr.ActivateCircuit(c.ID)
	}

	// Fail first circuit
	mgr.MarkCircuitFailed(circuits[0].ID)

	// Rebuild should work (we have 2 more relays)
	rebuilt, err := mgr.RebuildCircuit(circuits[0].ID)
	if err != nil {
		t.Fatalf("RebuildCircuit() error = %v", err)
	}
	if rebuilt == nil {
		t.Error("expected rebuilt circuit")
	}
}

func TestCircuitManager_Close(t *testing.T) {
	cfg := &CircuitConfig{
		HopCount:     1,
		CircuitCount: 2,
	}
	mgr := NewCircuitManager(cfg)

	relays := []RelayInfo{
		{PeerID: "r1"}, {PeerID: "r2"},
	}

	circuits, _ := mgr.BuildCircuits(nil, "dest", relays)
	for _, c := range circuits {
		mgr.ActivateCircuit(c.ID)
	}

	mgr.Close()

	if mgr.ActiveCircuitCount() != 0 {
		t.Error("expected 0 active after close")
	}
}

func TestCircuitState_String(t *testing.T) {
	tests := []struct {
		state    CircuitState
		expected string
	}{
		{StatePending, "pending"},
		{StateBuilding, "building"},
		{StateActive, "active"},
		{StateFailed, "failed"},
		{StateClosed, "closed"},
		{CircuitState(99), "unknown"},
	}

	for _, tt := range tests {
		if tt.state.String() != tt.expected {
			t.Errorf("expected %s, got %s", tt.expected, tt.state.String())
		}
	}
}
