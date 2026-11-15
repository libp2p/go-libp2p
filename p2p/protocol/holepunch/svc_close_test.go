package holepunch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	ma "github.com/multiformats/go-multiaddr"
)

type closeTestHost struct {
	host.Host
}

func (closeTestHost) ID() peer.ID                                         { return peer.ID("close-test-peer") }
func (closeTestHost) Addrs() []ma.Multiaddr                               { return nil }
func (closeTestHost) SetStreamHandler(protocol.ID, network.StreamHandler) {}
func (closeTestHost) RemoveStreamHandler(protocol.ID)                     {}

func TestWaitForPublicAddr_NoDeadlockOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	enteredListenAddrs := make(chan struct{})
	releaseListenAddrs := make(chan struct{})
	done := make(chan struct{})

	s := &Service{
		ctx:                ctx,
		ctxCancel:          cancel,
		host:               closeTestHost{},
		hasPublicAddrsChan: make(chan struct{}),
		listenAddrs: func() []ma.Multiaddr {
			close(enteredListenAddrs)
			<-releaseListenAddrs
			return []ma.Multiaddr{ma.StringCast("/ip4/1.2.3.4/tcp/1234")}
		},
	}

	s.refCount.Add(1)
	go func() {
		s.waitForPublicAddr()
		close(done)
	}()

	<-enteredListenAddrs
	cancel()
	close(releaseListenAddrs)
	<-done

	if !s.holePuncherMx.TryLock() {
		t.Fatal("holePuncherMx remained locked after cancellation path")
	}
	s.holePuncherMx.Unlock()

	closed := make(chan struct{})
	go func() {
		_ = s.Close()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked after waitForPublicAddr cancellation")
	}
}

// TestFilterHolePunchAddrs covers the gate applied to addresses received in a
// DCUtR CONNECT: only public IPs survive, and the count is capped so a single
// message cannot trigger an unbounded number of dials.
func TestFilterHolePunchAddrs(t *testing.T) {
	public := ma.StringCast("/ip4/1.2.3.4/tcp/1")
	got := filterHolePunchAddrs([]ma.Multiaddr{
		public,
		ma.StringCast("/ip4/127.0.0.1/tcp/1"),    // loopback
		ma.StringCast("/ip4/10.0.0.1/tcp/1"),     // private
		ma.StringCast("/dns4/example.com/tcp/1"), // DNS resolves to a peer-controlled IP
	})
	if len(got) != 1 || !got[0].Equal(public) {
		t.Fatalf("expected only the public IP, got %v", got)
	}

	many := make([]ma.Multiaddr, 0, maxHolePunchAddrs+10)
	for i := 0; i < maxHolePunchAddrs+10; i++ {
		many = append(many, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.4/tcp/%d", 1+i)))
	}
	if n := len(filterHolePunchAddrs(many)); n != maxHolePunchAddrs {
		t.Fatalf("expected cap at %d, got %d", maxHolePunchAddrs, n)
	}
}
