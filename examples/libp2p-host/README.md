# The libp2p 'host'

For most applications, the host is the basic building block you'll need to get started. This guide will show how to construct and use a simple host on one side, and a more fully-featured host on the other.

The host is an abstraction that manages services on top of a swarm. It provides a clean interface to connect to a service on a given remote peer.

If you want to create a host with a default configuration, you can do the following:

```go
import (
	"crypto/rand"
	"fmt"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/crypto"
)

// To construct a simple host with all the default settings, just use `New`
h, err := libp2p.New()
if err != nil {
	panic(err)
}
defer h.Close()

fmt.Printf("Hello World, my hosts ID is %s\n", h.ID())
```

If you want more control over the configuration, you can specify some options to the constructor. For a full list of all the configuration supported by the constructor [see the different options in the docs](https://godoc.org/github.com/libp2p/go-libp2p).

In this snippet we set a number of useful options like a custom ID and enable routing. This will improve discoverability and reachability of the peer on NAT'ed environments:

```go
// Set your own keypair
priv, _, err := crypto.GenerateKeyPair(
	crypto.Ed25519, // Select your key type. Ed25519 are nice short
	-1,             // Select key length when possible (i.e. RSA).
)
if err != nil {
	panic(err)
}

var idht *dht.IpfsDHT

h2, err := libp2p.New(
	// Use the keypair we generated
	libp2p.Identity(priv),
	// Multiple listen addresses
	libp2p.ListenAddrStrings(
		"/ip4/0.0.0.0/tcp/9000",      // regular tcp connections
		"/ip4/0.0.0.0/udp/9000/quic", // a UDP endpoint for the QUIC transport
	),
	// support TLS connections
	libp2p.Security(libp2ptls.ID, libp2ptls.New),
	// support Noise connections
	libp2p.Security(noise.ID, noise.New),
	// support QUIC
	libp2p.Transport(libp2pquic.NewTransport),
	// support any other default transports (TCP)
	libp2p.DefaultTransports,
	// Let's prevent our peer from having too many
	// connections by attaching a connection manager.
	libp2p.ConnectionManager(connmgr.NewConnManager(
		100,         // Lowwater
		400,         // HighWater,
		time.Minute, // GracePeriod
	)),
	// Attempt to open ports using uPNP for NATed hosts.
	libp2p.NATPortMap(),
	// Let this host use the DHT to find other hosts
	libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
		idht, err = dht.New(ctx, h)
		return idht, err
	}),
	// Let this host use relays and advertise itself on relays if
	// it finds it is behind NAT. Use libp2p.Relay(options...) to
	// enable active relays and more.
	libp2p.EnableAutoRelay(),
)
if err != nil {
	panic(err)
}
defer h2.Close()

fmt.Printf("Hello World, my second hosts ID is %s\n", h2.ID())
```

And that's it, you have a libp2p host and you're ready to start doing some awesome p2p networking!

In future guides we will go over ways to use hosts, configure them differently (hint: there are a huge number of ways to set these up), and interesting ways to apply this technology to various applications you might want to build.

To see this code all put together, take a look at [host.go](host.go).
