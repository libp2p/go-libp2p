package identify

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

const rateLimitErrorTolerance = 0.05

func getSleepDurationAndRequestCount(rps float64) (time.Duration, int) {
	sleepDuration := 100 * time.Millisecond
	requestCount := int(sleepDuration.Seconds() * float64(rps))
	if requestCount < 1 {
		// Adding 1ms to ensure we do get 1 request. If the rate is low enough that
		// 100ms won't have a single request adding 1ms won't error here.
		sleepDuration = time.Duration((1/rps)*float64(time.Second)) + 1*time.Millisecond
		requestCount = 1
	}
	return sleepDuration, requestCount
}

func assertRateLimiter(t *testing.T, rl *rateLimiter, addr ma.Multiaddr, allowed, errorMargin int) {
	t.Helper()
	for i := 0; i < allowed; i++ {
		require.True(t, rl.allow(addr))
	}
	for i := 0; i < errorMargin; i++ {
		rl.allow(addr)
	}
	require.False(t, rl.allow(addr))
}

func TestRateLimiterGlobal(t *testing.T) {
	addr := ma.StringCast("/ip4/127.0.0.1/udp/123/quic-v1")
	limits := []rateLimit{
		{RPS: 0.0, Burst: 1},
		{RPS: 0.8, Burst: 1},
		{RPS: 10, Burst: 20},
		{RPS: 100, Burst: 200},
		{RPS: 1000, Burst: 2000},
	}
	for _, limit := range limits {
		t.Run(fmt.Sprintf("limit %0.1f", limit.RPS), func(t *testing.T) {
			rl := &rateLimiter{
				GlobalLimit: limit,
			}
			if limit.RPS == 0 {
				// 0 implies no rate limiting, any large number would do
				for i := 0; i < 1000; i++ {
					require.True(t, rl.allow(addr))
				}
				return
			}
			assertRateLimiter(t, rl, addr, limit.Burst, int(limit.RPS*rateLimitErrorTolerance))
			sleepDuration, requestCount := getSleepDurationAndRequestCount(limit.RPS)
			time.Sleep(sleepDuration)
			assertRateLimiter(t, rl, addr, requestCount, int(float64(requestCount)*rateLimitErrorTolerance))
		})
	}
}

func TestRateLimiterNetworkPrefix(t *testing.T) {
	local := ma.StringCast("/ip4/127.0.0.1/udp/123/quic-v1")
	public := ma.StringCast("/ip4/1.1.1.1/udp/123/quic-v1")
	rl := &rateLimiter{
		NetworkPrefixLimits: []prefixRateLimit{
			{Prefix: netip.MustParsePrefix("127.0.0.0/24"), rateLimit: rateLimit{}},
		},
		GlobalLimit: rateLimit{RPS: 10, Burst: 10},
	}
	// element within prefix is allowed even over the limit
	for range rl.GlobalLimit.Burst + 100 {
		require.True(t, rl.allow(local))
	}
	// rate limit public ips
	assertRateLimiter(t, rl, public, rl.GlobalLimit.Burst, int(rl.GlobalLimit.RPS*rateLimitErrorTolerance))

	// public ip rejected
	require.False(t, rl.allow(public))
	// local ip accepted
	for range 100 {
		require.True(t, rl.allow(local))
	}
}

func subnetAddrs(prefix netip.Prefix) func() netip.Addr {
	next := prefix.Addr()
	return func() netip.Addr {
		addr := next
		next = addr.Next()
		if !prefix.Contains(addr) {
			next = prefix.Addr()
			addr = next
		}
		return addr
	}
}

