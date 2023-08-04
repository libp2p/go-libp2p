# go-libp2p-pubsub chat with rendezvous example

This example project allows multiple peers to chat among each other using go-libp2p-pubsub. 

Peers are discovered using a DHT, so no prior information (other than the rendezvous name) is required for each peer.

## Running

Clone this repo, then `cd` into the `examples/pubsub/basic-chat-with-rendezvous` directory:

```shell
git clone https://github.com/libp2p/go-libp2p
cd go-libp2p/examples/pubsub/basic-chat-with-rendezvous
```

Now you can either run with `go run`, or build and run the binary:

```shell
go run .

# or, build and run separately
go build .
./chat
```

To change the topic name, use the `-topicName` flag:

```shell
go run . -topicName=adifferenttopic
```

Try opening several terminals, each running the app. When you type a message and hit enter in one, it
should appear in all others that are connected to the same topic.

To quit, hit `Ctrl-C`.

