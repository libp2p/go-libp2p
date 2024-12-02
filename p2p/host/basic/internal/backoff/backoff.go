package backoff

import (
	"time"
)

var since = time.Since

const defaultDelay = 100 * time.Millisecond
const defaultMaxDelay = 1 * time.Minute

type ExpBackoff struct {
	Delay    time.Duration
	MaxDelay time.Duration

	failures int
	lastRun  time.Time
}

func (b *ExpBackoff) init() {
	if b.Delay == 0 {
		b.Delay = defaultDelay
	}
	if b.MaxDelay == 0 {
		b.MaxDelay = defaultMaxDelay
	}
}

func (b *ExpBackoff) calcDelay() time.Duration {
	delay := b.Delay * time.Duration(1<<(b.failures-1))
	delay = min(delay, b.MaxDelay)
	return delay
}

func (b *ExpBackoff) Run(f func() error) (err error, ran bool) {
	b.init()

	if b.failures != 0 {
		if since(b.lastRun) < b.calcDelay() {
			return nil, false
		}
	}

	b.lastRun = time.Now()
	err = f()
	if err == nil {
		b.failures = 0
	} else {
		b.failures++
	}
	return err, true
}
