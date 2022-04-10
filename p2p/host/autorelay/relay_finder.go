package autorelay

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	basic "github.com/libp2p/go-libp2p/p2p/host/basic"
	relayv1 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv1/relay"
	circuitv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	circuitv2_proto "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"

	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	protoIDv1 = string(relayv1.ProtoID)
	protoIDv2 = string(circuitv2_proto.ProtoIDv2Hop)
)

// Terminology:
// Candidate: Once we connect to a node and it supports (v1 / v2) relay protocol,
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
	numAttempts     int
}

type candidateOnBackoff struct {
	candidate
	nextConnAttempt time.Time
}

// relayFinder is a Host that uses relays for connectivity when a NAT is detected.
type relayFinder struct {
	bootTime time.Time
	host     *basic.BasicHost

	conf *config

	refCount  sync.WaitGroup
	ctxCancel context.CancelFunc

	peerChan <-chan peer.AddrInfo

	candidateFound      chan struct{} // receives every time we find a new relay candidate
	candidateMx         sync.Mutex
	candidates          map[peer.ID]*candidate
	candidatesOnBackoff []*candidateOnBackoff // this slice is always sorted by the nextConnAttempt time

	relayUpdated chan struct{}

	relayMx sync.Mutex
	relays  map[peer.ID]*circuitv2.Reservation // rsvp will be nil if it is a v1 relay

	cachedAddrs       []ma.Multiaddr
	cachedAddrsExpiry time.Time
}

func newRelayFinder(host *basic.BasicHost, peerChan <-chan peer.AddrInfo, conf *config) *relayFinder {
	r := &relayFinder{
		bootTime:       time.Now(),
		host:           host,
		conf:           conf,
		peerChan:       peerChan,
		candidates:     make(map[peer.ID]*candidate),
		candidateFound: make(chan struct{}, 1),
		relays:         make(map[peer.ID]*circuitv2.Reservation),
		relayUpdated:   make(chan struct{}, 1),
	}
	return r
}

func (rf *relayFinder) background(ctx context.Context) {
	rf.refCount.Add(1)
	go func() {
		defer rf.refCount.Done()
		rf.findNodes(ctx)
	}()

	subConnectedness, err := rf.host.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged))
	if err != nil {
		log.Error("failed to subscribe to the EvtPeerConnectednessChanged")
		return
	}
	defer subConnectedness.Close()

	bootDelayTimer := time.NewTimer(rf.conf.bootDelay)
	defer bootDelayTimer.Stop()
	refreshTicker := time.NewTicker(rsvpRefreshInterval)
	defer refreshTicker.Stop()
	backoffTicker := time.NewTicker(rf.conf.backoff / 5)
	defer backoffTicker.Stop()

	for {
		// when true, we need to identify push
		var push bool

		select {
		case ev, ok := <-subConnectedness.Out():
			if !ok {
				return
			}
			evt := ev.(event.EvtPeerConnectednessChanged)
			if evt.Connectedness != network.NotConnected {
				continue
			}
			rf.relayMx.Lock()
			if rf.usingRelay(evt.Peer) { // we were disconnected from a relay
				log.Debugw("disconnected from relay", "id", evt.Peer)
				delete(rf.relays, evt.Peer)
				push = true
			}
			rf.relayMx.Unlock()
		case <-rf.candidateFound:
			rf.handleNewCandidate(ctx)
		case <-bootDelayTimer.C:
			rf.handleNewCandidate(ctx)
		case <-rf.relayUpdated:
			push = true
		case now := <-refreshTicker.C:
			push = rf.refreshReservations(ctx, now)
		case now := <-backoffTicker.C:
			rf.checkForCandidatesOnBackoff(now)
		case <-ctx.Done():
			return
		}

		if push {
			rf.relayMx.Lock()
			rf.cachedAddrs = nil
			rf.relayMx.Unlock()
			rf.host.SignalAddressChange()
		}
	}
}

// findNodes accepts nodes from the channel and tests if they support relaying.
// It is run on both public and private nodes.
// It garbage collects old entries, so that nodes doesn't overflow.
// This makes sure that as soon as we need to find relay candidates, we have them available.
func (rf *relayFinder) findNodes(ctx context.Context) {
	for {
		select {
		case pi := <-rf.peerChan:
			log.Debugw("found node", "id", pi.ID)
			rf.candidateMx.Lock()
			numCandidates := len(rf.candidates)
			rf.candidateMx.Unlock()
			if numCandidates >= rf.conf.maxCandidates {
				log.Debugw("skipping node. Already have enough candidates", "id", pi.ID, "num", numCandidates, "max", rf.conf.maxCandidates)
				continue
			}
			rf.refCount.Add(1)
			go func() {
				defer rf.refCount.Done()
				rf.handleNewNode(ctx, pi)
			}()
		case <-ctx.Done():
			return
		}
	}
}

