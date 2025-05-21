package rate

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

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

func assertLimiter(t *testing.T, rl *Limiter, ipAddr netip.Addr, allowed, errorMargin int) {
	t.Helper()
	for i := 0; i < allowed; i++ {
		require.True(t, rl.Allow(ipAddr))
	}
	for i := 0; i < errorMargin; i++ {
		rl.Allow(ipAddr)
	}
	require.False(t, rl.Allow(ipAddr))
}

func TestLimiterGlobal(t *testing.T) {
	addr := netip.MustParseAddr("127.0.0.1")
	limits := []Limit{
		{RPS: 0.0, Burst: 1},
		{RPS: 0.8, Burst: 1},
		{RPS: 10, Burst: 20},
		{RPS: 100, Burst: 200},
		{RPS: 1000, Burst: 2000},
	}
	for _, limit := range limits {
		t.Run(fmt.Sprintf("limit %0.1f", limit.RPS), func(t *testing.T) {
			rl := &Limiter{
				GlobalLimit: limit,
			}
			if limit.RPS == 0 {
				// 0 implies no rate limiting, any large number would do
				for i := 0; i < 1000; i++ {
					require.True(t, rl.Allow(addr))
				}
				return
			}
			assertLimiter(t, rl, addr, limit.Burst, int(limit.RPS*rateLimitErrorTolerance))
			sleepDuration, requestCount := getSleepDurationAndRequestCount(limit.RPS)
			time.Sleep(sleepDuration)
			assertLimiter(t, rl, addr, requestCount, int(float64(requestCount)*rateLimitErrorTolerance))
		})
	}
}

func TestLimiterNetworkPrefix(t *testing.T) {
	local := netip.MustParseAddr("127.0.0.1")
	public := netip.MustParseAddr("1.1.1.1")
	rl := &Limiter{
		NetworkPrefixLimits: []PrefixLimit{
			{Prefix: netip.MustParsePrefix("127.0.0.0/24"), Limit: Limit{}},
		},
		GlobalLimit: Limit{RPS: 10, Burst: 10},
	}
	// element within prefix is allowed even over the limit
	for range rl.GlobalLimit.Burst + 100 {
		require.True(t, rl.Allow(local))
	}
	// rate limit public ips
	assertLimiter(t, rl, public, rl.GlobalLimit.Burst, int(rl.GlobalLimit.RPS*rateLimitErrorTolerance))

	// public ip rejected
	require.False(t, rl.Allow(public))
	// local ip accepted
	for range 100 {
		require.True(t, rl.Allow(local))
	}
}

func TestLimiterNetworkPrefixWidth(t *testing.T) {
	a1 := netip.MustParseAddr("1.1.1.1")
	a2 := netip.MustParseAddr("1.1.0.1")

	wideLimit := 20
	narrowLimit := 10
	rl := &Limiter{
		NetworkPrefixLimits: []PrefixLimit{
			{Prefix: netip.MustParsePrefix("1.1.0.0/16"), Limit: Limit{RPS: 0.01, Burst: wideLimit}},
			{Prefix: netip.MustParsePrefix("1.1.1.0/24"), Limit: Limit{RPS: 0.01, Burst: narrowLimit}},
		},
	}
	for range 2 * wideLimit {
		rl.Allow(a1)
	}
	// a1 rejected
	require.False(t, rl.Allow(a1))
	// a2 accepted
	for range wideLimit - narrowLimit {
		require.True(t, rl.Allow(a2))
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

func TestSubnetLimiter(t *testing.T) {
	assertOutput := func(outcome bool, rl *SubnetLimiter, subnetAddrs func() netip.Addr, n int) {
		t.Helper()
		for range n {
			require.Equal(t, outcome, rl.Allow(subnetAddrs(), time.Now()), "%d", n)
		}
	}

	t.Run("Simple", func(*testing.T) {
		// Keep the refil rate low
		v4Small := SubnetLimit{PrefixLength: 24, Limit: Limit{RPS: 0.0001, Burst: 10}}
		v4Large := SubnetLimit{PrefixLength: 16, Limit: Limit{RPS: 0.0001, Burst: 19}}

		v6Small := SubnetLimit{PrefixLength: 64, Limit: Limit{RPS: 0.0001, Burst: 10}}
		v6Large := SubnetLimit{PrefixLength: 48, Limit: Limit{RPS: 0.0001, Burst: 17}}
		rl := &SubnetLimiter{
			IPv4SubnetLimits: []SubnetLimit{v4Large, v4Small},
			IPv6SubnetLimits: []SubnetLimit{v6Large, v6Small},
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

	t.Run("Complex", func(*testing.T) {
		limits := []SubnetLimit{
			{PrefixLength: 32, Limit: Limit{RPS: 0.01, Burst: 10}},
			{PrefixLength: 24, Limit: Limit{RPS: 0.01, Burst: 20}},
			{PrefixLength: 16, Limit: Limit{RPS: 0.01, Burst: 30}},
			{PrefixLength: 8, Limit: Limit{RPS: 0.01, Burst: 40}},
		}
		rl := &SubnetLimiter{
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
		sl := SubnetLimiter{}
		for range 10000 {
			require.True(t, sl.Allow(netip.IPv6Loopback(), time.Now()))
		}
	})
}

func TestSubnetLimiterCleanup(t *testing.T) {
	tc := []struct {
		Limit
		TTL time.Duration
	}{
		{Limit: Limit{RPS: 1, Burst: 10}, TTL: 10 * time.Second},
		{Limit: Limit{RPS: 0.1, Burst: 2}, TTL: 20 * time.Second},
		{Limit: Limit{RPS: 1, Burst: 100}, TTL: 100 * time.Second},
		{Limit: Limit{RPS: 3, Burst: 6}, TTL: 2 * time.Second},
	}
	for i, tt := range tc {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			ip1, ip2 := netip.IPv6Loopback(), netip.MustParseAddr("2001::")
			sl := SubnetLimiter{IPv6SubnetLimits: []SubnetLimit{{PrefixLength: 64, Limit: tt.Limit}}}
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
			epsilon := 10 * time.Millisecond
			require.GreaterOrEqual(t, tt.FullAfter+epsilon, b.FullAt(now).Sub(now))
			require.LessOrEqual(t, tt.FullAfter-epsilon, b.FullAt(now).Sub(now))
		})
	}
}
