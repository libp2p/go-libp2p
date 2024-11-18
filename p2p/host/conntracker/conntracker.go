// Package conntracker holds the ConnTracker service. Which tracks a peer's
// connections and supported protocols.
package conntracker

import (
	"context"
	"errors"
	"time"

	"slices"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	multiaddr "github.com/multiformats/go-multiaddr"

	logging "github.com/ipfs/go-log/v2"
)

const pendingReqBound = 1_000_000
const gcInterval = time.Minute

// If we reach this many pending cleanup requests, we'll cleanup immediately.
const maxPendingCleanup = 1_000

// If we don't have maxPendingCleanup requests to cleanup, then we'll cleanup
// up at most this frequency if requested.
const maxCleanupFrequency = 10 * time.Second

var log = logging.Logger("bestconn")

// ConnTracker lets other services find the best connection to a peer. It relies
// on Identify to identify the protocols a peer supports on a given connection.
// If identify fails, it will still track the connection, but not record any
// protocols for that connection. If a protocol is found via Multistream Select
// (or any other protocol negotiation mechanism), the conntracker will update
// the supported protocols when the EvtProtocolNegotiationSuccess event is
// received.
type ConnTracker struct {
	svcCtx           context.Context
	stop             context.CancelFunc
	stopped          chan struct{}
	clock            clock
	sub              event.Subscription
	trackedConns     map[peer.ID]map[network.Conn]connMeta
	pendingReqs      map[peer.ID][]req
	totalPendingReqs int

	notifier   connNotifier
	connNotifs chan connNotif
	reqCh      chan req
	cleanupCh  chan peer.ID
}

type connNotifKind int

const (
	connNotifConnected connNotifKind = iota
	connNotifDisconnected
)

type connNotif struct {
	kind connNotifKind
	conn network.Conn
}

type clock interface {
	Now() time.Time
	Since(time.Time) time.Duration
	NewTicker(d time.Duration) *time.Ticker
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now()
}
func (realClock) Since(t time.Time) time.Duration {
	return time.Since(t)
}
func (realClock) NewTicker(d time.Duration) *time.Ticker {
	return time.NewTicker(d)
}

type connMeta struct {
	// Identify ran on this connection
	identified bool
	protos     map[protocol.ID]struct{}
}

type req struct {
	ctx context.Context

	// These are request parameters
	immediate       bool
	waitForIdentify bool
	p               peer.ID
	oneOf           []protocol.ID
	filter          func(c network.Conn) bool
	sort            func(a, b network.Conn) int

	// These are internal plumbing
	resCh       chan ConnWithMeta
	onFulfilled func()
}

type connNotifier interface {
	Notify(network.Notifiee)
	StopNotify(network.Notifiee)
}

func (ct *ConnTracker) Start(eb event.Bus, notifier connNotifier) error {
	sub, err := eb.Subscribe([]any{
		new(event.EvtPeerIdentificationCompleted),
		new(event.EvtPeerIdentificationFailed),
		new(event.EvtPeerConnectednessChanged),
		new(event.EvtProtocolNegotiationSuccess),
	})
	if err != nil {
		return err
	}

	ct.svcCtx, ct.stop = context.WithCancel(context.Background())
	ct.stopped = make(chan struct{})
	ct.sub = sub
	ct.trackedConns = make(map[peer.ID]map[network.Conn]connMeta)
	ct.pendingReqs = make(map[peer.ID][]req)
	ct.reqCh = make(chan req, 1)
	ct.cleanupCh = make(chan peer.ID, 16)
	ct.connNotifs = make(chan connNotif, 8)
	ct.notifier = notifier
	ct.notifier.Notify(ct)

	if ct.clock == nil {
		ct.clock = realClock{}
	}

	go ct.loop()

	return nil
}

func (ct *ConnTracker) Stop() {
	ct.notifier.StopNotify(ct)
	ct.stop()
	<-ct.stopped
	ct.notifier = nil
	ct.trackedConns = nil
	ct.pendingReqs = nil
	ct.connNotifs = nil
	ct.sub.Close()
}

