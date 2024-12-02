package eventbus

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"

	"github.com/libp2p/go-libp2p-testing/race"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type EventA struct{}
type EventB int

func getN() int {
	n := 50000
	if race.WithRace() {
		n = 1000
	}
	return n
}

func (EventA) String() string {
	return "Oh, Hello"
}

func TestDefaultSubIsBuffered(t *testing.T) {
	bus := NewBus()
	s, err := bus.Subscribe(new(EventA))
	if err != nil {
		t.Fatal(err)
	}
	if cap(s.(*sub).ch) == 0 {
		t.Fatalf("without any options subscribe should be buffered. was %d", cap(s.(*sub).ch))
	}
}

func TestEmit(t *testing.T) {
	bus := NewBus()
	sub, err := bus.Subscribe(new(EventA))
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		defer sub.Close()
		<-sub.Out()
	}()

	em, err := bus.Emitter(new(EventA))
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	em.Emit(EventA{})
}

func TestSub(t *testing.T) {
	bus := NewBus()
	sub, err := bus.Subscribe(new(EventB))
	if err != nil {
		t.Fatal(err)
	}

	var event EventB

	var wait sync.WaitGroup
	wait.Add(1)

	go func() {
		defer sub.Close()
		event = (<-sub.Out()).(EventB)
		wait.Done()
	}()

	em, err := bus.Emitter(new(EventB))
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	em.Emit(EventB(7))
	wait.Wait()

	if event != 7 {
		t.Error("got wrong event")
	}
}

func TestGetAllEventTypes(t *testing.T) {
	bus := NewBus()
	require.Empty(t, bus.GetAllEventTypes())

	// the wildcard subscription should be returned.
	_, err := bus.Subscribe(event.WildcardSubscription)
	require.NoError(t, err)

	_, err = bus.Subscribe(new(EventB))
	require.NoError(t, err)

	evts := bus.GetAllEventTypes()
	require.Len(t, evts, 1)
	require.Equal(t, reflect.TypeOf((*EventB)(nil)).Elem(), evts[0])

	_, err = bus.Emitter(new(EventA))
	require.NoError(t, err)

	evts = bus.GetAllEventTypes()
	require.Len(t, evts, 2)
	require.Contains(t, evts, reflect.TypeOf((*EventB)(nil)).Elem())
	require.Contains(t, evts, reflect.TypeOf((*EventA)(nil)).Elem())
}

func TestEmitNoSubNoBlock(t *testing.T) {
	bus := NewBus()

	em, err := bus.Emitter(new(EventA))
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	em.Emit(EventA{})
}

type mockLogger struct {
	mu   sync.Mutex
	logs []string
}

func (m *mockLogger) Errorf(format string, args ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = append(m.logs, fmt.Sprintf(format, args...))
}

func (m *mockLogger) Logs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.logs
}

func (m *mockLogger) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = nil
}

func TestEmitLogsErrorOnStall(t *testing.T) {
	oldLogger := log
	defer func() {
		log = oldLogger
	}()
	ml := &mockLogger{}
	log = ml

	bus1 := NewBus()
	bus2 := NewBus()

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

		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			logs := ml.Logs()
			found := false
			for _, log := range logs {
				if strings.Contains(log, "slow consumer") {
					found = true
					break
				}
			}
			assert.True(collect, found, "expected to find slow consumer log")
		}, 3*time.Second, 500*time.Millisecond)
		ml.Clear()

		// Close the subscriber so the worker can finish.
		sub.Close()
	}
}

func TestEmitOnClosed(t *testing.T) {
	bus := NewBus()

	em, err := bus.Emitter(new(EventA))
	if err != nil {
		t.Fatal(err)
	}
	em.Close()
	err = em.Emit(EventA{})
	if err == nil {
		t.Errorf("expected error")
	}
	if err.Error() != "emitter is closed" {
		t.Error("unexpected message")
	}
}

