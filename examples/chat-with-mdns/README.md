# p2p chat app with libp2p [support peer discovery using mdns]

This program demonstrates a simple p2p chat application. You will learn how to discover a peer in the network (using mdns), connect to it and open a chat stream. This example is heavily influenced by (and shamelessly copied from) `chat-with-rendezvous` example

## How to build this example?

```
go get -v -d ./...

go build
```

## Usage

Use two different terminal windows to run

```
./chat-with-mdns -port 6666
./chat-with-mdns -port 6668
```


## So how does it work?

1. **Configure a p2p host**
```go
ctx := context.Background()

// libp2p.New constructs a new libp2p Host.
// Other options can be added here.
host, err := libp2p.New()
```
[libp2p.New](https://godoc.org/github.com/libp2p/go-libp2p#New) is the constructor for libp2p node. It creates a host with given configuration.

2. **Set a default handler function for incoming connections.**

This function is called on the local peer when a remote peer initiate a connection and starts a stream with the local peer.
```go
// Set a function as stream handler.
host.SetStreamHandler("/chat/1.1.0", handleStream)
```

```handleStream``` is executed for each new stream incoming to the local peer. ```stream``` is used to exchange data between local and remote peer. This example uses non blocking functions for reading and writing from this stream.

```go
func handleStream(stream net.Stream) {

    // Create a buffer stream for non blocking read and write.
    rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))

    go readData(rw)
    go writeData(rw)

    // 'stream' will stay open until you close it (or the other side closes it).
}
```

3. **Find peers nearby using mdns**

Start [mdns discovery](https://godoc.org/github.com/libp2p/go-libp2p/p2p/discovery#NewMdnsService) service in host.

```go
ser, err := discovery.NewMdnsService(peerhost, rendezvous)
```
register [Notifee interface](https://godoc.org/github.com/libp2p/go-libp2p/p2p/discovery#Notifee) with service so that we get notified about peer discovery

```go
	n := &discoveryNotifee{}
	ser.RegisterNotifee(n)
```


4. **Open streams to peers found.**

Finally we open stream to the peers we found, as we find them

```go
	peer := <-peerChan // will block until we discover a peer
	fmt.Println("Found peer:", peer, ", connecting")

	if err := host.Connect(ctx, peer); err != nil {
		fmt.Println("Connection failed:", err)
	}

	// open a stream, this stream will be handled by handleStream other end
	stream, err := host.NewStream(ctx, peer.ID, protocol.ID(cfg.ProtocolID))

	if err != nil {
		fmt.Println("Stream open failed", err)
	} else {
		rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))

		go writeData(rw)
		go readData(rw)
		fmt.Println("Connected to:", peer)
	}
```

## Authors
1. Bineesh Lazar