func (ct *ConnTracker) gc() {
	for p, rs := range ct.pendingReqs {
		rs, n := clearCancelledReqs(rs)
		if n > 0 {
			ct.pendingReqs[p] = rs
			ct.totalPendingReqs -= n
		}
	}
}

func (ct *ConnTracker) updateProtos(p peer.ID, conn network.Conn, protos []protocol.ID, replace bool, identified bool) {
	meta := connMeta{identified: identified}

	// Check if we already have tracked this conn
	if conns, ok := ct.trackedConns[p]; ok {
		if connMeta, ok := conns[conn]; ok {
			// Keep the identified status
			if connMeta.identified {
				meta.identified = true
			}
			// reuse the map
			meta.protos = connMeta.protos
			if replace {
				clear(meta.protos)
			}
		}
	}
	if meta.protos == nil {
		meta.protos = make(map[protocol.ID]struct{}, len(protos))
	}

	for _, p := range protos {
		meta.protos[p] = struct{}{}
	}

	if _, ok := ct.trackedConns[p]; !ok {
		ct.trackedConns[p] = make(map[network.Conn]connMeta)
	}
	ct.trackedConns[p][conn] = meta
}

func (ct *ConnTracker) loop() {
	defer close(ct.stopped)
	gcTicker := ct.clock.NewTicker(gcInterval)
	defer gcTicker.Stop()

	// debounce many recurring cleanup requests.
	var lastCleanupTime time.Time
	pendingCleanup := make(map[peer.ID]struct{}, maxPendingCleanup)

	for {
		select {
		case <-ct.svcCtx.Done():
			return
		case <-gcTicker.C:
			ct.gc()
		case p := <-ct.cleanupCh:
			pendingCleanup[p] = struct{}{}
			if ct.clock.Since(lastCleanupTime) < maxCleanupFrequency && len(pendingCleanup) < maxPendingCleanup {
				continue
			}

			lastCleanupTime = ct.clock.Now()
			for p := range pendingCleanup {
				rs, n := clearCancelledReqs(ct.pendingReqs[p])
				if n > 0 {
					ct.pendingReqs[p] = rs
					ct.totalPendingReqs -= n
				}
			}
		case notif := <-ct.connNotifs:
			switch notif.kind {
			case connNotifConnected:
				ct.updateProtos(notif.conn.RemotePeer(), notif.conn, nil, false, false)
			case connNotifDisconnected:
				if m, ok := ct.trackedConns[notif.conn.RemotePeer()]; ok {
					delete(m, notif.conn)
				}
			}
		case evt := <-ct.sub.Out():
			switch evt := evt.(type) {
			case event.EvtPeerConnectednessChanged:
				// Clean up if a peer has disconnected
				switch evt.Connectedness {
				case network.Connected, network.Limited:
				// Do nothing. We'll add this connection when we get the identify.
				default:
					// clean up
					delete(ct.trackedConns, evt.Peer)
				}
			case event.EvtPeerIdentificationFailed:
				ct.updateProtos(evt.Peer, evt.Conn, nil, false, true)
				ct.totalPendingReqs -= ct.tryFulfillPendingReqs(evt.Peer)
			case event.EvtPeerIdentificationCompleted:
				ct.updateProtos(evt.Peer, evt.Conn, evt.Protocols, true, true)
				ct.totalPendingReqs -= ct.tryFulfillPendingReqs(evt.Peer)
			case event.EvtProtocolNegotiationSuccess:
				ct.updateProtos(evt.Peer, evt.Conn, []protocol.ID{evt.Protocol}, false, false)
				ct.totalPendingReqs -= ct.tryFulfillPendingReqs(evt.Peer)
			default:
				log.Debug("unknown event", evt)
				continue
			}
		case req := <-ct.reqCh:
			fulfilled := ct.fulfillReq(req)
			if fulfilled && req.onFulfilled != nil {
				req.onFulfilled()
			}
			if !fulfilled {
				if req.immediate {
					req.resCh <- ConnWithMeta{}
					continue
				}

				if ct.totalPendingReqs >= pendingReqBound {
					// Drop the request
					log.Warn("dropping request. Too many pending requests")
					continue
				}

				ct.totalPendingReqs++
				ct.pendingReqs[req.p] = append(ct.pendingReqs[req.p], req)
			}
		}
	}
}