func TestClosingRaces(t *testing.T) {
	subs := getN()
	emits := getN()

	var wg sync.WaitGroup
	var lk sync.RWMutex
	lk.Lock()

	wg.Add(subs + emits)

	b := NewBus()

	for i := 0; i < subs; i++ {
		go func() {
			lk.RLock()
			defer lk.RUnlock()

			sub, _ := b.Subscribe(new(EventA))
			time.Sleep(10 * time.Millisecond)
			sub.Close()

			wg.Done()
		}()
	}
	for i := 0; i < emits; i++ {
		go func() {
			lk.RLock()
			defer lk.RUnlock()

			emit, _ := b.Emitter(new(EventA))
			time.Sleep(10 * time.Millisecond)
			emit.Close()

			wg.Done()
		}()
	}

	time.Sleep(10 * time.Millisecond)
	lk.Unlock() // start everything

	wg.Wait()

	if len(b.(*basicBus).nodes) != 0 {
		t.Error("expected no nodes")
	}
}

func TestSubMany(t *testing.T) {
	bus := NewBus()

	var r atomic.Int32

	n := getN()
	var wait sync.WaitGroup
	var ready sync.WaitGroup
	wait.Add(n)
	ready.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			sub, err := bus.Subscribe(new(EventB))
			if err != nil {
				panic(err)
			}
			defer sub.Close()

			ready.Done()
			r.Add(int32((<-sub.Out()).(EventB)))
			wait.Done()
		}()
	}

	em, err := bus.Emitter(new(EventB))
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	ready.Wait()

	em.Emit(EventB(7))
	wait.Wait()

	if int(r.Load()) != 7*n {
		t.Error("got wrong result")
	}
}

func TestWildcardSubscription(t *testing.T) {
	bus := NewBus()
	sub, err := bus.Subscribe(event.WildcardSubscription)
	require.NoError(t, err)
	defer sub.Close()

	em1, err := bus.Emitter(new(EventA))
	require.NoError(t, err)
	defer em1.Close()

	em2, err := bus.Emitter(new(EventB))
	require.NoError(t, err)
	defer em2.Close()

	require.NoError(t, em1.Emit(EventA{}))
	require.NoError(t, em2.Emit(EventB(1)))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var evts []interface{}

LOOP:
	for {
		select {
		case evt := <-sub.Out():
			if evta, ok := evt.(EventA); ok {
				evts = append(evts, evta)
			}

			if evtb, ok := evt.(EventB); ok {
				evts = append(evts, evtb)
			}

			if len(evts) == 2 {
				break LOOP
			}

		case <-ctx.Done():
			t.Fatalf("did not receive events")
		}
	}
}

func TestManyWildcardSubscriptions(t *testing.T) {
	bus := NewBus()
	var subs []event.Subscription
	for i := 0; i < 10; i++ {
		sub, err := bus.Subscribe(event.WildcardSubscription)
		require.NoError(t, err)
		subs = append(subs, sub)
	}

	em1, err := bus.Emitter(new(EventA))
	require.NoError(t, err)
	defer em1.Close()

	em2, err := bus.Emitter(new(EventB))
	require.NoError(t, err)
	defer em2.Close()

	require.NoError(t, em1.Emit(EventA{}))
	require.NoError(t, em2.Emit(EventB(1)))

	// all 10 subscriptions received all 2 events.
	for _, s := range subs {
		require.Len(t, s.Out(), 2)
	}

	// close the first five subscriptions.
	for _, s := range subs[:5] {
		require.NoError(t, s.Close())
	}

	// emit another 2 events.
	require.NoError(t, em1.Emit(EventA{}))
	require.NoError(t, em2.Emit(EventB(1)))

	// the first five 0 events because it was closed. The other five
	// have 4 events.
	require.EventuallyWithT(t, func(t *assert.CollectT) {
		for _, s := range subs[:5] {
			require.Len(t, s.Out(), 0, "expected closed subscription to have flushed events")
		}
	}, 2*time.Second, 100*time.Millisecond)

	for _, s := range subs[5:] {
		require.Len(t, s.Out(), 4)
	}

	// close them all, the first five will be closed twice (asserts idempotency).
	for _, s := range subs {
		require.NoError(t, s.Close())
	}

	for _, s := range subs {
		require.Zero(t, s.(*wildcardSub).w.nSinks.Load())
	}
}

