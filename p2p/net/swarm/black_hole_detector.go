package swarm

import (
	"fmt"
	"sync"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

type BlackHoleState int

const (
	blackHoleStateProbing BlackHoleState = iota
	blackHoleStateAllowed
	blackHoleStateBlocked
)

func (st BlackHoleState) String() string {
	switch st {
	case blackHoleStateProbing:
		return "Probing"
	case blackHoleStateAllowed:
		return "Allowed"
	case blackHoleStateBlocked:
		return "Blocked"
	default:
		return fmt.Sprintf("Unknown %d", st)
	}
}

// BlackHoleSuccessCounter provides black hole filtering for dials. This filter should be used in concert
// with a UDP or IPv6 address filter to detect UDP or IPv6 black hole. In a black holed environment,
// dial requests are refused Requests are blocked if the number of successes in the last N dials is
// less than MinSuccesses.
// If a request succeeds in Blocked state, the filter state is reset and N subsequent requests are
// allowed before reevaluating black hole state. Dials cancelled when some other concurrent dial
// succeeded are counted as failures. A sufficiently large N prevents false negatives in such cases.
type BlackHoleSuccessCounter struct {
	// N is
	// 1. The minimum number of completed dials required before evaluating black hole state
	// 2. the minimum number of requests after which we probe the state of the black hole in
	// blocked state
	N int
	// MinSuccesses is the minimum number of Success required in the last n dials
	// to consider we are not blocked.
	MinSuccesses int
	// Name for the detector.
	Name string

	mu sync.Mutex
	// requests counts number of dial requests to peers. We handle request at a peer
	// level and record results at individual address dial level.
	requests int
	// dialResults of the last `n` dials. A successful dial is true.
	dialResults []bool
	// successes is the count of successful dials in outcomes
	successes int
	// state is the current state of the detector
	state BlackHoleState
}

// RecordResult records the outcome of a dial. A successful dial in Blocked state will change the
// state of the filter to Probing. A failed dial only blocks subsequent requests if the success
// fraction over the last n outcomes is less than the minSuccessFraction of the filter.
func (b *BlackHoleSuccessCounter) RecordResult(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == blackHoleStateBlocked && success {
		// If the call succeeds in a blocked state we reset to allowed.
		// This is better than slowly accumulating values till we cross the minSuccessFraction
		// threshold since a black hole is a binary property.
		b.reset()
		return
	}

	if success {
		b.successes++
	}
	b.dialResults = append(b.dialResults, success)

	if len(b.dialResults) > b.N {
		if b.dialResults[0] {
			b.successes--
		}
		b.dialResults = b.dialResults[1:]
	}

	b.updateState()
}

// HandleRequest returns the result of applying the black hole filter for the request.
func (b *BlackHoleSuccessCounter) HandleRequest() BlackHoleState {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.requests++

	if b.state == blackHoleStateAllowed {
		return blackHoleStateAllowed
	} else if b.state == blackHoleStateProbing || b.requests%b.N == 0 {
		return blackHoleStateProbing
	} else {
		return blackHoleStateBlocked
	}
}

func (b *BlackHoleSuccessCounter) reset() {
	b.successes = 0
	b.dialResults = b.dialResults[:0]
	b.requests = 0
	b.updateState()
}

func (b *BlackHoleSuccessCounter) updateState() {
	st := b.state

	if len(b.dialResults) < b.N {
		b.state = blackHoleStateProbing
	} else if b.successes >= b.MinSuccesses {
		b.state = blackHoleStateAllowed
	} else {
		b.state = blackHoleStateBlocked
	}

	if st != b.state {
		log.Debugf("%s blackHoleDetector state changed from %s to %s", b.Name, st, b.state)
	}
}

func (b *BlackHoleSuccessCounter) State() BlackHoleState {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.state
}

type blackHoleInfo struct {
	name            string
	state           BlackHoleState
	nextProbeAfter  int
	successFraction float64
}

func (b *BlackHoleSuccessCounter) info() blackHoleInfo {
	b.mu.Lock()
	defer b.mu.Unlock()

	nextProbeAfter := 0
	if b.state == blackHoleStateBlocked {
		nextProbeAfter = b.N - (b.requests % b.N)
	}

	successFraction := 0.0
	if len(b.dialResults) > 0 {
		successFraction = float64(b.successes) / float64(len(b.dialResults))
	}

	return blackHoleInfo{
		name:            b.Name,
		state:           b.state,
		nextProbeAfter:  nextProbeAfter,
		successFraction: successFraction,
	}
}

// blackHoleDetector provides UDP and IPv6 black hole detection using a `BlackHoleSuccessCounter` for each.
// For details of the black hole detection logic see `BlackHoleSuccessCounter`.
// In Read Only mode, detector doesn't update the state of underlying filters and refuses requests
// when black hole state is unknown. This is useful for Swarms made specifically for services like
// AutoNAT where we care about accurately reporting the reachability of a peer.
//
// Black hole filtering is done at a peer dial level to ensure that periodic probes to detect change
// of the black hole state are actually dialed and are not skipped because of dial prioritisation
// logic.
type blackHoleDetector struct {
	udp, ipv6 *BlackHoleSuccessCounter
	mt        MetricsTracer
	readOnly  bool
}

// FilterAddrs filters the peer's addresses removing black holed addresses
func (d *blackHoleDetector) FilterAddrs(addrs []ma.Multiaddr) (valid []ma.Multiaddr, blackHoled []ma.Multiaddr) {
	hasUDP, hasIPv6 := false, false
	for _, a := range addrs {
		if !manet.IsPublicAddr(a) {
			continue
		}
		if isProtocolAddr(a, ma.P_UDP) {
			hasUDP = true
		}
		if isProtocolAddr(a, ma.P_IP6) {
			hasIPv6 = true
		}
	}

	udpRes := blackHoleStateAllowed
	if d.udp != nil && hasUDP {
		udpRes = d.getFilterState(d.udp)
		d.trackMetrics(d.udp)
	}

	ipv6Res := blackHoleStateAllowed
	if d.ipv6 != nil && hasIPv6 {
		ipv6Res = d.getFilterState(d.ipv6)
		d.trackMetrics(d.ipv6)
	}

	blackHoled = make([]ma.Multiaddr, 0, len(addrs))
	return ma.FilterAddrs(
		addrs,
		func(a ma.Multiaddr) bool {
			if !manet.IsPublicAddr(a) {
				return true
			}
			// allow all UDP addresses while probing irrespective of IPv6 black hole state
			if udpRes == blackHoleStateProbing && isProtocolAddr(a, ma.P_UDP) {
				return true
			}
			// allow all IPv6 addresses while probing irrespective of UDP black hole state
			if ipv6Res == blackHoleStateProbing && isProtocolAddr(a, ma.P_IP6) {
				return true
			}

			if udpRes == blackHoleStateBlocked && isProtocolAddr(a, ma.P_UDP) {
				blackHoled = append(blackHoled, a)
				return false
			}
			if ipv6Res == blackHoleStateBlocked && isProtocolAddr(a, ma.P_IP6) {
				blackHoled = append(blackHoled, a)
				return false
			}
			return true
		},
	), blackHoled
}

// RecordResult updates the state of the relevant BlackHoleSuccessCounters for addr
func (d *blackHoleDetector) RecordResult(addr ma.Multiaddr, success bool) {
	if d.readOnly || !manet.IsPublicAddr(addr) {
		return
	}
	if d.udp != nil && isProtocolAddr(addr, ma.P_UDP) {
		d.udp.RecordResult(success)
		d.trackMetrics(d.udp)
	}
	if d.ipv6 != nil && isProtocolAddr(addr, ma.P_IP6) {
		d.ipv6.RecordResult(success)
		d.trackMetrics(d.ipv6)
	}
}

func (d *blackHoleDetector) getFilterState(f *BlackHoleSuccessCounter) BlackHoleState {
	if d.readOnly {
		if f.State() != blackHoleStateAllowed {
			return blackHoleStateBlocked
		}
		return blackHoleStateAllowed
	}
	return f.HandleRequest()
}

func (d *blackHoleDetector) trackMetrics(f *BlackHoleSuccessCounter) {
	if d.readOnly || d.mt == nil {
		return
	}
	// Track metrics only in non readOnly state
	info := f.info()
	d.mt.UpdatedBlackHoleSuccessCounter(info.name, info.state, info.nextProbeAfter, info.successFraction)
}
