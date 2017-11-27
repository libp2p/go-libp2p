# Protocol Multiplexing using rpc-style multicodecs, protobufs with libp2p

This examples shows how to use multicodecs (i.e. protobufs) to encode and transmit information between LibP2P hosts using LibP2P Streams.

Multicodecs present a common interface, making it very easy to swap the codec implementation if needed.

This example expects that you area already familiar with the [echo example](https://github.com/libp2p/go-libp2p/tree/master/examples/echo).

## Build

Compile the .proto files with the protobufs go compiler:

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

The example creates two LibP2P Hosts. Host1 opens a stream to Host2. Host2 has an `StreamHandler` to deal with the incoming stream. This is covered in the `echo` example.

Both hosts simulate a conversation. But rather than sending raw messages on the stream, each message in the conversation is encoded under a `json` object (using the `json` multicodec). For example:

The stream lasts until one of the sides closes it when the HangUp field is `true`.
