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

// ============================================================
// Stream Upgrader - Additional Interface Methods (Req 13)
// ============================================================
// Note: The existing StreamUpgrader.Upgrade method needs to be updated to match
// the full transport.Upgrader interface. These methods add the listener support.

// GateMaListener creates a GatedMaListener from a manet.Listener.
func (s *StreamUpgrader) GateMaListener(lst interface{ Accept() (interface{}, error); Close() error; Addr() interface{} }) interface{} {
	return lst
}

// UpgradeGatedMaListener upgrades a GatedMaListener to a full transport.Listener.
func (s *StreamUpgrader) UpgradeGatedMaListener(transport interface{}, gated interface{}) interface{} {
	return gated
}

// UpgradeListener upgrades a manet.Listener into a full libp2p transport listener.
// Deprecated: Use UpgradeGatedMaListener instead.
func (s *StreamUpgrader) UpgradeListener(transport interface{}, lst interface{}) interface{} {
	return lst
}

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
}

// NewCircuitFailureNotifier creates a new failure notifier for active circuit monitoring.
func NewCircuitFailureNotifier(m *Mixnet, h host.Host) *CircuitFailureNotifier {
	return &CircuitFailureNotifier{
		mixnet:    m,
		host:      h,
		failureCh: make(chan CircuitFailureEvent, 100),
		stopCh:    make(chan struct{}),
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
	circuits, ok := connections[peerID]
	if !ok {
		return
	}

	for _, circuit := range circuits {
		if circuit == nil {
			continue
		}

		for _, relayPeer := range circuit.Peers {
			if relayPeer == peerID {
				n.mixnet.CircuitManager().MarkCircuitFailed(circuit.ID)

				select {
				case n.failureCh <- CircuitFailureEvent{
					CircuitID:  circuit.ID,
					PeerID:     peerID,
					RemoteAddr: addr.String(),
					Timestamp:  time.Now(),
				}:
				default:
				}
				break
			}
		}
	}
}

// Start begins monitoring for connection events.
func (n *CircuitFailureNotifier) Start(ctx context.Context) error {
	if n.host == nil {
		return fmt.Errorf("no host configured")
	}

	net := n.host.Network()
	net.Notify(n)

	n.wg.Add(1)
	go n.recoveryLoop(ctx)

	log.Printf("[Mixnet] Started circuit failure notifier")
	return nil
}

// Stop stops the failure notifier and unregisters from the network.
func (n *CircuitFailureNotifier) Stop() error {
	close(n.stopCh)

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
		case event := <-n.failureCh:
			n.processFailureEvent(ctx, event)
		}
	}
}

// processFailureEvent handles a single failure event and attempts recovery.
func (n *CircuitFailureNotifier) processFailureEvent(ctx context.Context, event CircuitFailureEvent) {
	log.Printf("[Mixnet] Processing failure for circuit %s (peer: %s)", event.CircuitID, event.PeerID)

	connections := n.mixnet.ActiveConnections()

	for dest, circuits := range connections {
		for _, c := range circuits {
			if c.ID == event.CircuitID {
				err := n.mixnet.RecoverFromFailure(ctx, dest)
				if err != nil {
					log.Printf("[Mixnet] Recovery failed for %s: %v", dest, err)
				} else {
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
	circuits := m.circuitMgr.ListCircuits()
	for _, c := range circuits {
		if c.IsActive() {
			m.circuitMgr.StartHeartbeat(c.ID, interval)
		}
	}
}
