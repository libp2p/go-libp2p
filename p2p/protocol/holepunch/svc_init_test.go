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

type notifyInspectNetwork struct {
	network.Network
	notify func(network.Notifiee)
}

func (n *notifyInspectNetwork) Notify(notifiee network.Notifiee) {
	n.notify(notifiee)
}

type notifyInspectHost struct {
	host.Host
	network network.Network
	addrs   []ma.Multiaddr
}

func (*notifyInspectHost) ID() peer.ID { return peer.ID("notify-inspect-host") }

func (h *notifyInspectHost) Addrs() []ma.Multiaddr {
	return h.addrs
}

func (h *notifyInspectHost) Network() network.Network {
	return h.network
}

func (*notifyInspectHost) SetStreamHandler(protocol.ID, network.StreamHandler) {}

func TestHolePuncherFullyInitializedBeforeNetworkNotify(t *testing.T) {
	const timeout = 137 * time.Millisecond

	var (
		timeoutAtRegistration time.Duration
		notifyCalls           int
	)
	testNetwork := &notifyInspectNetwork{
		notify: func(notifiee network.Notifiee) {
			notifyCalls++
			registered, ok := notifiee.(*netNotifiee)
			if !ok {
				t.Fatalf("registered %T, want *netNotifiee", notifiee)
			}
			timeoutAtRegistration = (*holePuncher)(registered).directDialTimeout
		},
	}
	addrs := []ma.Multiaddr{ma.StringCast("/ip4/1.2.3.4/tcp/4001")}
	testHost := &notifyInspectHost{network: testNetwork, addrs: addrs}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	service := &Service{
		ctx:                ctx,
		host:               testHost,
		listenAddrs:        func() []ma.Multiaddr { return addrs },
		directDialTimeout:  timeout,
		hasPublicAddrsChan: make(chan struct{}),
	}
	service.refCount.Add(1)
	service.waitForPublicAddr()
	if service.holePuncher != nil {
		defer service.holePuncher.Close()
	}

	if notifyCalls != 1 {
		t.Fatalf("Notify calls = %d, want 1", notifyCalls)
	}
	if timeoutAtRegistration != timeout {
		t.Fatalf("directDialTimeout at registration = %s, want %s", timeoutAtRegistration, timeout)
	}
}
