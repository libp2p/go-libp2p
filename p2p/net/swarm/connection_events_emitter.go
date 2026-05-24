package swarm

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const connectednessEventQueueStallTimeout = 30 * time.Second

type peerConnectednessEventType int

const (
	removeConnEvent peerConnectednessEventType = iota
	addConnEvent
)

type peerConnectednessEvent struct {
	PeerID peer.ID
	Type   peerConnectednessEventType
}

// connectionEventsEmitter emits PeerConnectednessChanged events and dispatches
// per-conn Connected / Disconnected callbacks supplied by the owner.
//
// Under normal operation, we ensure that for any peer we connected to we always
// send at least 1 NotConnected Event after the peer disconnects. This is
// because peers can observe a connection before they are notified of the
// connection by a peer connectedness changed event.
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
	// peerConnectednessCh is intentionally bounded. A slow connectedness event
	// consumer should apply backpressure instead of allowing unbounded pending
	// peer state to accumulate in the swarm.
	peerConnectednessCh chan peerConnectednessEvent
	// lastConnectednessEvent is the last connectedness event sent for a particular peer.
	lastConnectednessEvent map[peer.ID]network.Connectedness
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
	closeMu        sync.Mutex
	closed         bool
	shuttingDown   atomic.Bool
	shutdownOnce   sync.Once
	shutdownNotify chan struct{}
	wg             sync.WaitGroup
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
		peerConnectednessCh:    make(chan peerConnectednessEvent, 32),
		lastConnectednessEvent: make(map[peer.ID]network.Connectedness),
		connectedness:          connectedness,
		emitter:                emitter,
		ctx:                    loopCtx,
		cancel:                 loopCancel,
		shutdownNotify:         make(chan struct{}),
		connected:              make(map[*Conn]struct{}),
		pendingDisconnect:      make(map[*Conn]struct{}),
		onConnected:            onConnected,
		onDisconnected:         onDisconnected,
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

	c.enqueuePeerConnectednessEvent(peerConnectednessEvent{
		PeerID: conn.RemotePeer(),
		Type:   addConnEvent,
	})

	// Dispatch onConnected before touching notifsLk so that a concurrent
	// RemoveConn cannot observe `connected[conn]` and fire onDisconnected
	// while onConnected is still in-flight.
	c.onConnected(conn)

	var dispatchDisconnect bool
	c.notifsLk.Lock()
	if _, ok := c.pendingDisconnect[conn]; ok {
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

// RemoveConn emits connection close events.
//
// Callers reachable from a PeerConnectednessChanged event subscriber
// or a Notifiee.Disconnected handler MUST invoke this async from a goroutine: a
// synchronous call from a subscriber/handler can deadlock the run loop (the
// loop is waiting for the subscriber, the subscriber is waiting on the channel
// push). swarm_conn.go's doClose dispatches RemoveConn from a goroutine for
// this reason.
func (c *connectionEventsEmitter) RemoveConn(conn *Conn) {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return
	}
	c.wg.Add(1)
	c.closeMu.Unlock()
	defer c.wg.Done()

	c.enqueuePeerConnectednessEvent(peerConnectednessEvent{
		PeerID: conn.RemotePeer(),
		Type:   removeConnEvent,
	})

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
	c.cancel()
	c.wg.Wait() // safe: closed=true blocks any new wg.Add
	// Do not make Swarm.Close wait on a slow eventbus subscriber. The loop
	// goroutine will finish once any in-flight Emit returns.
	go func() {
		c.loopWG.Wait()
		_ = c.emitter.Close()
	}()
}

// StartShutdown switches the enqueue path from runtime backpressure to
// best-effort delivery. During shutdown, freeing connection goroutines is more
// important than preserving every final connectedness event.
func (c *connectionEventsEmitter) StartShutdown() {
	c.shuttingDown.Store(true)
	c.shutdownOnce.Do(func() { close(c.shutdownNotify) })
}

func (c *connectionEventsEmitter) enqueuePeerConnectednessEvent(evt peerConnectednessEvent) {
	if c.shuttingDown.Load() {
		// Closing the swarm has already made the final peer state observable via
		// Network.Connectedness. Avoid blocking shutdown on a full event queue.
		select {
		case c.peerConnectednessCh <- evt:
		default:
			log.Warn("dropping connectedness event during swarm shutdown because the queue is full", "peer", evt.PeerID)
		}
		return
	}

	timer := time.NewTimer(connectednessEventQueueStallTimeout)
	defer timer.Stop()
	select {
	case c.peerConnectednessCh <- evt:
		return
	case <-c.shutdownNotify:
		return
	case <-c.ctx.Done():
		return
	case <-timer.C:
		log.Error("slow connectedness event consumer, connection notification stalled", "peer", evt.PeerID)
	}

	// Preserve the existing backpressure semantics after logging. This keeps the
	// queue bounded and avoids dropping connectedness events during normal use.
	select {
	case c.peerConnectednessCh <- evt:
	case <-c.shutdownNotify:
	case <-c.ctx.Done():
	}
}

func (c *connectionEventsEmitter) runEmitter() {
	defer c.loopWG.Done()
	for {
		select {
		case pce := <-c.peerConnectednessCh:
			c.notifyPeer(pce)
		case <-c.ctx.Done():
			for {
				select {
				case pce := <-c.peerConnectednessCh:
					c.notifyPeer(pce)
				default:
					return
				}
			}
		}
	}
}

// notifyPeer emits a PeerConnectednessChanged event when the peer's
// connectedness has changed since the last emit. For add events, it additionally
// emits NotConnected if the peer is already disconnected by the time we get
// here (i.e. a RemoveConn raced ahead of the matching AddConn): the peer may
// have observed the connection, so subscribers must still learn about it.
func (c *connectionEventsEmitter) notifyPeer(pce peerConnectednessEvent) {
	p := pce.PeerID
	forceNotConnectedEvent := pce.Type == addConnEvent

	oldState := c.lastConnectednessEvent[p]
	newState := c.connectedness(p)
	c.lastConnectednessEvent[p] = newState
	if c.lastConnectednessEvent[p] == network.NotConnected {
		delete(c.lastConnectednessEvent, p)
	}
	if newState != oldState ||
		(forceNotConnectedEvent && newState == network.NotConnected) {
		c.emitter.Emit(event.EvtPeerConnectednessChanged{
			Peer:          p,
			Connectedness: newState,
		})
	}
}
