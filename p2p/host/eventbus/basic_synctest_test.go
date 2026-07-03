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
	synctest.Test(t, func(t *testing.T) {
		const emitters = 3

		// Capture log output to confirm the slow-consumer path actually ran.
		logs := &mockLogger{}
		bus := NewBus(withLogger(slog.New(slog.NewTextHandler(logs, nil))))
		sub, err := bus.Subscribe(event.WildcardSubscription, BufSize(1))
		require.NoError(t, err)
		defer sub.Close()

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

// TestWildcardSubCloseReleasesDrainGoroutine checks that closing a wildcard
// subscription does not leak the drain goroutine started by removeSink. If the
// drainer is still blocked when the bubble ends, synctest reports a deadlock.
func TestWildcardSubCloseReleasesDrainGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus := NewBus()
		sub, err := bus.Subscribe(event.WildcardSubscription)
		require.NoError(t, err)
		require.NoError(t, sub.Close())
	})
}

// TestWildcardCloseUnblocksStalledEmit covers the slow-consumer safety valve: an
// emit stalled on a full wildcard subscriber, holding the node read lock, must
// be released when the subscription is closed. wildcardNode.removeSink starts
// draining the channel before it takes the write lock; were the order reversed,
// Close would deadlock against the read lock the stalled emit still holds.
func TestWildcardCloseUnblocksStalledEmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The stalled emit warns as part of this test; keep it out of the output.
		bus := NewBus()

		// An unbuffered subscriber has nowhere to put the event, so the emit below
		// stalls immediately without a fill step.
		sub, err := bus.Subscribe(event.WildcardSubscription, BufSize(0))
		require.NoError(t, err)

		em, err := bus.Emitter(new(EventB))
		require.NoError(t, err)
		defer em.Close()

		emitReturned := make(chan struct{})
		go func() {
			em.Emit(EventB(0))
			close(emitReturned)
		}()

		// Sleep past the warning timeout: the emit stalls, warns, and is now in
		// the deepest stall state — a bare send with no timeout escape, still
		// holding the node read lock.
		time.Sleep(slowConsumerWarningTimeout + time.Millisecond)
		synctest.Wait()

		// Close must free the stalled emit; synctest reports a deadlock if not.
		require.NoError(t, sub.Close())
		<-emitReturned
	})
}

func TestEmitLogsErrorOnStall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ml := mockLogger{}
		logger := slog.New(slog.NewTextHandler(&ml, nil))

		bus1 := NewBus(withLogger(logger))
		bus2 := NewBus(withLogger(logger))

		eventSub, err := bus1.Subscribe(new(EventA))
		if err != nil {
			t.Fatal(err)
		}

		wildcardSub, err := bus2.Subscribe(event.WildcardSubscription)
		if err != nil {
			t.Fatal(err)
		}

		testCases := []event.Subscription{eventSub, wildcardSub}
		eventBuses := []event.Bus{bus1, bus2}

		for i, sub := range testCases {
			bus := eventBuses[i]
			em, err := bus.Emitter(new(EventA))
			if err != nil {
				t.Fatal(err)
			}
			defer em.Close()

			go func() {
				for i := 0; i < subSettingsDefault.buffer+2; i++ {
					em.Emit(EventA{})
				}
			}()

			time.Sleep(3 * time.Second)
			logs := ml.Logs()
			found := false
			for _, log := range logs {
				if strings.Contains(log, "slow consumer") {
					found = true
					break
				}
			}
			require.True(t, found, "expected to find slow consumer log")
			ml.Clear()

			// Close the subscriber so the worker can finish.
			sub.Close()
		}
	})
}
