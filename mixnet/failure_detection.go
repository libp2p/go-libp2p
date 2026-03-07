// Package mixnet provides additional components for stream upgrading and failure detection.
package mixnet

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

const (
	defaultHeartbeatInterval   = 1 * time.Second
	failureDetectionDeadline   = 5 * time.Second
	failureDetectionPollPeriod = 1 * time.Second
)

// ============================================================
// Active Failure Detection - network.Notifiee Implementation (Req 10)
// ============================================================

// CircuitFailureEvent represents a circuit failure event for monitoring and recovery.
type CircuitFailureEvent struct {
	CircuitID  string
	PeerID     peer.ID
	RemoteAddr string
	Timestamp  time.Time
}

// CircuitFailureNotifier implements network.Notifiee for active failure detection.
type CircuitFailureNotifier struct {
	mixnet    *Mixnet
	host      host.Host
	failureCh chan CircuitFailureEvent
	stopCh    chan struct{}
	wg        sync.WaitGroup
	mu        sync.Mutex
	seen      map[string]struct{}
}

// NewCircuitFailureNotifier creates a new failure notifier for active circuit monitoring.
func NewCircuitFailureNotifier(m *Mixnet, h host.Host) *CircuitFailureNotifier {
	return &CircuitFailureNotifier{
		mixnet:    m,
		host:      h,
		failureCh: make(chan CircuitFailureEvent, 100),
		stopCh:    make(chan struct{}),
		seen:      make(map[string]struct{}),
	}
}

// Connected is called when a new connection is established.
func (n *CircuitFailureNotifier) Connected(net network.Network, conn network.Conn) {
	log.Printf("[Mixnet] Connected to peer: %s", conn.RemotePeer())
}

// Disconnected is called when a connection is closed.
func (n *CircuitFailureNotifier) Disconnected(net network.Network, conn network.Conn) {
	remotePeer := conn.RemotePeer()
	remoteAddr := conn.RemoteMultiaddr()

	log.Printf("[Mixnet] Disconnected from peer: %s at %s", remotePeer, remoteAddr)

	n.handleDisconnection(remotePeer, remoteAddr)
}

// Listen is called when the network starts listening on an address.
func (n *CircuitFailureNotifier) Listen(net network.Network, addr ma.Multiaddr) {
	log.Printf("[Mixnet] Listening on: %s", addr)
}

// ListenClose is called when the network stops listening on an address.
func (n *CircuitFailureNotifier) ListenClose(net network.Network, addr ma.Multiaddr) {
	log.Printf("[Mixnet] Stopped listening on: %s", addr)
}

// handleDisconnection processes a disconnection event and triggers recovery if needed.
func (n *CircuitFailureNotifier) handleDisconnection(peerID peer.ID, addr ma.Multiaddr) {
	connections := n.mixnet.ActiveConnections()
	for _, circuits := range connections {
		for _, circuit := range circuits {
			if circuit == nil {
				continue
			}
			for _, relayPeer := range circuit.Peers {
				if relayPeer == peerID {
					n.enqueueFailure(circuit.ID, peerID, addr.String())
					break
				}
			}
		}
	}
}

func (n *CircuitFailureNotifier) enqueueFailure(circuitID string, p peer.ID, addr string) {
	n.mu.Lock()
	if _, exists := n.seen[circuitID]; exists {
		n.mu.Unlock()
		return
	}
	n.seen[circuitID] = struct{}{}
	n.mu.Unlock()

	n.mixnet.CircuitManager().MarkCircuitFailed(circuitID)
	select {
	case n.failureCh <- CircuitFailureEvent{
		CircuitID:  circuitID,
		PeerID:     p,
		RemoteAddr: addr,
		Timestamp:  time.Now(),
	}:
	default:
	}
}

