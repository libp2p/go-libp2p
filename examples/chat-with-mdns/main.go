package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/multiformats/go-multiaddr"
)

func handleStream(stream network.Stream) {
	// Create a buffer stream for non-blocking read and write.
	r := bufio.NewReader(stream)

	go readData(r)
}

// Read messages from connected peers
// In this example, we only read from the peers but use another Stream
// to broadcast the messages
func readData(r *bufio.Reader) {
	for {
		str, err := r.ReadString('\n')
		if err != nil {
			fmt.Println("Error reading from buffer")

			return
		}

		if str == "" {
			return
		}

		if str != "\n" {
			// Green console colour: 	\x1b[32m
			// Reset console colour: 	\x1b[0m
			fmt.Printf("\x1b[32m%s\x1b[0m> ", str)
		}
	}
}

func main() {
	help := flag.Bool("help", false, "Display Help")
	cfg := parseFlags()

	if *help {
		fmt.Printf("Simple example for peer discovery using mDNS. mDNS is great when you have multiple peers in local LAN.")
		fmt.Printf("Usage: \n   Run './chat-with-mdns'\nor Run './chat-with-mdns -host [host] -port [port] -rendezvous [string] -pid [proto ID]'\n")

		os.Exit(0)
	}

	fmt.Printf("[*] Listening on: %s with port: %d\n", cfg.listenHost, cfg.listenPort)

	ctx := context.Background()
	r := rand.Reader

	// Creates a new RSA key pair for this host.
	prvKey, _, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, r)
	if err != nil {
		panic(err)
	}

	// 0.0.0.0 will listen on any interface device.
	sourceMultiAddr, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%d", cfg.listenHost, cfg.listenPort))

	// libp2p.New constructs a new libp2p Host.
	// Other options can be added here
	host, err := libp2p.New(
		libp2p.ListenAddrs(sourceMultiAddr),
		libp2p.Identity(prvKey),
	)
	if err != nil {
		panic(err)
	}

	// Set a function as stream handler.
	// This function is called when a peer initiates a connection and starts a stream with this peer.
	host.SetStreamHandler(protocol.ID(cfg.ProtocolID), handleStream)

	fmt.Printf("\n[*] Your Multiaddress Is: /ip4/%s/tcp/%v/p2p/%s\n", cfg.listenHost, cfg.listenPort, host.ID())

	peers := &Peers{
		peers: map[string]*Peer{},
	}

	go func() {
		stdReader := bufio.NewReader(os.Stdin)

		for {
			fmt.Print("> ")

			sendData, err := stdReader.ReadString('\n')
			if err != nil {
				fmt.Println("Error reading from stdin")
				panic(err)
			}

			peers.SendAll(fmt.Sprintf("%s: %s", host.ID().String(), sendData))
		}
	}()

	peerChan := initMDNS(host, cfg.RendezvousString)
	for { // allows multiple peers to join
		peer := <-peerChan // will block until we discover a peer
		fmt.Println("Found peer:", peer.ID, ", connecting")

		if err := host.Connect(ctx, peer); err != nil {
			fmt.Println("Connection failed:", err)

			continue
		}

		// open a stream, this stream will be handled by handleStream other end
		stream, err := host.NewStream(ctx, peer.ID, protocol.ID(cfg.ProtocolID))
		if err != nil {
			fmt.Println("Stream open failed", err)

			continue
		}

		p := &Peer{
			id:     string(peer.ID),
			stream: stream,
		}
		p.Start()

		fmt.Println("Connected to:", peer.ID)

		peers.AddPeer(p)
		peers.SendAll(fmt.Sprintf("%s Joined", host.ID().String()))
	}
}
