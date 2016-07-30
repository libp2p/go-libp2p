// package swarm implements a connection muxer with a pair of channels
// to synchronize all network communication.
package swarm

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"

	onion "github.com/david415/ipfs-onion-transport"
	peer "github.com/ipfs/go-libp2p-peer"
	pstore "github.com/ipfs/go-libp2p-peerstore"
	transport "github.com/ipfs/go-libp2p-transport"
	metrics "github.com/ipfs/go-libp2p/p2p/metrics"
	mconn "github.com/ipfs/go-libp2p/p2p/metrics/conn"
	inet "github.com/ipfs/go-libp2p/p2p/net"
	conn "github.com/ipfs/go-libp2p/p2p/net/conn"
	filter "github.com/ipfs/go-libp2p/p2p/net/filter"
	addrutil "github.com/ipfs/go-libp2p/p2p/net/swarm/addr"
	logging "github.com/ipfs/go-log"
	ma "github.com/jbenet/go-multiaddr"
	ps "github.com/jbenet/go-peerstream"
	pst "github.com/jbenet/go-stream-muxer"
	"github.com/jbenet/goprocess"
	goprocessctx "github.com/jbenet/goprocess/context"
	psmss "github.com/whyrusleeping/go-smux-multistream"
	spdy "github.com/whyrusleeping/go-smux-spdystream"
	yamux "github.com/whyrusleeping/go-smux-yamux"
	mafilter "github.com/whyrusleeping/multiaddr-filter"
	context "golang.org/x/net/context"
	"golang.org/x/net/proxy"
)

var log = logging.Logger("swarm2")

var PSTransport pst.Transport

func init() {
	msstpt := psmss.NewBlankTransport()

	ymxtpt := &yamux.Transport{
		AcceptBacklog:          8192,
		ConnectionWriteTimeout: time.Second * 10,
		KeepAliveInterval:      time.Second * 30,
		EnableKeepAlive:        true,
		MaxStreamWindowSize:    uint32(1024 * 512),
		LogOutput:              ioutil.Discard,
	}

	msstpt.AddTransport("/yamux/1.0.0", ymxtpt)
	msstpt.AddTransport("/spdy/3.1.0", spdy.Transport)

	// allow overriding of muxer preferences
	if prefs := os.Getenv("LIBP2P_MUX_PREFS"); prefs != "" {
		msstpt.OrderPreference = strings.Fields(prefs)
	}

	PSTransport = msstpt
}

// Swarm is a connection muxer, allowing connections to other peers to
// be opened and closed, while still using the same Chan for all
// communication. The Chan sends/receives Messages, which note the
// destination or source Peer.
//
// Uses peerstream.Swarm
type Swarm struct {
	swarm *ps.Swarm
	local peer.ID
	peers pstore.Peerstore
	connh ConnHandler

	dsync dialsync
	backf dialbackoff
	dialT time.Duration // mainly for tests

	dialer *conn.Dialer

	notifmu sync.RWMutex
	notifs  map[inet.Notifiee]ps.Notifiee

	transports []transport.Transport

	// filters for addresses that shouldnt be dialed
	Filters *filter.Filters

	// file descriptor rate limited
	fdRateLimit chan struct{}

	proc goprocess.Process
	ctx  context.Context
	bwc  metrics.Reporter

	limiter *dialLimiter
}