// Start begins monitoring for connection events.
func (n *CircuitFailureNotifier) Start(ctx context.Context) error {
	if n.host == nil {
		return fmt.Errorf("no host configured")
	}

	net := n.host.Network()
	net.Notify(n)

	n.wg.Add(2)
	go n.recoveryLoop(ctx)
	go n.monitorLoop(ctx)

	log.Printf("[Mixnet] Started circuit failure notifier")
	return nil
}

// Stop stops the failure notifier and unregisters from the network.
func (n *CircuitFailureNotifier) Stop() error {
	select {
	case <-n.stopCh:
		// Already stopped
		return nil
	default:
		close(n.stopCh)
	}

	if n.host != nil {
		n.host.Network().StopNotify(n)
	}

	n.wg.Wait()
	close(n.failureCh)

	log.Printf("[Mixnet] Stopped circuit failure notifier")
	return nil
}

// recoveryLoop handles circuit failures and triggers recovery.
func (n *CircuitFailureNotifier) recoveryLoop(ctx context.Context) {
	defer n.wg.Done()

	for {
		select {
		case <-n.stopCh:
			return
		case <-ctx.Done():
			return
		case event, ok := <-n.failureCh:
			if !ok {
				return
			}
			n.processFailureEvent(ctx, event)
		}
	}
}

func (n *CircuitFailureNotifier) monitorLoop(ctx context.Context) {
	defer n.wg.Done()
	ticker := time.NewTicker(failureDetectionPollPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-n.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.scanCircuits()
		}
	}
}

func (n *CircuitFailureNotifier) scanCircuits() {
	connections := n.mixnet.ActiveConnections()
	for _, circuits := range connections {
		for _, c := range circuits {
			if c == nil || !c.IsActive() {
				continue
			}

			if n.mixnet.CircuitManager().DetectFailure(c.ID) {
				n.enqueueFailure(c.ID, "", "")
				continue
			}

			// NOTE: Do not treat missing stream handler as a failure here.
			// Mixnet circuits may use short-lived streams for sharded sends, so a nil
			// stream is not necessarily a circuit failure.
			_, _ = n.mixnet.CircuitManager().GetStream(c.ID)

			// Connectedness checks can be noisy for short-lived circuits; rely on heartbeat instead.
			entry := c.Peers[0]

			if !c.LastHeartbeat.IsZero() && time.Since(c.LastHeartbeat) > failureDetectionDeadline {
				n.enqueueFailure(c.ID, entry, "")
			}
		}
	}
}

// processFailureEvent handles a single failure event and attempts recovery.
func (n *CircuitFailureNotifier) processFailureEvent(ctx context.Context, event CircuitFailureEvent) {
	log.Printf("[Mixnet] Processing failure for circuit %s (peer: %s)", event.CircuitID, event.PeerID)

	connections := n.mixnet.ActiveConnections()

	for dest, circuits := range connections {
		for _, c := range circuits {
			if c == nil {
				continue
			}
			if c.ID == event.CircuitID {
				err := n.mixnet.RecoverFromFailure(ctx, dest)
				if err != nil {
					log.Printf("[Mixnet] Recovery failed for %s: %v", dest, err)
				} else {
					n.mu.Lock()
					delete(n.seen, event.CircuitID)
					n.mu.Unlock()
					log.Printf("[Mixnet] Successfully recovered circuit to %s", dest)
				}
				return
			}
		}
	}
}

// FailureChan returns a channel that receives circuit failure events.
func (n *CircuitFailureNotifier) FailureChan() <-chan CircuitFailureEvent {
	return n.failureCh
}

// StartHeartbeatMonitoring starts active heartbeat monitoring for all circuits.
func (m *Mixnet) StartHeartbeatMonitoring(interval time.Duration) {
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	circuits := m.circuitMgr.ListCircuits()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range circuits {
		if c == nil || !c.IsActive() {
			continue
		}
		if _, ok := m.heartbeatStart[c.ID]; ok {
			continue
		}
		m.circuitMgr.StartHeartbeat(c.ID, interval)
		m.heartbeatStart[c.ID] = struct{}{}
	}
}
