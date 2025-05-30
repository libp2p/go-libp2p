package rcmgr

import (
	"net/netip"
	"time"

	"github.com/libp2p/go-libp2p/x/rate"
)

var defaultIPv4SubnetLimits = []rate.SubnetLimit{
	{
		PrefixLength: 32,
		Limit:        rate.Limit{RPS: 0.2, Burst: 2 * defaultMaxConcurrentConns},
	},
}

var defaultIPv6SubnetLimits = []rate.SubnetLimit{
	{
		PrefixLength: 56,
		Limit:        rate.Limit{RPS: 0.2, Burst: 2 * defaultMaxConcurrentConns},
	},
	{
		PrefixLength: 48,
		Limit:        rate.Limit{RPS: 0.5, Burst: 10 * defaultMaxConcurrentConns},
	},
}

// defaultNetworkPrefixLimits ensure that all connections on localhost always succeed
var defaultNetworkPrefixLimits = []rate.PrefixLimit{
	{
		Prefix: netip.MustParsePrefix("127.0.0.0/8"),
		Limit:  rate.Limit{},
	},
	{
		Prefix: netip.MustParsePrefix("::1/128"),
		Limit:  rate.Limit{},
	},
}

// WithConnRateLimiters sets a custom rate limiter for new connections.
// connRateLimiter is used for OpenConnection calls
func WithConnRateLimiters(connRateLimiter *rate.Limiter) Option {
	return func(rm *resourceManager) error {
		rm.connRateLimiter = connRateLimiter
		return nil
	}
}

func newConnRateLimiter() *rate.Limiter {
	return &rate.Limiter{
		NetworkPrefixLimits: defaultNetworkPrefixLimits,
		GlobalLimit:         rate.Limit{},
		SubnetRateLimiter: rate.SubnetLimiter{
			IPv4SubnetLimits: defaultIPv4SubnetLimits,
			IPv6SubnetLimits: defaultIPv6SubnetLimits,
			GracePeriod:      1 * time.Minute,
		},
	}
}
