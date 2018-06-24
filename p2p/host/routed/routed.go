package routedhost

import (
	"context"
	"fmt"
	"time"

	host "github.com/libp2p/go-libp2p-host"

	logging "github.com/ipfs/go-log"
	ifconnmgr "github.com/libp2p/go-libp2p-interface-connmgr"
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

type Routing interface {
	FindPeer(context.Context, peer.ID) (pstore.PeerInfo, error)
}

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
	if rh.Network().Connectedness(pi.ID) == inet.Connected {
		return nil
	}

	// if we were given some addresses, keep + use them.
	if len(pi.Addrs) > 0 {
		rh.Peerstore().AddAddrs(pi.ID, pi.Addrs, pstore.TempAddrTTL)
	}

	// Check if we have some addresses in our recent memory.
	addrs := rh.Peerstore().Addrs(pi.ID)
	hasSearched := false // Used to determine if we have used to router to search for addresses.

	if len(addrs) < 1 {
		// no addrs? find some with the routing system.
		hasSearched = true
		var err error
		addrs, err = rh.findPeerAddrs(ctx, pi.ID)
		if err != nil {
			return err
		}
	}

	// if we're here, we got some addrs. let's use our wrapped host to connect.
	pi.Addrs = addrs
	connErr := rh.host.Connect(ctx, pi)

	// If we could not connect with the addresses that we already had, we use
	// the router to search for new addresses. Unless we have already searched for
	// new addresses, then there is no reason to search again.
	if connErr != nil && hasSearched {
		return connErr
	} else if connErr != nil {
		newAddrs, findErr := rh.findPeerAddrs(ctx, pi.ID)

		if findErr != nil {
			return findErr // We could not find any addresses.
		}

		// If we have found new addresses we attempt to connect.
		if hasNewAddr(pi.Addrs, newAddrs) {
			pi.Addrs = addrs
			return rh.host.Connect(ctx, pi)
		}
	}

	return nil
}

func hasNewAddr(oldAddrs, newAddrs []ma.Multiaddr) bool {
	for _, addr := range newAddrs {
		if !hasAddr(addr, oldAddrs) {
			return true
		}
	}

	return false
}

func hasAddr(addr ma.Multiaddr, addrs []ma.Multiaddr) bool {
	for _, addr2 := range addrs {
		if addr2 == addr {
			return true
		}
	}

	return false
}

func (rh *RoutedHost) findPeerAddrs(ctx context.Context, id peer.ID) ([]ma.Multiaddr, error) {
	pi, err := rh.route.FindPeer(ctx, id)
	if err != nil {
		return nil, err // couldnt find any :(
	}

	if pi.ID != id {
		err = fmt.Errorf("routing failure: provided addrs for different peer")
		logRoutingErrDifferentPeers(ctx, id, pi.ID, err)
		return nil, err
	}

	return pi.Addrs, nil
}

func logRoutingErrDifferentPeers(ctx context.Context, wanted, got peer.ID, err error) {
	lm := make(lgbl.DeferredMap)
	lm["error"] = err
	lm["wantedPeer"] = func() interface{} { return wanted.Pretty() }
	lm["gotPeer"] = func() interface{} { return got.Pretty() }
	log.Event(ctx, "routingError", lm)
}

func (rh *RoutedHost) ID() peer.ID {
	return rh.host.ID()
}

func (rh *RoutedHost) Peerstore() pstore.Peerstore {
	return rh.host.Peerstore()
}

func (rh *RoutedHost) Addrs() []ma.Multiaddr {
	return rh.host.Addrs()
}

func (rh *RoutedHost) Network() inet.Network {
	return rh.host.Network()
}

func (rh *RoutedHost) Mux() *msmux.MultistreamMuxer {
	return rh.host.Mux()
}

func (rh *RoutedHost) SetStreamHandler(pid protocol.ID, handler inet.StreamHandler) {
	rh.host.SetStreamHandler(pid, handler)
}

func (rh *RoutedHost) SetStreamHandlerMatch(pid protocol.ID, m func(string) bool, handler inet.StreamHandler) {
	rh.host.SetStreamHandlerMatch(pid, m, handler)
}

func (rh *RoutedHost) RemoveStreamHandler(pid protocol.ID) {
	rh.host.RemoveStreamHandler(pid)
}

func (rh *RoutedHost) NewStream(ctx context.Context, p peer.ID, pids ...protocol.ID) (inet.Stream, error) {
	// Ensure we have a connection, with peer addresses resolved by the routing system (#207)
	// It is not sufficient to let the underlying host connect, it will most likely not have
	// any addresses for the peer without any prior connections.
	err := rh.Connect(ctx, pstore.PeerInfo{ID: p})
	if err != nil {
		return nil, err
	}

	return rh.host.NewStream(ctx, p, pids...)
}
func (rh *RoutedHost) Close() error {
	// no need to close IpfsRouting. we dont own it.
	return rh.host.Close()
}
func (rh *RoutedHost) ConnManager() ifconnmgr.ConnManager {
	return rh.host.ConnManager()
}

var _ (host.Host) = (*RoutedHost)(nil)
