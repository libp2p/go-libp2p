package identify

import (
	"container/heap"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"golang.org/x/time/rate"
)

// rateLimit is the configuration for a token bucket rate limiter.
// The bucket has a capacity of Burst, and is refilled at a rate of RPS tokens per second.
// In any given time interval T seconds, maximum events allowed will be `T*RPS + Burst`
type rateLimit struct {
	// RPS is the number of tokens added to the bucket per second.
	RPS float64
	// Burst is the maximum number of tokens in the bucket at any time.
	Burst int
}

// prefixRateLimit is a rate limit configuration that applies to a specific network prefix.
type prefixRateLimit struct {
	Prefix netip.Prefix
	rateLimit
}

// subnetRateLimit is a rate limit configuration that applies to a specific subnet.
type subnetRateLimit struct {
	PrefixLength int
	rateLimit
}

// rateLimiter rate limits new streams for a service. It allows setting NetworkPrefix specific,
// global, and subnet specific limits. Use 0 for no rate limiting.
// The ratelimiter maintains state that must be periodically cleaned up using Cleanup
type rateLimiter struct {
	// NetworkPrefixLimits are limits for streams where the peer IP falls within a specific
	// network prefix. It can be used to increase the limit for trusted networks and decrease the
	// limit for specific networks.
	NetworkPrefixLimits []prefixRateLimit
	// GlobalLimit is the limit for all streams where the peer IP doesn't fall within any
	// of the `NetworkPrefixLimits`
	GlobalLimit rateLimit
	// SubnetRateLimiter is a rate limiter for subnets.
	SubnetRateLimiter subnetRateLimiter

	initOnce             sync.Once
	globalBucket         *rate.Limiter
	networkPrefixBuckets []*rate.Limiter // ith element ratelimits ith NetworkPrefixLimits
}

func (r *rateLimiter) init() {
	r.initOnce.Do(func() {
		if r.GlobalLimit.RPS == 0 {
			r.globalBucket = rate.NewLimiter(rate.Inf, 0)
		} else {
			r.globalBucket = rate.NewLimiter(rate.Limit(r.GlobalLimit.RPS), r.GlobalLimit.Burst)
		}

		r.networkPrefixBuckets = make([]*rate.Limiter, 0, len(r.NetworkPrefixLimits))
		for _, limit := range r.NetworkPrefixLimits {
			if limit.RPS == 0 {
				r.networkPrefixBuckets = append(r.networkPrefixBuckets, rate.NewLimiter(rate.Inf, 0))
			} else {
				r.networkPrefixBuckets = append(r.networkPrefixBuckets, rate.NewLimiter(rate.Limit(limit.RPS), limit.Burst))
			}
		}
	})
}

func (r *rateLimiter) Limit(f func(s network.Stream)) func(s network.Stream) {
	r.init()
	return func(s network.Stream) {
		if !r.allow(s.Conn().RemoteMultiaddr()) {
			s.ResetWithError(network.StreamRateLimited)
			return
		}
		f(s)
	}
}

func (r *rateLimiter) allow(addr ma.Multiaddr) bool {
	r.init()
	// Check buckets from the most specific to the least.
	//
	// This ensures that a single peer cannot take up all the tokens in the global
	// rate limiting bucket. We *MUST* follow this order because the rate limiter
	// implementation doesn't have a `ReturnToken` method. If we checked the global
	// bucket before the specific bucket, and the specific bucket rejected the
	// request, there's no way to return the token to the global bucket. So all
	// rejected requests from the specific bucket would take up tokens from the global bucket.
	ip, err := manet.ToIP(addr)
	if err != nil {
		return r.globalBucket.Allow()
	}
	ipAddr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return r.globalBucket.Allow()
	}

	isWithinNetworkPrefix := false
	for i, limit := range r.NetworkPrefixLimits {
		if limit.Prefix.Contains(ipAddr) {
			if !r.networkPrefixBuckets[i].Allow() {
				return false
			}
			isWithinNetworkPrefix = true
		}
	}
	if isWithinNetworkPrefix {
		return true
	}

	if !r.SubnetRateLimiter.Allow(ipAddr, time.Now()) {
		return false
	}
	return r.globalBucket.Allow()
}

// tokenBucket is a *rate.Limiter with a `FullAfter` method.
type tokenBucket struct {
	*rate.Limiter
}

// FullAfter returns the duration from now after which the tokens will be full.
func (b *tokenBucket) FullAfter(now time.Time) time.Duration {
	tokensNeeded := float64(b.Limiter.Burst()) - b.Limiter.TokensAt(now)
	refillRate := float64(b.Limiter.Limit())
	eta := time.Duration((tokensNeeded / refillRate) * float64(time.Second))
	return eta
}

// prefixBucketWithExpiry is a token bucket with a prefix and Expiry. The expiry is when the bucket
// will be full with tokens.
type prefixBucketWithExpiry struct {
	tokenBucket
	Prefix netip.Prefix
	Expiry time.Time
}

// bucketHeap is a heap of buckets ordered by their Expiry. At expiry, the bucket
// is removed from the heap as a full bucket is indistinguishable from a new bucket.
type bucketHeap struct {
	prefixBucket  []prefixBucketWithExpiry
	prefixToIndex map[netip.Prefix]int
}

// Methods for the heap interface

// Len returns the length of the heap
func (h *bucketHeap) Len() int {
	return len(h.prefixBucket)
}

// Less compares two elements in the heap
func (h *bucketHeap) Less(i, j int) bool {
	return h.prefixBucket[i].Expiry.Before(h.prefixBucket[j].Expiry)
}

