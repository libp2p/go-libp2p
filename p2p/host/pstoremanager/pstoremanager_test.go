package pstoremanager_test

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/p2p/host/pstoremanager"

	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	"github.com/libp2p/go-eventbus"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

//go:generate sh -c "mockgen -package pstoremanager_test -destination mock_peerstore_test.go github.com/libp2p/go-libp2p-core/peerstore Peerstore"

func TestGracePeriod(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	eventBus := eventbus.NewBus()
	pstore := NewMockPeerstore(ctrl)
	const gracePeriod = 250 * time.Millisecond
	man, err := pstoremanager.NewPeerstoreManager(pstore, eventBus, pstoremanager.WithGracePeriod(gracePeriod))
	require.NoError(t, err)
	defer man.Close()
	man.Start()

	emitter, err := eventBus.Emitter(new(event.EvtPeerConnectednessChanged))
	require.NoError(t, err)
	start := time.Now()
	removed := make(chan struct{})
	pstore.EXPECT().RemovePeer(peer.ID("foobar")).DoAndReturn(func(p peer.ID) {
		defer close(removed)
		// make sure the call happened after the grace period
		require.GreaterOrEqual(t, time.Since(start), gracePeriod)
		require.LessOrEqual(t, time.Since(start), 3*gracePeriod)
	})
	require.NoError(t, emitter.Emit(event.EvtPeerConnectednessChanged{
		Peer:          "foobar",
		Connectedness: network.NotConnected,
	}))
	<-removed
}

func TestReconnect(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	eventBus := eventbus.NewBus()
	pstore := NewMockPeerstore(ctrl)
	const gracePeriod = 200 * time.Millisecond
	man, err := pstoremanager.NewPeerstoreManager(pstore, eventBus, pstoremanager.WithGracePeriod(gracePeriod))
	require.NoError(t, err)
	defer man.Close()
	man.Start()

	emitter, err := eventBus.Emitter(new(event.EvtPeerConnectednessChanged))
	require.NoError(t, err)
	require.NoError(t, emitter.Emit(event.EvtPeerConnectednessChanged{
		Peer:          "foobar",
		Connectedness: network.NotConnected,
	}))
	require.NoError(t, emitter.Emit(event.EvtPeerConnectednessChanged{
		Peer:          "foobar",
		Connectedness: network.Connected,
	}))
	time.Sleep(gracePeriod * 3 / 2)
	// There should have been no calls to RemovePeer.
	ctrl.Finish()
}

func TestClose(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	eventBus := eventbus.NewBus()
	pstore := NewMockPeerstore(ctrl)
	const gracePeriod = time.Hour
	man, err := pstoremanager.NewPeerstoreManager(pstore, eventBus, pstoremanager.WithGracePeriod(gracePeriod))
	require.NoError(t, err)
	man.Start()

	emitter, err := eventBus.Emitter(new(event.EvtPeerConnectednessChanged))
	require.NoError(t, err)

	sub, err := eventBus.Subscribe(&event.EvtPeerConnectednessChanged{})
	require.NoError(t, err)

	require.NoError(t, emitter.Emit(event.EvtPeerConnectednessChanged{
		Peer:          "foobar",
		Connectedness: network.NotConnected,
	}))

	// make sure the event is sent before we close
	select {
	case <-sub.Out():
	case <-time.After(5 * time.Second):
		t.Fatalf("Hit timeout")
	}

	done := make(chan struct{})
	pstore.EXPECT().RemovePeer(peer.ID("foobar")).Do(func(peer.ID) { close(done) })
	require.NoError(t, man.Close())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Hit timeout")
	}
}
