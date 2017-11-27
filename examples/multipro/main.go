package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"

	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	ps "gx/ipfs/QmPgDWmTmuzvP7QE5zwo1TmjbJme9pmZHNujB2453jkCTr/go-libp2p-peerstore"
	swarm "gx/ipfs/QmU219N3jn7QadVCeBUqGnAkwoXoUomrCwDuVQVuL7PB5W/go-libp2p-swarm"
	ma "gx/ipfs/QmXY77cVe7rVRQXZZQRioukUM7aRW3BTcAgJe12MCtb3Ji/go-multiaddr"
	peer "gx/ipfs/QmXYjuNuxVzXKJCfWasQk1RqkhVLDM9jtUKhqc2WPQmFSB/go-libp2p-peer"
	crypto "gx/ipfs/QmaPbCnUMBohSGo3KnxEa2bHqyJVVeEEcwtqJAYxerieBo/go-libp2p-crypto"
)

// helper method - create a lib-p2p host to listen on a port
func makeRandomNode(port int, done chan bool) *Node {
	// Ignoring most errors for brevity
	// See echo example for more details and better implementation
	priv, pub, _ := crypto.GenerateKeyPair(crypto.RSA, 2048)
	pid, _ := peer.IDFromPublicKey(pub)
	listen, _ := ma.NewMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port))
	peerStore := ps.NewPeerstore()
	peerStore.AddPrivKey(pid, priv)
	peerStore.AddPubKey(pid, pub)
	n, _ := swarm.NewNetwork(context.Background(), []ma.Multiaddr{listen}, pid, peerStore, nil)
	host := bhost.New(n)

	return NewNode(host, done)
}

func main() {
	// Choose random ports between 10000-10100
	rand.Seed(666)
	port1 := rand.Intn(100) + 10000
	port2 := port1 + 1

	done := make(chan bool, 1)

	// Make 2 hosts
	h1 := makeRandomNode(port1, done)
	h2 := makeRandomNode(port2, done)
	h1.host.Peerstore().AddAddrs(h2.host.ID(), h2.host.Addrs(), ps.PermanentAddrTTL)
	h2.host.Peerstore().AddAddrs(h1.host.ID(), h1.host.Addrs(), ps.PermanentAddrTTL)

	log.Printf("This is a conversation between %s and %s\n", h1.host.ID(), h2.host.ID())

	// send messages using the protocols
	h1.pingProtocol.Ping(h2)
	h2.pingProtocol.Ping(h1)
	h1.echoProtocol.Echo(h2)
	h2.echoProtocol.Echo(h1)

	// block until all responses have been processed
	for i := 0; i < 4; i++ {
		<-done
	}
}
