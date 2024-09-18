package swarm

import (
	"context"
	"errors"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// dialWorkerFunc is used by dialSync to spawn a new dial worker
type dialWorkerFunc func(context.Context, peer.ID, <-chan dialRequest)

// errConcurrentDialSuccessful is used to signal that a concurrent dial succeeded
var errConcurrentDialSuccessful = errors.New("concurrent dial successful")

// newDialSync constructs a new dialSync
func newDialSync(worker dialWorkerFunc) *dialSync {
	return &dialSync{
		dials:      make(map[peer.ID]*activeDial),
		dialWorker: worker,
	}
}

// dialSync is a dial synchronization helper that ensures that at most one dial
// to any given peer is active at any given time.
type dialSync struct {
	mutex      sync.Mutex
	dials      map[peer.ID]*activeDial
	dialWorker dialWorkerFunc
}

type activeDial struct {
	refCnt int

	ctx         context.Context
	cancelCause func(error)

	reqch chan dialRequest
}

func (ad *activeDial) dial(ctx context.Context) (*Conn, error) {
	dialCtx := ad.ctx

	if forceDirect, reason := network.GetForceDirectDial(ctx); forceDirect {
		dialCtx = network.WithForceDirectDial(dialCtx, reason)
	}
	if simConnect, isClient, reason := network.GetSimultaneousConnect(ctx); simConnect {
		dialCtx = network.WithSimultaneousConnect(dialCtx, isClient, reason)
	}

	resch := make(chan dialResponse, 1)
	select {
	case ad.reqch <- dialRequest{ctx: dialCtx, resch: resch}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-dialCtx.Done():
		return nil, dialCtx.Err()
	}

	select {
	case res := <-resch:
		return res.conn, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-dialCtx.Done():
		return nil, dialCtx.Err()
	}
}

func (ds *dialSync) getActiveDial(p peer.ID) (*activeDial, error) {
	ds.mutex.Lock()
	defer ds.mutex.Unlock()

	actd, ok := ds.dials[p]
	if !ok {
		// This code intentionally uses the background context. Otherwise, if the first call
		// to Dial is canceled, subsequent dial calls will also be canceled.
		ctx, cancel := context.WithCancelCause(context.Background())
		actd = &activeDial{
			ctx:         ctx,
			cancelCause: cancel,
			reqch:       make(chan dialRequest),
		}
		go ds.dialWorker(ctx, p, actd.reqch)
		ds.dials[p] = actd
	}
	// increase ref count before dropping mutex
	actd.refCnt++
	return actd, nil
}

// Dial initiates a dial to the given peer if there are none in progress
// then waits for the dial to that peer to complete.
func (ds *dialSync) Dial(ctx context.Context, p peer.ID) (*Conn, error) {
	ad, err := ds.getActiveDial(p)
	if err != nil {
		return nil, err
	}

	conn, err := ad.dial(ctx)
	if cause := context.Cause(ad.ctx); cause != nil {
		var haveInboundConn errHaveInboundConn
		if errors.As(cause, &haveInboundConn) {
			conn, err = haveInboundConn.c, nil
		}
	}

	ds.mutex.Lock()
	defer ds.mutex.Unlock()

	ad.refCnt--
	if ad.refCnt == 0 {
		if err == nil {
			ad.cancelCause(errConcurrentDialSuccessful)
		} else {
			ad.cancelCause(err)
		}
		close(ad.reqch)
		delete(ds.dials, p)
	}

	return conn, err
}

func (ds *dialSync) CancelActiveDial(p peer.ID, cause error) {
	ds.mutex.Lock()
	ad, ok := ds.dials[p]
	ds.mutex.Unlock()
	if !ok {
		return
	}
	ad.cancelCause(cause)
}
