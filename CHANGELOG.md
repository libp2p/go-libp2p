# Table Of Contents <!-- omit in toc -->
- [v0.26.0](#v0260)
- [v0.25.1](#v0251)
- [v0.25.0](#v0250)

# [v0.26.0](https://github.com/libp2p/go-libp2p/releases/tag/v0.26.0)

## 🔦 Highlights <!-- omit in toc -->

### Additional metrics <!-- omit in toc -->

Since the last release, we've added additional metrics to different components.
Metrics were added to:
* [AutoNat](https://github.com/libp2p/go-libp2p/pull/2086): Current Reachability Status and Confidence, Client and Server DialResponses, Server DialRejections. The dashboard is [avialable here](https://github.com/sukunrt/go-libp2p/blob/master/p2p/host/autonat/grafana-dashboards/autonat.json).

## 🐞 Bugfixes <!-- omit in toc -->

**Full Changelog**: https://github.com/libp2p/go-libp2p/compare/v0.25.1...v0.26.0

# [v0.25.1](https://github.com/libp2p/go-libp2p/releases/tag/v0.25.1)

Fix some test-utils used by https://github.com/libp2p/go-libp2p-kad-dht

* mocknet: Start host in mocknet by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2078
* chore: update go-multistream by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2081

**Full Changelog**: https://github.com/libp2p/go-libp2p/compare/v0.25.0...v0.25.1

# [v0.25.0](https://github.com/libp2p/go-libp2p/releases/tag/v0.25.0)

## 🔦 Highlights <!-- omit in toc -->

### Metrics <!-- omit in toc -->

We've started instrumenting the entire stack. In this release, we're adding metrics for:
* the swarm: tracking incoming and outgoing connections, transports, security protocols and stream multiplexers in use: (https://github.com/libp2p/go-libp2p/blob/master/p2p/net/swarm/grafana-dashboards/swarm.json)
* the event bus: tracking how different events are propagated through the stack and to external consumers (https://github.com/libp2p/go-libp2p/blob/master/p2p/host/eventbus/grafana-dashboards/eventbus.json)

Our metrics effort is still ongoing, see https://github.com/libp2p/go-libp2p/issues/1356 for progress. We'll add metrics and dashboards for more libp2p components in a future release.

### Switching to Google's official Protobuf compiler <!-- omit in toc -->

So far, we were using GoGo Protobuf to compile our Protobuf definitions to Go code. However, this library was deprecated in October last year: https://twitter.com/awalterschulze/status/1584553056100057088. We [benchmarked](https://github.com/libp2p/go-libp2p/issues/1976#issuecomment-1371527732) serialization and deserialization, and found that it's (only) 20% slower than GoGo. Since the vast majority of go-libp2p's CPU time is spent in code paths other than Protobuf handling, switching to the official compiler seemed like a worthwhile tradeoff.

### Removal of OpenSSL <!-- omit in toc -->

Before this release, go-libp2p had an option to use OpenSSL bindings for certain cryptographic primitives, mostly to speed up the generation of signatures and their verification. When building go-libp2p using `go build`, we'd use the standard library crypto packages. OpenSSL was only used when passing in a build tag: `go build -tags openssl`.
Maintaining our own fork of the long unmaintained [go-openssl package](https://github.com/libp2p/go-openssl) has proven to place a larger than expected maintenance burden on the libp2p stewards, and when we recently discovered a range of new bugs ([this](https://github.com/libp2p/go-openssl/issues/38) and [this](https://github.com/libp2p/go-libp2p/issues/1892) and [this](https://github.com/libp2p/go-libp2p/issues/1951)), we decided to re-evaluate if this code path is really worth it. The results surprised us, it turns out that:
* The Go standard library is faster than OpenSSL for all key types that are not RSA.
* Verifying RSA signatures is as fast as Ed25519 signatures using the Go standard library, and even faster in OpenSSL.
* Generating RSA signatures is painfully slow, both using Go standard library crypto and using OpenSSL (but even slower using Go standard library).

Now the good news is, that if your node is not using an RSA key, it will never create any RSA signatures (it might need to verify them though, when it connects to a node that uses RSA keys). If you're concerned about CPU performance, it's a good idea to avoid RSA keys (the same applies to bandwidth, RSA keys are huge!). Even for nodes using RSA keys, it turns out that generating the signatures is not a significant part of their CPU load, as verified by profiling one of Kubo's bootstrap nodes.

We therefore concluded that it's safe to drop this code path altogether, and thereby reduce our maintenance burden.

### New Resource Manager types <!-- omit in toc -->

* Introduces a new type `LimitVal` which can explicitly specify "use default", "unlimited", "block all", as well as any positive number. The zero value of `LimitVal` (the value when you create the object in Go) is "Use default".
  * The JSON marshalling of this is straightforward.
* Introduces a new `ResourceLimits` type which uses `LimitVal` instead of ints so it can encode the above for the resources.
* Changes `LimitConfig` to `PartialLimitConfig` and uses `ResourceLimits`. This along with the marshalling changes means you can now marshal the fact that some resource limit is set to block all.
  * Because the default is to use the defaults, this avoids the footgun of initializing the resource manager with 0 limits (that would block everything).
  
In general, you can go from a resource config with defaults to a concrete one with `.Build()`. e.g. `ResourceLimits.Build() => BaseLimit`, `PartialLimitConfig.Build() => ConcreteLimitConfig`, `LimitVal.Build() => int`. See PR #2000 for more details.

If you're using the defaults for the resource manager, there should be no changes needed.

### Other Breaking Changes <!-- omit in toc -->

We've cleaned up our API to consistently use `protocol.ID` for libp2p and application protocols. Specifically, this means that the peer store now uses `protocol.ID`s, and the host's `SetStreamHandler` as well.

## What's Changed <!-- omit in toc -->
* chore: use generic LRU cache by @muXxer in https://github.com/libp2p/go-libp2p/pull/1980
* core/crypto: drop all OpenSSL code paths by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1953
* add WebTransport to the list of default transports by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1915
* identify: remove old code targeting Go 1.17 by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1964
* core: remove introspection package by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1978
* identify: remove support for Identify Delta by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1975
* roadmap: remove optimizations of the TCP-based handshake by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1959
* circuitv2: correctly set the transport in the ConnectionState by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1972
* switch to Google's Protobuf library, make protobufs compile with go generate by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1979
* ci: run go generate as part of the go-check workflow by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1986
* ci: use GitHub token to install protoc by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1996
* feat: add some users to the readme by @p-shahi in https://github.com/libp2p/go-libp2p/pull/1981
* CI: Fast multidimensional Interop tests by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/1991
* Fix: Ignore zero values when marshalling Limits. by @ajnavarro in https://github.com/libp2p/go-libp2p/pull/1998
* feat: add ci flakiness score to readme by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2002
* peerstore: make it possible to use an empty peer ID by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2006
* feat: rcmgr: Export resource manager errors by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2008
* feat: ci test-plans: Parse test timeout parameter for interop test by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2014
* Clean addresses with peer id before adding to addrbook by @sukunrt in https://github.com/libp2p/go-libp2p/pull/2007
* Expose muxer ids by @aschmahmann in https://github.com/libp2p/go-libp2p/pull/2012
* swarm: add a basic metrics tracer by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1973
* consistently use protocol.ID instead of strings by @sukunrt in https://github.com/libp2p/go-libp2p/pull/2004
* swarm metrics: fix datasource for dashboard by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2024
* chore: remove textual roadmap in favor for Starmap by @p-shahi in https://github.com/libp2p/go-libp2p/pull/2036
* rcmgr: *: Always close connscope by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2037
* chore: remove license files from the eventbus package by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2042
* Migrate to test-plan composite action by @thomaseizinger in https://github.com/libp2p/go-libp2p/pull/2039
* use quic-go and webtransport-go from quic-go organization by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2040
* holepunch: fix flaky test by not removing holepunch protocol handler by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1948
* quic / webtransport: extend test to test dialing a draft-29 and a v1  by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1957
* p2p/test: add test for EvtLocalAddressesUpdated event by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2016
* quic, tcp: only register Prometheus counters when metrics are enabled by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1971
* p2p/test: fix flaky notification test by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2051
* quic: disable sending of Version Negotiation packets by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2015
* eventbus: add metrics by @sukunrt in https://github.com/libp2p/go-libp2p/pull/2038
* metrics: use a single slice pool for all metrics tracer by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2054
* webtransport: tidy up some test output by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2053
* set names for eventbus event subscriptions by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2057
* autorelay: Split libp2p.EnableAutoRelay into 2 functions by @sukunrt in https://github.com/libp2p/go-libp2p/pull/2022
* rcmgr: Use prometheus SDK for rcmgr metrics by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2044
* websocket: Replace gorilla websocket transport with nhooyr websocket transport by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/1982
* rcmgr: add libp2p prefix to all metrics by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2063
* chore: git-ignore various flavors of qlog files by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2064
* interop: Update interop test to match spec by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2049
* chore: update webtransport-go to v0.5.1 by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2072
* identify: refactor sending of Identify pushes by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/1984
* feat!: rcmgr: Change LimitConfig to use LimitVal type by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2000
* p2p/test/quic: use contexts with a timeout for Connect calls by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2070
* identify: add some basic metrics by @marten-seemann in https://github.com/libp2p/go-libp2p/pull/2069
* chore: Release v0.25.0 by @MarcoPolo in https://github.com/libp2p/go-libp2p/pull/2077

## New Contributors <!-- omit in toc -->
* @muXxer made their first contribution in https://github.com/libp2p/go-libp2p/pull/1980
* @ajnavarro made their first contribution in https://github.com/libp2p/go-libp2p/pull/1998
* @sukunrt made their first contribution in https://github.com/libp2p/go-libp2p/pull/2007
* @thomaseizinger made their first contribution in https://github.com/libp2p/go-libp2p/pull/2039

**Full Changelog**: https://github.com/libp2p/go-libp2p/compare/v0.24.2...v0.25.0