func TestWildcardValidations(t *testing.T) {
	bus := NewBus()

	_, err := bus.Subscribe([]interface{}{event.WildcardSubscription, new(EventA), new(EventB)})
	require.Error(t, err)

	_, err = bus.Emitter(event.WildcardSubscription)
	require.Error(t, err)
}

func TestSubType(t *testing.T) {
	bus := NewBus()
	sub, err := bus.Subscribe([]interface{}{new(EventA), new(EventB)})
	if err != nil {
		t.Fatal(err)
	}

	var event fmt.Stringer

	var wait sync.WaitGroup
	wait.Add(1)

	go func() {
		defer sub.Close()
		event = (<-sub.Out()).(EventA)
		wait.Done()
	}()

	em, err := bus.Emitter(new(EventA))
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	em.Emit(EventA{})
	wait.Wait()

	if event.String() != "Oh, Hello" {
		t.Error("didn't get the correct message")
	}
}

func TestNonStateful(t *testing.T) {
	bus := NewBus()
	em, err := bus.Emitter(new(EventB))
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	sub1, err := bus.Subscribe(new(EventB), BufSize(1))
	if err != nil {
		t.Fatal(err)
	}
	defer sub1.Close()

	select {
	case <-sub1.Out():
		t.Fatal("didn't expect to get an event")
	default:
	}

	em.Emit(EventB(1))

	select {
	case e := <-sub1.Out():
		if e.(EventB) != 1 {
			t.Fatal("got wrong event")
		}
	default:
		t.Fatal("expected to get an event")
	}

	sub2, err := bus.Subscribe(new(EventB), BufSize(1))
	if err != nil {
		t.Fatal(err)
	}
	defer sub2.Close()

	select {
	case <-sub2.Out():
		t.Fatal("didn't expect to get an event")
	default:
	}
}

func TestStateful(t *testing.T) {
	bus := NewBus()
	em, err := bus.Emitter(new(EventB), Stateful)
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	em.Emit(EventB(2))

	sub, err := bus.Subscribe(new(EventB), BufSize(1))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	if (<-sub.Out()).(EventB) != 2 {
		t.Fatal("got wrong event")
	}
}

func TestCloseBlocking(t *testing.T) {
	bus := NewBus()
	em, err := bus.Emitter(new(EventB))
	if err != nil {
		t.Fatal(err)
	}

	sub, err := bus.Subscribe(new(EventB))
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		em.Emit(EventB(159))
	}()

	time.Sleep(10 * time.Millisecond) // make sure that emit is blocked

	sub.Close() // cancel sub
}