func TestSubnetRateLimiter(t *testing.T) {
	assertOutput := func(outcome bool, rl *subnetRateLimiter, subnetAddrs func() netip.Addr, n int) {
		t.Helper()
		for range n {
			require.Equal(t, outcome, rl.Allow(subnetAddrs(), time.Now()), "%d", n)
		}
	}

	t.Run("Simple", func(t *testing.T) {
		// Keep the refil rate low
		v4Small := subnetRateLimit{PrefixLength: 24, rateLimit: rateLimit{RPS: 0.0001, Burst: 10}}
		v4Large := subnetRateLimit{PrefixLength: 16, rateLimit: rateLimit{RPS: 0.0001, Burst: 19}}

		v6Small := subnetRateLimit{PrefixLength: 64, rateLimit: rateLimit{RPS: 0.0001, Burst: 10}}
		v6Large := subnetRateLimit{PrefixLength: 48, rateLimit: rateLimit{RPS: 0.0001, Burst: 17}}
		rl := &subnetRateLimiter{
			IPv4SubnetLimits: []subnetRateLimit{v4Large, v4Small},
			IPv6SubnetLimits: []subnetRateLimit{v6Large, v6Small},
		}

		v4SubnetAddr1 := subnetAddrs(netip.MustParsePrefix("192.168.1.1/24"))
		v4SubnetAddr2 := subnetAddrs(netip.MustParsePrefix("192.168.2.1/24"))
		v6SubnetAddr1 := subnetAddrs(netip.MustParsePrefix("2001:0:0:1::/64"))
		v6SubnetAddr2 := subnetAddrs(netip.MustParsePrefix("2001:0:0:2::/64"))

		assertOutput(true, rl, v4SubnetAddr1, v4Small.Burst)
		assertOutput(false, rl, v4SubnetAddr1, v4Large.Burst)

		assertOutput(true, rl, v4SubnetAddr2, v4Large.Burst-v4Small.Burst)
		assertOutput(false, rl, v4SubnetAddr2, v4Large.Burst)

		assertOutput(true, rl, v6SubnetAddr1, v6Small.Burst)
		assertOutput(false, rl, v6SubnetAddr1, v6Large.Burst)

		assertOutput(true, rl, v6SubnetAddr2, v6Large.Burst-v6Small.Burst)
		assertOutput(false, rl, v6SubnetAddr2, v6Large.Burst)
	})

	t.Run("Complex", func(t *testing.T) {
		limits := []subnetRateLimit{
			{PrefixLength: 32, rateLimit: rateLimit{RPS: 0.01, Burst: 10}},
			{PrefixLength: 24, rateLimit: rateLimit{RPS: 0.01, Burst: 20}},
			{PrefixLength: 16, rateLimit: rateLimit{RPS: 0.01, Burst: 30}},
			{PrefixLength: 8, rateLimit: rateLimit{RPS: 0.01, Burst: 40}},
		}
		rl := &subnetRateLimiter{
			IPv4SubnetLimits: limits,
		}

		snAddrs := []func() netip.Addr{
			subnetAddrs(netip.MustParsePrefix("192.168.1.1/32")),
			subnetAddrs(netip.MustParsePrefix("192.168.1.2/24")),
			subnetAddrs(netip.MustParsePrefix("192.168.2.1/16")),
			subnetAddrs(netip.MustParsePrefix("192.0.1.1/8")),
		}
		for i, addrsFunc := range snAddrs {
			prev := 0
			if i > 0 {
				prev = limits[i-1].Burst
			}
			assertOutput(true, rl, addrsFunc, limits[i].Burst-prev)
			assertOutput(false, rl, addrsFunc, limits[i].Burst)
		}
	})

	t.Run("Zero", func(t *testing.T) {
		sl := subnetRateLimiter{}
		for range 10000 {
			require.True(t, sl.Allow(netip.IPv6Loopback(), time.Now()))
		}
	})
}

func TestSubnetRateLimiterCleanup(t *testing.T) {
	tc := []struct {
		rateLimit
		TTL time.Duration
	}{
		{rateLimit: rateLimit{RPS: 1, Burst: 10}, TTL: 10 * time.Second},
		{rateLimit: rateLimit{RPS: 0.1, Burst: 2}, TTL: 20 * time.Second},
		{rateLimit: rateLimit{RPS: 1, Burst: 100}, TTL: 100 * time.Second},
		{rateLimit: rateLimit{RPS: 3, Burst: 6}, TTL: 2 * time.Second},
	}
	for i, tt := range tc {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			ip1, ip2 := netip.IPv6Loopback(), netip.MustParseAddr("2001::")
			sl := subnetRateLimiter{IPv6SubnetLimits: []subnetRateLimit{{PrefixLength: 64, rateLimit: tt.rateLimit}}}
			now := time.Now()
			// Empty the ip1 bucket
			for range tt.Burst {
				require.True(t, sl.Allow(ip1, now))
			}
			for range tt.Burst / 2 {
				require.True(t, sl.Allow(ip2, now))
			}
			epsilon := 100 * time.Millisecond
			// just before ip1 expiry
			now = now.Add(tt.TTL).Add(-epsilon)
			sl.cleanUp(now) // ip2 will be removed
			require.Equal(t, 1, sl.ipv6Heaps[0].Len())
			// just after ip1 expiry
			now = now.Add(2 * epsilon)
			require.True(t, sl.Allow(ip2, now))        // remove the ip1 bucket
			require.Equal(t, 1, sl.ipv6Heaps[0].Len()) // ip2 added in the previous call
		})
	}
}

func TestTokenBucketFullAfter(t *testing.T) {
	tc := []struct {
		*rate.Limiter
		FullAfter time.Duration
	}{
		{Limiter: rate.NewLimiter(1, 10), FullAfter: 10 * time.Second},
		{Limiter: rate.NewLimiter(0.01, 10), FullAfter: 1000 * time.Second},
		{Limiter: rate.NewLimiter(0.01, 1), FullAfter: 100 * time.Second},
	}
	for i, tt := range tc {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			b := tokenBucket{tt.Limiter}
			now := time.Now()
			for range b.Burst() {
				tt.Allow()
			}
			require.GreaterOrEqual(t, tt.FullAfter, b.FullAfter(now))
		})
	}
}
