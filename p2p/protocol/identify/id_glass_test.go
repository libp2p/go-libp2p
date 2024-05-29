package identify

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	recordPb "github.com/libp2p/go-libp2p/core/record/pb"
	blhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	ma "github.com/multiformats/go-multiaddr"
	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFastDisconnect(t *testing.T) {
	// This test checks to see if we correctly abort sending an identify
	// response if the peer disconnects before we handle the request.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	target := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer target.Close()
	ids, err := NewIDService(target)
	require.NoError(t, err)
	defer ids.Close()
	ids.Start()

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
		ids.handleIdentifyRequest(s)

		// Ok, allow the outer test to continue.
		select {
		case <-sync:
		case <-ctx.Done():
			return
		}
	})

	source := blhost.NewBlankHost(swarmt.GenSwarm(t))
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

func TestWrongSignedPeerRecord(t *testing.T) {
	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h1.Close()
	ids, err := NewIDService(h1)
	require.NoError(t, err)
	ids.Start()
	defer ids.Close()

	h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h2.Close()
	ids2, err := NewIDService(h2)
	require.NoError(t, err)
	ids2.Start()
	defer ids2.Close()

	h3 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h2.Close()
	ids3, err := NewIDService(h3)
	require.NoError(t, err)
	ids3.Start()
	defer ids3.Close()

	h2.Connect(context.Background(), peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()})
	s, err := h2.NewStream(context.Background(), h1.ID(), IDPush)
	require.NoError(t, err)

	err = ids3.sendIdentifyResp(s, true)
	// This should fail because the peer record is signed by h3, not h2
	require.NoError(t, err)
	time.Sleep(time.Second)

	require.Empty(t, h1.Peerstore().Addrs(h3.ID()), "h1 should not know about h3 since it was relayed over h2")
}

func TestInvalidSignedPeerRecord(t *testing.T) {
	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h1.Close()
	ids, err := NewIDService(h1)
	require.NoError(t, err)
	ids.Start()
	defer ids.Close()

	h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h2.Close()
	ids2, err := NewIDService(h2)
	require.NoError(t, err)
	// We don't want to start the identify service, we'll manage the messages h2
	// sends manually so we can tweak it
	// ids2.Start()

	h2.Connect(context.Background(), peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()})
	require.Empty(t, h1.Peerstore().Addrs(h2.ID()))

	s, err := h2.NewStream(context.Background(), h1.ID(), IDPush)
	require.NoError(t, err)

	ids2.updateSnapshot()
	ids2.currentSnapshot.Lock()
	snapshot := ids2.currentSnapshot.snapshot
	ids2.currentSnapshot.Unlock()
	mes := ids2.createBaseIdentifyResponse(s.Conn(), &snapshot)
	fmt.Println("Signed record is", snapshot.record)
	marshalled, err := snapshot.record.Marshal()
	require.NoError(t, err)

	var envPb recordPb.Envelope
	err = proto.Unmarshal(marshalled, &envPb)
	require.NoError(t, err)

	envPb.Signature = []byte("invalid")

	mes.SignedPeerRecord, err = proto.Marshal(&envPb)
	require.NoError(t, err)

	err = ids2.writeChunkedIdentifyMsg(s, mes)
	require.NoError(t, err)
	fmt.Println("Done sending msg")
	s.Close()

	// Wait a bit for h1 to process the message
	time.Sleep(1 * time.Second)

	cab, ok := h1.Peerstore().(peerstore.CertifiedAddrBook)
	require.True(t, ok)
	require.Nil(t, cab.GetPeerRecord(h2.ID()))
}

func TestIncomingAddrFilter(t *testing.T) {
	lhAddr := ma.StringCast("/ip4/127.0.0.1/udp/123/quic-v1")
	privAddr := ma.StringCast("/ip4/192.168.1.101/tcp/123")
	pubAddr := ma.StringCast("/ip6/2001::1/udp/123/quic-v1")
	pubDNSAddr := ma.StringCast("/dns/example.com/udp/123/quic-v1")
	privDNSAddr := ma.StringCast("/dns4/localhost/udp/123/quic-v1")
	tests := []struct {
		output []ma.Multiaddr
		remote ma.Multiaddr
	}{
		{
			output: []ma.Multiaddr{lhAddr, privAddr, pubAddr, pubDNSAddr, privDNSAddr},
			remote: lhAddr,
		},
		{
			output: []ma.Multiaddr{privAddr, pubAddr, pubDNSAddr, privDNSAddr},
			remote: privAddr,
		},
		{
			output: []ma.Multiaddr{pubAddr, pubDNSAddr},
			remote: pubAddr,
		},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("remote:%s", tc.remote), func(t *testing.T) {
			input := []ma.Multiaddr{lhAddr, privAddr, pubAddr, pubDNSAddr, privDNSAddr}
			got := filterAddrs(input, tc.remote)
			require.ElementsMatch(t, tc.output, got, "%s\n%s", tc.output, got)
		})
	}
}
