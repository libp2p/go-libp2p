# Building a P2P app with go-libp2p

Libp2p is a peer-to-peer networking library that allows developers to easily add p2p connectivity between their users. Setting it up and using it is simple and easy! In this brief tutorial, we'll create a simple peer-to-peer chat application with optional support for tor transports.

To start, make sure you have Go installed and set up. Then install libp2p and some other dependencies we need with:

```shell
$ make deps
```

This project is broken across three files to help keep things focused and organized. The source file we'll be reviewing in this tutorial is `main.go`. The other two files, `flags.go` and `tor.go`, define command-line flags and a few helper functions for establishing our tor transport, respectively.

We will start with a few imports. These imports include `go-libp2p` itself, our pubsub library "floodsub", the IPFS DHT, and a few other helper packages to tie things together. 

```go
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
	libp2pdht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p-peerstore"
	"github.com/libp2p/go-libp2p-transport"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
)
```

Next up, lets start constructing the pieces! First, we will set up our libp2p host. The host is the main abstraction that users of go-libp2p will deal with, It lets you connect to other peers, open new streams, and register protocol stream handlers.

Next up, some configuration and initialization. We parse our command-line flags and start working on constructing our libp2p [`Host`](https://godoc.org/github.com/libp2p/go-libp2p-host#Host). We copy the user-provided addresses to listen on and optionally add a tor onion address to listen on if we're using the tor transport. **Note**: Due to some specifics of the tor transport layer's implementation, your local tor instance must be configured such that the SOCKS5 proxy and controller have the **same password**.

```go
func main() {
	config := ParseFlags()
	ctx := context.Background()

	// Configure p2p host
	var tpts []transport.Transport
	addrs := make([]multiaddr.Multiaddr, len(config.ListenAddresses))
	copy(addrs, config.ListenAddresses)

	// Configure tor if options present
	if config.TorConfig != nil {
		torAddr, err := findOrCreateTorServiceAddress(config.TorConfig)
		if err != nil {
			panic(err)
		}
		addrs = append(addrs, torAddr)
		torTpt, err := newOnionTransport(config.TorConfig)
		if err != nil {
			panic(err)
		}
		tpts = append(tpts, torTpt)

		if err = addTorTransportToAddrUtil(); err != nil {
			panic(err)
		}
	}
```

With our new transport configured (in the event that we're using tor), we can create our `Host`. The `Host` object will be most developer's primary point-of-contact with `libp2p`. The `Host` is so-named because it is both a client and a server. Behind the scenes, it manages a whole host (groan) of difficult tasks, including connection management and reuse, stream multiplexing, and encryption. After creating our host, we print out all of the addresses at which it can be reached so that we might tell other clients. This subtly showcases some of `libp2p`'s power, potentially many transports and protocols supported by a single, simple abstraction.

```go
tptOptions := libp2p.Transports(tpts...)
listenAddrs := libp2p.ListenAddrs(addrs...)

// Set up a libp2p host.
host, err := libp2p.New(ctx, tptOptions, listenAddrs)
if err != nil {
    panic(err)
}

fmt.Println("To connect to this peer at the following addresses:")
for _, addr := range host.Addrs() {
    fmt.Printf("- %s/ipfs/%s\n", addr.String(), host.ID().Pretty())
}
```

Next, we set up our libp2p "floodsub" pubsub instance. This is how we will communicate with other users of our app. It gives us a simple many-to-many communication primitive to play with.

```go
// Construct ourselves a pubsub instance using that libp2p host.
fsub, err := floodsub.NewFloodSub(ctx, host)
if err != nil {
	panic(err)
}
```

Finally, we initialize a DHT so that we can discover other peers chatting on our topic! To ensure that it stays up to date, we call `dht.Bootstrap`, which will periodically check to see that peers in our table are still live.

```go
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

```

Next, we connect to the peers provided in our command line arguments. We will query these peers via our DHT to help us find other peers we might not have started with.

```go
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
```

Next up the rendezvous. We need a way for users of our app to automatically find each other. One way of doing this with the DHT is to tell it that you are providing a certain unique value, and then to search for others in the DHT claiming to also be providing that value. This way we can use that value's location in the DHT as a rendezvous point to meet other peers at.

Now, to make use of the DHT! First, we compute the hash of our topic name (think of it as our chat room's name). Then, we must announce ourselves as "providing" on that topic, i.e. we've got the resource represented by that hash. We do this by launching a coroutine to run a function we'll discuss in a moment, `provide`. Then we query the DHT for other peers providing on this topic!

```go
// Using the sha256 of our "topic" as our rendezvous value
c, _ := cid.NewPrefixV1(cid.Raw, multihash.SHA2_256).Sum([]byte(config.TopicName))

// Announce ourselves as participating in this topic
go provide(dht, c)

// Now, look for other peers who have announced. Growing our DHT is important,
// because it allows us manage our own peer relationships independent of the
// node(s) we bootstrap off of. If one of the nodes we bootstrap off of goes
// offline, we can still continue to chat!
fmt.Println("Searching DHT for peers...")
tctx, _ := context.WithTimeout(ctx, time.Second*10)
peers, err := dht.FindProviders(tctx, c)
if err != nil {
    panic(err)
}
fmt.Printf("Found %d peers!\n", len(peers))
```

We have to define a background loop to provide on a topic to account for the case when we are the first peer to join the network! When there are no other peers in our network, we've got no one to announce to and the call fails. This loop will continue until we successfully announce ourselves to a peer.

```go
func provide(dht *libp2pdht.IpfsDHT, topic *cid.Cid) {
	fmt.Println("Announcing ourselves...")
	for {
		ctx, _ := context.WithTimeout(dht.Context(), 10*time.Second)
		if err := dht.Provide(ctx, topic, true); err == nil {
			fmt.Println("Successfully announced!")
			break
		}
		time.Sleep(5 * time.Second)
	}
}
```

Back to the main function! After our queries to the DHT complete, we open connections to all of our peers so that we can communicate with them via floodsub.

```go
	if p.ID == host.ID() {
		// No sense connecting to ourselves
		continue
	}

	tctx, _ = context.WithTimeout(ctx, time.Second*5)
	if err := host.Connect(tctx, p); err != nil {
		fmt.Println("Failed to connect to peer: ", err)
	}
}

```

At this point, we've discovered our peer network and connected to some peers. It's time to activate floodsub!

```go
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
```

And with that, you have a simple chat app! Build it with:

```shell
go build -o libp2p-demo *.go
```

And then run it:

```shell
./libp2p-demo
```

