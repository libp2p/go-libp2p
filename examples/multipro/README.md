# Protocol Multiplexing using rpc-style multicodecs, protobufs with libp2p

This examples shows how to use multicodecs (i.e. protobufs) to encode and transmit information between LibP2P hosts using LibP2P Streams.
Multicodecs present a common interface, making it very easy to swap the codec implementation if needed.
This example expects that you area already familiar with the [echo example](https://github.com/libp2p/go-libp2p/tree/master/examples/echo).

## Build

Install gx:
```sh
> go get -u github.com/whyrusleeping/gx

```

Run GX from the root libp2p source dir: 
```sh
>gx install
```

Build libp2p:
```sh
> make deps
> make
```

Run from `multipro` directory

```sh
> go build
```


## Usage

```sh
> ./multipro

```

## Details

The example creates two LibP2P Hosts supporting 2 protocols: ping and echo.

Each protocol consists RPC-style requests and responses and each request and response is a typed protobufs message (and a go data object).

This is a different pattern then defining a whole p2p protocol as one protobuf message with lots of optional fields (as can be observed in various p2p-lib protocols using protobufs such as dht).

The example shows how to match async received responses with their requests. This is useful when processing a response requires access to the request data.

The idea is to use lib-p2p protocol multiplexing on a per-message basis.

### Features
1. 2 fully implemented protocols using an RPC-like request-response pattern - Ping and Echo
2. Scaffolding for quickly implementing new app-level versioned RPC-like protocols
3. Full authentication of incoming message data by author (who might not be the message's sender peer)
4. Base p2p format in protobufs with fields shared by all protocol messages
5. Full access to request data when processing a response.

## Author
@avive


