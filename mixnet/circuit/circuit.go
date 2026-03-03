package circuit

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// CircuitState represents the state of a circuit
type CircuitState int

const (
	// Circuit states
	StatePending CircuitState = iota
	StateBuilding
	StateActive
	StateFailed
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

// Circuit represents a single mixnet circuit
type Circuit struct {
	ID          string
	Peers       []peer.ID // Ordered from entry to exit
	State       CircuitState
	CreatedAt   time.Time
	UpdatedAt   time.Time
	FailureCount int
	mu          sync.RWMutex
}

// NewCircuit creates a new circuit
func NewCircuit(id string, peers []peer.ID) *Circuit {
	return &Circuit{
		ID:        id,
		Peers:     peers,
		State:     StatePending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// SetState sets the circuit state
func (c *Circuit) SetState(state CircuitState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.State = state
	c.UpdatedAt = time.Now()
}

// GetState returns the current circuit state
func (c *Circuit) GetState() CircuitState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.State
}

// MarkFailed marks the circuit as failed
func (c *Circuit) MarkFailed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.State = StateFailed
	c.FailureCount++
	c.UpdatedAt = time.Now()
}

// IsActive checks if the circuit is active
func (c *Circuit) IsActive() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.State == StateActive
}

// Entry returns the entry relay peer ID
func (c *Circuit) Entry() peer.ID {
	if len(c.Peers) == 0 {
		return ""
	}
	return c.Peers[0]
}

// Exit returns the exit relay peer ID
func (c *Circuit) Exit() peer.ID {
	if len(c.Peers) == 0 {
		return ""
	}
	return c.Peers[len(c.Peers)-1]
}
