# Protocol Multiplexing using rpc-style multicodecs, protobufs with libp2p

This examples shows how to use multicodecs (i.e. protobufs) to encode and transmit information between LibP2P hosts using LibP2P Streams.
Multicodecs present a common interface, making it very easy to swap the codec implementation if needed.
This example expects that you area already familiar with the [echo example](https://github.com/libp2p/go-libp2p/tree/master/examples/echo).

## Build

Compile the .proto files using the protobufs go compiler:

```
protoc --go_out=. ./p2p.proto
```


From `multipro` base source folder:

```
> go build
```


## Usage

```
> ./multipro

```

## Details

The example creates two LibP2P Hosts supporting 2 protocols: ping and echo.

Each protocol consists RPC-style requests and respones and each request and response is a typed protobufs message (and a go data object). 

This is a different pattern then defining a whole p2p protocol as 1 protobuf message with lots of optional fields (as can be observed in various p2p-lib protocols using protobufs such as dht).

The example shows how to match async received responses with their requests. This is useful when processing a response requires access to the request data.

## Author

@avive


