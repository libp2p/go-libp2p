# libp2p host with AutoTLS

This example builds on the [libp2p host example](../libp2p-host) example and demonstrates how to use [AutoTLS](https://blog.ipfs.tech/2024-shipyard-improving-ipfs-on-the-web/#autotls-with-libp2p-direct) to automatically generate a wildcard Let's Encrypt TLS certificate unique to the libp2p host (`*.<PeerID>.libp2p.direct`), enabling browsers to directly connect to the libp2p host.

For this example to work, you need to have a public IP address and be publicly reachable.

## Running the example

From the `go-libp2p/examples` directory run the following:

```sh
cd autotls/
go run .
```
