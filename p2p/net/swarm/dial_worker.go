package swarm

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

// /////////////////////////////////////////////////////////////////////////////////
// lo and behold, The Dialer
// TODO explain how all this works
// ////////////////////////////////////////////////////////////////////////////////

type dialRequest struct {
	ctx   context.Context
	resch chan dialResponse
}

type dialResponse struct {
	conn *Conn
	err  error
}

type pendRequest struct {
	req   dialRequest               // the original request
	err   *DialError                // dial error accumulator
	addrs map[ma.Multiaddr]struct{} // pending addr dials
}

type addrDial struct {
	addr     ma.Multiaddr
	ctx      context.Context
	conn     *Conn
	err      error
	requests []int
	dialed   bool
}

type dialWorker struct {
	s        *Swarm
	peer     peer.ID
	reqch    <-chan dialRequest
	reqno    int
	requests map[int]*pendRequest
	pending  map[ma.Multiaddr]*addrDial
	resch    chan dialResult

	connected bool // true when a connection has been successfully established

	nextDial []ma.Multiaddr

	// ready when we have more addresses to dial (nextDial is not empty)
	triggerDial <-chan struct{}

	// ds schedules dials for a request
	ds *dialScheduler

	// for testing
	wg sync.WaitGroup
}

func newDialWorker(s *Swarm, p peer.ID, reqch <-chan dialRequest) *dialWorker {
	return &dialWorker{
		s:        s,
		peer:     p,
		reqch:    reqch,
		requests: make(map[int]*pendRequest),
		pending:  make(map[ma.Multiaddr]*addrDial),
		resch:    make(chan dialResult),
		ds:       newDialScheduler(),
	}
}

func (w *dialWorker) loop() {
	w.wg.Add(1)
	defer w.wg.Done()
	defer w.s.limiter.clearAllPeerDials(w.peer)

	// used to signal readiness to dial and completion of the dial
	ready := make(chan struct{})
	close(ready)
	currDials := 0
	w.ds.start()
loop:
	for {
		select {
		case req, ok := <-w.reqch:
			if !ok {
				w.ds.close()
				return
			}

			c, err := w.s.bestAcceptableConnToPeer(req.ctx, w.peer)
			if c != nil || err != nil {
				req.resch <- dialResponse{conn: c, err: err}
				continue loop
			}

			addrs, err := w.s.addrsForDial(req.ctx, w.peer)
			if err != nil {
				req.resch <- dialResponse{err: err}
				continue loop
			}

			// create the pending request object
			pr := &pendRequest{
				req:   req,
				err:   &DialError{Peer: w.peer},
				addrs: make(map[ma.Multiaddr]struct{}),
			}
			for _, a := range addrs {
				pr.addrs[a] = struct{}{}
			}

			// check if any of the address have a completed dial already
			for a := range pr.addrs {
				ad, ok := w.pending[a]
				if !ok || !ad.dialed {
					continue
				}

				if ad.conn != nil {
					// dial to this addr was successful, complete the request
					req.resch <- dialResponse{conn: ad.conn}
					continue loop
				}

				if ad.err != nil {
					// dial to this addr errored, accumulate the error
					pr.err.recordErr(a, ad.err)
					delete(pr.addrs, a)
					continue
				}
			}

			// all request applicable addrs have been dialed, we must have errored
			if len(pr.addrs) == 0 {
				req.resch <- dialResponse{err: pr.err}
				continue loop
			}

			// the request has some pending or new dials, track it and schedule new dials
			w.reqno++
			w.requests[w.reqno] = pr

			var toschedule []ma.Multiaddr
			for a := range pr.addrs {
				ad, ok := w.pending[a]
				if !ok {
					w.pending[a] = &addrDial{
						addr:     a,
						ctx:      req.ctx,
						requests: []int{w.reqno},
					}
				} else {
					if simConnect, isClient, reason := network.GetSimultaneousConnect(req.ctx); simConnect {
						if simConnect, _, _ := network.GetSimultaneousConnect(ad.ctx); !simConnect {
							ad.ctx = network.WithSimultaneousConnect(ad.ctx, isClient, reason)
						}
					}
					ad.requests = append(ad.requests, w.reqno)
				}
				toschedule = append(toschedule, a)
			}

			simConnect, _, _ := network.GetSimultaneousConnect(req.ctx)
			w.ds.schedule(toschedule, simConnect)
			if currDials == 0 {
				w.ds.triggerNext()
			}

		case addr := <-w.ds.C:
			// spawn the dial
			ad := w.pending[addr]
			if ad.dialed {
				continue loop
			}
			ad.dialed = true
			err := w.s.dialNextAddr(ad.ctx, w.peer, addr, w.resch)
			if err != nil {
				w.dispatchError(ad, err)
			} else {
				currDials++
			}

		case res := <-w.resch:
			if res.Conn != nil {
				w.connected = true
			}
			currDials--
			if currDials == 0 {
				w.ds.triggerNext()
			}
			ad := w.pending[res.Addr]

			if res.Conn != nil {
				// we got a connection, add it to the swarm
				conn, err := w.s.addConn(res.Conn, network.DirOutbound)
				if err != nil {
					// oops no, we failed to add it to the swarm
					res.Conn.Close()
					w.dispatchError(ad, err)
					continue loop
				}

				// dispatch to still pending requests
				for _, reqno := range ad.requests {
					pr, ok := w.requests[reqno]
					if !ok {
						// it has already dispatched a connection
						continue
					}

					pr.req.resch <- dialResponse{conn: conn}
					delete(w.requests, reqno)
				}

				ad.conn = conn
				ad.requests = nil

				continue loop
			}

			// it must be an error -- add backoff if applicable and dispatch
			if res.Err != context.Canceled && !w.connected {
				// we only add backoff if there has not been a successful connection
				// for consistency with the old dialer behavior.
				w.s.backf.AddBackoff(w.peer, res.Addr)
			}

			w.dispatchError(ad, res.Err)
		}
	}
}

// dispatches an error to a specific addr dial
func (w *dialWorker) dispatchError(ad *addrDial, err error) {
	ad.err = err
	for _, reqno := range ad.requests {
		pr, ok := w.requests[reqno]
		if !ok {
			// has already been dispatched
			continue
		}

		// accumulate the error
		pr.err.recordErr(ad.addr, err)

		delete(pr.addrs, ad.addr)
		if len(pr.addrs) == 0 {
			// all addrs have erred, dispatch dial error
			// but first do a last one check in case an acceptable connection has landed from
			// a simultaneous dial that started later and added new acceptable addrs
			c, _ := w.s.bestAcceptableConnToPeer(pr.req.ctx, w.peer)
			if c != nil {
				pr.req.resch <- dialResponse{conn: c}
			} else {
				pr.req.resch <- dialResponse{err: pr.err}
			}
			delete(w.requests, reqno)
		}
	}

	ad.requests = nil

	// if it was a backoff, clear the address dial so that it doesn't inhibit new dial requests.
	// this is necessary to support active listen scenarios, where a new dial comes in while
	// another dial is in progress, and needs to do a direct connection without inhibitions from
	// dial backoff.
	// it is also necessary to preserve consisent behaviour with the old dialer -- TestDialBackoff
	// regresses without this.
	if err == ErrDialBackoff {
		delete(w.pending, ad.addr)
	}
}
