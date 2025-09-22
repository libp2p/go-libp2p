package transport_integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	crypto "github.com/libp2p/go-libp2p/core/crypto"
	peer "github.com/libp2p/go-libp2p/core/peer"
	peerstore "github.com/libp2p/go-libp2p/core/peerstore"
	network "github.com/libp2p/go-libp2p/core/network"

	ma "github.com/multiformats/go-multiaddr"
)

func TestCircuitV2Integration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	relay, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.EnableRelayService(),
	)
	if err != nil {
		t.Fatalf("failed to start relay host: %v", err)
	}
	defer func() { _ = relay.Close() }()

	relayAddrs := relay.Addrs()
	if len(relayAddrs) == 0 {
		t.Fatalf("relay has no addresses")
	}
	relayAddr := relayAddrs[0]

	privB, pubB, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		t.Fatalf("failed to generate keypair for host B: %v", err)
	}
	pidB, err := peer.IDFromPublicKey(pubB)
	if err != nil {
		t.Fatalf("failed to compute peer id from pubkey: %v", err)
	}

	relayID := relay.ID()
	addrFactory := func(_ []ma.Multiaddr) []ma.Multiaddr {
		out := make([]ma.Multiaddr, 0, 1)
		relayAddrStr := relayAddr.String()
		circuitStr := fmt.Sprintf("%s/p2p/%s/p2p-circuit/p2p/%s", relayAddrStr, relayID.String(), pidB.String())
		maddr, err := ma.NewMultiaddr(circuitStr)
		if err != nil {
			t.Fatalf("failed to create circuit multiaddr: %v", err)
		}
		out = append(out, maddr)
		return out
	}

	hostB, err := libp2p.New(
		libp2p.Identity(privB),
		libp2p.NoListenAddrs,
		libp2p.AddrsFactory(addrFactory),
	)
	if err != nil {
		t.Fatalf("failed to create host B: %v", err)
	}
	defer func() { _ = hostB.Close() }()

	hostB.SetStreamHandler("/echo/1.0.0", func(s network.Stream) {
		defer s.Close()
		_, _ = io.Copy(s, s)
	})

	hostA, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("failed to create host A: %v", err)
	}
	defer func() { _ = hostA.Close() }()

	circuitAddrStr := fmt.Sprintf("%s/p2p/%s/p2p-circuit/p2p/%s", relayAddr.String(), relayID.String(), pidB.String())
	circuitAddr, err := ma.NewMultiaddr(circuitAddrStr)
	if err != nil {
		t.Fatalf("failed to build circuit multiaddr: %v", err)
	}

	hostA.Peerstore().AddAddr(pidB, circuitAddr, peerstore.TempAddrTTL)

	pi := peer.AddrInfo{ID: pidB, Addrs: []ma.Multiaddr{circuitAddr}}
	if err := hostA.Connect(ctx, pi); err != nil {
		t.Fatalf("hostA failed to connect to hostB via relay: %v", err)
	}

	stream, err := hostA.NewStream(ctx, pidB, "/echo/1.0.0")
	if err != nil {
		t.Fatalf("failed to open stream to B via relay: %v", err)
	}
	defer stream.Close()

	want := []byte("hello over circuitv2")
	if _, err := stream.Write(want); err != nil {
		t.Fatalf("failed to write to stream: %v", err)
	}
	_ = stream.CloseWrite()

	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("failed to read echo reply: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("echo mismatch: want=%q got=%q", string(want), string(got))
	}
}
