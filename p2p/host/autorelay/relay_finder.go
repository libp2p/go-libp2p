package autorelay

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/benbjohnson/clock"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	basic "github.com/libp2p/go-libp2p/p2p/host/basic"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	circuitv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	circuitv2_proto "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const protoIDv2 = circuitv2_proto.ProtoIDv2Hop

// Terminology:
// Candidate: Once we connect to a node and it supports relay protocol,
// we call it a candidate, and consider using it as a relay.
// Relay: Out of the list of candidates, we select a relay to connect to.
// Currently, we just randomly select a candidate, but we can employ more sophisticated
// selection strategies here (e.g. by facotring in the RTT).

const (
	rsvpRefreshInterval = time.Minute
	rsvpExpirationSlack = 2 * time.Minute

	autorelayTag = "autorelay"
)

type candidate struct {
	added           time.Time
	supportsRelayV2 bool
	ai              peer.AddrInfo
}

// relayFinder is a Host that uses relays for connectivity when a NAT is detected.
type relayFinder struct {
	bootTime time.Time
	host     *basic.BasicHost

	conf *config

	refCount sync.WaitGroup

	ctxCancel   context.CancelFunc
	ctxCancelMx sync.Mutex

	peerSource PeerSource

	candidateFound             chan struct{} // receives every time we find a new relay candidate
	candidateMx                sync.Mutex
	candidates                 map[peer.ID]*candidate
	backoff                    map[peer.ID]time.Time
	maybeConnectToRelayTrigger chan struct{} // cap: 1
	// Any time _something_ hapens that might cause us to need new candidates.
	// This could be
	// * the disconnection of a relay
	// * the failed attempt to obtain a reservation with a current candidate
	// * a candidate is deleted due to its age
	maybeRequestNewCandidates chan struct{} // cap: 1.

	relayUpdated chan struct{}

	relayMx sync.Mutex
	relays  map[peer.ID]*circuitv2.Reservation

	cachedAddrs       []ma.Multiaddr
	cachedAddrsExpiry time.Time

	// A channel that may trigger a run of scheduled background tasks. Needed to
	// avoid race conditions with the background task loop and the mock clock.
	// For example:
	// We may `.Add` some time to the mock clock which triggers the workTimer. The results in work in two goroutines
	// Goroutine A (inside the mock clock):
	//   - Go through each each registered timer and `Tick` anything that happened before out next end time. `Tick` simply puts a value on the timer's channel.
	//   - If there are no more registered timers, set the final end time and return.
	// Goroutine B (our background task loop):
	//   - Wait for a value from the workTimer channel.
	//   - Run scheduled tasks, then reset the workTimer to the next scheduled run.
	//   - If the above happens after the last step in Goroutine A, then we reset the timer to end time + duration rather than "now" + duration.
	maybeRunBackgroundTasks chan struct{}
}

func newRelayFinder(host *basic.BasicHost, peerSource PeerSource, conf *config) *relayFinder {
	if peerSource == nil {
		panic("Can not create a new relayFinder. Need a Peer Source fn or a list of static relays. Refer to the documentation around `libp2p.EnableAutoRelay`")
	}

	return &relayFinder{
		bootTime:                   conf.clock.Now(),
		host:                       host,
		conf:                       conf,
		peerSource:                 peerSource,
		candidates:                 make(map[peer.ID]*candidate),
		backoff:                    make(map[peer.ID]time.Time),
		candidateFound:             make(chan struct{}, 1),
		maybeConnectToRelayTrigger: make(chan struct{}, 1),
		maybeRequestNewCandidates:  make(chan struct{}, 1),
		maybeRunBackgroundTasks:    make(chan struct{}, 1),
		relays:                     make(map[peer.ID]*circuitv2.Reservation),
		relayUpdated:               make(chan struct{}, 1),
	}
}

type scheduledWorkTimes struct {
	leastFrequentInterval       time.Duration
	nextRefresh                 time.Time
	nextBackoff                 time.Time
	nextOldCandidateCheck       time.Time
	nextAllowedCallToPeerSource time.Time
}

