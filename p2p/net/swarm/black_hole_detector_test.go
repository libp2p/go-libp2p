package swarm

import (
	"fmt"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestBlackHoleFilterReset(t *testing.T) {
	n := 10
	bhf := &blackHoleFilter{n: n, minSuccesses: 2, name: "test"}
	var i = 0
	// calls up to n should be probing
	for i = 1; i <= n; i++ {
		if bhf.HandleRequest() != blackHoleResultProbing {
			t.Fatalf("expected calls up to n to be probes")
		}
		bhf.RecordResult(false)
	}

	// after threshold calls every nth call should be a probe
	for i = n + 1; i < 42; i++ {
		result := bhf.HandleRequest()
		if (i%n == 0 && result != blackHoleResultProbing) || (i%n != 0 && result != blackHoleResultBlocked) {
			t.Fatalf("expected every nth dial to be a probe")
		}
	}

	bhf.RecordResult(true)
	// check if calls up to n are probes again
	for i = 0; i < n; i++ {
		if bhf.HandleRequest() != blackHoleResultProbing {
			t.Fatalf("expected black hole detector state to reset after success")
		}
		bhf.RecordResult(false)
	}

	// next call should be blocked
	if bhf.HandleRequest() != blackHoleResultBlocked {
		t.Fatalf("expected dial to be blocked")
	}
}

func TestBlackHoleFilterSuccessFraction(t *testing.T) {
	n := 10
	tests := []struct {
		minSuccesses, successes int
		result                  blackHoleResult
	}{
		{minSuccesses: 5, successes: 5, result: blackHoleResultAllowed},
		{minSuccesses: 3, successes: 3, result: blackHoleResultAllowed},
		{minSuccesses: 5, successes: 4, result: blackHoleResultBlocked},
		{minSuccesses: 5, successes: 7, result: blackHoleResultAllowed},
		{minSuccesses: 3, successes: 1, result: blackHoleResultBlocked},
		{minSuccesses: 0, successes: 0, result: blackHoleResultAllowed},
		{minSuccesses: 10, successes: 10, result: blackHoleResultAllowed},
	}
	for i, tc := range tests {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			bhf := blackHoleFilter{n: n, minSuccesses: tc.minSuccesses}
			for i := 0; i < tc.successes; i++ {
				bhf.RecordResult(true)
			}
			for i := 0; i < n-tc.successes; i++ {
				bhf.RecordResult(false)
			}
			got := bhf.HandleRequest()
			if got != tc.result {
				t.Fatalf("expected %d got %d", tc.result, got)
			}
		})
	}
}

func TestBlackHoleDetectorInApplicableAddress(t *testing.T) {
	udpConfig := blackHoleConfig{Enabled: true, N: 10, MinSuccesses: 5}
	ipv6Config := blackHoleConfig{Enabled: true, N: 10, MinSuccesses: 5}
	bhd := newBlackHoleDetector(udpConfig, ipv6Config, nil)
	addrs := []ma.Multiaddr{
		ma.StringCast("/ip4/1.2.3.4/tcp/1234"),
		ma.StringCast("/ip4/1.2.3.4/tcp/1233"),
		ma.StringCast("/ip6/::1/udp/1234/quic-v1"),
		ma.StringCast("/ip4/192.168.1.5/udp/1234/quic-v1"),
	}
	for i := 0; i < 1000; i++ {
		filteredAddrs := bhd.FilterAddrs(addrs)
		require.ElementsMatch(t, addrs, filteredAddrs)
		for j := 0; j < len(addrs); j++ {
			bhd.RecordResult(addrs[j], false)
		}
	}
}

func TestBlackHoleDetectorUDPDisabled(t *testing.T) {
	ipv6Config := blackHoleConfig{Enabled: true, N: 10, MinSuccesses: 5}
	bhd := newBlackHoleDetector(blackHoleConfig{Enabled: false}, ipv6Config, nil)
	publicAddr := ma.StringCast("/ip4/1.2.3.4/udp/1234/quic-v1")
	privAddr := ma.StringCast("/ip4/192.168.1.5/udp/1234/quic-v1")
	for i := 0; i < 100; i++ {
		bhd.RecordResult(publicAddr, false)
	}
	addrs := []ma.Multiaddr{publicAddr, privAddr}
	require.ElementsMatch(t, addrs, bhd.FilterAddrs(addrs))
}

