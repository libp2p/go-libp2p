# The libp2p 'host'

For most applications, the host is the basic building block you'll need to get started. This guide will show how to construct and use a simple host on one side, and a more fully-featured host on the other.

The host is an abstraction that manages services on top of a swarm. It provides a clean interface to connect to a service on a given remote peer.

If you want to create a host with a default configuration refer to the example in `./host.go`

If you want more control over the configuration, you can specify some options to the constructor. For a full list of all the configuration supported by the constructor [see the different options in the docs](https://godoc.org/github.com/libp2p/go-libp2p).

In `./host.go` we set a number of useful options like a custom ID and enable routing. This will improve discoverability and reachability of the peer on NAT'ed environments.

In future guides we will go over ways to use hosts, configure them differently (hint: there are a huge number of ways to set these up), and interesting ways to apply this technology to various applications you might want to build.

To see this code all put together, take a look at [host.go](host.go).
