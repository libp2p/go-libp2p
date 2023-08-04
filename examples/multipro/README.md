# Protocol Multiplexing using rpc-style protobufs with libp2p

This example shows how to use protobufs to encode and transmit information between libp2p hosts using libp2p Streams.
This example expects that you are already familiar with the [echo example](https://github.com/libp2p/go-libp2p/tree/master/examples/echo).

## Build

From the `go-libp2p/examples` directory run the following:

```sh
> cd multipro/
> go build
```

## Usage

```sh
> ./multipro
```

## Details

The example creates two libp2p Hosts supporting 2 protocols: ping and echo.

Each protocol consists of RPC-style requests and responses and each request and response is a typed protobufs message (and a go data object).

This is a different pattern than defining a whole p2p protocol as one protobuf message with lots of optional fields (as can be observed in various p2p-lib protocols using protobufs such as dht).

The example shows how to match async received responses with their requests. This is useful when processing a response requires access to the request data.

The idea is to use libp2p protocol multiplexing on a per-message basis.

### Features
1. 2 fully implemented protocols using an RPC-like request-response pattern - Ping and Echo
2. Scaffolding for quickly implementing new app-level versioned RPC-like protocols
3. Full authentication of incoming message data by author (who might not be the message's sender peer)
4. Base p2p format in protobufs with fields shared by all protocol messages
5. Full access to request data when processing a response.

## Author
@avive
