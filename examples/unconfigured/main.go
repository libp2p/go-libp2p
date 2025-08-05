package main

import (
	"log"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
)

func main() {
	priv, _, err := crypto.GenerateKeyPair(
		crypto.Ed25519,
		-1,
	)
	if err != nil {
		panic(err)
	}

	h2, err := libp2p.NewWithoutDefaults(
		libp2p.Identity(priv),
		libp2p.NoListenAddrs,
		libp2p.Security(noise.ID, noise.New),
		libp2p.NoTransports,
	)
	if err != nil {
		panic(err)
	}
	defer h2.Close()

	log.Printf("Hello World, my second hosts ID is %s\n", h2.ID())
}