func (rf *relayFinder) background(ctx context.Context) {
	peerSourceRateLimiter := make(chan struct{}, 1)
	rf.refCount.Add(1)
	go func() {
		defer rf.refCount.Done()
		rf.findNodes(ctx, peerSourceRateLimiter)
	}()

	rf.refCount.Add(1)
	go func() {
		defer rf.refCount.Done()
		rf.handleNewCandidates(ctx)
	}()

	// Workaround for mock clock so we don't miss scheduled background work. See
	// the comment around maybeRunBackgroundTasks.
	if _, ok := rf.conf.clock.(*clock.Mock); ok {
		go func() {
			ticker := time.NewTicker(time.Second)
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					select {
					case rf.maybeRunBackgroundTasks <- struct{}{}:
					default:
					}
				}
			}
		}()
	}

	subConnectedness, err := rf.host.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged), eventbus.Name("autorelay (relay finder)"))
	if err != nil {
		log.Error("failed to subscribe to the EvtPeerConnectednessChanged")
		return
	}
	defer subConnectedness.Close()

	bootDelayTimer := rf.conf.clock.Timer(rf.conf.bootDelay)
	defer bootDelayTimer.Stop()

	// This is the least frequent event. It's our fallback timer if we don't have any other work to do.
	leastFrequentInterval := rf.conf.minInterval
	if rf.conf.backoff > leastFrequentInterval {
		leastFrequentInterval = rf.conf.backoff
	}
	if rf.conf.maxCandidateAge > leastFrequentInterval {
		leastFrequentInterval = rf.conf.maxCandidateAge
	}
	if rsvpRefreshInterval > leastFrequentInterval {
		leastFrequentInterval = rf.conf.maxCandidateAge
	}

	now := rf.conf.clock.Now()

	scheduledWork := &scheduledWorkTimes{
		leastFrequentInterval:       leastFrequentInterval,
		nextRefresh:                 now.Add(rsvpRefreshInterval),
		nextBackoff:                 now.Add(rf.conf.backoff / 5),
		nextOldCandidateCheck:       now.Add(rf.conf.maxCandidateAge / 5),
		nextAllowedCallToPeerSource: now.Add(-time.Second), // allow immediately
	}

	workTimer := rf.conf.clock.Timer(rf.runScheduledWork(ctx, now, scheduledWork, peerSourceRateLimiter).Sub(now))
	defer workTimer.Stop()

	for {
		select {
		case ev, ok := <-subConnectedness.Out():
			if !ok {
				return
			}
			evt := ev.(event.EvtPeerConnectednessChanged)
			if evt.Connectedness != network.NotConnected {
				continue
			}
			push := false

			rf.relayMx.Lock()
			if rf.usingRelay(evt.Peer) { // we were disconnected from a relay
				log.Debugw("disconnected from relay", "id", evt.Peer)
				delete(rf.relays, evt.Peer)
				rf.notifyMaybeConnectToRelay()
				rf.notifyMaybeNeedNewCandidates()
				push = true
			}
			rf.relayMx.Unlock()

			if push {
				rf.clearCachedAddrsAndSignalAddressChange()
			}
		case <-rf.candidateFound:
			rf.notifyMaybeConnectToRelay()
		case <-bootDelayTimer.C:
			rf.notifyMaybeConnectToRelay()
		case <-rf.relayUpdated:
			rf.clearCachedAddrsAndSignalAddressChange()
		case <-workTimer.C:
			now := rf.conf.clock.Now()
			nextTime := rf.runScheduledWork(ctx, now, scheduledWork, peerSourceRateLimiter)
			workTimer.Reset(nextTime.Sub(now))
		case <-rf.maybeRunBackgroundTasks:
			// Ignore the next time because we aren't scheduling any future work here
			_ = rf.runScheduledWork(ctx, rf.conf.clock.Now(), scheduledWork, peerSourceRateLimiter)
		case <-ctx.Done():
			return
		}
	}
}

func (rf *relayFinder) clearCachedAddrsAndSignalAddressChange() {
	rf.relayMx.Lock()
	rf.cachedAddrs = nil
	rf.relayMx.Unlock()
	rf.host.SignalAddressChange()
}

