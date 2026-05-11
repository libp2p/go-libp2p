package swarm

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// connectionEventsEmitter emits PeerConnectednessChanged events and dispatches
// per-conn Connected / Disconnected callbacks supplied by the owner.
//
// We ensure that for any peer we connected to we always sent atleast 1 NotConnected Event after
// the peer disconnects. This is because peers can observe a connection before they are notified
// of the connection by a peer connectedness changed event.
//
// For the per-conn callbacks, we guarantee that for a given *Conn,
// onDisconnected never fires before the corresponding onConnected has finished.
// RemoveConn that arrives before AddConn for the same conn is "parked" until
// AddConn dispatches onConnected, and then onDisconnected is dispatched
// immediately after.
//
// Callers that want a non-blocking close (e.g. doClose) invoke RemoveConn
// from a goroutine; AddConn always runs onConnected synchronously so callers
// see Connected fire before they continue.
type connectionEventsEmitter struct {
	// newConns is the channel that holds the peerIDs we recently connected to
	newConns      chan peer.ID
	removeConnsMx sync.Mutex
	// removeConns is a slice of peerIDs we have recently closed connections to
	removeConns []peer.ID
	// removeConnNotif is used to notify the event loop on an addition to `removeConns`.
	removeConnNotif chan struct{}
	// lastEvent is the last connectedness event sent for a particular peer.
	lastEvent map[peer.ID]network.Connectedness
	// connectedness is the function that gives the peers current connectedness state
	connectedness func(peer.ID) network.Connectedness
	// onConnected fires synchronously when a conn becomes connected.
	onConnected func(*Conn)
	// onDisconnected fires when a conn is removed; for the parked-RemoveConn
	// case it runs from inside AddConn after onConnected has completed.
	onDisconnected func(*Conn)
	// emitter is the PeerConnectednessChanged event emitter
	emitter event.Emitter
	// closeMu serializes the (closed-check + wg.Add) pair against Close. It is
	// only held across that pair, never during the actual dispatch work, so
	// Add/Remove operations across different conns still run concurrently.
	closeMu sync.Mutex
	closed  bool
	wg      sync.WaitGroup
	// loopCtx / loopWG track the runEmitter goroutine separately from the
	// in-flight Add/Remove operations.
	loopWG sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc

	// notifsLk guards the connected and pendingDisconnect maps.
	// onConnected / onDisconnected are invoked OUTSIDE this lock so concurrent
	// conns don't serialize.
	notifsLk          sync.Mutex
	connected         map[*Conn]struct{}
	pendingDisconnect map[*Conn]struct{}
}

func newConnectionEventsEmitter(
	connectedness func(peer.ID) network.Connectedness,
	emitter event.Emitter,
	onConnected func(*Conn),
	onDisconnected func(*Conn),
) *connectionEventsEmitter {
	loopCtx, loopCancel := context.WithCancel(context.Background())
	c := &connectionEventsEmitter{
		newConns:          make(chan peer.ID, 32),
		lastEvent:         make(map[peer.ID]network.Connectedness),
		removeConnNotif:   make(chan struct{}, 1),
		connectedness:     connectedness,
		emitter:           emitter,
		ctx:               loopCtx,
		cancel:            loopCancel,
		connected:         make(map[*Conn]struct{}),
		pendingDisconnect: make(map[*Conn]struct{}),
		onConnected:       onConnected,
		onDisconnected:    onDisconnected,
	}
	c.loopWG.Add(1)
	go c.runEmitter()
	return c
}

func (c *connectionEventsEmitter) AddConn(conn *Conn) {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return
	}
	c.wg.Add(1)
	c.closeMu.Unlock()
	defer c.wg.Done()

	c.newConns <- conn.RemotePeer()

	// Dispatch onConnected before touching notifsLk so that a concurrent
	// RemoveConn cannot observe `connected[conn]` and fire onDisconnected
	// while onConnected is still in-flight.
	c.onConnected(conn)

	var dispatchDisconnect bool
	c.notifsLk.Lock()
	if _, parked := c.pendingDisconnect[conn]; parked {
		delete(c.pendingDisconnect, conn)
		dispatchDisconnect = true
	} else {
		c.connected[conn] = struct{}{}
	}
	c.notifsLk.Unlock()

	if dispatchDisconnect {
		c.onDisconnected(conn)
	}
}

func (c *connectionEventsEmitter) RemoveConn(conn *Conn) {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return
	}
	c.wg.Add(1)
	c.closeMu.Unlock()
	defer c.wg.Done()

	c.removeConnsMx.Lock()
	// This queue is roughly bounded by the total number of added connections we
	// have. If consumers of connectedness events are slow, we apply
	// backpressure to AddConn operations.
	//
	// We purposefully don't block/backpressure here to avoid deadlocks. If this were
	// a channel and an event consumer closed the connection this would deadlock on a
	// push to a full channel.
	c.removeConns = append(c.removeConns, conn.RemotePeer())
	c.removeConnsMx.Unlock()

	select {
	case c.removeConnNotif <- struct{}{}:
	default:
	}

	var dispatchDisconnect bool
	c.notifsLk.Lock()
	if _, ok := c.connected[conn]; ok {
		delete(c.connected, conn)
		dispatchDisconnect = true
	} else {
		// RemoveConn arrived before AddConn finished dispatching onConnected.
		// Park; AddConn will dispatch onDisconnected once onConnected is done.
		c.pendingDisconnect[conn] = struct{}{}
	}
	c.notifsLk.Unlock()

	if dispatchDisconnect {
		c.onDisconnected(conn)
	}
}

func (c *connectionEventsEmitter) Close() {
	c.closeMu.Lock()
	c.closed = true
	c.closeMu.Unlock()
	c.wg.Wait() // safe: closed=true blocks any new wg.Add
	c.cancel()
	c.loopWG.Wait()
}

func (c *connectionEventsEmitter) runEmitter() {
	defer c.loopWG.Done()
	for {
		select {
		case p := <-c.newConns:
			c.notifyPeer(p, true)
		case <-c.removeConnNotif:
			c.sendConnRemovedNotifications()
		case <-c.ctx.Done():
			for {
				select {
				case p := <-c.newConns:
					c.notifyPeer(p, true)
				case <-c.removeConnNotif:
					c.sendConnRemovedNotifications()
				default:
					return
				}
			}
		}
	}
}

// notifyPeer sends the peer connectedness event using the emitter.
// Use forceNotConnectedEvent = true to send a NotConnected event even if
// no Connected event was sent for this peer.
// In case a peer is disconnected before we sent the Connected event, we still
// send the Disconnected event because a connection to the peer can be observed
// in such cases.
func (c *connectionEventsEmitter) notifyPeer(p peer.ID, forceNotConnectedEvent bool) {
	oldState := c.lastEvent[p]
	c.lastEvent[p] = c.connectedness(p)
	if c.lastEvent[p] == network.NotConnected {
		delete(c.lastEvent, p)
	}
	if (forceNotConnectedEvent && c.lastEvent[p] == network.NotConnected) || c.lastEvent[p] != oldState {
		c.emitter.Emit(event.EvtPeerConnectednessChanged{
			Peer:          p,
			Connectedness: c.lastEvent[p],
		})
	}
}

func (c *connectionEventsEmitter) sendConnRemovedNotifications() {
	c.removeConnsMx.Lock()
	removeConns := c.removeConns
	c.removeConns = nil
	c.removeConnsMx.Unlock()
	for _, p := range removeConns {
		c.notifyPeer(p, false)
	}
}