func (rf *relayFinder) notifyNewCandidate() {
	select {
	case rf.candidateFound <- struct{}{}:
	default:
	}
}

// handleNewNode tests if a peer supports circuit v1 or v2.
// This method is only run on private nodes.
// If a peer does, it is added to the candidates map.
// Note that just supporting the protocol doesn't guarantee that we can also obtain a reservation.
func (rf *relayFinder) handleNewNode(ctx context.Context, pi peer.AddrInfo) {
	rf.relayMx.Lock()
	relayInUse := rf.usingRelay(pi.ID)
	rf.relayMx.Unlock()
	if relayInUse {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	supportsV2, err := rf.tryNode(ctx, pi)
	if err != nil {
		log.Debugf("node %s not accepted as a candidate: %s", pi.ID, err)
		return
	}
	rf.candidateMx.Lock()
	if len(rf.candidates) > rf.conf.maxCandidates {
		rf.candidateMx.Unlock()
		return
	}
	log.Debugw("node supports relay protocol", "peer", pi.ID, "supports circuit v2", supportsV2)
	rf.candidates[pi.ID] = &candidate{ai: pi, supportsRelayV2: supportsV2}
	rf.candidateMx.Unlock()

	rf.notifyNewCandidate()
}

// tryNode checks if a peer actually supports either circuit v1 or circuit v2.
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

	protos, err := rf.host.Peerstore().SupportsProtocols(pi.ID, protoIDv1, protoIDv2)
	if err != nil {
		return false, fmt.Errorf("error checking relay protocol support for peer %s: %w", pi.ID, err)
	}

	// If the node speaks both, prefer circuit v2
	var supportsV1, supportsV2 bool
	for _, proto := range protos {
		switch proto {
		case protoIDv1:
			supportsV1 = true
		case protoIDv2:
			supportsV2 = true
		}
	}
	if !supportsV1 && !supportsV2 {
		return false, errors.New("doesn't speak circuit v1 or v2")
	}
	return supportsV2, nil
}

func (rf *relayFinder) handleNewCandidate(ctx context.Context) {
	rf.relayMx.Lock()
	numRelays := len(rf.relays)
	rf.relayMx.Unlock()
	// We're already connected to our desired number of relays. Nothing to do here.
	if numRelays == rf.conf.desiredRelays {
		return
	}

	rf.candidateMx.Lock()
	defer rf.candidateMx.Unlock()
	if len(rf.candidates) == 0 {
		return
	}

	if len(rf.conf.staticRelays) != 0 {
		// make sure we read all static relays before continuing
		if len(rf.peerChan) > 0 && len(rf.candidates) < rf.conf.minCandidates && time.Since(rf.bootTime) < rf.conf.bootDelay {
			return
		}
	} else if len(rf.relays) == 0 && len(rf.candidates) < rf.conf.minCandidates && time.Since(rf.bootTime) < rf.conf.bootDelay {
		// During the startup phase, we don't want to connect to the first candidate that we find.
		// Instead, we wait until we've found at least minCandidates, and then select the best of those.
		// However, if that takes too long (longer than bootDelay), we still go ahead.
		return
	}

	candidates := rf.selectCandidates()

	// We now iterate over the candidates, attempting (sequentially) to get reservations with them, until
	// we reach the desired number of relays.
	rf.refCount.Add(1)
	go func() {
		defer rf.refCount.Done()

		for _, cand := range candidates {
			id := cand.ai.ID
			var failed bool
			var rsvp *circuitv2.Reservation

			// make sure we're still connected.
			if rf.host.Network().Connectedness(id) != network.Connected {
				if err := rf.host.Connect(ctx, cand.ai); err != nil {
					log.Debugw("failed to reconnect", "peer", cand.ai, "error", err)
					rf.candidateMx.Lock()
					delete(rf.candidates, cand.ai.ID)
					rf.candidateMx.Unlock()
					continue
				}
			}
			if cand.supportsRelayV2 {
				var err error
				rsvp, err = circuitv2.Reserve(ctx, rf.host, cand.ai)
				if err != nil {
					failed = true
					log.Debugw("failed to reserve slot", "id", id, "error", err)
				}
			} else {
				ok, err := relayv1.CanHop(ctx, rf.host, id)
				if err != nil {
					failed = true
					log.Debugw("error querying relay for v1 hop", "id", id, "error", err)
				}
				if !ok {
					failed = true
					log.Debugw("relay can't hop", "id", id)
				}
			}
			rf.candidateMx.Lock()
			if failed {
				cand.numAttempts++
				delete(rf.candidates, id)
				// We failed to obtain a reservation for too many times. We give up.
				if cand.numAttempts >= rf.conf.maxAttempts {
					log.Debugw("failed to obtain a reservation with. Giving up.", "id", id, "num attempts", cand.numAttempts)
				} else {
					rf.moveCandidateToBackoff(cand)
				}
				rf.candidateMx.Unlock()
				continue
			}
			rf.candidateMx.Unlock()
			log.Debugw("adding new relay", "id", id)
			rf.relayMx.Lock()
			rf.relays[id] = rsvp
			numRelays := len(rf.relays)
			rf.relayMx.Unlock()

			rf.host.ConnManager().Protect(id, autorelayTag) // protect the connection

			select {
			case rf.relayUpdated <- struct{}{}:
			default:
			}
			if numRelays >= rf.conf.desiredRelays {
				break
			}
		}
	}()
}

