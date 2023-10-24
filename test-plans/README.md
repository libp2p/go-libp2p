# test-plans test implementation

This folder defines the implementation for the test-plans interop tests.

# Running this test locally

You can run this test locally by having a local Redis instance and by having
another peer that this test can dial or listen for. For example to test that we
can dial/listen for ourselves we can do the following:

1. Start redis (needed by the tests): `docker run --rm -it -p 6379:6379
   redis/redis-stack`.
2. In one terminal run the dialer: `redis_addr=localhost:6379 ip="0.0.0.0"
   transport=quic-v1 security=quic muxer=quic is_dialer="true" go run
   ./cmd/ping`
3. In another terminal, run the listener: `redis_addr=localhost:6379
   ip="0.0.0.0" transport=quic-v1 security=quic muxer=quic is_dialer="false" go
   run ./cmd/ping`


To test the interop with other versions do something similar, except replace one
of these nodes with the other version's interop test.

# Running all interop tests locally with Compose

To run this test against all released libp2p versions you'll need to have the
[libp2p/test-plans](https://github.com/libp2p/test-plans) checked out. Then do
the following (from the root directory of this repository):

1. Build the image: `docker build -t go-libp2p-head -f test-plans/PingDockerfile .`.
2. Build the images for all released versions in `libp2p/test-plans`: `(cd <path
   to >/libp2p/test-plans/transport-interop/ && make)`.
3. Run the test:
```
GO_LIBP2P="$PWD"; (cd <path to >/libp2p/test-plans/transport-interop/ && npm run test -- --extra-version=$GO_LIBP2P/test-plans/ping-version.json --name-filter="go-libp2p-head")

```
