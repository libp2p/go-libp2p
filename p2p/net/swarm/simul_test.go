package swarm_test

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	. "github.com/libp2p/go-libp2p/p2p/net/swarm"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	"github.com/libp2p/go-libp2p-testing/ci"
	ma "github.com/multiformats/go-multiaddr"
)

func TestSimultOpen(t *testing.T) {
	t.Parallel()
	swarms := makeSwarms(t, 2, swarmt.OptDisableReuseport)

	// connect everyone
	{
		var wg sync.WaitGroup
		connect := func(s *Swarm, dst peer.ID, addr ma.Multiaddr) {
			defer wg.Done()
			// copy for other peer
			log.Debugf("TestSimultOpen: connecting: %s --> %s (%s)", s.LocalPeer(), dst, addr)
			s.Peerstore().AddAddr(dst, addr, peerstore.PermanentAddrTTL)
			if _, err := s.DialPeer(context.Background(), dst); err != nil {
				t.Error("error swarm dialing to peer", err)
			}
		}

		log.Info("Connecting swarms simultaneously.")
		wg.Add(2)
		go connect(swarms[0], swarms[1].LocalPeer(), swarms[1].ListenAddresses()[0])
		go connect(swarms[1], swarms[0].LocalPeer(), swarms[0].ListenAddresses()[0])
		wg.Wait()
	}

	for _, s := range swarms {
		s.Close()
	}
}

func TestSimultOpenMany(t *testing.T) {
	// t.Skip("very very slow")

	addrs := 20
	rounds := 10
	if ci.IsRunning() || runtime.GOOS == "darwin" {
		// osx has a limit of 256 file descriptors
		addrs = 10
		rounds = 5
	}
	subtestSwarm(t, addrs, rounds)
}

func TestSimultOpenFewStress(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	// t.Skip("skipping for another test")
	t.Parallel()

	msgs := 40
	swarms := 2
	rounds := 10
	// rounds := 100

	for i := 0; i < rounds; i++ {
		subtestSwarm(t, swarms, msgs)
		<-time.After(10 * time.Millisecond)
	}
}