// must be called with mutex locked
func (rf *relayFinder) moveCandidateToBackoff(cand *candidate) {
	if len(rf.candidatesOnBackoff) >= rf.conf.maxCandidates {
		log.Debugw("already have enough candidates on backoff. Dropping.", "id", cand.ai.ID)
		return
	}
	log.Debugw("moving candidate to backoff", "id", cand.ai.ID)
	backoff := rf.conf.backoff * (1 << (cand.numAttempts - 1))
	// introduce a bit of jitter
	backoff = (backoff * time.Duration(16+rand.Intn(8))) / time.Duration(20)
	rf.candidatesOnBackoff = append(rf.candidatesOnBackoff, &candidateOnBackoff{
		candidate:       *cand,
		nextConnAttempt: time.Now().Add(backoff),
	})
}

func (rf *relayFinder) checkForCandidatesOnBackoff(now time.Time) {
	rf.candidateMx.Lock()
	defer rf.candidateMx.Unlock()

	for _, cand := range rf.candidatesOnBackoff {
		if cand.nextConnAttempt.After(now) {
			break
		}
		if len(rf.candidates) >= rf.conf.maxCandidates {
			// drop this candidate if we already have enough others
			log.Debugw("cannot move backoff'ed candidate back. Already have enough candidates.", "id", cand.ai.ID)
		} else {
			log.Debugw("moving backoff'ed candidate back", "id", cand.ai.ID)
			rf.candidates[cand.ai.ID] = &candidate{
				added:           cand.added,
				supportsRelayV2: cand.supportsRelayV2,
				ai:              cand.ai,
				numAttempts:     cand.numAttempts,
			}
			rf.notifyNewCandidate()
		}
		rf.candidatesOnBackoff = rf.candidatesOnBackoff[1:]
	}
}

func (rf *relayFinder) refreshReservations(ctx context.Context, now time.Time) bool {
	rf.relayMx.Lock()

	// find reservations about to expire and refresh them in parallel
	g := new(errgroup.Group)
	for p, rsvp := range rf.relays {
		if rsvp == nil { // this is a circuit v1 relay, there is no reservation
			continue
		}
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
	var candidates []*candidate
	for _, cand := range rf.candidates {
		candidates = append(candidates, cand)
	}

	// TODO: better relay selection strategy; this just selects random relays,
	// but we should probably use ping latency as the selection metric
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	return candidates
}

// This function is computes the NATed relay addrs when our status is private:
// - The public addrs are removed from the address set.
// - The non-public addrs are included verbatim so that peers behind the same NAT/firewall
//   can still dial us directly.
// - On top of those, we add the relay-specific addrs for the relays to which we are
//   connected. For each non-private relay addr, we encapsulate the p2p-circuit addr
//   through which we can be dialed.
func (rf *relayFinder) relayAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	rf.relayMx.Lock()
	defer rf.relayMx.Unlock()

	if rf.cachedAddrs != nil && time.Now().Before(rf.cachedAddrsExpiry) {
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
	rf.cachedAddrsExpiry = time.Now().Add(30 * time.Second)

	return raddrs
}

func (rf *relayFinder) Start() error {
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
	log.Debug("stopping relay finder")
	if rf.ctxCancel != nil {
		rf.ctxCancel()
	}
	rf.refCount.Wait()
	rf.ctxCancel = nil
	return nil
}
