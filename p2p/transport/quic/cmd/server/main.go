package main

import (
	"fmt"
	"log"
	"os"

	cmdlib "github.com/libp2p/go-libp2p/p2p/transport/quic/cmd/lib"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Printf("Usage: %s <port>", os.Args[0])
		return
	}
	if err := cmdlib.RunServer(os.Args[1], nil); err != nil {
		log.Fatalf(err.Error())
	}
}
