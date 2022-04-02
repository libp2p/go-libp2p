package autorelay

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/libp2p/go-libp2p-core/discovery"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/routing"

	basic "github.com/libp2p/go-libp2p/p2p/host/basic"
	relayv1 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv1/relay"
	circuitv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	circuitv2_proto "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	RelayRendezvous = "/libp2p/relay"

	rsvpRefreshInterval = time.Minute
	rsvpExpirationSlack = 2 * time.Minute

	autorelayTag = "autorelay"

	protoIDv1 = string(relayv1.ProtoID)
	protoIDv2 = string(circuitv2_proto.ProtoIDv2Hop)
)

var (
	DesiredRelays = 1

	BootDelay = 20 * time.Second
)

// DefaultRelays are the known PL-operated v1 relays; will be decommissioned in 2022.
var DefaultRelays = []string{
	"/ip4/147.75.80.110/tcp/4001/p2p/QmbFgm5zan8P6eWWmeyfncR5feYEMPbht5b1FW1C37aQ7y",
	"/ip4/147.75.80.110/udp/4001/quic/p2p/QmbFgm5zan8P6eWWmeyfncR5feYEMPbht5b1FW1C37aQ7y",
	"/ip4/147.75.195.153/tcp/4001/p2p/QmW9m57aiBDHAkKj9nmFSEn7ZqrcF1fZS4bipsTCHburei",
	"/ip4/147.75.195.153/udp/4001/quic/p2p/QmW9m57aiBDHAkKj9nmFSEn7ZqrcF1fZS4bipsTCHburei",
	"/ip4/147.75.70.221/tcp/4001/p2p/Qme8g49gm3q4Acp7xWBKg3nAa9fxZ1YmyDJdyGgoG6LsXh",
	"/ip4/147.75.70.221/udp/4001/quic/p2p/Qme8g49gm3q4Acp7xWBKg3nAa9fxZ1YmyDJdyGgoG6LsXh",
}

var defaultStaticRelays []peer.AddrInfo

func init() {
	for _, s := range DefaultRelays {
		pi, err := peer.AddrInfoFromString(s)
		if err != nil {
			panic(fmt.Sprintf("failed to initialize default static relays: %s", err))
		}
		defaultStaticRelays = append(defaultStaticRelays, *pi)
	}
}

type Option func(*AutoRelay) error
type StaticRelayOption Option

func WithStaticRelays(static []peer.AddrInfo) StaticRelayOption {
	return func(r *AutoRelay) error {
		if len(r.static) > 0 {
			return errors.New("can't set static relays, static relays already configured")
		}
		r.static = static
		return nil
	}
}

func WithDefaultStaticRelays() StaticRelayOption {
	return WithStaticRelays(defaultStaticRelays)
}

func WithDiscoverer(discover discovery.Discoverer) Option {
	return func(r *AutoRelay) error {
		r.discover = discover
		return nil
	}
}

// AutoRelay is a Host that uses relays for connectivity when a NAT is detected.
type AutoRelay struct {
	host     *basic.BasicHost
	discover discovery.Discoverer
	router   routing.PeerRouting
	addrsF   basic.AddrsFactory

	static []peer.AddrInfo

	refCount  sync.WaitGroup
	ctxCancel context.CancelFunc

	relayFound        chan struct{}
	findRelaysRunning int32 // to be used as an atomic

	mx     sync.Mutex
	relays map[peer.ID]*circuitv2.Reservation // rsvp will be nil if it is a v1 relay
	status network.Reachability

	cachedAddrs       []ma.Multiaddr
	cachedAddrsExpiry time.Time
}

