package libp2p

import (
	"github.com/libp2p/go-libp2p-core/connmgr"
	"github.com/libp2p/go-libp2p-core/control"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

// FiltersConnectionGater is an adapter that turns multiaddr.Filter into a
// connmgr.ConnectionGater. It's not intended to be used directly by the user,
// but it can.
type FiltersConnectionGater ma.Filters

var _ connmgr.ConnectionGater = (*FiltersConnectionGater)(nil)

func (f *FiltersConnectionGater) InterceptAddrDial(_ peer.ID, addr ma.Multiaddr) (allow bool) {
	return !(*ma.Filters)(f).AddrBlocked(addr)
}

func (f *FiltersConnectionGater) InterceptPeerDial(p peer.ID) (allow bool) {
	return true
}

func (f *FiltersConnectionGater) InterceptAccept(_ network.ConnMultiaddrs) (allow bool) {
	return true
}

func (f *FiltersConnectionGater) InterceptSecured(_ network.Direction, _ peer.ID, _ network.ConnMultiaddrs) (allow bool) {
	return true
}

func (f *FiltersConnectionGater) InterceptUpgraded(_ network.Conn) (allow bool, reason control.DisconnectReason) {
	return true, 0
}
