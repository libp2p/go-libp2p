package main

import (
	"context"
	"log"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

func main() {
	run()
}

func run() {
	// Create two "unreachable" libp2p hosts that want to communicate.
	// We are configuring them with no listen addresses to mimic hosts
	// that cannot be directly dialed due to problematic firewall/NAT
	// configurations.
	unreachable1, err := libp2p.New(
		libp2p.NoListenAddrs,
		// Usually EnableRelay() is not required as it is enabled by default
		// but NoListenAddrs overrides this, so we're adding it in explicitly again.
		libp2p.EnableRelay(),
	)
	if err != nil {
		log.Printf("Failed to create unreachable1: %v", err)
		return
	}

	unreachable2, err := libp2p.New(
		libp2p.NoListenAddrs,
		libp2p.EnableRelay(),
	)
	if err != nil {
		log.Printf("Failed to create unreachable2: %v", err)
		return
	}

	log.Println("First let's attempt to directly connect")

	// Attempt to connect the unreachable hosts directly
	unreachable2info := peer.AddrInfo{
		ID:    unreachable2.ID(),
		Addrs: unreachable2.Addrs(),
	}

	err = unreachable1.Connect(context.Background(), unreachable2info)
	if err == nil {
		log.Printf("This actually should have failed.")
		return
	}

	log.Println("As suspected we cannot directly dial between the unreachable hosts")

	// Create a host to act as a middleman to relay messages on our behalf
	relay1, err := libp2p.New()
	if err != nil {
		log.Printf("Failed to create relay1: %v", err)
		return
	}

	// Configure the host to offer the circuit relay service.
	// Any host that is directly dialable in the network (or on the internet)
	// can offer a circuit relay service, this isn't just the job of
	// "dedicated" relay services.
	// In circuit relay v2 (which we're using here!) it is rate limited so that
	// any node can offer this service safely
	_, err = relay.New(relay1)
	if err != nil {
		log.Printf("Failed to instantiate the relay: %v", err)
		return
	}

	relay1info := peer.AddrInfo{
		ID:    relay1.ID(),
		Addrs: relay1.Addrs(),
	}

	// Connect both unreachable1 and unreachable2 to relay1
	if err := unreachable1.Connect(context.Background(), relay1info); err != nil {
		log.Printf("Failed to connect unreachable1 and relay1: %v", err)
		return
	}

	if err := unreachable2.Connect(context.Background(), relay1info); err != nil {
		log.Printf("Failed to connect unreachable2 and relay1: %v", err)
		return
	}

	// Now, to test the communication, let's set up a protocol handler on unreachable2
	unreachable2.SetStreamHandler("/customprotocol", func(s network.Stream) {
		log.Println("Awesome! We're now communicating via the relay!")

		// End the example
		s.Close()
	})

	// Hosts that want to have messages relayed on their behalf need to reserve a slot
	// with the circuit relay service host
	// As we will open a stream to unreachable2, unreachable2 needs to make the
	// reservation
	_, err = client.Reserve(context.Background(), unreachable2, relay1info)
	if err != nil {
		log.Printf("unreachable2 failed to receive a relay reservation from relay1. %v", err)
		return
	}

	// Now create a new address for unreachable2 that specifies to communicate via
	// relay1 using a circuit relay
	relayaddr, err := ma.NewMultiaddr("/p2p/" + relay1info.ID.String() + "/p2p-circuit/p2p/" + unreachable2.ID().String())
	if err != nil {
		log.Println(err)
		return
	}

	// Since we just tried and failed to dial, the dialer system will, by default
	// prevent us from redialing again so quickly. Since we know what we're doing, we
	// can use this ugly hack (it's on our TODO list to make it a little cleaner)
	// to tell the dialer "no, its okay, let's try this again"
	unreachable1.Network().(*swarm.Swarm).Backoff().Clear(unreachable2.ID())

	log.Println("Now let's attempt to connect the hosts via the relay node")

	// Open a connection to the previously unreachable host via the relay address
	unreachable2relayinfo := peer.AddrInfo{
		ID:    unreachable2.ID(),
		Addrs: []ma.Multiaddr{relayaddr},
	}
	if err := unreachable1.Connect(context.Background(), unreachable2relayinfo); err != nil {
		log.Printf("Unexpected error here. Failed to connect unreachable1 and unreachable2: %v", err)
		return
	}

	log.Println("Yep, that worked!")

	// Woohoo! we're connected!
	// Let's start talking!

	// Because we don't have a direct connection to the destination node - we have a relayed connection -
	// the connection is marked as transient. Since the relay limits the amount of data that can be
	// exchanged over the relayed connection, the application needs to explicitly opt-in into using a
	// relayed connection. In general, we should only do this if we have low bandwidth requirements,
	// and we're happy for the connection to be killed when the relayed connection is replaced with a
	// direct (holepunched) connection.
	s, err := unreachable1.NewStream(network.WithAllowLimitedConn(context.Background(), "customprotocol"), unreachable2.ID(), "/customprotocol")
	if err != nil {
		log.Println("Whoops, this should have worked...: ", err)
		return
	}

	s.Read(make([]byte, 1)) // block until the handler closes the stream
}
