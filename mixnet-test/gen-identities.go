package main

import (
	"encoding/hex"
	"fmt"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func main() {
	for i := 0; i < 8; i++ {
		priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
		id, _ := peer.IDFromPrivateKey(priv)
		privBytes, _ := crypto.MarshalPrivateKey(priv)
		fmt.Printf("Node %d: ID=%s PRIV=%s\n", i, id, hex.EncodeToString(privBytes))
	}
}