// NewSwarm constructs a Swarm, with a Chan.
func NewSwarm(ctx context.Context, listenAddrs []ma.Multiaddr,
	local peer.ID, peers pstore.Peerstore, bwc metrics.Reporter) (*Swarm, error) {

	listenAddrs, err := filterAddrs(listenAddrs)
	if err != nil {
		return nil, err
	}

	wrap := func(c transport.Conn) transport.Conn {
		return mconn.WrapConn(bwc, c)
	}

	// POC setup for onion transport

	// The Tor socks user and password can be set to random values
	// to tell little-t tor to make a new circuit.
	// It's probably OK to leave them blank because when connecting
	// to a new onion a new tor circuit will have to be created anyway.
	auth := proxy.Auth{
		User:     "",
		Password: "",
	}

	// XXX FIXME: The tor control net and addr should be user specified!
	// Note: for sandboxing purposes UNIX domain sockets are preferred instead of TCP.
	controlNet := "tcp"
	controlAddr := "127.0.0.1:9051"
	onionTransport := NewOnionTransport(controlNet, controlAddr, nil, &auth)

	s := &Swarm{
		swarm:  ps.NewSwarm(PSTransport),
		local:  local,
		peers:  peers,
		ctx:    ctx,
		dialT:  DialTimeout,
		notifs: make(map[inet.Notifiee]ps.Notifiee),
		transports: []transport.Transport{
			transport.NewTCPTransport(),
			transport.NewUtpTransport(),
			onionTransport,
		},
		bwc:         bwc,
		fdRateLimit: make(chan struct{}, concurrentFdDials),
		Filters:     filter.NewFilters(),
		dialer:      conn.NewDialer(local, peers.PrivKey(local), wrap),
	}

	s.limiter = newDialLimiter(s.dialAddr)

	// configure Swarm
	s.proc = goprocessctx.WithContextAndTeardown(ctx, s.teardown)
	s.SetConnHandler(nil) // make sure to setup our own conn handler.

	err = s.setupInterfaces(listenAddrs)
	if err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Swarm) teardown() error {
	return s.swarm.Close()
}

func (s *Swarm) AddAddrFilter(f string) error {
	m, err := mafilter.NewMask(f)
	if err != nil {
		return err
	}

	s.Filters.AddDialFilter(m)
	return nil
}

func filterAddrs(listenAddrs []ma.Multiaddr) ([]ma.Multiaddr, error) {
	if len(listenAddrs) > 0 {
		filtered := addrutil.FilterUsableAddrs(listenAddrs)
		if len(filtered) < 1 {
			return nil, fmt.Errorf("swarm cannot use any addr in: %s", listenAddrs)
		}
		listenAddrs = filtered
	}

	return listenAddrs, nil
}

func (s *Swarm) Listen(addrs ...ma.Multiaddr) error {
	addrs, err := filterAddrs(addrs)
	if err != nil {
		return err
	}

	return s.setupInterfaces(addrs)
}

// Process returns the Process of the swarm
func (s *Swarm) Process() goprocess.Process {
	return s.proc
}

// Context returns the context of the swarm
func (s *Swarm) Context() context.Context {
	return s.ctx
}

// Close stops the Swarm.
func (s *Swarm) Close() error {
	return s.proc.Close()
}

// StreamSwarm returns the underlying peerstream.Swarm
func (s *Swarm) StreamSwarm() *ps.Swarm {
	return s.swarm
}

// SetConnHandler assigns the handler for new connections.
// See peerstream. You will rarely use this. See SetStreamHandler
func (s *Swarm) SetConnHandler(handler ConnHandler) {

	// handler is nil if user wants to clear the old handler.
	if handler == nil {
		s.swarm.SetConnHandler(func(psconn *ps.Conn) {
			s.connHandler(psconn)
		})
		return
	}

	s.swarm.SetConnHandler(func(psconn *ps.Conn) {
		// sc is nil if closed in our handler.
		if sc := s.connHandler(psconn); sc != nil {
			// call the user's handler. in a goroutine for sync safety.
			go handler(sc)
		}
	})
}

// SetStreamHandler assigns the handler for new streams.
// See peerstream.
func (s *Swarm) SetStreamHandler(handler inet.StreamHandler) {
	s.swarm.SetStreamHandler(func(s *ps.Stream) {
		handler(wrapStream(s))
	})
}

