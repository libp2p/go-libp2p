package autonatv2

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"golang.org/x/exp/rand"
)

const (
	ServiceName      = "libp2p.autonatv2"
	DialBackProtocol = "/libp2p/autonat/2/dial-back"
	DialProtocol     = "/libp2p/autonat/2/dial-request"

	maxMsgSize            = 8192
	streamTimeout         = 15 * time.Second
	dialBackStreamTimeout = 5 * time.Second
	dialBackDialTimeout   = 10 * time.Second
	dialBackMaxMsgSize    = 1024
	minHandshakeSizeBytes = 30_000 // for amplification attack prevention
	maxHandshakeSizeBytes = 100_000
	// maxPeerAddresses is the number of addresses in a dial request the server
	// will inspect, rest are ignored.
	maxPeerAddresses = 50
)

var (
	ErrNoValidPeers = errors.New("no valid peers for autonat v2")
	ErrDialRefused  = errors.New("dial refused")

	log = logging.Logger("autonatv2")
)

// Request is the request to verify reachability of a single address
type Request struct {
	// Addr is the multiaddr to verify
	Addr ma.Multiaddr
	// SendDialData indicates whether to send dial data if the server requests it for Addr
	SendDialData bool
}

// Result is the result of the CheckReachability call
type Result struct {
	// Addr is the dialed address
	Addr ma.Multiaddr
	// Reachability of the dialed address
	Reachability network.Reachability
	// Status is the outcome of the dialback
	Status pb.DialStatus
}

// AutoNAT implements the AutoNAT v2 client and server.
// Users can check reachability for their addresses using the CheckReachability method.
// The server provides amplification attack prevention and rate limiting.
type AutoNAT struct {
	host host.Host

	// for cleanly closing
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	srv *server
	cli *client

	mx    sync.Mutex
	peers *peersMap
	// allowPrivateAddrs enables using private and localhost addresses for reachability checks.
	// This is only useful for testing.
	allowPrivateAddrs bool
}

// New returns a new AutoNAT instance.
// host and dialerHost should have the same dialing capabilities. In case the host doesn't support
// a transport, dial back requests for address for that transport will be ignored.
func New(host host.Host, dialerHost host.Host, opts ...AutoNATOption) (*AutoNAT, error) {
	s := defaultSettings()
	for _, o := range opts {
		if err := o(s); err != nil {
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	an := &AutoNAT{
		host:              host,
		ctx:               ctx,
		cancel:            cancel,
		srv:               newServer(host, dialerHost, s),
		cli:               newClient(host),
		allowPrivateAddrs: s.allowPrivateAddrs,
		peers:             newPeersMap(),
	}
	return an, nil
}

func (an *AutoNAT) background(sub event.Subscription) {
	for {
		select {
		case <-an.ctx.Done():
			sub.Close()
			an.wg.Done()
			return
		case e := <-sub.Out():
			switch evt := e.(type) {
			case event.EvtPeerProtocolsUpdated:
				an.updatePeer(evt.Peer)
			case event.EvtPeerConnectednessChanged:
				an.updatePeer(evt.Peer)
			case event.EvtPeerIdentificationCompleted:
				an.updatePeer(evt.Peer)
			}
		}
	}
}

func (an *AutoNAT) Start() error {
	// Listen on event.EvtPeerProtocolsUpdated, event.EvtPeerConnectednessChanged
	// event.EvtPeerIdentificationCompleted to maintain our set of autonat supporting peers.
	sub, err := an.host.EventBus().Subscribe([]interface{}{
		new(event.EvtPeerProtocolsUpdated),
		new(event.EvtPeerConnectednessChanged),
		new(event.EvtPeerIdentificationCompleted),
	})
	if err != nil {
		return fmt.Errorf("event subscription failed: %w", err)
	}
	an.cli.Start()
	an.srv.Start()

	an.wg.Add(1)
	go an.background(sub)
	return nil
}

func (an *AutoNAT) Close() {
	an.cancel()
	an.wg.Wait()
	an.srv.Close()
	an.cli.Close()
	an.peers = nil
}

// GetReachability makes a single dial request for checking reachability for requested addresses
func (an *AutoNAT) GetReachability(ctx context.Context, reqs []Request) (Result, error) {
	if !an.allowPrivateAddrs {
		for _, r := range reqs {
			if !manet.IsPublicAddr(r.Addr) {
				return Result{}, fmt.Errorf("private address cannot be verified by autonatv2: %s", r.Addr)
			}
		}
	}
	an.mx.Lock()
	p := an.peers.GetRand()
	an.mx.Unlock()
	if p == "" {
		return Result{}, ErrNoValidPeers
	}

	res, err := an.cli.GetReachability(ctx, p, reqs)
	if err != nil {
		log.Debugf("reachability check with %s failed, err: %s", p, err)
		return Result{}, fmt.Errorf("reachability check with %s failed: %w", p, err)
	}
	log.Debugf("reachability check with %s successful", p)
	return res, nil
}

func (an *AutoNAT) updatePeer(p peer.ID) {
	an.mx.Lock()
	defer an.mx.Unlock()

	// There are no ordering gurantees between identify and swarm events. Check peerstore
	// and swarm for the current state
	protos, err := an.host.Peerstore().SupportsProtocols(p, DialProtocol)
	connectedness := an.host.Network().Connectedness(p)
	if err == nil && slices.Contains(protos, DialProtocol) && connectedness == network.Connected {
		an.peers.Put(p)
	} else {
		an.peers.Delete(p)
	}
}

// peersMap provides random access to a set of peers. This is useful when the map iteration order is
// not sufficiently random.
type peersMap struct {
	peerIdx map[peer.ID]int
	peers   []peer.ID
}

func newPeersMap() *peersMap {
	return &peersMap{
		peerIdx: make(map[peer.ID]int),
		peers:   make([]peer.ID, 0),
	}
}

func (p *peersMap) GetRand() peer.ID {
	if len(p.peers) == 0 {
		return ""
	}
	return p.peers[rand.Intn(len(p.peers))]
}

func (p *peersMap) Put(pid peer.ID) {
	if _, ok := p.peerIdx[pid]; ok {
		return
	}
	p.peers = append(p.peers, pid)
	p.peerIdx[pid] = len(p.peers) - 1
}

func (p *peersMap) Delete(pid peer.ID) {
	idx, ok := p.peerIdx[pid]
	if !ok {
		return
	}
	p.peers[idx] = p.peers[len(p.peers)-1]
	p.peerIdx[p.peers[idx]] = idx
	p.peers = p.peers[:len(p.peers)-1]
	delete(p.peerIdx, pid)
}
