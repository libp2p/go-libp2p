package autonat

import (
	"context"
	"math/rand"
	"slices"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"

	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var log = logging.Logger("autonat")

const maxConfidence = 3

// AmbientAutoNAT is the implementation of ambient NAT autodiscovery
type AmbientAutoNAT struct {
	host host.Host

	*config

	ctx               context.Context
	ctxCancel         context.CancelFunc // is closed when Close is called
	backgroundRunning chan struct{}      // is closed when the background go routine exits

	inboundConn   chan network.Conn
	dialResponses chan error
	// Used when testing the autonat service
	observations chan network.Reachability
	// status is an autoNATResult reflecting current status.
	status atomic.Pointer[network.Reachability]
	// Reflects the confidence on of the NATStatus being private, as a single
	// dialback may fail for reasons unrelated to NAT.
	// If it is <3, then multiple autoNAT peers may be contacted for dialback
	// If only a single autoNAT peer is known, then the confidence increases
	// for each failure until it reaches 3.
	confidence    int
	lastInbound   time.Time
	lastProbe     time.Time
	recentProbes  map[peer.ID]time.Time
	pendingProbes int
	ourAddrs      map[string]struct{}

	service *autoNATService

	emitReachabilityChanged event.Emitter
	subscriber              event.Subscription
}

// StaticAutoNAT is a simple AutoNAT implementation when a single NAT status is desired.
type StaticAutoNAT struct {
	host         host.Host
	reachability network.Reachability
	service      *autoNATService
}

// New creates a new NAT autodiscovery system attached to a host
func New(h host.Host, options ...Option) (AutoNAT, error) {
	var err error
	conf := new(config)
	conf.host = h
	conf.dialPolicy.host = h

	if err = defaults(conf); err != nil {
		return nil, err
	}
	if conf.addressFunc == nil {
		if aa, ok := h.(interface{ AllAddrs() []ma.Multiaddr }); ok {
			conf.addressFunc = aa.AllAddrs
		} else {
			conf.addressFunc = h.Addrs
		}
	}

	for _, o := range options {
		if err = o(conf); err != nil {
			return nil, err
		}
	}
	emitReachabilityChanged, _ := h.EventBus().Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)

	var service *autoNATService
	if (!conf.forceReachability || conf.reachability == network.ReachabilityPublic) && conf.dialer != nil {
		service, err = newAutoNATService(conf)
		if err != nil {
			return nil, err
		}
		service.Enable()
	}

	if conf.forceReachability {
		emitReachabilityChanged.Emit(event.EvtLocalReachabilityChanged{Reachability: conf.reachability})

		return &StaticAutoNAT{
			host:         h,
			reachability: conf.reachability,
			service:      service,
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	as := &AmbientAutoNAT{
		ctx:               ctx,
		ctxCancel:         cancel,
		backgroundRunning: make(chan struct{}),
		host:              h,
		config:            conf,
		inboundConn:       make(chan network.Conn, 5),
		dialResponses:     make(chan error, 1),
		observations:      make(chan network.Reachability, 1),

		emitReachabilityChanged: emitReachabilityChanged,
		service:                 service,
		recentProbes:            make(map[peer.ID]time.Time),
		ourAddrs:                make(map[string]struct{}),
	}
	reachability := network.ReachabilityUnknown
	as.status.Store(&reachability)

	subscriber, err := as.host.EventBus().Subscribe(
		[]any{new(event.EvtLocalAddressesUpdated), new(event.EvtPeerIdentificationCompleted)},
		eventbus.Name("autonat"),
	)
	if err != nil {
		return nil, err
	}
	as.subscriber = subscriber

	go as.background()

	return as, nil
}

// Status returns the AutoNAT observed reachability status.
func (as *AmbientAutoNAT) Status() network.Reachability {
	s := as.status.Load()
	return *s
}

func (as *AmbientAutoNAT) emitStatus() {
	status := *as.status.Load()
	as.emitReachabilityChanged.Emit(event.EvtLocalReachabilityChanged{Reachability: status})
	if as.metricsTracer != nil {
		as.metricsTracer.ReachabilityStatus(status)
	}
}

func ipInList(candidate ma.Multiaddr, list []ma.Multiaddr) bool {
	candidateIP, _ := manet.ToIP(candidate)
	for _, i := range list {
		if ip, err := manet.ToIP(i); err == nil && ip.Equal(candidateIP) {
			return true
		}
	}
	return false
}

func (as *AmbientAutoNAT) background() {
	defer close(as.backgroundRunning)
	// wait a bit for the node to come online and establish some connections
	// before starting autodetection
	delay := as.config.bootDelay

	subChan := as.subscriber.Out()
	defer as.subscriber.Close()
	defer as.emitReachabilityChanged.Close()

	// Fallback timer to update address in case EvtLocalAddressesUpdated is not emitted.
	// TODO: The event not emitting properly is a bug. This is a workaround.
	addrChangeTicker := time.NewTicker(30 * time.Minute)
	defer addrChangeTicker.Stop()

	timer := time.NewTimer(delay)
	defer timer.Stop()
	timerRunning := true
	forceProbe := false
	for {
		select {
		case conn := <-as.inboundConn:
			localAddrs := as.host.Addrs()
			if manet.IsPublicAddr(conn.RemoteMultiaddr()) &&
				!ipInList(conn.RemoteMultiaddr(), localAddrs) {
				as.lastInbound = time.Now()
			}
		case <-addrChangeTicker.C:
			// schedule a new probe if addresses have changed
		case e := <-subChan:
			switch e := e.(type) {
			case event.EvtPeerIdentificationCompleted:
				if proto, err := as.host.Peerstore().SupportsProtocols(e.Peer, AutoNATProto); err == nil && len(proto) > 0 {
					forceProbe = true
				}
			case event.EvtLocalAddressesUpdated:
				// schedule a new probe if addresses have changed
			default:
				log.Errorf("unknown event type: %T", e)
			}
		case obs := <-as.observations:
			as.recordObservation(obs)
			continue
		case err, ok := <-as.dialResponses:
			if !ok {
				return
			}
			as.pendingProbes--
			if IsDialRefused(err) {
				forceProbe = true
			} else {
				as.handleDialResponse(err)
			}
		case <-timer.C:
			timerRunning = false
			forceProbe = false
			// Update the last probe time. We use it to ensure
			// that we don't spam the peerstore.
			as.lastProbe = time.Now()
			peer := as.getPeerToProbe()
			as.tryProbe(peer)
		case <-as.ctx.Done():
			return
		}
		// On address update, reduce confidence from maximum so that we schedule
		// the next probe sooner
		hasNewAddr := as.checkAddrs()
		if hasNewAddr && as.confidence == maxConfidence {
			as.confidence--
		}

		if timerRunning && !timer.Stop() {
			<-timer.C
		}
		timer.Reset(as.scheduleProbe(forceProbe))
		timerRunning = true
	}
}

func (as *AmbientAutoNAT) checkAddrs() (hasNewAddr bool) {
	currentAddrs := as.addressFunc()
	hasNewAddr = slices.ContainsFunc(currentAddrs, func(a ma.Multiaddr) bool {
		_, ok := as.ourAddrs[string(a.Bytes())]
		return !ok
	})
	clear(as.ourAddrs)
	for _, a := range currentAddrs {
		if !manet.IsPublicAddr(a) {
			continue
		}
		as.ourAddrs[string(a.Bytes())] = struct{}{}
	}
	return hasNewAddr
}

// scheduleProbe calculates when the next probe should be scheduled for.
func (as *AmbientAutoNAT) scheduleProbe(forceProbe bool) time.Duration {
	now := time.Now()
	currentStatus := *as.status.Load()
	nextProbeAfter := as.config.refreshInterval
	receivedInbound := as.lastInbound.After(as.lastProbe)
	switch {
	case forceProbe && currentStatus == network.ReachabilityUnknown:
		// retry very quicky if forceProbe is true *and* we don't know our reachability
		// limit all peers fetch from peerstore to 1 per second.
		nextProbeAfter = 2 * time.Second
		nextProbeAfter = 2 * time.Second
	case currentStatus == network.ReachabilityUnknown,
		as.confidence < maxConfidence,
		currentStatus != network.ReachabilityPublic && receivedInbound:
		// Retry quickly in case:
		// 1. Our reachability is Unknown
		// 2. We don't have enough confidence in our reachability.
		// 3. We're private but we received an inbound connection.
		nextProbeAfter = as.config.retryInterval
	case currentStatus == network.ReachabilityPublic && receivedInbound:
		// We are public and we received an inbound connection recently,
		// wait a little longer
		nextProbeAfter *= 2
		nextProbeAfter = min(nextProbeAfter, maxRefreshInterval)
	}
	nextProbeTime := as.lastProbe.Add(nextProbeAfter)
	if nextProbeTime.Before(now) {
		nextProbeTime = now
	}
	if as.metricsTracer != nil {
		as.metricsTracer.NextProbeTime(nextProbeTime)
	}

	return nextProbeTime.Sub(now)
}

// handleDialResponse updates the current status based on dial response.
func (as *AmbientAutoNAT) handleDialResponse(dialErr error) {
	var observation network.Reachability
	switch {
	case dialErr == nil:
		observation = network.ReachabilityPublic
	case IsDialError(dialErr):
		observation = network.ReachabilityPrivate
	default:
		observation = network.ReachabilityUnknown
	}

	as.recordObservation(observation)
}

// recordObservation updates NAT status and confidence
func (as *AmbientAutoNAT) recordObservation(observation network.Reachability) {

	currentStatus := *as.status.Load()

	if observation == network.ReachabilityPublic {
		changed := false
		if currentStatus != network.ReachabilityPublic {
			// Aggressively switch to public from other states ignoring confidence
			log.Debugf("NAT status is public")

			// we are flipping our NATStatus, so confidence drops to 0
			as.confidence = 0
			if as.service != nil {
				as.service.Enable()
			}
			changed = true
		} else if as.confidence < maxConfidence {
			as.confidence++
		}
		as.status.Store(&observation)
		if changed {
			as.emitStatus()
		}
	} else if observation == network.ReachabilityPrivate {
		if currentStatus != network.ReachabilityPrivate {
			if as.confidence > 0 {
				as.confidence--
			} else {
				log.Debugf("NAT status is private")

				// we are flipping our NATStatus, so confidence drops to 0
				as.confidence = 0
				as.status.Store(&observation)
				if as.service != nil {
					as.service.Disable()
				}
				as.emitStatus()
			}
		} else if as.confidence < maxConfidence {
			as.confidence++
			as.status.Store(&observation)
		}
	} else if as.confidence > 0 {
		// don't just flip to unknown, reduce confidence first
		as.confidence--
	} else {
		log.Debugf("NAT status is unknown")
		as.status.Store(&observation)
		if currentStatus != network.ReachabilityUnknown {
			if as.service != nil {
				as.service.Enable()
			}
			as.emitStatus()
		}
	}
	if as.metricsTracer != nil {
		as.metricsTracer.ReachabilityStatusConfidence(as.confidence)
	}
}

func (as *AmbientAutoNAT) tryProbe(p peer.ID) {
	if p == "" || as.pendingProbes > 5 {
		return
	}
	info := as.host.Peerstore().PeerInfo(p)
	as.recentProbes[p] = time.Now()
	as.pendingProbes++
	go as.probe(&info)
}

func (as *AmbientAutoNAT) probe(pi *peer.AddrInfo) {
	cli := NewAutoNATClient(as.host, as.config.addressFunc, as.metricsTracer)
	ctx, cancel := context.WithTimeout(as.ctx, as.config.requestTimeout)
	defer cancel()

	err := cli.DialBack(ctx, pi.ID)
	log.Debugf("Dialback through peer %s completed: err: %s", pi.ID, err)

	select {
	case as.dialResponses <- err:
	case <-as.ctx.Done():
		return
	}
}

func (as *AmbientAutoNAT) getPeerToProbe() peer.ID {
	peers := as.host.Network().Peers()
	if len(peers) == 0 {
		return ""
	}

	// clean old probes
	fixedNow := time.Now()
	for k, v := range as.recentProbes {
		if fixedNow.Sub(v) > as.throttlePeerPeriod {
			delete(as.recentProbes, k)
		}
	}

	// Shuffle peers
	for n := len(peers); n > 0; n-- {
		randIndex := rand.Intn(n)
		peers[n-1], peers[randIndex] = peers[randIndex], peers[n-1]
	}

	for _, p := range peers {
		info := as.host.Peerstore().PeerInfo(p)
		// Exclude peers which don't support the autonat protocol.
		if proto, err := as.host.Peerstore().SupportsProtocols(p, AutoNATProto); len(proto) == 0 || err != nil {
			continue
		}

		if as.config.dialPolicy.skipPeer(info.Addrs) {
			continue
		}
		return p
	}

	return ""
}

func (as *AmbientAutoNAT) Close() error {
	as.ctxCancel()
	if as.service != nil {
		return as.service.Close()
	}
	<-as.backgroundRunning
	return nil
}

// Status returns the AutoNAT observed reachability status.
func (s *StaticAutoNAT) Status() network.Reachability {
	return s.reachability
}

func (s *StaticAutoNAT) Close() error {
	if s.service != nil {
		return s.service.Close()
	}
	return nil
}
