//go:build js
// +build js

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
	// Create a new libp2p Host that uses the WebTransport protocol
	h, err := libp2p.New(
		libp2p.Transport(libp2pwebtransport.New),
	)
	if err != nil {
		panic(err)
	}

	// Define the multiaddress to connect to
	multiaddrStr := "/ip4/20.77.12.206/udp/53249/quic-v1/webtransport/certhash/uEiCF2z9Z-7ZhVq2fiUg1NbQPdUT6YuPAiW8-Javsg9wbkQ/certhash/uEiCE4wCzfgqlrALJqlp4XauM0gHMoQokBN8QPWL-fWb1Gg"
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
	if err := h.Connect(context.Background(), *info); err != nil {
		log.Fatalf("Failed to connect to peer: %v", err)
	}

	fmt.Println("Connected to peer:", info.ID)
}
