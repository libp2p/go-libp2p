package mocknet

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"sync"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	ma "github.com/multiformats/go-multiaddr"
)

// peernet implements network.Network
type peernet struct {
	mocknet *mocknet // parent

	peer    peer.ID
	ps      peerstore.Peerstore
	emitter event.Emitter

	// conns are actual live connections between peers.
	// many conns could run over each link.
	// **conns are NOT shared between peers**
	connsByPeer map[peer.ID]map[*conn]struct{}
	connsByLink map[*link]map[*conn]struct{}

	// connection gater to check before dialing or accepting connections. May be nil to allow all.
	gater connmgr.ConnectionGater

	// implement network.Network
	streamHandler network.StreamHandler

	notifmu sync.Mutex
	notifs  map[network.Notifiee]struct{}

	sync.RWMutex
}

// newPeernet constructs a new peernet
func newPeernet(m *mocknet, p peer.ID, opts PeerOptions, bus event.Bus) (*peernet, error) {
	emitter, err := bus.Emitter(&event.EvtPeerConnectednessChanged{})
	if err != nil {
		return nil, err
	}

	n := &peernet{
		mocknet: m,
		peer:    p,
		ps:      opts.ps,
		gater:   opts.gater,
		emitter: emitter,

		connsByPeer: map[peer.ID]map[*conn]struct{}{},
		connsByLink: map[*link]map[*conn]struct{}{},

		notifs: make(map[network.Notifiee]struct{}),
	}

	return n, nil
}

func (pn *peernet) Close() error {
	// close the connections
	for _, c := range pn.allConns() {
		c.Close()
	}
	pn.emitter.Close()
	return pn.ps.Close()
}

// allConns returns all the connections between this peer and others
func (pn *peernet) allConns() []*conn {
	pn.RLock()
	var cs []*conn
	for _, csl := range pn.connsByPeer {
		for c := range csl {
			cs = append(cs, c)
		}
	}
	pn.RUnlock()
	return cs
}

func (pn *peernet) Peerstore() peerstore.Peerstore {
	return pn.ps
}

func (pn *peernet) String() string {
	return fmt.Sprintf("<mock.peernet %s - %d conns>", pn.peer, len(pn.allConns()))
}

// handleNewStream is an internal function to trigger the client's handler
func (pn *peernet) handleNewStream(s network.Stream) {
	pn.RLock()
	handler := pn.streamHandler
	pn.RUnlock()
	if handler != nil {
		go handler(s)
	}
}

// DialPeer attempts to establish a connection to a given peer.
// Respects the context.
func (pn *peernet) DialPeer(ctx context.Context, p peer.ID) (network.Conn, error) {
	return pn.connect(p)
}

func (pn *peernet) connect(p peer.ID) (*conn, error) {
	if p == pn.peer {
		return nil, fmt.Errorf("attempted to dial self %s", p)
	}

	// first, check if we already have live connections
	pn.RLock()
	cs, found := pn.connsByPeer[p]
	if found && len(cs) > 0 {
		var chosen *conn
		for c := range cs { // because cs is a map
			chosen = c // select first
			break
		}
		pn.RUnlock()
		return chosen, nil
	}
	pn.RUnlock()

	if pn.gater != nil && !pn.gater.InterceptPeerDial(p) {
		log.Debugf("gater disallowed outbound connection to peer %s", p)
		return nil, fmt.Errorf("%v connection gater disallowed connection to %v", pn.peer, p)
	}
	log.Debugf("%s (newly) dialing %s", pn.peer, p)

	// ok, must create a new connection. we need a link
	links := pn.mocknet.LinksBetweenPeers(pn.peer, p)
	if len(links) < 1 {
		return nil, fmt.Errorf("%s cannot connect to %s", pn.peer, p)
	}

	// if many links found, how do we select? for now, randomly...
	// this would be an interesting place to test logic that can measure
	// links (network interfaces) and select properly
	l := links[rand.Intn(len(links))]

	log.Debugf("%s dialing %s openingConn", pn.peer, p)
	// create a new connection with link
	return pn.openConn(p, l.(*link))
}

