package relay_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
)

func Test_RelayIntegration(t *testing.T) {
	const DefaultTimeout = 10 * time.Second

	t.Run("Succeed", func(t *testing.T) {
		assertions := assert.New(t)

		// Prepare relay ==============================================
		relayIdentity, _, err := crypto.GenerateEd25519Key(rand.Reader)
		assertions.Nil(err, "failed to prepare relay private key")

		relayHost, err := libp2p.New(
			libp2p.ListenAddrStrings("/ip4/0.0.0.0/udp/0/quic-v1"),
			libp2p.Identity(relayIdentity),
		)
		assertions.Nil(err, "failed to prepare relay p2p host")
		defer relayHost.Close()

		relaySvc, err := relay.New(relayHost,
			relay.WithInfiniteLimits(),
		)
		assertions.Nil(err, "failed to prepare relay service")
		defer relaySvc.Close()

		// Prepare server =============================================
		serverIdentity, _, err := crypto.GenerateEd25519Key(rand.Reader)
		assertions.Nil(err, "failed to prepare server private key")

		serverHost, err := libp2p.New(
			libp2p.EnableRelay(),
			libp2p.NoListenAddrs,
			libp2p.Identity(serverIdentity),
		)
		assertions.Nil(err, "failed to prepare server p2p host")
		defer relayHost.Close()

		const ProtocolId = "/my/protocol/1.0.0"
		var payload = []byte("ping")
		serverHost.SetStreamHandler(ProtocolId, func(s network.Stream) {
			defer s.Close()

			s.Write(payload)
		})

		reserveCtx, cancel := context.WithTimeout(context.TODO(), DefaultTimeout)
		defer cancel()
		reservation, err := client.Reserve(reserveCtx, serverHost, relayHost.Peerstore().PeerInfo(relayHost.ID()))
		assertions.Nil(err, "failed to reserve address")

		p2pCircuit, err := multiaddr.NewMultiaddr("/p2p-circuit/p2p/" + reservation.Voucher.Peer.String())
		assertions.Nil(err, "failed to prepare multiaddr")

		circuitAddrs := make([]multiaddr.Multiaddr, 0, len(reservation.Addrs))
		for _, addr := range reservation.Addrs {
			circuitAddrs = append(circuitAddrs, addr.Encapsulate(p2pCircuit))
		}

		assertions.NotEmpty(reservation.Addrs, "no reservation addresses found")
		t.Logf("Reserved addresses:")
		for _, addr := range reservation.Addrs {
			t.Logf("- %v", addr)
		}
		t.Logf("Circuit addresses:")
		for _, addr := range circuitAddrs {
			t.Logf("- %v", addr)
		}

		// This should be fixed
		t.Logf("Known addresses by server:")
		for _, addr := range serverHost.Addrs() {
			t.Logf("- %v", addr)
		}
		// TODO: In the future this should be auto populated
		// assertions.NotEmpty(serverHost.Addrs(), "server doesn't know its newly created addresses")

		// Prepare client =============================================
		clientIdentity, _, err := crypto.GenerateEd25519Key(rand.Reader)
		assertions.Nil(err, "failed to prepare client private key")

		clientHost, err := libp2p.New(
			libp2p.EnableRelay(),
			libp2p.NoListenAddrs,
			libp2p.Identity(clientIdentity),
		)
		assertions.Nil(err, "failed to prepare client p2p host")
		defer relayHost.Close()

		// Client uses the reserved addresses give to server
		connectCtx, cancel := context.WithTimeout(context.TODO(), DefaultTimeout)
		defer cancel()
		err = clientHost.Connect(connectCtx, peer.AddrInfo{ID: serverHost.ID(), Addrs: circuitAddrs})
		assertions.Nil(err, "failed to connect to peer")

		streamCtx, cancel := context.WithTimeout(context.TODO(), DefaultTimeout)
		defer cancel()
		stream, err := clientHost.NewStream(streamCtx, serverHost.ID(), ProtocolId)
		assertions.Nil(err, "failed to open stream to protocol")
		defer stream.Close()

		var recv = make([]byte, len(payload))
		_, err = stream.Read(recv)
		assertions.Nil(err, "failed to read from stream")

		assertions.Equal(payload, recv, "received wrong data")
	})
}
