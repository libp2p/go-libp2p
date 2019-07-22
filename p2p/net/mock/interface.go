// Package mocknet provides a mock net.Network to test with.
//
// - a Mocknet has many network.Networks
// - a Mocknet has many Links
// - a Link joins two network.Networks
// - network.Conns and network.Streams are created by network.Networks
package mocknet

import (
	"io"
	"time"

	ic "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"

	ma "github.com/multiformats/go-multiaddr"
)

type Mocknet interface {

	// GenPeer generates a peer and its network.Network in the Mocknet
	GenPeer() (host.Host, error)

	// AddPeer adds an existing peer. we need both a privkey and addr.
	// ID is derived from PrivKey
	AddPeer(ic.PrivKey, ma.Multiaddr) (host.Host, error)
	AddPeerWithPeerstore(peer.ID, peerstore.Peerstore) (host.Host, error)

	// retrieve things (with randomized iteration order)
	Peers() []peer.ID
	Net(peer.ID) network.Network
	Nets() []network.Network
	Host(peer.ID) host.Host
	Hosts() []host.Host
	Links() LinkMap
	LinksBetweenPeers(a, b peer.ID) []Link
	LinksBetweenNets(a, b network.Network) []Link

	// Links are the **ability to connect**.
	// think of Links as the physical medium.
	// For p1 and p2 to connect, a link must exist between them.
	// (this makes it possible to test dial failures, and
	// things like relaying traffic)
	LinkPeers(peer.ID, peer.ID) (Link, error)
	LinkNets(network.Network, network.Network) (Link, error)
	Unlink(Link) error
	UnlinkPeers(peer.ID, peer.ID) error
	UnlinkNets(network.Network, network.Network) error

	// LinkDefaults are the default options that govern links
	// if they do not have their own option set.
	SetLinkDefaults(LinkOptions)
	LinkDefaults() LinkOptions

	// Connections are the usual. Connecting means Dialing.
	// **to succeed, peers must be linked beforehand**
	ConnectPeers(peer.ID, peer.ID) (network.Conn, error)
	ConnectNets(network.Network, network.Network) (network.Conn, error)
	DisconnectPeers(peer.ID, peer.ID) error
	DisconnectNets(network.Network, network.Network) error
	LinkAll() error
	ConnectAllButSelf() error
}

// LinkOptions are used to change aspects of the links.
// Sorry but they dont work yet :(
type LinkOptions struct {
	Latency   time.Duration
	Bandwidth float64 // in bytes-per-second
	// we can make these values distributions down the road.
}

// Link represents the **possibility** of a connection between
// two peers. Think of it like physical network links. Without
// them, the peers can try and try but they won't be able to
// connect. This allows constructing topologies where specific
// nodes cannot talk to each other directly. :)
type Link interface {
	Networks() []network.Network
	Peers() []peer.ID

	SetOptions(LinkOptions)
	Options() LinkOptions

	// Metrics() Metrics
}

// LinkMap is a 3D map to give us an easy way to track links.
// (wow, much map. so data structure. how compose. ahhh pointer)
type LinkMap map[string]map[string]map[Link]struct{}

// Printer lets you inspect things :)
type Printer interface {
	// MocknetLinks shows the entire Mocknet's link table :)
	MocknetLinks(mn Mocknet)
	NetworkConns(ni network.Network)
}

// PrinterTo returns a Printer ready to write to w.
func PrinterTo(w io.Writer) Printer {
	return &printer{w}
}
