package relaysvc

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/stretchr/testify/require"
)

func TestReachabilityChangeEvent(t *testing.T) {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t))
	rmgr := NewRelayManager(h)
	emitter, err := rmgr.host.EventBus().Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)
	if err != nil {
		t.Fatal(err)
	}
	evt := event.EvtLocalReachabilityChanged{Reachability: network.ReachabilityPublic}
	emitter.Emit(evt)
	require.Eventually(
		t,
		func() bool { rmgr.mutex.Lock(); defer rmgr.mutex.Unlock(); return rmgr.relay != nil },
		1*time.Second,
		100*time.Millisecond,
		"relay should be set on public reachability")

	evt = event.EvtLocalReachabilityChanged{Reachability: network.ReachabilityPrivate}
	emitter.Emit(evt)
	require.Eventually(
		t,
		func() bool { rmgr.mutex.Lock(); defer rmgr.mutex.Unlock(); return rmgr.relay == nil },
		3*time.Second,
		100*time.Millisecond,
		"relay should be nil on private reachability")

	evt = event.EvtLocalReachabilityChanged{Reachability: network.ReachabilityPublic}
	emitter.Emit(evt)
	evt = event.EvtLocalReachabilityChanged{Reachability: network.ReachabilityUnknown}
	emitter.Emit(evt)
	require.Eventually(
		t,
		func() bool { rmgr.mutex.Lock(); defer rmgr.mutex.Unlock(); return rmgr.relay == nil },
		3*time.Second,
		100*time.Millisecond,
		"relay should be nil on unknown reachability")

	evt = event.EvtLocalReachabilityChanged{Reachability: network.ReachabilityPublic}
	emitter.Emit(evt)
	var relay *relayv2.Relay
	require.Eventually(
		t,
		func() bool { rmgr.mutex.Lock(); defer rmgr.mutex.Unlock(); relay = rmgr.relay; return relay != nil },
		3*time.Second,
		100*time.Millisecond,
		"relay should be set on public event")
	emitter.Emit(evt)
	require.Never(t,
		func() bool { rmgr.mutex.Lock(); defer rmgr.mutex.Unlock(); return relay != rmgr.relay },
		250*time.Millisecond,
		25*time.Millisecond,
		"relay should not be updated on receiving the same event")
}
