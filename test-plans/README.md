# test-plans test implementation (go-libp2p)

This folder contains go-libp2p’s transport interop “ping” test app and Dockerfile used by the `libp2p/test-plans` transport suite.

## Canonical docs

See the upstream transport harness documentation:

- `https://github.com/libp2p/test-plans/blob/master/transport/README.md`

## Run locally (same as CI)

From your **go-libp2p repo root**:

1. Clone the harness: `git clone https://github.com/libp2p/test-plans.git test-plans-harness`
2. Install `yq` and `docker compose` (see upstream README).
3. Inject this repo as the Go implementation and run:

```bash
cd test-plans-harness/transport
yq eval -i '.implementations = ((.implementations // []) | map(select(.id != "go-v0.47-pre"))) + [{"id":"go-v0.47-pre","source":{"type":"local","path":strenv(GO_LIBP2P_DIR),"dockerfile":"test-plans/PingDockerfile"},"transports":["tcp","ws","wss","quic-v1","webtransport","webrtc-direct"],"secureChannels":["tls","noise"],"muxers":["yamux"]}]' images.yaml
GO_LIBP2P_DIR=/path/to/your/go-libp2p ./run.sh --test-select "go-v0.47-pre" --test-ignore "~failing" --force-image-rebuild --debug --cache-dir /path/to/your/go-libp2p/.cache -y
```

Use your actual go-libp2p path for `GO_LIBP2P_DIR` and `--cache-dir`. To reproduce a single failing test, use `--test-select "exact test name"` (from the summary).

## Outdated docs

Historical notes that referenced the old `transport-interop/` + `npm`-based runner are kept in:

- `README.OUTDATED.md`
