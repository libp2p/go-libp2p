package main

import (
	"context"
	"fmt"
	"log"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pwebtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
	"github.com/multiformats/go-multiaddr"
)

func main() {
	ctx := context.Background()
	// Create a new libp2p Host that uses the WebTransport protocol
	h, err := libp2p.New(
		libp2p.Transport(libp2pwebtransport.New),
	)
	if err != nil {
		panic(err)
	}

	// Define the multiaddress to connect to
	multiaddrStr := "/ip4/12.144.75.172/udp/4001/quic-v1/webtransport/certhash/uEiBYjbsQlBmvE2iO7JU6OilEHaokJPgDh9POSXU-T42tVw/certhash/uEiDLMJWuxuf4-2RBP1ln_Ic1uXPnXOpJKFFlFmDJWOH4sg/p2p/12D3KooWFxAMbz588VcN4Ae69nMiGvVscWEyEoA6A3fcJxhSzBFM"

	ma, err := multiaddr.NewMultiaddr(multiaddrStr)
	if err != nil {
		log.Fatalf("Failed to parse multiaddress: %v", err)
	}

	// Extract the peer ID from the multiaddress
	info, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		log.Fatalf("Failed to extract peer info: %v", err)
	}

	// Connect to the peer
	if err := h.Connect(ctx, *info); err != nil {
		log.Fatalf("Failed to connect to peer: %v", err)
	}

	fmt.Println("Connected to peer:", info.ID)

	remotePeer := info.ID
	protocols, err := h.Peerstore().GetProtocols(remotePeer)

	if err != nil {
		panic(err)
	}
	fmt.Println("Protocols of remote peer:", protocols)
	select {}
}
