package swarm_test

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	. "github.com/libp2p/go-libp2p/p2p/net/swarm"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestPeers(t *testing.T) {
	ctx := context.Background()
	swarms := makeSwarms(t, 2)
	s1 := swarms[0]
	s2 := swarms[1]

	connect := func(s *Swarm, dst peer.ID, addr ma.Multiaddr) {
		// TODO: make a DialAddr func.
		s.Peerstore().AddAddr(dst, addr, peerstore.PermanentAddrTTL)
		// t.Logf("connections from %s", s.LocalPeer())
		// for _, c := range s.ConnsToPeer(dst) {
		// 	t.Logf("connection from %s to %s: %v", s.LocalPeer(), dst, c)
		// }
		// t.Logf("")
		if _, err := s.DialPeer(ctx, dst); err != nil {
			t.Fatal("error swarm dialing to peer", err)
		}
		// t.Log(s.swarm.Dump())
	}

	connect(s1, s2.LocalPeer(), s2.ListenAddresses()[0])
	require.Eventually(t, func() bool { return len(s2.Peers()) > 0 }, 3*time.Second, 50*time.Millisecond)
	connect(s2, s1.LocalPeer(), s1.ListenAddresses()[0])

	for i := 0; i < 100; i++ {
		connect(s1, s2.LocalPeer(), s2.ListenAddresses()[0])
		connect(s2, s1.LocalPeer(), s1.ListenAddresses()[0])
	}

	for _, s := range swarms {
		log.Infof("%s swarm routing table: %s", s.LocalPeer(), s.Peers())
	}

	test := func(s *Swarm) {
		expect := 1
		actual := len(s.Peers())
		if actual != expect {
			t.Errorf("%s has %d peers, not %d: %v", s.LocalPeer(), actual, expect, s.Peers())
		}
		actual = len(s.Conns())
		if actual != expect {
			t.Errorf("%s has %d conns, not %d: %v", s.LocalPeer(), actual, expect, s.Conns())
		}
	}

	test(s1)
	test(s2)
}
