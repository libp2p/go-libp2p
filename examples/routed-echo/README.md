# Routed Host: echo client/server

This example is intended to follow up the basic host and echo examples by adding use of the ipfs distributed hash table to lookup peers.

Functionally, this example works similarly to the echo example, however setup of the host includes wrapping it with a Kademila hash table, so it can find peers using only their IDs. 

We'll also enable NAT port mapping to illustrate the setup, although it isn't guaranteed to actually be used to make the connections.  Additionally, this example uses the newer `libp2p.New` constructor.

## Build

From `go-libp2p/examples` base folder:

```
> cd routed-echo/
> go build
```

## Usage


```
> ./routed-echo -l 10000
2018/02/19 12:22:32 I can be reached at:
2018/02/19 12:22:32 /ip4/127.0.0.1/tcp/10000/p2p/QmfRY4vuKpU2tApACrbmYFn9xoeNzMQhLXg7nKnyvnzHeL
2018/02/19 12:22:32 /ip4/192.168.1.203/tcp/10000/p2p/QmfRY4vuKpU2tApACrbmYFn9xoeNzMQhLXg7nKnyvnzHeL
2018/02/19 12:22:32 Now run "./routed-echo -l 10001 -d QmfRY4vuKpU2tApACrbmYFn9xoeNzMQhLXg7nKnyvnzHeL" on a different terminal
2018/02/19 12:22:32 listening for connections
```

The listener libp2p host will print its randomly generated Base58 encoded ID string, which combined with the ipfs DHT, can be used to reach the host, despite lacking other connection details.  By **default**, this example will bootstrap off your **local IPFS peer** (assuming one is running). If you'd rather bootstrap off the same peers go-ipfs uses, pass the `-global` flag in both terminals.

Now, launch another node that talks to the listener:

```
> ./routed-echo -l 10001 -d QmfRY4vuKpU2tApACrbmYFn9xoeNzMQhLXg7nKnyvnzHeL
```

As in other examples, the new node will send the message `"Hello, world!"` to the listener, which will in turn echo it over the stream and close it. The listener logs the message, and the sender logs the response.

## Details

The `makeRoutedHost()` function creates a [go-libp2p routedhost](https://godoc.org/github.com/libp2p/go-libp2p/p2p/host/routed) object. `routedhost` objects wrap [go-libp2p basichost](https://godoc.org/github.com/libp2p/go-libp2p/p2p/host/basic) and add the ability to lookup a peers address using the ipfs distributed hash table as implemented by [go-libp2p-kad-dht](https://godoc.org/github.com/libp2p/go-libp2p-kad-dht).

In order to create the routed host, the example needs:

- A [go-libp2p basichost](https://godoc.org/github.com/libp2p/go-libp2p/p2p/host/basic) as in other examples.
- A [go-libp2p-kad-dht](https://godoc.org/github.com/libp2p/go-libp2p-kad-dht) which provides the ability to lookup peers by ID.  Wrapping takes place via `routedHost := rhost.Wrap(basicHost, dht)`

A `routedhost` can now open streams (bi-directional channel between to peers) using [NewStream](https://godoc.org/github.com/libp2p/go-libp2p/p2p/host/basic#BasicHost.NewStream) and use them to send and receive data tagged with a `Protocol.ID` (a string). The host can also listen for incoming connections for a given
`Protocol` with [`SetStreamHandle()`](https://godoc.org/github.com/libp2p/go-libp2p/p2p/host/basic#BasicHost.SetStreamHandler).  The advantage of the routed host is that only the Peer ID is required to make the connection, not the underlying address details, since they are provided by the DHT.

The example makes use of all of this to enable communication between a listener and a sender using protocol `/echo/1.0.0` (which could be any other thing).