func (rf *relayFinder) runScheduledWork(ctx context.Context, now time.Time, scheduledWork *scheduledWorkTimes, peerSourceRateLimiter chan<- struct{}) time.Time {
	log.Debugw("running scheduled work", "now", now)
	nextTime := now.Add(scheduledWork.leastFrequentInterval)

	if now.After(scheduledWork.nextRefresh) {
		scheduledWork.nextRefresh = now.Add(rsvpRefreshInterval)
		if rf.refreshReservations(ctx, now) {
			rf.clearCachedAddrsAndSignalAddressChange()
		}
	}

	if now.After(scheduledWork.nextBackoff) {
		scheduledWork.nextBackoff = rf.clearBackoff(now)
	}

	if now.After(scheduledWork.nextOldCandidateCheck) {
		scheduledWork.nextOldCandidateCheck = rf.clearOldCandidates(now)
	}

	if now.After(scheduledWork.nextAllowedCallToPeerSource) {
		scheduledWork.nextAllowedCallToPeerSource = scheduledWork.nextAllowedCallToPeerSource.Add(rf.conf.minInterval)
		select {
		case peerSourceRateLimiter <- struct{}{}:
		default:
		}
	}

	// Find the next time we need to run scheduled work.
	if scheduledWork.nextRefresh.Before(nextTime) {
		nextTime = scheduledWork.nextRefresh
	}
	if scheduledWork.nextBackoff.Before(nextTime) {
		nextTime = scheduledWork.nextBackoff
	}
	if scheduledWork.nextOldCandidateCheck.Before(nextTime) {
		nextTime = scheduledWork.nextOldCandidateCheck
	}
	if scheduledWork.nextAllowedCallToPeerSource.Before(nextTime) {
		nextTime = scheduledWork.nextAllowedCallToPeerSource
	}
	if nextTime == now {
		// Only happens in CI with a mock clock
		nextTime = nextTime.Add(1) // avoids an infinite loop
	}

	return nextTime
}

// clearOldCandidates clears old candidates from the map. Returns the next time
// to run this function.
func (rf *relayFinder) clearOldCandidates(now time.Time) time.Time {
	// If we don't have any candidates, we should run this again in rf.conf.maxCandidateAge.
	nextTime := now.Add(rf.conf.maxCandidateAge)

	var deleted bool
	rf.candidateMx.Lock()
	defer rf.candidateMx.Unlock()
	for id, cand := range rf.candidates {
		expiry := cand.added.Add(rf.conf.maxCandidateAge)
		if expiry.After(now) {
			if expiry.Before(nextTime) {
				nextTime = expiry
			}
		} else {
			deleted = true
			log.Debugw("deleting candidate due to age", "id", id, "now", now, "added", cand.added, "expiry", expiry)
			delete(rf.candidates, id)

		}
	}
	if deleted {
		rf.notifyMaybeNeedNewCandidates()
	}

	return nextTime
}

// clearBackoff clears old backoff entries from the map. Returns the next time
// to run this function.
func (rf *relayFinder) clearBackoff(now time.Time) time.Time {
	nextTime := now.Add(rf.conf.backoff)

	rf.candidateMx.Lock()
	defer rf.candidateMx.Unlock()
	for id, t := range rf.backoff {
		expiry := t.Add(rf.conf.backoff)
		if expiry.After(now) {
			if expiry.Before(nextTime) {
				nextTime = expiry
			}
		} else {
			log.Debugw("removing backoff for node", "id", id)
			delete(rf.backoff, id)
		}
	}

	return nextTime
}

// findNodes accepts nodes from the channel and tests if they support relaying.
// It is run on both public and private nodes.
// It garbage collects old entries, so that nodes doesn't overflow.
// This makes sure that as soon as we need to find relay candidates, we have them available.
// peerSourceRateLimiter is used to limit how often we call the peer source.
func (rf *relayFinder) findNodes(ctx context.Context, peerSourceRateLimiter <-chan struct{}) {
	var peerChan <-chan peer.AddrInfo
	var wg sync.WaitGroup
	for {
		rf.candidateMx.Lock()
		numCandidates := len(rf.candidates)
		rf.candidateMx.Unlock()

		if peerChan == nil && numCandidates < rf.conf.minCandidates {
			select {
			case <-peerSourceRateLimiter:
				peerChan = rf.peerSource(ctx, rf.conf.maxCandidates)
			case <-ctx.Done():
				return
			}
		}

		select {
		case <-rf.maybeRequestNewCandidates:
			continue
		case pi, ok := <-peerChan:
			log.Debugw("got node from peer source", "id", pi.ID, "ok", ok, "now", rf.conf.clock.Now())
			if !ok {
				wg.Wait()
				peerChan = nil
				continue
			}
			log.Debugw("found node", "id", pi.ID)
			rf.candidateMx.Lock()
			numCandidates := len(rf.candidates)
			backoffStart, isOnBackoff := rf.backoff[pi.ID]
			rf.candidateMx.Unlock()
			if isOnBackoff {
				log.Debugw("skipping node that we recently failed to obtain a reservation with", "id", pi.ID, "last attempt", rf.conf.clock.Since(backoffStart))
				continue
			}
			if numCandidates >= rf.conf.maxCandidates {
				log.Debugw("skipping node. Already have enough candidates", "id", pi.ID, "num", numCandidates, "max", rf.conf.maxCandidates)
				continue
			}
			rf.refCount.Add(1)
			wg.Add(1)
			go func() {
				defer rf.refCount.Done()
				defer wg.Done()
				if added := rf.handleNewNode(ctx, pi); added {
					rf.notifyNewCandidate()
				}
			}()
		case <-ctx.Done():
			return
		}
	}
}