func TestBlackHoleDetectorIPv6Disabled(t *testing.T) {
	udpConfig := blackHoleConfig{Enabled: true, N: 10, MinSuccesses: 5}
	bhd := newBlackHoleDetector(udpConfig, blackHoleConfig{Enabled: false}, nil)
	publicAddr := ma.StringCast("/ip6/1::1/tcp/1234")
	privAddr := ma.StringCast("/ip6/::1/tcp/1234")
	addrs := []ma.Multiaddr{publicAddr, privAddr}
	for i := 0; i < 100; i++ {
		bhd.RecordResult(publicAddr, false)
	}
	require.ElementsMatch(t, addrs, bhd.FilterAddrs(addrs))
}

func TestBlackHoleDetectorProbes(t *testing.T) {
	bhd := &blackHoleDetector{
		udp:  &blackHoleFilter{n: 2, minSuccesses: 1, name: "udp"},
		ipv6: &blackHoleFilter{n: 3, minSuccesses: 1, name: "ipv6"},
	}
	udp6Addr := ma.StringCast("/ip6/1::1/udp/1234/quic-v1")
	addrs := []ma.Multiaddr{udp6Addr}
	for i := 0; i < 3; i++ {
		bhd.RecordResult(udp6Addr, false)
	}
	for i := 1; i < 100; i++ {
		filteredAddrs := bhd.FilterAddrs(addrs)
		if i%2 == 0 || i%3 == 0 {
			if len(filteredAddrs) == 0 {
				t.Fatalf("expected probe to be allowed irrespective of the state of other black hole filter")
			}
		} else {
			if len(filteredAddrs) != 0 {
				t.Fatalf("expected dial to be blocked %s", filteredAddrs)
			}
		}
	}

}

func TestBlackHoleDetectorAddrFiltering(t *testing.T) {
	udp6Pub := ma.StringCast("/ip6/1::1/udp/1234/quic-v1")
	udp6Pri := ma.StringCast("/ip6/::1/udp/1234/quic-v1")
	upd4Pub := ma.StringCast("/ip4/1.2.3.4/udp/1234/quic-v1")
	udp4Pri := ma.StringCast("/ip4/192.168.1.5/udp/1234/quic-v1")
	tcp6Pub := ma.StringCast("/ip6/1::1/tcp/1234/quic-v1")
	tcp6Pri := ma.StringCast("/ip6/::1/tcp/1234/quic-v1")
	tcp4Pub := ma.StringCast("/ip4/1.2.3.4/tcp/1234/quic-v1")
	tcp4Pri := ma.StringCast("/ip4/192.168.1.5/tcp/1234/quic-v1")

	makeBHD := func(udpBlocked, ipv6Blocked bool) *blackHoleDetector {
		bhd := &blackHoleDetector{
			udp:  &blackHoleFilter{n: 100, minSuccesses: 10, name: "udp"},
			ipv6: &blackHoleFilter{n: 100, minSuccesses: 10, name: "ipv6"},
		}
		for i := 0; i < 100; i++ {
			bhd.RecordResult(upd4Pub, !udpBlocked)
		}
		for i := 0; i < 100; i++ {
			bhd.RecordResult(tcp6Pub, !ipv6Blocked)
		}
		return bhd
	}

	allInput := []ma.Multiaddr{udp6Pub, udp6Pri, upd4Pub, udp4Pri, tcp6Pub, tcp6Pri,
		tcp4Pub, tcp4Pri}

	udpBlockedOutput := []ma.Multiaddr{udp6Pri, udp4Pri, tcp6Pub, tcp6Pri, tcp4Pub, tcp4Pri}
	bhd := makeBHD(true, false)
	require.ElementsMatch(t, udpBlockedOutput, bhd.FilterAddrs(allInput))

	ip6BlockedOutput := []ma.Multiaddr{udp6Pri, upd4Pub, udp4Pri, tcp6Pri, tcp4Pub, tcp4Pri}
	bhd = makeBHD(false, true)
	require.ElementsMatch(t, ip6BlockedOutput, bhd.FilterAddrs(allInput))

	bothBlockedOutput := []ma.Multiaddr{udp6Pri, udp4Pri, tcp6Pri, tcp4Pub, tcp4Pri}
	bhd = makeBHD(true, true)
	require.ElementsMatch(t, bothBlockedOutput, bhd.FilterAddrs(allInput))
}
