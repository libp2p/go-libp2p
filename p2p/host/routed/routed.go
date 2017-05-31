// Package routedhost provides a wrapper for libp2p Host and a generic Routing
// mechanism, which allow them to discover hosts which are not in their
// peerstores.
package routedhost

import (
	"context"
	"fmt"
	"time"

	host "github.com/libp2p/go-libp2p-host"

	logging "github.com/ipfs/go-log"
	lgbl "github.com/libp2p/go-libp2p-loggables"
	inet "github.com/libp2p/go-libp2p-net"
	peer "github.com/libp2p/go-libp2p-peer"
	pstore "github.com/libp2p/go-libp2p-peerstore"
	protocol "github.com/libp2p/go-libp2p-protocol"
	ma "github.com/multiformats/go-multiaddr"
	msmux "github.com/multiformats/go-multistream"
)

var log = logging.Logger("routedhost")

// AddressTTL is the expiry time for our addresses.
// We expire them quickly.
const AddressTTL = time.Second * 10

// RoutedHost is a p2p Host that includes a routing system.
// This allows the Host to find the addresses for peers when
// it does not have them.
type RoutedHost struct {
	host  host.Host // embedded other host.
	route Routing
}

// The Routing interface allows to wrap any peer discovery implementations
type Routing interface {
	FindPeer(context.Context, peer.ID) (pstore.PeerInfo, error)
}

// Wrap creates a RoutedHost by wrapping a regular Host and a Routing
// implementation.
func Wrap(h host.Host, r Routing) *RoutedHost {
	return &RoutedHost{h, r}
}

// Connect ensures there is a connection between this host and the peer with
// given peer.ID. See (host.Host).Connect for more information.
//
// RoutedHost's Connect differs in that if the host has no addresses for a
// given peer, it will use its routing system to try to find some.
func (rh *RoutedHost) Connect(ctx context.Context, pi pstore.PeerInfo) error {
	// first, check if we're already connected.
	if len(rh.Network().ConnsToPeer(pi.ID)) > 0 {
		return nil
	}

	// if we were given some addresses, keep + use them.
	if len(pi.Addrs) > 0 {
		rh.Peerstore().AddAddrs(pi.ID, pi.Addrs, pstore.TempAddrTTL)
	}

	// Check if we have some addresses in our recent memory.
	addrs := rh.Peerstore().Addrs(pi.ID)
	if len(addrs) < 1 {

		// no addrs? find some with the routing system.
		pi2, err := rh.route.FindPeer(ctx, pi.ID)
		if err != nil {
			return err // couldnt find any :(
		}
		if pi2.ID != pi.ID {
			err = fmt.Errorf("routing failure: provided addrs for different peer")
			logRoutingErrDifferentPeers(ctx, pi.ID, pi2.ID, err)
			return err
		}
		addrs = pi2.Addrs
	}

	// if we're here, we got some addrs. let's use our wrapped host to connect.
	pi.Addrs = addrs
	return rh.host.Connect(ctx, pi)
}

func logRoutingErrDifferentPeers(ctx context.Context, wanted, got peer.ID, err error) {
	lm := make(lgbl.DeferredMap)
	lm["error"] = err
	lm["wantedPeer"] = func() interface{} { return wanted.Pretty() }
	lm["gotPeer"] = func() interface{} { return got.Pretty() }
	log.Event(ctx, "routingError", lm)
}

// ID returns the (local) peer.ID associated with this Host
func (rh *RoutedHost) ID() peer.ID {
	return rh.host.ID()
}

// Peerstore returns the Host's repository of Peer Addresses and Keys.
func (rh *RoutedHost) Peerstore() pstore.Peerstore {
	return rh.host.Peerstore()
}

// Addrs returns all the addresses of BasicHost at this moment in time.
func (rh *RoutedHost) Addrs() []ma.Multiaddr {
	return rh.host.Addrs()
}

// Network returns the Network interface of the Host
func (rh *RoutedHost) Network() inet.Network {
	return rh.host.Network()
}

// Mux returns the Mux multiplexing incoming streams to protocol handlers.
func (rh *RoutedHost) Mux() *msmux.MultistreamMuxer {
	return rh.host.Mux()
}

// SetStreamHandler sets the protocol handler on the Host's Mux.
// This is equivalent to:
//   host.Mux().SetHandler(proto, handler)
// (Threadsafe)
func (rh *RoutedHost) SetStreamHandler(pid protocol.ID, handler inet.StreamHandler) {
	rh.host.SetStreamHandler(pid, handler)
}

// SetStreamHandlerMatch sets the protocol handler on the Host's Mux
// using a matching function to do protocol comparisons
func (rh *RoutedHost) SetStreamHandlerMatch(pid protocol.ID, m func(string) bool, handler inet.StreamHandler) {
	rh.host.SetStreamHandlerMatch(pid, m, handler)
}

// RemoveStreamHandler removes the handler matching the given protocol ID.
func (rh *RoutedHost) RemoveStreamHandler(pid protocol.ID) {
	rh.host.RemoveStreamHandler(pid)
}

// NewStream opens a new stream to given peer p, and writes a p2p/protocol
// header with given protocol.ID. If there is no connection to p, attempts
// to create one. If ProtocolID is "", writes no header.
// (Threadsafe)
func (rh *RoutedHost) NewStream(ctx context.Context, p peer.ID, pids ...protocol.ID) (inet.Stream, error) {
	return rh.host.NewStream(ctx, p, pids...)
}

// Close shuts down the Host's services (network, etc).
func (rh *RoutedHost) Close() error {
	// no need to close IpfsRouting. we dont own it.
	return rh.host.Close()
}

var _ (host.Host) = (*RoutedHost)(nil)
