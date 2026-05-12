package holepunch

import (
	"context"
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

func TestWaitForPublicAddrUnlocksMutexWhenCanceledAfterAddressDiscovery(t *testing.T) {
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

	// Force cancellation after waitForPublicAddr has entered listenAddrs, but
	// before listenAddrs returns a public address. The next code path in
	// waitForPublicAddr acquires holePuncherMx and observes the canceled context.
	<-enteredListenAddrs
	cancel()
	close(releaseListenAddrs)
	<-done

	// The canceled-after-address-discovery path must not leak the mutex.
	if !s.holePuncherMx.TryLock() {
		t.Fatal("holePuncherMx remained locked after cancellation path")
	}
	s.holePuncherMx.Unlock()

	// Close takes the same mutex. If waitForPublicAddr leaked it, Close would
	// block forever.
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
