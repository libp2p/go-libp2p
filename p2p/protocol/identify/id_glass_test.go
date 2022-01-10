package identify

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/p2p/host/blank"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	swarmt "github.com/libp2p/go-libp2p-swarm/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFastDisconnect(t *testing.T) {
	// This test checks to see if we correctly abort sending an identify
	// response if the peer disconnects before we handle the request.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	target, err := blank.NewHost(swarmt.GenSwarm(t))
	require.NoError(t, err)
	defer target.Close()
	ids, err := NewIDService(target)
	require.NoError(t, err)
	defer ids.Close()

	sync := make(chan struct{})
	target.SetStreamHandler(ID, func(s network.Stream) {
		// Wait till the stream is set up on both sides.
		select {
		case <-sync:
		case <-ctx.Done():
			return
		}

		// Kill the connection, and make sure we're completely disconnected.
		assert.Eventually(t,
			func() bool {
				for _, conn := range target.Network().ConnsToPeer(s.Conn().RemotePeer()) {
					conn.Close()
				}
				return target.Network().Connectedness(s.Conn().RemotePeer()) != network.Connected
			},
			2*time.Second,
			time.Millisecond,
		)
		// Now try to handle the response.
		// This should not block indefinitely, or panic, or anything like that.
		//
		// However, if we have a bug, that _could_ happen.
		ids.sendIdentifyResp(s)

		// Ok, allow the outer test to continue.
		select {
		case <-sync:
		case <-ctx.Done():
			return
		}
	})

	source, err := blank.NewHost(swarmt.GenSwarm(t))
	require.NoError(t, err)
	defer source.Close()

	// only connect to the first address, to make sure we only end up with one connection
	require.NoError(t, source.Connect(ctx, peer.AddrInfo{ID: target.ID(), Addrs: target.Addrs()}))
	s, err := source.NewStream(ctx, target.ID(), ID)
	require.NoError(t, err)
	select {
	case sync <- struct{}{}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	s.Reset()
	select {
	case sync <- struct{}{}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// double-check to make sure we didn't actually timeout somewhere.
	require.NoError(t, ctx.Err())
}
