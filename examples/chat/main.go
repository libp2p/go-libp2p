package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-ipfs-addr"
	"github.com/libp2p/go-floodsub"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-crypto"
	libp2pdht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p-peerstore"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
)

// Useful only when we're the first node to launch in a peer network. Will
// attempt to announce to the peer network that we're subscribed (and producing)
// on the chat topic. This is necessary because, as the first peer, we have no
// other peers to announce to when we start up. For peers joining an existing
// network, this will succeed in its first iteration.
func provide(dht *libp2pdht.IpfsDHT, topic *cid.Cid) {
	fmt.Println("Announcing ourselves...")
	for {
		ctx, cancel := context.WithTimeout(dht.Context(), 10*time.Second)
		defer cancel()
		if err := dht.Provide(ctx, topic, true); err == nil {
			fmt.Println("Successfully announced!")
			break
		}
		time.Sleep(5 * time.Second)
	}
}

func main() {
	config := ParseFlags()
	ctx := context.Background()

	// Configure p2p host
	privkey, _, err := crypto.GenerateKeyPair(crypto.RSA, 1024)
	if err != nil {
		panic(err)
	}
	tpts := libp2p.DefaultTransports
	addrs := make([]multiaddr.Multiaddr, len(config.ListenAddresses))
	copy(addrs, config.ListenAddresses)

	// Configure tor if options present
	if config.TorConfig != nil {
		torAddr, err := findOrCreateTorServiceAddress(config.TorConfig)
		if err != nil {
			panic(err)
		}
		addrs = append(addrs, torAddr)
		torTpt, err := newOnionTransport(config.TorConfig, privkey)
		if err != nil {
			panic(err)
		}

		tptOption := libp2p.Transport(torTpt)
		if config.TorConfig.OnlyOnion {
			tpts = libp2p.ChainOptions(tpts, tptOption)
		} else {
			tpts = tptOption
		}
	}

	options := []libp2p.Option{tpts, libp2p.DefaultMuxers}
	options = append(options, libp2p.ListenAddrs(addrs...))
	options = append(options, libp2p.Identity(privkey))

	// Set up a libp2p host.
	host, err := libp2p.New(ctx, options...)
	if err != nil {
		panic(err)
	}

	fmt.Println("To connect to this peer at the following addresses:")
	for _, addr := range host.Addrs() {
		fmt.Printf("- %s/ipfs/%s\n", addr.String(), host.ID().Pretty())
	}

	// Construct a pubsub instance using that libp2p host.
	fsub, err := floodsub.NewFloodSub(ctx, host)
	if err != nil {
		panic(err)
	}

	// Start a DHT, for use in peer discovery. We can't just make a new DHT client
	// because we want each peer to maintain its own local copy of the DHT, so
	// that the bootstrapping node of the DHT can go down without inhibitting
	// future peer discovery.
	dht := libp2pdht.NewDHT(ctx, host, datastore.NewMapDatastore())

	// Bootstrap the DHT. In the default configuration, this spawns a Background
	// thread that will refresh the peer table every five minutes.
	if err = dht.Bootstrap(ctx); err != nil {
		panic(err)
	}

	// Attempt to connect to every peer in our peer list.
	for _, addr := range config.BootstrapPeers {
		iaddr, err := ipfsaddr.ParseMultiaddr(addr)
		if err != nil {
			panic(err)
		}

		pinfo, err := peerstore.InfoFromP2pAddr(iaddr.Multiaddr())
		if err != nil {
			panic(err)
		}

		if err := host.Connect(ctx, *pinfo); err != nil {
			fmt.Println("Bootstrapping to peer failed: ", err)
		}
	}

	// Using the sha256 of our "topic" as our rendezvous value
	c, _ := cid.NewPrefixV1(cid.Raw, multihash.SHA2_256).Sum([]byte(config.TopicName))

	// Announce ourselves as participating in this topic
	go provide(dht, c)

	// Now, look for other peers who have announced. Growing our DHT is important,
	// because it allows us manage our own peer relationships independent of the
	// node(s) we bootstrap off of. If one of the nodes we bootstrap off of goes
	// offline, we can still continue to chat!
	fmt.Println("Searching DHT for peers...")
	var peers []peerstore.PeerInfo
	{
		tctx, tcancel := context.WithTimeout(ctx, time.Second*10)
		defer tcancel()
		peers, err = dht.FindProviders(tctx, c)
		if err != nil {
			tcancel()
			panic(err)
		}
		fmt.Printf("Found %d peers!\n", len(peers))
	}

	// Connect to the peers we've just discovered.
	for _, p := range peers {
		if p.ID == host.ID() {
			// No sense connecting to ourselves
			continue
		}

		{
			tctx, tcancel := context.WithTimeout(ctx, time.Second*5)
			defer tcancel()
			if err = host.Connect(tctx, p); err != nil {
				fmt.Println("Failed to connect to peer: ", err)
			}
		}
	}

	// Subscribe to our topic at the swarm level.
	sub, err := fsub.Subscribe(config.TopicName)
	if err != nil {
		panic(err)
	}

	// In a coroutine, listen for messages from our peers and print them to the
	// screen
	go func() {
		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				panic(err)
			}
			fmt.Printf("%s: %s\n", msg.GetFrom(), string(msg.GetData()))
		}
	}()

	// In the main thread, read input from the user to broadcast to our peers.
	fmt.Println("Type something and hit enter to send:")
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		if err := fsub.Publish(config.TopicName, scan.Bytes()); err != nil {
			panic(err)
		}
	}
}