func NewAutoRelay(bhost *basic.BasicHost, router routing.PeerRouting, opts ...Option) (*AutoRelay, error) {
	ctx, cancel := context.WithCancel(context.Background())
	ar := &AutoRelay{
		ctxCancel:  cancel,
		host:       bhost,
		router:     router,
		addrsF:     bhost.AddrsFactory,
		relays:     make(map[peer.ID]*circuitv2.Reservation),
		relayFound: make(chan struct{}, 1),
		status:     network.ReachabilityUnknown,
	}
	for _, opt := range opts {
		if err := opt(ar); err != nil {
			return nil, err
		}
	}
	bhost.AddrsFactory = ar.hostAddrs
	ar.refCount.Add(1)
	go ar.background(ctx)
	return ar, nil
}

func (ar *AutoRelay) hostAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	return ar.relayAddrs(ar.addrsF(addrs))
}

func (ar *AutoRelay) background(ctx context.Context) {
	defer ar.refCount.Done()

	subReachability, err := ar.host.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		log.Error("failed to subscribe to the EvtLocalReachabilityChanged")
		return
	}
	defer subReachability.Close()
	subConnectedness, err := ar.host.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged))
	if err != nil {
		log.Error("failed to subscribe to the EvtPeerConnectednessChanged")
		return
	}
	defer subConnectedness.Close()

	ticker := time.NewTicker(rsvpRefreshInterval)
	defer ticker.Stop()

	for {
		// when true, we need to identify push
		var push bool

		select {
		case ev, ok := <-subConnectedness.Out():
			if !ok {
				return
			}
			evt := ev.(event.EvtPeerConnectednessChanged)
			switch evt.Connectedness {
			case network.Connected:
				// If we just connect to one of our static relays, get a reservation immediately.
				for _, pi := range ar.static {
					if pi.ID == evt.Peer {
						rsvp, ok := ar.tryRelay(ctx, pi)
						if ok {
							ar.mx.Lock()
							ar.relays[pi.ID] = rsvp
							ar.mx.Unlock()
						}
						push = true
						break
					}
				}
			case network.NotConnected:
				ar.mx.Lock()
				if ar.usingRelay(evt.Peer) { // we were disconnected from a relay
					delete(ar.relays, evt.Peer)
					push = true
				}
				ar.mx.Unlock()
			}
		case ev, ok := <-subReachability.Out():
			if !ok {
				return
			}
			evt := ev.(event.EvtLocalReachabilityChanged)

			if evt.Reachability == network.ReachabilityPrivate {
				// findRelays is a long-lived task (runs up to 2.5 minutes)
				// Make sure we only start it once.
				if atomic.CompareAndSwapInt32(&ar.findRelaysRunning, 0, 1) {
					go func() {
						defer atomic.StoreInt32(&ar.findRelaysRunning, 0)
						ar.findRelays(ctx)
					}()
				}
			}

			ar.mx.Lock()
			// if our reachability changed
			if ar.status != evt.Reachability && evt.Reachability != network.ReachabilityUnknown {
				push = true
			}
			ar.status = evt.Reachability
			ar.mx.Unlock()
		case <-ar.relayFound:
			push = true
		case now := <-ticker.C:
			push = ar.refreshReservations(ctx, now)
		case <-ctx.Done():
			return
		}

		if push {
			ar.mx.Lock()
			ar.cachedAddrs = nil
			ar.mx.Unlock()
			ar.host.SignalAddressChange()
		}
	}
}

func (ar *AutoRelay) refreshReservations(ctx context.Context, now time.Time) bool {
	ar.mx.Lock()
	if ar.status == network.ReachabilityPublic {
		// we are public, forget about the relays, unprotect peers
		for p := range ar.relays {
			ar.host.ConnManager().Unprotect(p, autorelayTag)
			delete(ar.relays, p)
		}

		ar.mx.Unlock()
		return true
	}

	if len(ar.relays) == 0 {
		ar.mx.Unlock()
		return false
	}

	// find reservations about to expire and refresh them in parallel
	g := new(errgroup.Group)
	for p, rsvp := range ar.relays {
		if rsvp == nil {
			// this is a circuitv1 relay, there is no reservation
			continue
		}
		if now.Add(rsvpExpirationSlack).Before(rsvp.Expiration) {
			continue
		}

		p := p
		g.Go(func() error { return ar.refreshRelayReservation(ctx, p) })
	}
	ar.mx.Unlock()

	err := g.Wait()
	return err != nil
}