type ConnWithMeta struct {
	network.Conn
	Identified         bool
	supportedProtocols map[protocol.ID]struct{}
	MatchingProtocols  []protocol.ID
}

func (c *ConnWithMeta) SupportsProtocol(p protocol.ID) bool {
	_, ok := c.supportedProtocols[p]
	return ok
}

// wrapConnWithMeta wraps a network.Conn with the supported protocols that
// intersect the requested oneOf protocols. It preserves the order of the oneOf
// protocols.
func wrapConnWithMeta(c network.Conn, meta connMeta, oneOf []protocol.ID) ConnWithMeta {
	supportedProtocols := make(map[protocol.ID]struct{}, len(meta.protos))
	for p := range meta.protos {
		supportedProtocols[p] = struct{}{}
	}

	var matchingProtocols []protocol.ID
	if len(oneOf) == 0 {
		// If no oneOf protocols are provided, return all supported protocols.
		matchingProtocols = make([]protocol.ID, 0, len(meta.protos))
		for p := range meta.protos {
			matchingProtocols = append(matchingProtocols, p)
		}
	} else {
		matchingProtocols = make([]protocol.ID, 0, len(oneOf))
		for _, p := range oneOf {
			if _, ok := meta.protos[p]; ok {
				matchingProtocols = append(matchingProtocols, p)
			}
		}
	}

	return ConnWithMeta{
		Conn:               c,
		Identified:         meta.identified,
		MatchingProtocols:  matchingProtocols,
		supportedProtocols: supportedProtocols,
	}
}

