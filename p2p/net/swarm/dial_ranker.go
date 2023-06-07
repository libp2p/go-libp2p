package swarm

import (
	"sort"
	"strconv"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// The 250ms value is from happy eyeballs RFC 8305. This is a rough estimate of 1 RTT
const (
	// duration by which TCP dials are delayed relative to QUIC dial
	PublicTCPDelay  = 250 * time.Millisecond
	PrivateTCPDelay = 30 * time.Millisecond

	// duration by which QUIC dials are delayed relative to first QUIC dial
	PublicQUICDelay  = 250 * time.Millisecond
	PrivateQUICDelay = 30 * time.Millisecond

	// RelayDelay is the duration by which relay dials are delayed relative to direct addresses
	RelayDelay = 250 * time.Millisecond
)

// NoDelayDialRanker ranks addresses with no delay. This is useful for simultaneous connect requests.
func NoDelayDialRanker(addrs []ma.Multiaddr) []network.AddrDelay {
	return getAddrDelay(addrs, 0, 0, 0)
}

// DefaultDialRanker determines the ranking of outgoing connection attempts.
//
// Addresses are grouped into four distinct groups:
//
//   - private addresses (localhost and local networks (RFC 1918))
//   - public IPv4 addresses
//   - public IPv6 addresses
//   - relay addresses
//
// Within each group, the addresses are ranked according to the ranking logic described below.
// We then dial addresses according to this ranking, with short timeouts applied between dial attempts.
// This ranking logic dramatically reduces the number of simultaneous dial attempts, while introducing
// no additional latency in the vast majority of cases.
//
// The private, public IPv4 and public IPv6 groups are dialed in parallel.
// Dialing relay addresses is delayed by 500 ms, if we have any non-relay alternatives.
//
// In a future iteration, IPv6 will be given a headstart over IPv4, as recommended by Happy Eyeballs RFC 8305.
// This is not enabled yet, since some ISPs are still IPv4-only, and dialing IPv6 addresses will therefore
// always fail.
// The correct solution is to detect this situation, and not attempt to dial IPv6 addresses at all.
// IPv6 blackhole detection is tracked in https://github.com/libp2p/go-libp2p/issues/1605.
//
// Within each group (private, public IPv4, public IPv6, relay addresses) we apply the following
// ranking logic:
//
//  1. If two QUIC addresses are present,  dial the QUIC address with the lowest port first:
//     This is more likely to be the listen port. After this we dial the rest of the QUIC addresses delayed by
//     250ms (PublicQUICDelay) for public addresses, and 30ms (PrivateQUICDelay) for local addresses.
//  2. If a QUIC or WebTransport address is present, TCP addresses dials are delayed relative to the last QUIC dial:
//     We prefer to end up with a QUIC connection. For public addresses, the delay introduced is 250ms (PublicTCPDelay),
//     and for private addresses 30ms (PrivateTCPDelay).
func DefaultDialRanker(addrs []ma.Multiaddr) []network.AddrDelay {
	relay, addrs := filterAddrs(addrs, isRelayAddr)
	pvt, addrs := filterAddrs(addrs, manet.IsPrivateAddr)
	ip4, addrs := filterAddrs(addrs, func(a ma.Multiaddr) bool { return isProtocolAddr(a, ma.P_IP4) })
	ip6, addrs := filterAddrs(addrs, func(a ma.Multiaddr) bool { return isProtocolAddr(a, ma.P_IP6) })

	var relayOffset time.Duration = 0
	if len(ip4) > 0 || len(ip6) > 0 {
		// if there is a public direct address available delay relay dials
		relayOffset = RelayDelay
	}

	res := make([]network.AddrDelay, 0, len(addrs))
	for i := 0; i < len(addrs); i++ {
		res = append(res, network.AddrDelay{Addr: addrs[i], Delay: 0})
	}
	res = append(res, getAddrDelay(pvt, PrivateTCPDelay, PrivateQUICDelay, 0)...)
	res = append(res, getAddrDelay(ip4, PublicTCPDelay, PublicQUICDelay, 0)...)
	res = append(res, getAddrDelay(ip6, PublicTCPDelay, PublicQUICDelay, 0)...)
	res = append(res, getAddrDelay(relay, PublicTCPDelay, PublicQUICDelay, relayOffset)...)
	return res
}

// getAddrDelay ranks a group of addresses(private, ip4, ip6) according to the ranking logic
// explained in defaultDialRanker.
// offset is used to delay all addresses by a fixed duration. This is useful for delaying all relay
// addresses relative to direct addresses
func getAddrDelay(addrs []ma.Multiaddr, tcpDelay time.Duration, quicDelay time.Duration,
	offset time.Duration) []network.AddrDelay {
	sort.Slice(addrs, func(i, j int) bool { return score(addrs[i]) < score(addrs[j]) })

	res := make([]network.AddrDelay, 0, len(addrs))
	quicCount := 0
	for _, a := range addrs {
		delay := offset
		switch {
		case isProtocolAddr(a, ma.P_QUIC) || isProtocolAddr(a, ma.P_QUIC_V1):
			// For QUIC addresses we dial a single address first and then wait for QUICDelay
			// After QUICDelay we dial rest of the QUIC addresses
			if quicCount > 0 {
				delay += quicDelay
			}
			quicCount++
		case isProtocolAddr(a, ma.P_TCP):
			if quicCount >= 2 {
				delay += 2 * quicDelay
			} else if quicCount == 1 {
				delay += tcpDelay
			}
		}
		res = append(res, network.AddrDelay{Addr: a, Delay: delay})
	}
	return res
}

// score scores a multiaddress for dialing delay. lower is better
func score(a ma.Multiaddr) int {
	// the lower 16 bits of the result are the relavant port
	// the higher bits rank the protocol
	// low ports are ranked higher because they're more likely to
	// be listen addresses
	if _, err := a.ValueForProtocol(ma.P_WEBTRANSPORT); err == nil {
		p, _ := a.ValueForProtocol(ma.P_UDP)
		pi, _ := strconv.Atoi(p) // cannot error
		return pi + (1 << 18)
	}
	if _, err := a.ValueForProtocol(ma.P_QUIC); err == nil {
		p, _ := a.ValueForProtocol(ma.P_UDP)
		pi, _ := strconv.Atoi(p) // cannot error
		return pi + (1 << 17)
	}
	if _, err := a.ValueForProtocol(ma.P_QUIC_V1); err == nil {
		p, _ := a.ValueForProtocol(ma.P_UDP)
		pi, _ := strconv.Atoi(p) // cannot error
		return pi
	}

	if p, err := a.ValueForProtocol(ma.P_TCP); err == nil {
		pi, _ := strconv.Atoi(p) // cannot error
		return pi + (1 << 19)
	}
	return (1 << 30)
}

func isProtocolAddr(a ma.Multiaddr, p int) bool {
	found := false
	ma.ForEach(a, func(c ma.Component) bool {
		if c.Protocol().Code == p {
			found = true
			return false
		}
		return true
	})
	return found
}

// filterAddrs filters an address slice in place
func filterAddrs(addrs []ma.Multiaddr, f func(a ma.Multiaddr) bool) (filtered, rest []ma.Multiaddr) {
	j := 0
	for i := 0; i < len(addrs); i++ {
		if f(addrs[i]) {
			addrs[i], addrs[j] = addrs[j], addrs[i]
			j++
		}
	}
	return addrs[:j], addrs[j:]
}
