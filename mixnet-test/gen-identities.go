package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func main() {
	count := 10
	if len(os.Args) > 1 {
		if n, err := strconv.Atoi(os.Args[1]); err == nil && n > 0 {
			count = n
		}
	}
	for i := 0; i < count; i++ {
		priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
		id, _ := peer.IDFromPrivateKey(priv)
		privBytes, _ := crypto.MarshalPrivateKey(priv)
		fmt.Printf("Node %d: ID=%s PRIV=%s\n", i, id, hex.EncodeToString(privBytes))
	}
}
