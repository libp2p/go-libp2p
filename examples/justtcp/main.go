package main

import (
	"context"
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	tcpt "github.com/libp2p/go-tcp-transport"
	ma "github.com/multiformats/go-multiaddr"
)

func fatal(i interface{}) {
	fmt.Println(i)
	os.Exit(1)
}

func main() {
	laddr, err := ma.NewMultiaddr("/ip4/0.0.0.0/tcp/5555")
	if err != nil {
		fatal(err)
	}

	// create a new swarm with a dummy peer ID, no private key, and no stream muxer
	s := swarm.NewBlankSwarm(context.Background(), "bob", nil, nil)

	// Add a TCP transport to it
	s.AddTransport(tcpt.NewTCPTransport())

	// Add an address to start listening on
	err = s.AddListenAddr(laddr)
	if err != nil {
		fatal(err)
	}

	// Set a handler for incoming connections
	s.SetConnHandler(func(c *swarm.Conn) {
		fmt.Println("Got a new connection!")
		defer c.Close()
		buf := make([]byte, 1024)
		for {
			n, err := c.RawConn().Read(buf)
			if err != nil {
				fmt.Println(err)
				return
			}
			fmt.Printf("read: %q\n", string(buf[:n]))

			_, err = c.RawConn().Write(buf[:n])
			if err != nil {
				fmt.Println(err)
				return
			}
		}
	})

	// Wait forever
	<-make(chan bool)
}
