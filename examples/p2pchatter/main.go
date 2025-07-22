package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const chatProtocol = "/p2pchat/1.0.0"

func main() {
	ctx := context.Background()
	host, err := libp2p.New()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Node started with Peer ID", host.ID())
	fmt.Println("Listening addressess: ")
	for _, addr := range host.Addrs() {
		fmt.Printf("- %s/p2p/%s\n", addr, host.ID())
	}
	host.SetStreamHandler(chatProtocol, handleStream)

	if len(os.Args) > 1 {
		peerAddr, _ := ma.NewMultiaddr(os.Args[1])
		info, _ := peer.AddrInfoFromP2pAddr(peerAddr)
		if err := host.Connect(ctx, *info); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Connected to: ", info.ID)
		st, err := host.NewStream(ctx, info.ID, chatProtocol)
		if err != nil {
			log.Fatal(err)
		}
		handleStream(st)
	}
	select {}
}

func handleStream(s network.Stream) {
	fmt.Println("New stream opened!!!")
	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))

	go func() {
		for {
			str, err := rw.ReadString('\n')
			if err != nil {
				return
			}
			if str != "" {
				fmt.Printf("\x1b[32m%s\x1b[0m", str)
			}
		}
	}()

	go func() {
		stdReader := bufio.NewReader(os.Stdin)
		for {
			sendData, _ := stdReader.ReadString('\n')
			rw.WriteString(sendData)
			rw.Flush()
		}
	}()
}