// Swap swaps two elements in the heap
func (h *bucketHeap) Swap(i, j int) {
	h.prefixBucket[i], h.prefixBucket[j] = h.prefixBucket[j], h.prefixBucket[i]
	h.prefixToIndex[h.prefixBucket[i].Prefix] = i
	h.prefixToIndex[h.prefixBucket[j].Prefix] = j
}

// Push adds a new element to the heap
func (h *bucketHeap) Push(x any) {
	item := x.(prefixBucketWithExpiry)
	h.prefixBucket = append(h.prefixBucket, item)
	h.prefixToIndex[item.Prefix] = len(h.prefixBucket) - 1
}

// Pop removes and returns the top element from the heap
func (h *bucketHeap) Pop() any {
	old := h.prefixBucket
	n := len(old)
	item := old[n-1]
	h.prefixBucket = old[0 : n-1]
	delete(h.prefixToIndex, item.Prefix)
	return item
}

// Upsert updates or inserts the prefix's bucket with bucket.
func (h *bucketHeap) Top() prefixBucketWithExpiry {
	if h.Len() == 0 {
		return prefixBucketWithExpiry{}
	}
	return h.prefixBucket[0]
}

// Upsert updates or inserts the prefix's bucket with bucket.
func (h *bucketHeap) Upsert(prefix netip.Prefix, bucket prefixBucketWithExpiry) {
	if i, ok := h.prefixToIndex[prefix]; ok {
		h.prefixBucket[i] = bucket
		heap.Fix(h, i)
		return
	}
	heap.Push(h, bucket)
}

// Get returns the limiter for a prefix
func (h *bucketHeap) Get(prefix netip.Prefix) prefixBucketWithExpiry {
	if i, ok := h.prefixToIndex[prefix]; ok {
		return h.prefixBucket[i]
	}
	return prefixBucketWithExpiry{}
}

type subnetRateLimiter struct {
	// IPv4SubnetLimits are the per subnet limits for streams with IPv4 Peers.
	IPv4SubnetLimits []subnetRateLimit
	// IPv6SubnetLimits are the per subnet limits for streams with IPv6 Peers.
	IPv6SubnetLimits []subnetRateLimit
	// GracePeriod is the time to wait to remove a full capacity bucket.
	// Keeping a bucket around helps prevent allocations
	GracePeriod time.Duration

	initOnce  sync.Once
	mx        sync.Mutex
	ipv4Heaps []*bucketHeap
	ipv6Heaps []*bucketHeap
}

func (s *subnetRateLimiter) init() {
	s.initOnce.Do(func() {
		slices.SortFunc(s.IPv4SubnetLimits, func(a, b subnetRateLimit) int { return b.PrefixLength - a.PrefixLength }) // smaller prefix length last
		slices.SortFunc(s.IPv6SubnetLimits, func(a, b subnetRateLimit) int { return b.PrefixLength - a.PrefixLength })
		s.ipv4Heaps = make([]*bucketHeap, len(s.IPv4SubnetLimits))
		for i := range s.IPv4SubnetLimits {
			s.ipv4Heaps[i] = &bucketHeap{
				prefixBucket:  make([]prefixBucketWithExpiry, 0),
				prefixToIndex: make(map[netip.Prefix]int),
			}
			heap.Init(s.ipv4Heaps[i])
		}

		s.ipv6Heaps = make([]*bucketHeap, len(s.IPv6SubnetLimits))
		for i := range s.IPv6SubnetLimits {
			s.ipv6Heaps[i] = &bucketHeap{
				prefixBucket:  make([]prefixBucketWithExpiry, 0),
				prefixToIndex: make(map[netip.Prefix]int),
			}
			heap.Init(s.ipv6Heaps[i])
		}
	})
}

func (s *subnetRateLimiter) Allow(ipAddr netip.Addr, now time.Time) bool {
	s.init()
	s.mx.Lock()
	defer s.mx.Unlock()

	s.cleanUp(now)

	var subNetLimits []subnetRateLimit
	var heaps []*bucketHeap
	if ipAddr.Is4() {
		subNetLimits = s.IPv4SubnetLimits
		heaps = s.ipv4Heaps
	} else {
		subNetLimits = s.IPv6SubnetLimits
		heaps = s.ipv6Heaps
	}

	for i, limit := range subNetLimits {
		prefix, err := ipAddr.Prefix(limit.PrefixLength)
		if err != nil {
			return false // we have a ipaddr this shouldn't happen
		}

		bucket := heaps[i].Get(prefix)
		if bucket == (prefixBucketWithExpiry{}) {
			bucket = prefixBucketWithExpiry{
				Prefix:      prefix,
				tokenBucket: tokenBucket{rate.NewLimiter(rate.Limit(limit.RPS), limit.Burst)},
			}
		}

		if !bucket.Limiter.Allow() {
			// bucket is empty, its expiry would have been set correctly last time
			return false
		}
		bucket.Expiry = now.Add(bucket.FullAfter(now)).Add(s.GracePeriod)
		heaps[i].Upsert(prefix, bucket)
	}
	return true
}

// cleanUp removes limiters that have expired by now.
func (s *subnetRateLimiter) cleanUp(now time.Time) {
	for _, h := range s.ipv4Heaps {
		for h.Len() > 0 {
			oldest := h.Top()
			if oldest.Expiry.After(now) {
				break
			}
			heap.Pop(h)
		}
	}

	for _, h := range s.ipv6Heaps {
		for h.Len() > 0 {
			oldest := h.Top()
			if oldest.Expiry.After(now) {
				break
			}
			heap.Pop(h)
		}
	}
}
