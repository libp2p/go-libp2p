package identify

import (
	"net/netip"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"golang.org/x/time/rate"
)

// events will be (time_in_seconds * PerSecond) + Burst.
// Set PerSecond to 0 for no rate limiting
type rateLimit struct {
	// PerSecond is the number of tokens added to the bucket per second.
	PerSecond rate.Limit
	// Burst is the maximum number of tokens in the bucket at any time.
	Burst int
}

// networkPrefixRateLimit is a rate limit configuration that applies to a specific network prefix.
type networkPrefixRateLimit struct {
	Prefix netip.Prefix
	rateLimit
}

// subnetRateLimit is a rate limit configuration that applies to a specific subnet.
type subnetRateLimit struct {
	PrefixLength int
	rateLimit
}

type limiterWithLastSeen struct {
	*rate.Limiter
	lastSeen time.Time
}

// rateLimiter rate limits new streams for a service. It allows setting NetworkPrefix specific,
// global, and subnet specific limits. Use 0 for no rate limiting.
// The ratelimiter maintains state that must be periodically cleaned up using Cleanup
type rateLimiter struct {
	// NetworkPrefixLimits are limits for streams where the peer IP falls within a specific
	// network prefix. It can be used to increase the limit for trusted networks and decrease the
	// limit for specific networks.
	NetworkPrefixLimits []networkPrefixRateLimit
	// GlobalLimit is the limit for all streams where the peer IP doesn't fall within any
	// of the `NetworkPrefixLimits`
	GlobalLimit rateLimit
	// IPv4SubnetLimits are the per subnet limits for streams with IPv4 Peers.
	IPv4SubnetLimits []subnetRateLimit
	// IPv6SubnetLimits are the per subnet limits for streams with IPv6 Peers.
	IPv6SubnetLimits []subnetRateLimit

	initOnce             sync.Once
	globalBucket         *rate.Limiter
	networkPrefixBuckets []*rate.Limiter // ith element ratelimits ith NetworkPrefixLimits
	mx                   sync.Mutex
	ipv4SubnetBuckets    []map[netip.Prefix]limiterWithLastSeen // ith element ratelimits ith IPv4SubnetLimits
	ipv6SubnetBuckets    []map[netip.Prefix]limiterWithLastSeen // ith element ratelimits ith IPv6SubnetLimits
}

func (r *rateLimiter) init() {
	r.initOnce.Do(func() {
		if r.GlobalLimit.PerSecond == 0 {
			r.globalBucket = rate.NewLimiter(rate.Inf, 0)
		} else {
			r.globalBucket = rate.NewLimiter(r.GlobalLimit.PerSecond, r.GlobalLimit.Burst)
		}

		r.ipv4SubnetBuckets = make([]map[netip.Prefix]limiterWithLastSeen, len(r.IPv4SubnetLimits))
		for i := range r.IPv4SubnetLimits {
			r.ipv4SubnetBuckets[i] = make(map[netip.Prefix]limiterWithLastSeen)
		}
		r.ipv6SubnetBuckets = make([]map[netip.Prefix]limiterWithLastSeen, len(r.IPv6SubnetLimits))
		for i := range r.IPv6SubnetLimits {
			r.ipv6SubnetBuckets[i] = make(map[netip.Prefix]limiterWithLastSeen)
		}

		r.networkPrefixBuckets = make([]*rate.Limiter, 0, len(r.NetworkPrefixLimits))
		for _, limit := range r.NetworkPrefixLimits {
			if limit.PerSecond == 0 {
				r.networkPrefixBuckets = append(r.networkPrefixBuckets, rate.NewLimiter(rate.Inf, 0))
			} else {
				r.networkPrefixBuckets = append(r.networkPrefixBuckets, rate.NewLimiter(limit.PerSecond, limit.Burst))
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
	// rate limiting bucket. We *MUST* do this, because the rate limiter implementation
	// doesn't have a `ReturnToken` method. If we checked the global bucket before
	// the specific bucket, and the specific bucket rejected the request, there's no
	// way to return the token to the global bucket. So all rejected requests from the
	// specific bucket would take up tokens from the global bucket.
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

	if !r.allowSubnet(ipAddr) {
		return false
	}
	return r.globalBucket.Allow()
}

func (r *rateLimiter) allowSubnet(ipAddr netip.Addr) bool {
	r.mx.Lock()
	defer r.mx.Unlock()
	var subNetLimits []subnetRateLimit
	var subNetBuckets []map[netip.Prefix]limiterWithLastSeen
	if ipAddr.Is4() {
		subNetLimits = r.IPv4SubnetLimits
		subNetBuckets = r.ipv4SubnetBuckets
	} else {
		subNetLimits = r.IPv6SubnetLimits
		subNetBuckets = r.ipv6SubnetBuckets
	}
	for i, limit := range subNetLimits {
		prefix, err := ipAddr.Prefix(limit.PrefixLength)
		if err != nil {
			// no need to check r.global here. If we have an ip address we'll have a prefix
			return false
		}
		if subNetBuckets[i][prefix].Limiter == nil {
			// Note: It doesn't make sense to have this be inf with 0 limits.
			// A subnetBucket with infinite limit might as well be removed from the list
			subNetBuckets[i][prefix] = limiterWithLastSeen{
				Limiter:  rate.NewLimiter(limit.PerSecond, limit.Burst),
				lastSeen: time.Now(),
			}
		} else {
			subNetBuckets[i][prefix] = limiterWithLastSeen{
				Limiter:  subNetBuckets[i][prefix].Limiter,
				lastSeen: time.Now(),
			}
		}
		if !subNetBuckets[i][prefix].Limiter.Allow() {
			// This is slightly wrong for the same reason that `allow` must check from most to least specific.
			// We should ideally put the tokens back to the previous rate limiting buckets(0-(i-1))
			// before returning false
			return false
		}
	}
	return true
}

func (r *rateLimiter) Cleanup() {
	r.init()
	now := time.Now()

	r.mx.Lock()
	defer r.mx.Unlock()
	for i := range r.ipv4SubnetBuckets {
		for prefix, limiter := range r.ipv4SubnetBuckets[i] {
			if limiter.TokensAt(now) >= float64(limiter.Burst()) {
				delete(r.ipv4SubnetBuckets[i], prefix)
			}
		}
	}
	for i := range r.ipv6SubnetBuckets {
		for prefix, limiter := range r.ipv6SubnetBuckets[i] {
			if limiter.TokensAt(now) >= float64(limiter.Burst()) {
				delete(r.ipv6SubnetBuckets[i], prefix)
			}
		}
	}
}
