package identify

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/stretchr/testify/require"

	blhost "github.com/libp2p/go-libp2p-blankhost"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
)

func TestFastDisconnect(t *testing.T) {
	// This test checks to see if we correctly abort sending an identify
	// response if the peer disconnects before we handle the request.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	target := blhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	defer target.Close()
	ids, err := NewIDService(target)
	require.NoError(t, err)
	defer ids.Close()

	sync := make(chan struct{})
	target.SetStreamHandler(ID, func(s network.Stream) {
		// Wait till the stream is setup on both sides.
		select {
		case <-sync:
		case <-ctx.Done():
			return
		}

		// Kill the connection, and make sure we're completely disconnected.
		s.Conn().Close()
		for target.Network().Connectedness(s.Conn().RemotePeer()) == network.Connected {
			// let something else run
			time.Sleep(time.Millisecond)
		}
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

	source := blhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	defer source.Close()

	err = source.Connect(ctx, peer.AddrInfo{ID: target.ID(), Addrs: target.Addrs()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := source.NewStream(ctx, target.ID(), ID)
	if err != nil {
		t.Fatal(err)
	}
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
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
}