// fulfillReq returns true if the request was fulfilled
func (ct *ConnTracker) fulfillReq(r req) bool {
	if r.ctx.Err() != nil {
		// Request has been cancelled
		return true
	}

	conns := make([]network.Conn, 0, len(ct.trackedConns[r.p]))
	for c, m := range ct.trackedConns[r.p] {
		if c.IsClosed() {
			delete(ct.trackedConns[r.p], c)
			continue
		}
		if r.waitForIdentify && !m.identified {
			continue
		}
		if r.filter != nil && !r.filter(c) {
			continue
		}
		if len(r.oneOf) != 0 {
			found := false
			for _, p := range r.oneOf {
				if _, ok := m.protos[p]; ok {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if r.sort == nil {
			r.resCh <- wrapConnWithMeta(c, m, r.oneOf)
			return true
		}
		conns = append(conns, c)
	}
	if r.sort != nil {
		slices.SortFunc(conns, r.sort)
	}

	if len(conns) > 0 {
		r.resCh <- wrapConnWithMeta(conns[0], ct.trackedConns[r.p][conns[0]], r.oneOf)
		return true
	}
	return false
}

// tryFulfillPendingReqs will attempt to fulfill pending requests.
// returns the number of requests fulfilled
func (ct *ConnTracker) tryFulfillPendingReqs(p peer.ID) int {
	l := len(ct.pendingReqs[p])
	ct.pendingReqs[p] = slices.DeleteFunc(ct.pendingReqs[p], ct.fulfillReq)
	if len(ct.pendingReqs[p]) == 0 {
		ct.pendingReqs[p] = nil
	}
	return l - len(ct.pendingReqs[p])
}

// clearCancelledReqs will clear all cancelled requests for a peer
// Returns the new slice and the number of cancelled requests
func clearCancelledReqs(rs []req) ([]req, int) {
	l := len(rs)
	rs = slices.DeleteFunc(rs, func(r req) bool {
		return r.ctx.Err() != nil
	})
	if len(rs) == 0 {
		rs = nil
	}
	return rs, l - len(rs)
}

func (ct *ConnTracker) cleanup(p peer.ID) {
	select {
	case ct.cleanupCh <- p:
	case <-ct.stopped:
		log.Debug("dropping cleanup request: service stopped")
	default:
		log.Debug("dropping cleanup request: channel full")
	}
}

var _ network.Notifiee = (*ConnTracker)(nil)

func (ct *ConnTracker) Connected(_ network.Network, c network.Conn) {
	select {
	case ct.connNotifs <- connNotif{kind: connNotifConnected, conn: c}:
	case <-ct.svcCtx.Done():
		log.Debug("dropping connection notification: service stopped")
	}
}

func (ct *ConnTracker) Disconnected(_ network.Network, c network.Conn) {
	select {
	case ct.connNotifs <- connNotif{kind: connNotifDisconnected, conn: c}:
	case <-ct.svcCtx.Done():
		log.Debug("dropping disconnection notification: service stopped")
	}
}

// Listen implements network.Notifiee.
func (ct *ConnTracker) Listen(network.Network, multiaddr.Multiaddr) {
	// unused
}

// ListenClose implements network.Notifiee.
func (ct *ConnTracker) ListenClose(network.Network, multiaddr.Multiaddr) {
	// unused
}

type GetBestConnOpts struct {
	OneOf []protocol.ID

	// Optional. If a filter function is provided, it will further filter the connections.
	// The filter function should return true if the connection is acceptable.
	FilterFn func(c network.Conn) bool
	// Optional. If a sort function is provided, it will be used to sort the connections.
	// Refer to slices.SortFunc for the signature of the sort function.
	// The first conenction in the sorted list will be returned
	SortFn          func(a, b network.Conn) int
	WaitForIdentify bool
	// If true, will return a response as soon as possible, even if no connection is available.
	AllowNoConn bool
}

// GetBestConn will return the best conn to a peer capable of using the
// provided protocol.
// If no connection is currently available this will block.
// If an empty oneOf protocolID is passed, any connection that has done `Identify`
// will be returned.
func (ct *ConnTracker) GetBestConn(ctx context.Context, peer peer.ID, opts GetBestConnOpts) (ConnWithMeta, error) {
	r := req{
		ctx:             ctx,
		p:               peer,
		oneOf:           opts.OneOf,
		filter:          opts.FilterFn,
		sort:            opts.SortFn,
		immediate:       opts.AllowNoConn,
		waitForIdentify: opts.WaitForIdentify,
	}
	res, err := ct.sendReq(ctx, r)
	if err != nil {
		return ConnWithMeta{}, err
	}
	select {
	case <-ctx.Done():
		return ConnWithMeta{}, ctx.Err()
	case conn := <-res:
		if conn.Conn == nil {
			return ConnWithMeta{}, ErrNoConn
		}
		return conn, nil
	}
}

var (
	ErrNoConn  = errors.New("no connection available")
	ErrStopped = errors.New("conntracker is stopped")
)

// GetBestConnChan is like GetBestConn but returns a channel that contains the best connection.
func (ct *ConnTracker) GetBestConnChan(ctx context.Context, peer peer.ID, opts GetBestConnOpts) (<-chan ConnWithMeta, error) {
	r := req{
		ctx:             ctx,
		p:               peer,
		oneOf:           opts.OneOf,
		filter:          opts.FilterFn,
		sort:            opts.SortFn,
		immediate:       opts.AllowNoConn,
		waitForIdentify: opts.WaitForIdentify,
	}
	return ct.sendReq(ctx, r)
}

func (ct *ConnTracker) sendReq(ctx context.Context, r req) (<-chan ConnWithMeta, error) {
	if r.resCh == nil {
		r.resCh = make(chan ConnWithMeta, 1)
	}

	stopCleanup := context.AfterFunc(ctx, func() {
		ct.cleanup(r.p)
	})

	r.onFulfilled = func() {
		// No need to cleanup, we fulfilled the request.
		stopCleanup()
	}

	select {
	case ct.reqCh <- r:
		return r.resCh, nil
	case <-ct.stopped:
		stopCleanup()
		// In case the conntracker is stopped, don't block. return an error.
		return nil, ErrStopped
	}
}

func NoLimitedConnFilter(c network.Conn) bool {
	return !c.Stat().Limited
}

// TODO: add a basic sort function that essentially copies [isBetterConn] in swarm.go
