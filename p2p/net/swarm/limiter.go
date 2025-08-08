package swarm

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
)

type dialJob struct {
	addr    ma.Multiaddr
	peer    peer.ID
	ctx     context.Context
	resp    chan transport.DialUpdate
	timeout time.Duration
}

func (dj *dialJob) cancelled() bool {
	return dj.ctx.Err() != nil
}

type dialLimiter struct {
	lk sync.Mutex

	fdConsuming int
	fdLimit     int
	waitingOnFd []*dialJob

	dialFunc dialfunc

	activePerPeer      map[peer.ID]int
	perPeerLimit       int
	waitingOnPeerLimit map[peer.ID][]*dialJob
}

type dialfunc func(context.Context, peer.ID, ma.Multiaddr, chan<- transport.DialUpdate) (transport.CapableConn, error)

func newDialLimiter(df dialfunc) *dialLimiter {
	fd := ConcurrentFdDials
	if env := os.Getenv("LIBP2P_SWARM_FD_LIMIT"); env != "" {
		if n, err := strconv.ParseInt(env, 10, 32); err == nil {
			fd = int(n)
		}
	}
	return newDialLimiterWithParams(df, fd, DefaultPerPeerRateLimit)
}

func newDialLimiterWithParams(df dialfunc, fdLimit, perPeerLimit int) *dialLimiter {
	return &dialLimiter{
		fdLimit:            fdLimit,
		perPeerLimit:       perPeerLimit,
		waitingOnPeerLimit: make(map[peer.ID][]*dialJob),
		activePerPeer:      make(map[peer.ID]int),
		dialFunc:           df,
	}
}

// freeFDToken frees FD token and if there are any schedules another waiting dialJob
// in it's place
func (dl *dialLimiter) freeFDToken() {
	log.Debug("[limiter] freeing FD token", "waiting", len(dl.waitingOnFd), "fd_consuming", dl.fdConsuming)
	dl.fdConsuming--

	for len(dl.waitingOnFd) > 0 {
		next := dl.waitingOnFd[0]
		dl.waitingOnFd[0] = nil // clear out memory
		dl.waitingOnFd = dl.waitingOnFd[1:]

		if len(dl.waitingOnFd) == 0 {
			// clear out memory.
			dl.waitingOnFd = nil
		}

		// Skip over canceled dials instead of queuing up a goroutine.
		if next.cancelled() {
			dl.freePeerToken(next)
			continue
		}
		dl.fdConsuming++

		// we already have activePerPeer token at this point so we can just dial
		go dl.executeDial(next)
		return
	}
}

func (dl *dialLimiter) freePeerToken(dj *dialJob) {
	log.Debug("[limiter] freeing peer token",
		"peer", dj.peer,
		"addr", dj.addr,
		"active_for_peer", dl.activePerPeer[dj.peer],
		"waiting_on_peer_limit", len(dl.waitingOnPeerLimit[dj.peer]))
	// release tokens in reverse order than we take them
	dl.activePerPeer[dj.peer]--
	if dl.activePerPeer[dj.peer] == 0 {
		delete(dl.activePerPeer, dj.peer)
	}

	waitlist := dl.waitingOnPeerLimit[dj.peer]
	for len(waitlist) > 0 {
		next := waitlist[0]
		waitlist[0] = nil // clear out memory
		waitlist = waitlist[1:]

		if len(waitlist) == 0 {
			delete(dl.waitingOnPeerLimit, next.peer)
		} else {
			dl.waitingOnPeerLimit[next.peer] = waitlist
		}

		if next.cancelled() {
			continue
		}

		dl.activePerPeer[next.peer]++ // just kidding, we still want this token

		dl.addCheckFdLimit(next)
		return
	}
}

func (dl *dialLimiter) finishedDial(dj *dialJob) {
	dl.lk.Lock()
	defer dl.lk.Unlock()
	if dl.shouldConsumeFd(dj.addr) {
		dl.freeFDToken()
	}

	dl.freePeerToken(dj)
}

