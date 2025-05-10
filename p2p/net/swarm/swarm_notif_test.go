//go:build !js
// +build !js

package swarm_test

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	. "github.com/libp2p/go-libp2p/p2p/net/swarm"

	ma "github.com/multiformats/go-multiaddr"

	"github.com/stretchr/testify/require"
)

func TestNotifications(t *testing.T) {
	const swarmSize = 5

	notifiees := make([]*netNotifiee, swarmSize)

	swarms := makeSwarms(t, swarmSize)
	defer func() {
		for i, s := range swarms {
			select {
			case <-notifiees[i].listenClose:
				t.Error("should not have been closed")
			default:
			}
			require.NoError(t, s.Close())
			select {
			case <-notifiees[i].listenClose:
			default:
				t.Error("expected a listen close notification")
			}
		}
	}()

	const timeout = 5 * time.Second

	// signup notifs
	for i, swarm := range swarms {
		n := newNetNotifiee(swarmSize)
		swarm.Notify(n)
		notifiees[i] = n
	}

	connectSwarms(t, context.Background(), swarms)

	time.Sleep(50 * time.Millisecond)
	// should've gotten 5 by now.

	// test everyone got the correct connection opened calls
	for i, s := range swarms {
		n := notifiees[i]
		notifs := make(map[peer.ID][]network.Conn)
		for j, s2 := range swarms {
			if i == j {
				continue
			}

			// this feels a little sketchy, but its probably okay
			for len(s.ConnsToPeer(s2.LocalPeer())) != len(notifs[s2.LocalPeer()]) {
				select {
				case c := <-n.connected:
					nfp := notifs[c.RemotePeer()]
					notifs[c.RemotePeer()] = append(nfp, c)
				case <-time.After(timeout):
					t.Fatal("timeout")
				}
			}
		}

		for p, cons := range notifs {
			expect := s.ConnsToPeer(p)
			if len(expect) != len(cons) {
				t.Fatal("got different number of connections")
			}

			for _, c := range cons {
				var found bool
				for _, c2 := range expect {
					if c == c2 {
						found = true
						break
					}
				}

				if !found {
					t.Fatal("connection not found!")
				}
			}
		}
	}

	complement := func(c network.Conn) (*Swarm, *netNotifiee, *Conn) {
		for i, s := range swarms {
			for _, c2 := range s.Conns() {
				if c.LocalMultiaddr().Equal(c2.RemoteMultiaddr()) &&
					c2.LocalMultiaddr().Equal(c.RemoteMultiaddr()) {
					return s, notifiees[i], c2.(*Conn)
				}
			}
		}
		t.Fatal("complementary conn not found", c)
		return nil, nil, nil
	}

	// close conns
	for i, s := range swarms {
		n := notifiees[i]
		for _, c := range s.Conns() {
			_, n2, c2 := complement(c)
			c.Close()
			c2.Close()

			var c3, c4 network.Conn
			select {
			case c3 = <-n.disconnected:
			case <-time.After(timeout):
				t.Fatal("timeout")
			}
			if c != c3 {
				t.Fatal("got incorrect conn", c, c3)
			}

			select {
			case c4 = <-n2.disconnected:
			case <-time.After(timeout):
				t.Fatal("timeout")
			}
			if c2 != c4 {
				t.Fatal("got incorrect conn", c, c2)
			}
		}
	}
}

type netNotifiee struct {
	listen       chan ma.Multiaddr
	listenClose  chan ma.Multiaddr
	connected    chan network.Conn
	disconnected chan network.Conn
}

func newNetNotifiee(buffer int) *netNotifiee {
	return &netNotifiee{
		listen:       make(chan ma.Multiaddr, buffer),
		listenClose:  make(chan ma.Multiaddr, buffer),
		connected:    make(chan network.Conn, buffer),
		disconnected: make(chan network.Conn, buffer),
	}
}

func (nn *netNotifiee) Listen(n network.Network, a ma.Multiaddr) {
	nn.listen <- a
}
func (nn *netNotifiee) ListenClose(n network.Network, a ma.Multiaddr) {
	nn.listenClose <- a
}
func (nn *netNotifiee) Connected(n network.Network, v network.Conn) {
	nn.connected <- v
}
func (nn *netNotifiee) Disconnected(n network.Network, v network.Conn) {
	nn.disconnected <- v
}
