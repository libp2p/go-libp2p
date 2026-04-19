package routing

import (
	"context"
	"slices"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

// QueryEventType indicates the query event's type.
type QueryEventType int

// Number of events to buffer.
var QueryEventBufferSize = 16

const (
	// Sending a query to a peer.
	SendingQuery QueryEventType = iota
	// Got a response from a peer.
	PeerResponse
	// Found a "closest" peer (not currently used).
	FinalPeer
	// Got an error when querying.
	QueryError
	// Found a provider.
	Provider
	// Found a value.
	Value
	// Adding a peer to the query.
	AddingPeer
	// Dialing a peer.
	DialingPeer
)

// QueryEvent is emitted for every notable event that happens during a DHT query.
//
// Publishers may mutate Responses (and the AddrInfo values it points at)
// freely after calling PublishQueryEvent: the event is deep-copied before
// it reaches subscribers. Subscribers must still treat their copy as
// read-only, since events are fanned out by pointer to a single consumer
// channel.
type QueryEvent struct {
	ID        peer.ID
	Type      QueryEventType
	Responses []*peer.AddrInfo
	Extra     string
}

type routingQueryKey struct{}
type eventChannel struct {
	mu  sync.Mutex
	ctx context.Context
	ch  chan<- *QueryEvent
}

// waitThenClose is spawned in a goroutine when the channel is registered. This
// safely cleans up the channel when the context has been canceled.
func (e *eventChannel) waitThenClose() {
	<-e.ctx.Done()
	e.mu.Lock()
	close(e.ch)
	// 1. Signals that we're done.
	// 2. Frees memory (in case we end up hanging on to this for a while).
	e.ch = nil
	e.mu.Unlock()
}

// send sends an event on the event channel, aborting if either the passed or
// the internal context expire.
func (e *eventChannel) send(ctx context.Context, ev *QueryEvent) {
	e.mu.Lock()
	// Closed.
	if e.ch == nil {
		e.mu.Unlock()
		return
	}
	// in case the passed context is unrelated, wait on both.
	select {
	case e.ch <- ev:
	case <-e.ctx.Done():
	case <-ctx.Done():
	}
	e.mu.Unlock()
}

// RegisterForQueryEvents registers a query event channel with the given
// context. The returned context can be passed to DHT queries to receive query
// events on the returned channels.
//
// The passed context MUST be canceled when the caller is no longer interested
// in query events.
func RegisterForQueryEvents(ctx context.Context) (context.Context, <-chan *QueryEvent) {
	ch := make(chan *QueryEvent, QueryEventBufferSize)
	ech := &eventChannel{ch: ch, ctx: ctx}
	go ech.waitThenClose()
	return context.WithValue(ctx, routingQueryKey{}, ech), ch
}

// PublishQueryEvent publishes a query event to the query event channel
// associated with the given context, if any.
//
// The event's Responses slice (and each AddrInfo.Addrs slice it points
// at) is deep-copied before delivery, so the caller can safely keep
// mutating its own copy after this call returns.
func PublishQueryEvent(ctx context.Context, ev *QueryEvent) {
	ich := ctx.Value(routingQueryKey{})
	if ich == nil {
		return
	}

	// We *want* to panic here.
	ech := ich.(*eventChannel)
	ech.send(ctx, cloneForPublish(ev))
}

// cloneForPublish returns ev with Responses (and each AddrInfo.Addrs)
// replaced by independent copies. Without this, a publisher mutating its
// AddrInfo slice after PublishQueryEvent returns would race with any
// subscriber reading Responses.
//
// The deeper Multiaddr values are treated as immutable by convention and
// are not copied.
func cloneForPublish(ev *QueryEvent) *QueryEvent {
	if len(ev.Responses) == 0 {
		return ev
	}
	out := *ev
	out.Responses = make([]*peer.AddrInfo, len(ev.Responses))
	for i, ai := range ev.Responses {
		if ai == nil {
			continue
		}
		cp := *ai
		cp.Addrs = slices.Clone(ai.Addrs)
		out.Responses[i] = &cp
	}
	return &out
}

// SubscribesToQueryEvents returns true if the context subscribes to query
// events. If this function returns false, calling `PublishQueryEvent` on the
// context will be a no-op.
func SubscribesToQueryEvents(ctx context.Context) bool {
	return ctx.Value(routingQueryKey{}) != nil
}
