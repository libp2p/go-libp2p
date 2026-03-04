package circuit

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// CircuitState represents the current lifecycle stage of a mixnet circuit.
type CircuitState int

const (
	// StatePending indicates the circuit has been defined but building hasn't started.
	StatePending CircuitState = iota
	// StateBuilding indicates the multi-hop handshake is currently in progress.
	StateBuilding
	// StateActive indicates the circuit is fully established and ready for data.
	StateActive
	// StateFailed indicates the circuit establishment or maintenance failed.
	StateFailed
	// StateClosed indicates the circuit has been intentionally closed.
	StateClosed
)

func (s CircuitState) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateBuilding:
		return "building"
	case StateActive:
		return "active"
	case StateFailed:
		return "failed"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// Circuit represents a single multi-hop path through the mixnet.
type Circuit struct {
	// ID is a unique identifier for the circuit.
	ID          string
	// Peers is an ordered list of relay nodes, from entry to exit.
	Peers       []peer.ID
	// State is the current status of the circuit.
	State       CircuitState
	// CreatedAt is the time when the circuit was first created.
	CreatedAt   time.Time
	// UpdatedAt is the time of the last state change.
	UpdatedAt   time.Time
	// FailureCount is the number of failed operations on this circuit.
	FailureCount int
	// LastHeartbeat is the last time a heartbeat was received for this circuit.
	LastHeartbeat time.Time
	mu          sync.RWMutex
}

// NewCircuit creates a new Circuit instance with the given ID and set of relay peers.
func NewCircuit(id string, peers []peer.ID) *Circuit {
	return &Circuit{
		ID:        id,
		Peers:     peers,
		State:     StatePending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// SetState updates the circuit's state and record the time of the change.
func (c *Circuit) SetState(state CircuitState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.State = state
	c.UpdatedAt = time.Now()
}

// GetState returns the current state of the circuit.
func (c *Circuit) GetState() CircuitState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.State
}

// MarkFailed marks the circuit as failed and increments the failure counter.
func (c *Circuit) MarkFailed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.State = StateFailed
	c.FailureCount++
	c.UpdatedAt = time.Now()
}

// IsActive returns true if the circuit is in the StateActive state.
func (c *Circuit) IsActive() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.State == StateActive
}

// Entry returns the peer.ID of the first relay in the circuit.
func (c *Circuit) Entry() peer.ID {
	if len(c.Peers) == 0 {
		return ""
	}
	return c.Peers[0]
}

// Exit returns the peer.ID of the last relay in the circuit.
func (c *Circuit) Exit() peer.ID {
	if len(c.Peers) == 0 {
		return ""
	}
	return c.Peers[len(c.Peers)-1]
}
