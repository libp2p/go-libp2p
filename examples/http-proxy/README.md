# HTTP proxy service with libp2p

This example shows how to create a simple HTTP proxy service with libp2p:

```
                                                                                                    XXX
                                                                                                   XX  XXXXXX
                                                                                                  X         XX
                                                                                        XXXXXXX  XX          XX XXXXXXXXXX
                  +----------------+                +-----------------+              XXX      XXX            XXX        XXX
 HTTP Request     |                |                |                 |             XX                                    XX
+----------------->                | libp2p stream  |                 |  HTTP       X                                      X
                  |  Local peer    <---------------->  Remote peer    <------------->     HTTP SERVER - THE INTERNET      XX
<-----------------+                |                |                 | Req & Resp   XX                                   X
  HTTP Response   |  libp2p host   |                |  libp2p host    |               XXXX XXXX XXXXXXXXXXXXXXXXXXXX   XXXX
                  +----------------+                +-----------------+                                            XXXXX
```

In order to proxy an HTTP request, we create a local peer which listens on `localhost:9900`. HTTP requests performed to that address are tunneled via a libp2p stream to a remote peer, which then performs the HTTP requests and sends the response back to the local peer, which relays it to the user.

Note that this is a very simple approach to a proxy, and does not perform any header management, nor supports HTTPS. The `proxy.go` code is thoroughly commented, detailing what is happening in every step.

## Build

From the `go-libp2p/examples` directory run the following:

```
> cd http-proxy/
> go build
```

## Usage

First run the "remote" peer as follows. It will print a local peer address. If you would like to run this on a separate machine, please replace the IP accordingly:

```sh
> ./http-proxy
Proxy server is ready
libp2p-peer addresses:
/ip4/127.0.0.1/tcp/12000/p2p/QmddTrQXhA9AkCpXPTkcY7e22NK73TwkUms3a44DhTKJTD
```

Then run the local peer, indicating that it will need to forward http requests to the remote peer as follows:

```
> ./http-proxy -d /ip4/127.0.0.1/tcp/12000/p2p/QmddTrQXhA9AkCpXPTkcY7e22NK73TwkUms3a44DhTKJTD
Proxy server is ready
libp2p-peer addresses:
/ip4/127.0.0.1/tcp/12001/p2p/Qmaa2AYTha1UqcFVX97p9R1UP7vbzDLY7bqWsZw1135QvN
proxy listening on  127.0.0.1:9900
```

As you can see, the proxy prints the listening address `127.0.0.1:9900`. You can now use this address as a proxy, for example with `curl`:

```
> curl -x "127.0.0.1:9900" "http://ipfs.io/p2p/QmfUX75pGRBRDnjeoMkQzuQczuCup2aYbeLxz5NzeSu9G6"
it works!
```

Learn more about how curl uses proxies [here](https://everything.curl.dev/usingcurl/proxies/http).