func (pn *peernet) openConn(r peer.ID, l *link) (*conn, error) {
	lc, rc := l.newConnPair(pn)
	addConnPair(pn, rc.net, lc, rc)
	log.Debugf("%s opening connection to %s", pn.LocalPeer(), lc.RemotePeer())
	abort := func() {
		_ = lc.Close()
		_ = rc.Close()
	}
	if pn.gater != nil && !pn.gater.InterceptAddrDial(lc.remote, lc.remoteAddr) {
		abort()
		return nil, fmt.Errorf("%v rejected dial to %v on addr %v", lc.local, lc.remote, lc.remoteAddr)
	}
	if rc.net.gater != nil && !rc.net.gater.InterceptAccept(rc) {
		abort()
		return nil, fmt.Errorf("%v rejected connection from %v", rc.local, rc.remote)
	}
	if err := checkSecureAndUpgrade(network.DirOutbound, pn.gater, lc); err != nil {
		abort()
		return nil, err
	}
	if err := checkSecureAndUpgrade(network.DirInbound, rc.net.gater, rc); err != nil {
		abort()
		return nil, err
	}

	go rc.net.remoteOpenedConn(rc)
	pn.addConn(lc)
	return lc, nil
}

func checkSecureAndUpgrade(dir network.Direction, gater connmgr.ConnectionGater, c *conn) error {
	if gater == nil {
		return nil
	}
	if !gater.InterceptSecured(dir, c.remote, c) {
		return fmt.Errorf("%v rejected secure handshake with %v", c.local, c.remote)
	}
	allow, _ := gater.InterceptUpgraded(c)
	if !allow {
		return fmt.Errorf("%v rejected upgrade with %v", c.local, c.remote)
	}
	return nil
}

// addConnPair adds connection to both peernets at the same time
// must be followerd by pn1.addConn(c1) and pn2.addConn(c2)
func addConnPair(pn1, pn2 *peernet, c1, c2 *conn) {
	var l1, l2 = pn1, pn2 // peernets in lock order
	// bytes compare as string compare is lexicographical
	if bytes.Compare([]byte(l1.LocalPeer()), []byte(l2.LocalPeer())) > 0 {
		l1, l2 = l2, l1
	}

	l1.Lock()
	l2.Lock()

	add := func(pn *peernet, c *conn) {
		_, found := pn.connsByPeer[c.RemotePeer()]
		if !found {
			pn.connsByPeer[c.RemotePeer()] = map[*conn]struct{}{}
		}
		pn.connsByPeer[c.RemotePeer()][c] = struct{}{}

		_, found = pn.connsByLink[c.link]
		if !found {
			pn.connsByLink[c.link] = map[*conn]struct{}{}
		}
		pn.connsByLink[c.link][c] = struct{}{}
	}
	add(pn1, c1)
	add(pn2, c2)

	c1.notifLk.Lock()
	c2.notifLk.Lock()
	l2.Unlock()
	l1.Unlock()
}

func (pn *peernet) remoteOpenedConn(c *conn) {
	log.Debugf("%s accepting connection from %s", pn.LocalPeer(), c.RemotePeer())
	pn.addConn(c)
}

// addConn constructs and adds a connection
// to given remote peer over given link
func (pn *peernet) addConn(c *conn) {
	defer c.notifLk.Unlock()

	pn.notifyAll(func(n network.Notifiee) {
		n.Connected(pn, c)
	})

	pn.emitter.Emit(event.EvtPeerConnectednessChanged{
		Peer:          c.remote,
		Connectedness: network.Connected,
	})
}

// removeConn removes a given conn
func (pn *peernet) removeConn(c *conn) {
	pn.Lock()
	cs, found := pn.connsByLink[c.link]
	if !found || len(cs) < 1 {
		panic(fmt.Sprintf("attempting to remove a conn that doesnt exist %p", c.link))
	}
	delete(cs, c)

	cs, found = pn.connsByPeer[c.remote]
	if !found {
		panic(fmt.Sprintf("attempting to remove a conn that doesnt exist %v", c.remote))
	}
	delete(cs, c)
	pn.Unlock()

	// notify asynchronously to mimic Swarm
	// FIXME: IIRC, we wanted to make notify for Close synchronous
	go func() {
		c.notifLk.Lock()
		defer c.notifLk.Unlock()
		pn.notifyAll(func(n network.Notifiee) {
			n.Disconnected(c.net, c)
		})
	}()

	c.net.emitter.Emit(event.EvtPeerConnectednessChanged{
		Peer:          c.remote,
		Connectedness: network.NotConnected,
	})
}

// LocalPeer the network's LocalPeer
func (pn *peernet) LocalPeer() peer.ID {
	return pn.peer
}

