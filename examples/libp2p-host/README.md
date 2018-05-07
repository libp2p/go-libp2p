# The libp2p 'host'

For most applications, the host is the basic building block you'll need to get started. This guide will show how to construct and use a simple host.

The host is an abstraction that manages services on top of a swarm. It provides a clean interface to connect to a service on a given remote peer.

First, you'll need an ID. A host's ID is the hash of its public key. To generate an ID, you can do the following:

```go
import (
	"crypto/rand"

	crypto "github.com/libp2p/go-libp2p-crypto"
	peer "github.com/libp2p/go-libp2p-peer"
	pstore "github.com/libp2p/go-libp2p-peerstore"
)

// Generate an identity keypair using go's cryptographic randomness source
priv, pub, err := crypto.GenerateEd25519Key(rand.Reader)
if err != nil {
	panic(err)
}

// A peers ID is the hash of its public key
pid, err := peer.IDFromPublicKey(pub)
if err != nil {
	panic(err)
}
```

Now you know who you are. The next step is to create a libp2p host. The host object abstracts all of the work of setting up a 'swarm network' to handle the peers you will connect to, incoming connections from other peers, and negotiation of new outbound connections.

In its simplest form, the host constructor can be invoked with no arguments to use the default settings:

```go
import (
	"context"
	libp2p "github.com/libp2p/go-libp2p"
)

// Make a context to govern the lifespan of the swarm
ctx := context.Background()

// To construct a simple host with all the default settings, just use `New`
h, err := libp2p.New(ctx)
if err != nil {
	panic(err)
}

fmt.Printf("Hello World, my hosts ID is %s\n", h.ID())

```

Let's look at a slightly more complex example that actually uses the identity we created earlier and specifies where to listen for incoming connections:

```go
	h2, err := libp2p.New(ctx,
		// Use your own created keypair
		libp2p.Identity(priv),

		// Set your own listen address
		// The config takes an array of addresses, specify as many as you want.
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/9000"),
	)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Hello World, my second hosts ID is %s\n", h2.ID())
```

Above we have shown two examples of the host constructor `libp2p.New()`.  The simplicity of the host constructor actually elides over two important implementation details.  First, note that internally the call `libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/9000")` is creating a `multiaddr` from a string; the manual equivalent would look like this:

```go
import ma "github.com/multiformats/go-multiaddr"

...

maddr, err := ma.NewMultiaddr("/ip4/0.0.0.0/tcp/9000")
if err != nil {
	panic(err)
}
```

You could then pass `maddr` directly to `libp2p.New()`.

Second, another important implementation detail hidden within the host constructor is the notion of a `peerstore`: a place where host IDs are stored.  Internally, `libp2p.New()` is creating a peerstore in a manner similar to this:

```go
import (
	pstore "github.com/libp2p/go-libp2p-peerstore"
)

// We've created the identity, now we need to store it.
// A peerstore holds information about peers, including your own
ps := pstore.NewPeerstore()
ps.AddPrivKey(pid, priv)
ps.AddPubKey(pid, pub)
```

In future guides we will go over ways to use hosts, configure them differently (hint: there are a huge number of ways to set these up), and interesting ways to apply this technology to various applications you might want to build.

To see this code all put together, take a look at [host.go](host.go).