func (ar *AutoRelay) refreshRelayReservation(ctx context.Context, p peer.ID) error {
	rsvp, err := circuitv2.Reserve(ctx, ar.host, peer.AddrInfo{ID: p})

	ar.mx.Lock()
	defer ar.mx.Unlock()

	if err != nil {
		log.Debugf("failed to refresh relay slot reservation with %s: %s", p, err)

		delete(ar.relays, p)
		// unprotect the connection
		ar.host.ConnManager().Unprotect(p, autorelayTag)
	} else {
		log.Debugf("refreshed relay slot reservation with %s", p)
		ar.relays[p] = rsvp
	}

	return err
}

func (ar *AutoRelay) findRelays(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for retry := 0; retry < 5; retry++ {
		if retry > 0 {
			log.Debug("no relays connected; retrying in 30s")
			select {
			case <-timer.C:
			case <-ctx.Done():
				return
			}
		}

		if foundAtLeastOneRelay := ar.findRelaysOnce(ctx); foundAtLeastOneRelay {
			return
		}
	}
}

func (ar *AutoRelay) findRelaysOnce(ctx context.Context) bool {
	relays, err := ar.discoverRelays(ctx)
	if err != nil {
		log.Debugf("error discovering relays: %s", err)
		return false
	}
	log.Debugf("discovered %d relays", len(relays))
	relays = ar.selectRelays(ctx, relays)
	log.Debugf("selected %d relays", len(relays))

	var found bool
	for _, pi := range relays {
		ar.mx.Lock()
		relayInUse := ar.usingRelay(pi.ID)
		ar.mx.Unlock()
		if relayInUse {
			continue
		}
		rsvp, ok := ar.tryRelay(ctx, pi)
		if !ok {
			continue
		}
		// make sure we're still connected.
		if ar.host.Network().Connectedness(pi.ID) != network.Connected {
			continue
		}
		found = true
		ar.mx.Lock()
		ar.relays[pi.ID] = rsvp
		// protect the connection
		ar.host.ConnManager().Protect(pi.ID, autorelayTag)
		numRelays := len(ar.relays)
		ar.mx.Unlock()

		if numRelays >= DesiredRelays {
			break
		}
	}
	if found {
		ar.relayFound <- struct{}{}
		return true
	}
	return false
}

// usingRelay returns if we're currently using the given relay.
func (ar *AutoRelay) usingRelay(p peer.ID) bool {
	_, ok := ar.relays[p]
	return ok
}

// addRelay adds the given relay to our set of relays.
// returns true when we add a new relay
func (ar *AutoRelay) tryRelay(ctx context.Context, pi peer.AddrInfo) (*circuitv2.Reservation, bool) {
	if !ar.connect(ctx, pi) {
		return nil, false
	}

	protos, err := ar.host.Peerstore().SupportsProtocols(pi.ID, protoIDv1, protoIDv2)
	if err != nil {
		log.Debugf("error checking relay protocol support for peer %s: %s", pi.ID, err)
		return nil, false
	}

	var supportsv1, supportsv2 bool
protoLoop:
	for _, proto := range protos {
		switch proto {
		case protoIDv1:
			supportsv1 = true
		case protoIDv2:
			supportsv2 = true
			break protoLoop
		}
	}

	switch {
	case supportsv2:
		rsvp, err := circuitv2.Reserve(ctx, ar.host, pi)
		if err != nil {
			log.Debugf("error reserving slot with %s: %s", pi.ID, err)
			return nil, false
		}
		return rsvp, true
	case supportsv1:
		ok, err := relayv1.CanHop(ctx, ar.host, pi.ID)
		if err != nil {
			log.Debugf("error querying relay %s for v1 hop: %s", pi.ID, err)
			return nil, false
		}
		return nil, ok
	default: // supports neither, unusable relay.
		return nil, false
	}
}

