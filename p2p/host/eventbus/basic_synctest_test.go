//go:build go1.25

package eventbus

import (
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libp2p/go-libp2p/core/event"

	"github.com/stretchr/testify/require"
)

// TestWildcardSlowConsumerNoDeadlock checks that concurrent emitters to a full
// wildcard queue all make progress once the queue drains, even after the
// slow-consumer warning fires. Each emit must time its stall on its own timer:
// a timer shared across concurrent emitters lets one emitter consume the single
// tick and leaves the others blocked forever on the already-drained timer
// channel, while holding the node's read lock.
//
// synctest gives us a fake clock, so the one-second warning timeout fires
// instantly and deterministically instead of relying on real sleeps.
func TestWildcardSlowConsumerNoDeadlock(t *testing.T) {
	// Capture log output to confirm the slow-consumer path actually ran.
	defer func(orig *slog.Logger) { log = orig }(log)
	logs := &mockLogger{}
	log = slog.New(slog.NewTextHandler(logs, nil))

	synctest.Test(t, func(t *testing.T) {
		const emitters = 3

		bus := NewBus()
		sub, err := bus.Subscribe(event.WildcardSubscription, BufSize(1))
		require.NoError(t, err)
		// The wildcard subscription is intentionally left open: wildcardSub.Close
		// starts a drain goroutine that only returns once the subscription channel
		// closes, which never happens for wildcard subs. Closing it here would
		// leave that goroutine running and trip synctest's end-of-test check.

		em, err := bus.Emitter(new(EventB))
		require.NoError(t, err)
		defer em.Close()

		// drainOne frees a single buffer slot, letting one stalled emit proceed.
		drainOne := func() { <-sub.Out() }

		// Fill the single buffer slot so every later emit stalls on a full queue.
		require.NoError(t, em.Emit(EventB(0)))

		// Prime the slow-consumer path with one stalled emit, then release it
		// before the timeout. The historical bug parked a single reusable timer
		// on the node during this first stall; the concurrent emitters below then
		// raced on it. The fix gives every emit its own timer, so this is now just
		// a harmless warm-up, but it is what makes the deadlock reproducible.
		primed := make(chan struct{})
		go func() {
			em.Emit(EventB(0))
			close(primed)
		}()
		synctest.Wait() // primer is stalled in the slow path
		drainOne()      // let the primer complete via a normal send
		<-primed

		// Concurrent emitters now all stall on the full queue and enter the slow
		// path together.
		done := make(chan struct{}, emitters)
		for range emitters {
			go func() {
				em.Emit(EventB(0))
				done <- struct{}{}
			}()
		}
		synctest.Wait() // every emitter is blocked waiting on its timer

		// Pass the warning timeout so every stalled emit's timer fires.
		time.Sleep(slowConsumerWarningTimeout + time.Millisecond)
		synctest.Wait() // every emitter has warned and is now blocked on its send

		warnings := strings.Count(strings.Join(logs.Logs(), ""), "slow consumer")
		require.Equal(t, emitters, warnings, "each stalled emitter should warn once")

		// One drain per emitter; each frees a slot for exactly one stalled send.
		for range emitters {
			drainOne()
		}

		// Every emitter must return. A shared timer would leave one emitter stuck
		// forever draining the already-consumed timer channel, which synctest
		// reports as a deadlock.
		for range emitters {
			<-done
		}
	})
}