func TestSubFailFully(t *testing.T) {
	bus := NewBus()
	em, err := bus.Emitter(new(EventB))
	if err != nil {
		t.Fatal(err)
	}

	_, err = bus.Subscribe([]interface{}{new(EventB), 5})
	if err == nil || err.Error() != "subscribe called with non-pointer type" {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		em.Emit(EventB(159)) // will hang if sub doesn't fail properly
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
}

func TestSubCloseMultiple(t *testing.T) {
	bus := NewBus()

	sub, err := bus.Subscribe([]interface{}{new(EventB)})
	require.NoError(t, err)
	err = sub.Close()
	require.NoError(t, err)
	err = sub.Close()
	require.NoError(t, err)
}

func testMany(t testing.TB, subs, emits, msgs int, stateful bool) {
	if race.WithRace() && subs+emits > 5000 {
		t.SkipNow()
	}

	bus := NewBus()

	var r atomic.Int64

	var wait sync.WaitGroup
	var ready sync.WaitGroup
	wait.Add(subs + emits)
	ready.Add(subs)

	for i := 0; i < subs; i++ {
		go func() {
			sub, err := bus.Subscribe(new(EventB))
			if err != nil {
				panic(err)
			}
			defer sub.Close()

			ready.Done()
			for i := 0; i < emits*msgs; i++ {
				e, ok := <-sub.Out()
				if !ok {
					panic("wat")
				}
				r.Add(int64(e.(EventB)))
			}
			wait.Done()
		}()
	}

	for i := 0; i < emits; i++ {
		go func() {
			em, err := bus.Emitter(new(EventB), func(settings interface{}) error {
				settings.(*emitterSettings).makeStateful = stateful
				return nil
			})
			if err != nil {
				panic(err)
			}
			defer em.Close()

			ready.Wait()

			for i := 0; i < msgs; i++ {
				em.Emit(EventB(97))
			}

			wait.Done()
		}()
	}

	wait.Wait()

	if int(r.Load()) != 97*subs*emits*msgs {
		t.Fatal("got wrong result")
	}
}

func TestBothMany(t *testing.T) {
	testMany(t, 10000, 100, 10, false)
}

type benchCase struct {
	subs     int
	emits    int
	stateful bool
}

func (bc benchCase) name() string {
	return fmt.Sprintf("subs-%03d/emits-%03d/stateful-%t", bc.subs, bc.emits, bc.stateful)
}

func genTestCases() []benchCase {
	ret := make([]benchCase, 0, 200)
	for stateful := 0; stateful < 2; stateful++ {
		for subs := uint(0); subs <= 8; subs = subs + 4 {
			for emits := uint(0); emits <= 8; emits = emits + 4 {
				ret = append(ret, benchCase{1 << subs, 1 << emits, stateful == 1})
			}
		}
	}
	return ret
}

func BenchmarkEvents(b *testing.B) {
	for _, bc := range genTestCases() {
		b.Run(bc.name(), benchMany(bc))
	}
}

func benchMany(bc benchCase) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		subs := bc.subs
		emits := bc.emits
		stateful := bc.stateful
		bus := NewBus()
		var wait sync.WaitGroup
		var ready sync.WaitGroup
		wait.Add(subs + emits)
		ready.Add(subs + emits)

		for i := 0; i < subs; i++ {
			go func() {
				sub, err := bus.Subscribe(new(EventB))
				if err != nil {
					panic(err)
				}
				defer sub.Close()

				ready.Done()
				ready.Wait()
				for i := 0; i < (b.N/emits)*emits; i++ {
					_, ok := <-sub.Out()
					if !ok {
						panic("wat")
					}
				}
				wait.Done()
			}()
		}

		for i := 0; i < emits; i++ {
			go func() {
				em, err := bus.Emitter(new(EventB), func(settings interface{}) error {
					settings.(*emitterSettings).makeStateful = stateful
					return nil
				})
				if err != nil {
					panic(err)
				}
				defer em.Close()

				ready.Done()
				ready.Wait()

				for i := 0; i < b.N/emits; i++ {
					em.Emit(EventB(97))
				}

				wait.Done()
			}()
		}
		ready.Wait()
		b.ResetTimer()
		wait.Wait()
	}
}

var div = 100

func BenchmarkSubscribe(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N/div; i++ {
		bus := NewBus()
		for j := 0; j < div; j++ {
			bus.Subscribe(new(EventA))
		}
	}
}

func BenchmarkEmitter(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N/div; i++ {
		bus := NewBus()
		for j := 0; j < div; j++ {
			bus.Emitter(new(EventA))
		}
	}
}

func BenchmarkSubscribeAndEmitter(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N/div; i++ {
		bus := NewBus()
		for j := 0; j < div; j++ {
			bus.Subscribe(new(EventA))
			bus.Emitter(new(EventA))
		}
	}
}