func (ar *AutoRelay) connect(ctx context.Context, pi peer.AddrInfo) bool {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if len(pi.Addrs) == 0 {
		var err error
		pi, err = ar.router.FindPeer(ctx, pi.ID)
		if err != nil {
			log.Debugf("error finding relay peer %s: %s", pi.ID, err.Error())
			return false
		}
	}

	err := ar.host.Connect(ctx, pi)
	if err != nil {
		log.Debugf("error connecting to relay %s: %s", pi.ID, err.Error())
		return false
	}

	// wait for identify to complete in at least one conn so that we can check the supported protocols
	conns := ar.host.Network().ConnsToPeer(pi.ID)
	if len(conns) == 0 {
		return false
	}

	ready := make(chan struct{}, len(conns))
	for _, conn := range conns {
		go func(conn network.Conn) {
			select {
			case <-ar.host.IDService().IdentifyWait(conn):
				ready <- struct{}{}
			case <-ctx.Done():
			}
		}(conn)
	}

	select {
	case <-ready:
	case <-ctx.Done():
		return false
	}

	return true
}

func (ar *AutoRelay) discoverRelays(ctx context.Context) ([]peer.AddrInfo, error) {
	if len(ar.static) > 0 {
		return ar.static, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var ret []peer.AddrInfo
	ch, err := ar.discover.FindPeers(ctx, RelayRendezvous, discovery.Limit(1000))
	if err != nil {
		return nil, err
	}
	for p := range ch {
		ret = append(ret, p)
	}
	return ret, nil
}

func (ar *AutoRelay) selectRelays(ctx context.Context, pis []peer.AddrInfo) []peer.AddrInfo {
	// TODO: better relay selection strategy; this just selects random relays,
	// but we should probably use ping latency as the selection metric
	rand.Shuffle(len(pis), func(i, j int) {
		pis[i], pis[j] = pis[j], pis[i]
	})
	return pis
}

// This function is computes the NATed relay addrs when our status is private:
// - The public addrs are removed from the address set.
// - The non-public addrs are included verbatim so that peers behind the same NAT/firewall
//   can still dial us directly.
// - On top of those, we add the relay-specific addrs for the relays to which we are
//   connected. For each non-private relay addr, we encapsulate the p2p-circuit addr
//   through which we can be dialed.
func (ar *AutoRelay) relayAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	ar.mx.Lock()
	defer ar.mx.Unlock()

	if ar.status != network.ReachabilityPrivate {
		return addrs
	}

	raddrs := make([]ma.Multiaddr, 0, 4+len(ar.cachedAddrs))
	// only keep private addrs from the original addr set
	for _, addr := range addrs {
		if manet.IsPrivateAddr(addr) {
			raddrs = append(raddrs, addr)
		}
	}
	numPrivateAddrs := len(raddrs)
	if len(ar.cachedAddrs) > 0 && time.Now().Before(ar.cachedAddrsExpiry) {
		return append(raddrs, ar.cachedAddrs...)
	}

	// add relay specific addrs to the list
	for p := range ar.relays {
		circuit := ma.StringCast(fmt.Sprintf("/p2p/%s/p2p-circuit", p.Pretty()))
		for _, addr := range cleanupAddressSet(ar.host.Peerstore().Addrs(p)) {
			pub := addr.Encapsulate(circuit)
			raddrs = append(raddrs, pub)
		}
	}
	ar.cachedAddrs = raddrs[numPrivateAddrs:] // only cache the relay addresses
	ar.cachedAddrsExpiry = time.Now().Add(30 * time.Second)
	return raddrs
}

func (ar *AutoRelay) Close() error {
	ar.ctxCancel()
	ar.refCount.Wait()
	return nil
}
