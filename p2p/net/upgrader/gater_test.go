package upgrader_test

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

type testGater struct {
	sync.Mutex

	blockAccept, blockSecured bool
}

var _ connmgr.ConnectionGater = (*testGater)(nil)

func (t *testGater) BlockAccept(block bool) {
	t.Lock()
	defer t.Unlock()

	t.blockAccept = block
}

func (t *testGater) BlockSecured(block bool) {
	t.Lock()
	defer t.Unlock()

	t.blockSecured = block
}

func (t *testGater) InterceptPeerDial(_ peer.ID) (allow bool) {
	panic("not implemented")
}

func (t *testGater) InterceptAddrDial(_ peer.ID, _ ma.Multiaddr) (allow bool) {
	panic("not implemented")
}

func (t *testGater) InterceptAccept(_ network.ConnMultiaddrs) (allow bool) {
	t.Lock()
	defer t.Unlock()

	return !t.blockAccept
}

func (t *testGater) InterceptSecured(_ network.Direction, _ peer.ID, _ network.ConnMultiaddrs) (allow bool) {
	t.Lock()
	defer t.Unlock()

	return !t.blockSecured
}

func (t *testGater) InterceptUpgraded(_ network.Conn) (allow bool, reason control.DisconnectReason) {
	panic("not implemented")
}
