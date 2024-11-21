# http over libp2p proxy

This is a simple proxy server that proxies native HTTP requests to a target
server using HTTP over libp2p streams.

The motivating use case is for use with the metrics server. This proxy lets us
expose a standard HTTP server to prometheus while proxying metrics requests over
libp2p.

## Usage

```
PROXY_TARGET="multiaddr:/ip4/127.0.0.1/tcp/49346/p2p/.../http-path/some-path" go run ./cmd/libp2phttpproxy
```

In another terminal:
```
curl localhost:5005
```