// Peers returns the connected peers
func (pn *peernet) Peers() []peer.ID {
	pn.RLock()
	defer pn.RUnlock()

	peers := make([]peer.ID, 0, len(pn.connsByPeer))
	for _, cs := range pn.connsByPeer {
		for c := range cs {
			peers = append(peers, c.remote)
			break
		}
	}
	return peers
}

// Conns returns all the connections of this peer
func (pn *peernet) Conns() []network.Conn {
	pn.RLock()
	defer pn.RUnlock()

	out := make([]network.Conn, 0, len(pn.connsByPeer))
	for _, cs := range pn.connsByPeer {
		for c := range cs {
			out = append(out, c)
		}
	}
	return out
}

func (pn *peernet) ConnsToPeer(p peer.ID) []network.Conn {
	pn.RLock()
	defer pn.RUnlock()

	cs, found := pn.connsByPeer[p]
	if !found || len(cs) == 0 {
		return nil
	}

	cs2 := make([]network.Conn, 0, len(cs))
	for c := range cs {
		cs2 = append(cs2, c)
	}
	return cs2
}

// ClosePeer connections to peer
func (pn *peernet) ClosePeer(p peer.ID) error {
	pn.RLock()
	cs, found := pn.connsByPeer[p]
	if !found {
		pn.RUnlock()
		return nil
	}

	conns := make([]*conn, 0, len(cs))
	for c := range cs {
		conns = append(conns, c)
	}
	pn.RUnlock()
	for _, c := range conns {
		c.Close()
	}
	return nil
}

// BandwidthTotals returns the total amount of bandwidth transferred
func (pn *peernet) BandwidthTotals() (in uint64, out uint64) {
	// need to implement this. probably best to do it in swarm this time.
	// need a "metrics" object
	return 0, 0
}

// Listen tells the network to start listening on given multiaddrs.
func (pn *peernet) Listen(addrs ...ma.Multiaddr) error {
	pn.Peerstore().AddAddrs(pn.LocalPeer(), addrs, peerstore.PermanentAddrTTL)
	return nil
}

// ListenAddresses returns a list of addresses at which this network listens.
func (pn *peernet) ListenAddresses() []ma.Multiaddr {
	return pn.Peerstore().Addrs(pn.LocalPeer())
}

// InterfaceListenAddresses returns a list of addresses at which this network
// listens. It expands "any interface" addresses (/ip4/0.0.0.0, /ip6/::) to
// use the known local interfaces.
func (pn *peernet) InterfaceListenAddresses() ([]ma.Multiaddr, error) {
	return pn.ListenAddresses(), nil
}

// Connectedness returns a state signaling connection capabilities
// For now only returns Connecter || NotConnected. Expand into more later.
func (pn *peernet) Connectedness(p peer.ID) network.Connectedness {
	pn.Lock()
	defer pn.Unlock()

	cs, found := pn.connsByPeer[p]
	if found && len(cs) > 0 {
		return network.Connected
	}
	return network.NotConnected
}

// NewStream returns a new stream to given peer p.
// If there is no connection to p, attempts to create one.
func (pn *peernet) NewStream(ctx context.Context, p peer.ID) (network.Stream, error) {
	c, err := pn.DialPeer(ctx, p)
	if err != nil {
		return nil, err
	}
	return c.NewStream(ctx)
}

// SetStreamHandler sets the new stream handler on the Network.
// This operation is thread-safe.
func (pn *peernet) SetStreamHandler(h network.StreamHandler) {
	pn.Lock()
	pn.streamHandler = h
	pn.Unlock()
}

// Notify signs up Notifiee to receive signals when events happen
func (pn *peernet) Notify(f network.Notifiee) {
	pn.notifmu.Lock()
	pn.notifs[f] = struct{}{}
	pn.notifmu.Unlock()
}

// StopNotify unregisters Notifiee from receiving signals
func (pn *peernet) StopNotify(f network.Notifiee) {
	pn.notifmu.Lock()
	delete(pn.notifs, f)
	pn.notifmu.Unlock()
}

// notifyAll runs the notification function on all Notifiees
func (pn *peernet) notifyAll(notification func(f network.Notifiee)) {
	pn.notifmu.Lock()
	// notify synchronously to mimic Swarm
	for n := range pn.notifs {
		notification(n)
	}
	pn.notifmu.Unlock()
}

func (pn *peernet) ResourceManager() network.ResourceManager {
	return &network.NullResourceManager{}
}

func (pn *peernet) CanDial(p peer.ID, addr ma.Multiaddr) bool {
	return true
}