func (rf *relayFinder) notifyMaybeConnectToRelay() {
	select {
	case rf.maybeConnectToRelayTrigger <- struct{}{}:
	default:
	}
}

func (rf *relayFinder) notifyMaybeNeedNewCandidates() {
	select {
	case rf.maybeRequestNewCandidates <- struct{}{}:
	default:
	}
}

func (rf *relayFinder) notifyNewCandidate() {
	select {
	case rf.candidateFound <- struct{}{}:
	default:
	}
}

// handleNewNode tests if a peer supports circuit v2.
// This method is only run on private nodes.
// If a peer does, it is added to the candidates map.
// Note that just supporting the protocol doesn't guarantee that we can also obtain a reservation.
func (rf *relayFinder) handleNewNode(ctx context.Context, pi peer.AddrInfo) (added bool) {
	log.Debugw("handleNewNode", "id", pi.ID, "now", rf.conf.clock.Now())
	rf.relayMx.Lock()
	relayInUse := rf.usingRelay(pi.ID)
	rf.relayMx.Unlock()
	if relayInUse {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	supportsV2, err := rf.tryNode(ctx, pi)
	if err != nil {
		log.Debugf("node %s not accepted as a candidate: %s", pi.ID, err)
		return false
	}
	rf.candidateMx.Lock()
	if len(rf.candidates) > rf.conf.maxCandidates {
		rf.candidateMx.Unlock()
		return false
	}
	log.Debugw("node supports relay protocol", "peer", pi.ID, "supports circuit v2", supportsV2, "now", rf.conf.clock.Now())
	rf.candidates[pi.ID] = &candidate{
		added:           rf.conf.clock.Now(),
		ai:              pi,
		supportsRelayV2: supportsV2,
	}
	rf.candidateMx.Unlock()
	return true
}

// tryNode checks if a peer actually supports either circuit v2.
// It does not modify any internal state.
func (rf *relayFinder) tryNode(ctx context.Context, pi peer.AddrInfo) (supportsRelayV2 bool, err error) {
	if err := rf.host.Connect(ctx, pi); err != nil {
		return false, fmt.Errorf("error connecting to relay %s: %w", pi.ID, err)
	}

	conns := rf.host.Network().ConnsToPeer(pi.ID)
	for _, conn := range conns {
		if isRelayAddr(conn.RemoteMultiaddr()) {
			return false, errors.New("not a public node")
		}
	}

	// wait for identify to complete in at least one conn so that we can check the supported protocols
	ready := make(chan struct{}, 1)
	for _, conn := range conns {
		go func(conn network.Conn) {
			select {
			case <-rf.host.IDService().IdentifyWait(conn):
				select {
				case ready <- struct{}{}:
				default:
				}
			case <-ctx.Done():
			}
		}(conn)
	}

	select {
	case <-ready:
	case <-ctx.Done():
		return false, ctx.Err()
	}

	protos, err := rf.host.Peerstore().SupportsProtocols(pi.ID, protoIDv2)
	if err != nil {
		return false, fmt.Errorf("error checking relay protocol support for peer %s: %w", pi.ID, err)
	}
	if len(protos) == 0 {
		return false, errors.New("doesn't speak circuit v2")
	}
	return true, nil
}

// When a new node that could be a relay is found, we receive a notification on the maybeConnectToRelayTrigger chan.
// This function makes sure that we only run one instance of maybeConnectToRelay at once, and buffers
// exactly one more trigger event to run maybeConnectToRelay.
func (rf *relayFinder) handleNewCandidates(ctx context.Context) {
	sem := make(chan struct{}, 1)
	for {
		select {
		case <-ctx.Done():
			return
		case <-rf.maybeConnectToRelayTrigger:
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			rf.maybeConnectToRelay(ctx)
			<-sem
		}
	}
}

func (rf *relayFinder) maybeConnectToRelay(ctx context.Context) {
	log.Debug("maybeConnectToRelay")
	rf.relayMx.Lock()
	numRelays := len(rf.relays)
	rf.relayMx.Unlock()
	// We're already connected to our desired number of relays. Nothing to do here.
	if numRelays == rf.conf.desiredRelays {
		log.Debug("We got the numRelays we want")
		return
	}
	log.Debug("Need to connect to relay")

	rf.candidateMx.Lock()
	log.Debug("maybeConnectToRelay: got candidate lock")
	if len(rf.relays) == 0 && len(rf.candidates) < rf.conf.minCandidates && rf.conf.clock.Since(rf.bootTime) < rf.conf.bootDelay {
		log.Debug("In startup phase, not connecting to relay yet")
		// During the startup phase, we don't want to connect to the first candidate that we find.
		// Instead, we wait until we've found at least minCandidates, and then select the best of those.
		// However, if that takes too long (longer than bootDelay), we still go ahead.
		rf.candidateMx.Unlock()
		return
	}
	if len(rf.candidates) == 0 {
		log.Debug("maybeConnectToRelay: no candidates")
		rf.candidateMx.Unlock()
		return
	}
	candidates := rf.selectCandidates()
	log.Debug("maybeConnectToRelay: selected candidates")
	rf.candidateMx.Unlock()

	// We now iterate over the candidates, attempting (sequentially) to get reservations with them, until
	// we reach the desired number of relays.
	for _, cand := range candidates {
		id := cand.ai.ID
		rf.relayMx.Lock()
		usingRelay := rf.usingRelay(id)
		rf.relayMx.Unlock()
		if usingRelay {
			log.Debugw("maybeConnectToRelay: already using relay", "id", id)
			rf.candidateMx.Lock()
			delete(rf.candidates, id)
			rf.candidateMx.Unlock()
			rf.notifyMaybeNeedNewCandidates()
			continue
		}
		log.Debug("Making rsvp to relay", "id", id)
		rsvp, err := rf.connectToRelay(ctx, cand)
		if err != nil {
			log.Debugw("failed to connect to relay", "peer", id, "error", err)
			rf.notifyMaybeNeedNewCandidates()
			continue
		}
		log.Debugw("adding new relay", "id", id)
		rf.relayMx.Lock()
		rf.relays[id] = rsvp
		numRelays := len(rf.relays)
		rf.relayMx.Unlock()
		rf.notifyMaybeNeedNewCandidates()

		rf.host.ConnManager().Protect(id, autorelayTag) // protect the connection

		select {
		case rf.relayUpdated <- struct{}{}:
		default:
		}

		if numRelays >= rf.conf.desiredRelays {
			break
		}
	}
}

func (rf *relayFinder) connectToRelay(ctx context.Context, cand *candidate) (*circuitv2.Reservation, error) {
	id := cand.ai.ID

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var rsvp *circuitv2.Reservation

	// make sure we're still connected.
	if rf.host.Network().Connectedness(id) != network.Connected {
		log.Debugw("We aren't connected to the relay. Connecting...", "id", id)
		if err := rf.host.Connect(ctx, cand.ai); err != nil {
			log.Debugw("failed to connect to the relay.", "id", id, "err", err)
			rf.candidateMx.Lock()
			delete(rf.candidates, cand.ai.ID)
			rf.candidateMx.Unlock()
			return nil, fmt.Errorf("failed to connect: %w", err)
		}
	}

	log.Debugw("Connected to relay", "id", id)

	rf.candidateMx.Lock()
	rf.backoff[id] = rf.conf.clock.Now()
	rf.candidateMx.Unlock()
	var err error
	if cand.supportsRelayV2 {
		log.Debugw("reserving relay", "id", id)
		rsvp, err = circuitv2.Reserve(ctx, rf.host, cand.ai)
		if err != nil {
			log.Debugw("failed reserved relay", "id", id, "err", err)
			err = fmt.Errorf("failed to reserve slot: %w", err)
		}
		log.Debugw("reserved relay", "id", id)
	}
	rf.candidateMx.Lock()
	delete(rf.candidates, id)
	rf.candidateMx.Unlock()
	return rsvp, err
}

func (rf *relayFinder) refreshReservations(ctx context.Context, now time.Time) bool {
	rf.relayMx.Lock()

	// find reservations about to expire and refresh them in parallel
	g := new(errgroup.Group)
	for p, rsvp := range rf.relays {
		if now.Add(rsvpExpirationSlack).Before(rsvp.Expiration) {
			continue
		}

		p := p
		g.Go(func() error { return rf.refreshRelayReservation(ctx, p) })
	}
	rf.relayMx.Unlock()

	err := g.Wait()
	return err != nil
}

func (rf *relayFinder) refreshRelayReservation(ctx context.Context, p peer.ID) error {
	rsvp, err := circuitv2.Reserve(ctx, rf.host, peer.AddrInfo{ID: p})

	rf.relayMx.Lock()
	defer rf.relayMx.Unlock()

	if err != nil {
		log.Debugw("failed to refresh relay slot reservation", "relay", p, "error", err)

		delete(rf.relays, p)
		// unprotect the connection
		rf.host.ConnManager().Unprotect(p, autorelayTag)
		return err
	}

	log.Debugw("refreshed relay slot reservation", "relay", p)
	rf.relays[p] = rsvp
	return nil
}

// usingRelay returns if we're currently using the given relay.
func (rf *relayFinder) usingRelay(p peer.ID) bool {
	_, ok := rf.relays[p]
	return ok
}

// selectCandidates returns an ordered slice of relay candidates.
// Callers should attempt to obtain reservations with the candidates in this order.
func (rf *relayFinder) selectCandidates() []*candidate {
	now := rf.conf.clock.Now()
	candidates := make([]*candidate, 0, len(rf.candidates))
	for _, cand := range rf.candidates {
		if cand.added.Add(rf.conf.maxCandidateAge).After(now) {
			candidates = append(candidates, cand)
		}
	}

	// TODO: better relay selection strategy; this just selects random relays,
	// but we should probably use ping latency as the selection metric
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	return candidates
}

// This function is computes the NATed relay addrs when our status is private:
//   - The public addrs are removed from the address set.
//   - The non-public addrs are included verbatim so that peers behind the same NAT/firewall
//     can still dial us directly.
//   - On top of those, we add the relay-specific addrs for the relays to which we are
//     connected. For each non-private relay addr, we encapsulate the p2p-circuit addr
//     through which we can be dialed.
func (rf *relayFinder) relayAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	rf.relayMx.Lock()
	defer rf.relayMx.Unlock()

	if rf.cachedAddrs != nil && rf.conf.clock.Now().Before(rf.cachedAddrsExpiry) {
		return rf.cachedAddrs
	}

	raddrs := make([]ma.Multiaddr, 0, 4*len(rf.relays)+4)

	// only keep private addrs from the original addr set
	for _, addr := range addrs {
		if manet.IsPrivateAddr(addr) {
			raddrs = append(raddrs, addr)
		}
	}

	// add relay specific addrs to the list
	for p := range rf.relays {
		addrs := cleanupAddressSet(rf.host.Peerstore().Addrs(p))

		circuit := ma.StringCast(fmt.Sprintf("/p2p/%s/p2p-circuit", p.Pretty()))
		for _, addr := range addrs {
			pub := addr.Encapsulate(circuit)
			raddrs = append(raddrs, pub)
		}
	}

	rf.cachedAddrs = raddrs
	rf.cachedAddrsExpiry = rf.conf.clock.Now().Add(30 * time.Second)

	return raddrs
}

func (rf *relayFinder) Start() error {
	rf.ctxCancelMx.Lock()
	defer rf.ctxCancelMx.Unlock()
	if rf.ctxCancel != nil {
		return errors.New("relayFinder already running")
	}
	log.Debug("starting relay finder")
	ctx, cancel := context.WithCancel(context.Background())
	rf.ctxCancel = cancel
	rf.refCount.Add(1)
	go func() {
		defer rf.refCount.Done()
		rf.background(ctx)
	}()
	return nil
}

func (rf *relayFinder) Stop() error {
	rf.ctxCancelMx.Lock()
	defer rf.ctxCancelMx.Unlock()
	log.Debug("stopping relay finder")
	if rf.ctxCancel != nil {
		rf.ctxCancel()
	}
	rf.refCount.Wait()
	rf.ctxCancel = nil
	return nil
}
