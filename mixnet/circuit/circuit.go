package circuit

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type CircuitState int

const (
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

type Circuit struct {
	ID             string
	Peers          []peer.ID
	State          CircuitState
	CreatedAt      time.Time
	UpdatedAt      time.Time
	FailureCount   int
	LastHeartbeat time.Time
	mu             sync.RWMutex
}

func NewCircuit(id string, peers []peer.ID) *Circuit {
	return &Circuit{
		ID:        id,
		Peers:     peers,
		State:     StatePending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func (c *Circuit) GetState() CircuitState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.State
}

func (c *Circuit) SetState(state CircuitState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.State = state
	c.UpdatedAt = time.Now()
}

func (c *Circuit) MarkFailed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.State = StateFailed
	c.FailureCount++
	c.UpdatedAt = time.Now()
}

func (c *Circuit) IsActive() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.State == StateActive
}

func (c *Circuit) Entry() peer.ID {
	if len(c.Peers) == 0 {
		return ""
	}
	return c.Peers[0]
}

func (c *Circuit) Exit() peer.ID {
	if len(c.Peers) == 0 {
		return ""
	}
	return c.Peers[len(c.Peers)-1]
}

// FailureEvent represents a circuit failure event for active failure detection.
type FailureEvent struct {
	CircuitID  string
	PeerID     peer.ID
	RemoteAddr string
	Timestamp  time.Time
}