func (dl *dialLimiter) shouldConsumeFd(addr ma.Multiaddr) bool {
	// we don't consume FD's for relay addresses for now as they will be consumed when the Relay Transport
	// actually dials the Relay server. That dial call will also pass through this limiter with
	// the address of the relay server i.e. non-relay address.
	_, err := addr.ValueForProtocol(ma.P_CIRCUIT)

	isRelay := err == nil

	return !isRelay && isFdConsumingAddr(addr)
}

func (dl *dialLimiter) addCheckFdLimit(dj *dialJob) {
	if dl.shouldConsumeFd(dj.addr) {
		if dl.fdConsuming >= dl.fdLimit {
			log.Debug("[limiter] blocked dial waiting on FD token",
				"peer", dj.peer,
				"addr", dj.addr,
				"fd_consuming", dl.fdConsuming,
				"fd_limit", dl.fdLimit,
				"waiting", len(dl.waitingOnFd))
			dl.waitingOnFd = append(dl.waitingOnFd, dj)
			return
		}

		log.Debug("[limiter] taking FD token",
			"peer", dj.peer,
			"addr", dj.addr,
			"prev_consuming", dl.fdConsuming)
		// take token
		dl.fdConsuming++
	}

	log.Debug("[limiter] executing dial",
		"peer", dj.peer,
		"addr", dj.addr,
		"fd_consuming", dl.fdConsuming,
		"waiting", len(dl.waitingOnFd))
	go dl.executeDial(dj)
}

func (dl *dialLimiter) addCheckPeerLimit(dj *dialJob) {
	if dl.activePerPeer[dj.peer] >= dl.perPeerLimit {
		log.Debug("[limiter] blocked dial waiting on peer limit",
			"peer", dj.peer,
			"addr", dj.addr,
			"active", dl.activePerPeer[dj.peer],
			"peer_limit", dl.perPeerLimit,
			"waiting", len(dl.waitingOnPeerLimit[dj.peer]))
		wlist := dl.waitingOnPeerLimit[dj.peer]
		dl.waitingOnPeerLimit[dj.peer] = append(wlist, dj)
		return
	}
	dl.activePerPeer[dj.peer]++

	dl.addCheckFdLimit(dj)
}

// AddDialJob tries to take the needed tokens for starting the given dial job.
// If it acquires all needed tokens, it immediately starts the dial, otherwise
// it will put it on the waitlist for the requested token.
func (dl *dialLimiter) AddDialJob(dj *dialJob) {
	dl.lk.Lock()
	defer dl.lk.Unlock()

	log.Debug("[limiter] adding a dial job through limiter", "addr", dj.addr)
	dl.addCheckPeerLimit(dj)
}

func (dl *dialLimiter) clearAllPeerDials(p peer.ID) {
	dl.lk.Lock()
	defer dl.lk.Unlock()
	delete(dl.waitingOnPeerLimit, p)
	log.Debug("[limiter] clearing all peer dials", "peer", p)
	// NB: the waitingOnFd list doesn't need to be cleaned out here, we will
	// remove them as we encounter them because they are 'cancelled' at this
	// point
}

// executeDial calls the dialFunc, and reports the result through the response
// channel when finished. Once the response is sent it also releases all tokens
// it held during the dial.
func (dl *dialLimiter) executeDial(j *dialJob) {
	defer dl.finishedDial(j)
	if j.cancelled() {
		return
	}

	dctx, cancel := context.WithTimeout(j.ctx, j.timeout)
	defer cancel()

	con, err := dl.dialFunc(dctx, j.peer, j.addr, j.resp)
	kind := transport.UpdateKindDialSuccessful
	if err != nil {
		kind = transport.UpdateKindDialFailed
	}
	select {
	case j.resp <- transport.DialUpdate{Kind: kind, Conn: con, Addr: j.addr, Err: err}:
	case <-j.ctx.Done():
		if con != nil {
			con.Close()
		}
	}
}
