package test

import (
	"sort"
	"sync"
	"time"
)

type MockClock struct {
	mu           sync.Mutex
	now          time.Time
	timers       []*mockInstantTimer
	advanceBySem chan struct{}
}

type mockInstantTimer struct {
	c      *MockClock
	mu     sync.Mutex
	when   time.Time
	active bool
	ch     chan time.Time
}

func (t *mockInstantTimer) Ch() <-chan time.Time {
	return t.ch
}

func (t *mockInstantTimer) Reset(d time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := t.active
	t.active = true
	t.when = d

	// Schedule any timers that need to run. This will run this timer if t.when is before c.now
	go t.c.AdvanceBy(0)

	return wasActive
}

func (t *mockInstantTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}

func NewMockClock() *MockClock {
	return &MockClock{now: time.Unix(0, 0), advanceBySem: make(chan struct{}, 1)}
}

// InstantTimer implements a timer that triggers at a fixed instant in time as opposed to after a
// fixed duration from the moment of creation/reset.
//
// In test environments, when using a Timer which fires after a duration, there is a race between
// the goroutine moving time forward using `clock.Advanceby` and the goroutine resetting the
// timer by doing `timer.Reset(desiredInstant.Sub(time.Now()))`. The value of
// `desiredInstance.sub(time.Now())` is different depending on whether `clock.AdvanceBy` finishes
// before or after the timer reset.
func (c *MockClock) InstantTimer(when time.Time) *mockInstantTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &mockInstantTimer{
		c:      c,
		when:   when,
		ch:     make(chan time.Time, 1),
		active: true,
	}
	c.timers = append(c.timers, t)
	return t
}

// Since implements autorelay.ClockWithInstantTimer
func (c *MockClock) Since(t time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now.Sub(t)
}

func (c *MockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *MockClock) AdvanceBy(dur time.Duration) {
	c.advanceBySem <- struct{}{}
	defer func() { <-c.advanceBySem }()

	c.mu.Lock()
	now := c.now
	endTime := c.now.Add(dur)
	c.mu.Unlock()

	// sort timers by when
	if len(c.timers) > 1 {
		sort.Slice(c.timers, func(i, j int) bool {
			c.timers[i].mu.Lock()
			c.timers[j].mu.Lock()
			defer c.timers[i].mu.Unlock()
			defer c.timers[j].mu.Unlock()
			return c.timers[i].when.Before(c.timers[j].when)
		})
	}

	for _, t := range c.timers {
		t.mu.Lock()
		if !t.active {
			t.mu.Unlock()
			continue
		}
		if !t.when.After(now) {
			t.active = false
			t.mu.Unlock()
			// This may block if the channel is full, but that's intended. This way our mock clock never gets too far ahead of consumer.
			// This also prevents us from dropping times because we're advancing too fast.
			t.ch <- now
		} else if !t.when.After(endTime) {
			now = t.when
			c.mu.Lock()
			c.now = now
			c.mu.Unlock()

			t.active = false
			t.mu.Unlock()
			// This may block if the channel is full, but that's intended. See comment above
			t.ch <- c.now
		} else {
			t.mu.Unlock()
		}
	}
	c.mu.Lock()
	c.now = endTime
	c.mu.Unlock()
}
