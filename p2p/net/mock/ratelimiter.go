package mocknet

import (
	"sync"
	"time"
)

// A RateLimiter is used by a link to determine how long to wait before sending
// data given a bandwidth cap.
type RateLimiter struct {
	lock         sync.Mutex
	bandwidth    float64       // bytes per nanosecond
	allowance    float64       // in bytes
	maxAllowance float64       // in bytes
	lastUpdate   time.Time     // when allowance was updated last
	count        int           // number of times rate limiting was applied
	duration     time.Duration // total delay introduced due to rate limiting
}

// Creates a new RateLimiter with bandwidth (in bytes/sec)
func NewRateLimiter(bandwidth float64) *RateLimiter {
	//  convert bandwidth to bytes per nanosecond
	b := bandwidth / float64(time.Second)
	return &RateLimiter{
		bandwidth:    b,
		allowance:    0,
		maxAllowance: bandwidth,
		lastUpdate:   time.Now(),
	}
}

// Changes bandwidth of a RateLimiter and resets its allowance
func (r *RateLimiter) UpdateBandwidth(bandwidth float64) {
	r.lock.Lock()
	defer r.lock.Unlock()
	//  Convert bandwidth from bytes/second to bytes/nanosecond
	b := bandwidth / float64(time.Second)
	r.bandwidth = b
	//  Reset allowance
	r.allowance = 0
	r.maxAllowance = bandwidth
	r.lastUpdate = time.Now()
}

// Returns how long to wait before sending data with length 'dataSize' bytes
func (r *RateLimiter) Limit(dataSize int) time.Duration {
	r.lock.Lock()
	defer r.lock.Unlock()
	//  update time
	var duration time.Duration = time.Duration(0)
	if r.bandwidth == 0 {
		return duration
	}
	current := time.Now()
	elapsedTime := current.Sub(r.lastUpdate)
	r.lastUpdate = current

	allowance := r.allowance + float64(elapsedTime)*r.bandwidth
	//  allowance can't exceed bandwidth
	if allowance > r.maxAllowance {
		allowance = r.maxAllowance
	}

	allowance -= float64(dataSize)
	if allowance < 0 {
		//  sleep until allowance is back to 0
		duration = time.Duration(-allowance / r.bandwidth)
		//  rate limiting was applied, record stats
		r.count++
		r.duration += duration
	}

	r.allowance = allowance
	return duration
}
