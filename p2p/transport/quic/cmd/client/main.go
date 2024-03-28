package main

import (
	"fmt"
	"log"
	"os"

	cmdlib "github.com/libp2p/go-libp2p/p2p/transport/quic/cmd/lib"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Printf("Usage: %s <multiaddr> <peer id>", os.Args[0])
		return
	}
	if err := cmdlib.RunClient(os.Args[1], os.Args[2]); err != nil {
		log.Fatalf(err.Error())
	}
}