// NewStreamWithPeer creates a new stream on any available connection to p
func (s *Swarm) NewStreamWithPeer(ctx context.Context, p peer.ID) (*Stream, error) {
	// if we have no connections, try connecting.
	if len(s.ConnectionsToPeer(p)) == 0 {
		log.Debug("Swarm: NewStreamWithPeer no connections. Attempting to connect...")
		if _, err := s.Dial(ctx, p); err != nil {
			return nil, err
		}
	}
	log.Debug("Swarm: NewStreamWithPeer...")

	// TODO: think about passing a context down to NewStreamWithGroup
	st, err := s.swarm.NewStreamWithGroup(p)
	return wrapStream(st), err
}

// StreamsWithPeer returns all the live Streams to p
func (s *Swarm) StreamsWithPeer(p peer.ID) []*Stream {
	return wrapStreams(ps.StreamsWithGroup(p, s.swarm.Streams()))
}

// ConnectionsToPeer returns all the live connections to p
func (s *Swarm) ConnectionsToPeer(p peer.ID) []*Conn {
	return wrapConns(ps.ConnsWithGroup(p, s.swarm.Conns()))
}

// Connections returns a slice of all connections.
func (s *Swarm) Connections() []*Conn {
	return wrapConns(s.swarm.Conns())
}

// CloseConnection removes a given peer from swarm + closes the connection
func (s *Swarm) CloseConnection(p peer.ID) error {
	conns := s.swarm.ConnsWithGroup(p) // boom.
	for _, c := range conns {
		c.Close()
	}
	return nil
}

// Peers returns a copy of the set of peers swarm is connected to.
func (s *Swarm) Peers() []peer.ID {
	conns := s.Connections()

	seen := make(map[peer.ID]struct{})
	peers := make([]peer.ID, 0, len(conns))
	for _, c := range conns {
		p := c.RemotePeer()
		if _, found := seen[p]; found {
			continue
		}

		seen[p] = struct{}{}
		peers = append(peers, p)
	}
	return peers
}

// LocalPeer returns the local peer swarm is associated to.
func (s *Swarm) LocalPeer() peer.ID {
	return s.local
}

func (s *Swarm) Backoff() *dialbackoff {
	return &s.backf
}

// notifyAll sends a signal to all Notifiees
func (s *Swarm) notifyAll(notify func(inet.Notifiee)) {
	s.notifmu.RLock()
	for f := range s.notifs {
		go notify(f)
	}
	s.notifmu.RUnlock()
}

// Notify signs up Notifiee to receive signals when events happen
func (s *Swarm) Notify(f inet.Notifiee) {
	// wrap with our notifiee, to translate function calls
	n := &ps2netNotifee{net: (*Network)(s), not: f}

	s.notifmu.Lock()
	s.notifs[f] = n
	s.notifmu.Unlock()

	// register for notifications in the peer swarm.
	s.swarm.Notify(n)
}

// StopNotify unregisters Notifiee fromr receiving signals
func (s *Swarm) StopNotify(f inet.Notifiee) {
	s.notifmu.Lock()
	n, found := s.notifs[f]
	if found {
		delete(s.notifs, f)
	}
	s.notifmu.Unlock()

	if found {
		s.swarm.StopNotify(n)
	}
}

type ps2netNotifee struct {
	net *Network
	not inet.Notifiee
}

func (n *ps2netNotifee) Connected(c *ps.Conn) {
	n.not.Connected(n.net, inet.Conn((*Conn)(c)))
}

func (n *ps2netNotifee) Disconnected(c *ps.Conn) {
	n.not.Disconnected(n.net, inet.Conn((*Conn)(c)))
}

func (n *ps2netNotifee) OpenedStream(s *ps.Stream) {
	n.not.OpenedStream(n.net, inet.Stream((*Stream)(s)))
}

func (n *ps2netNotifee) ClosedStream(s *ps.Stream) {
	n.not.ClosedStream(n.net, inet.Stream((*Stream)(s)))
}
