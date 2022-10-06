# go-libp2p roadmap Q4’22/Q1’23

## 🛣️ Milestones
### 2022

#### Early Q4 (October)
-   A.1⚡Handshakes at the Speed of Light - **[Early Muxer Negotiation](https://github.com/libp2p/specs/issues/426)**

#### End of Q4 (December)
-   B.1 🧠 Smart Dialing - **Happy Eyeballs**
-   C 📊 Comprehensive Metrics - **Swarm metrics**
-   D.1 📺 Universal Browser Connectivity - **[WebRTC](https://github.com/libp2p/go-libp2p/pull/1655)**

### 2023

#### Early Q1 (January)
-   B.2 🧠 Smart Dialing - **Quic Blackhole detector**
-   E 🧪 Future-proof testing
-   A.2 ⚡ Handshakes at the Speed of Light - **[Adding security protocol](https://github.com/libp2p/specs/pull/353)**

#### Mid Q1 (February)
-   B.3 🧠 Smart Dialing - **RTT estimation**
    -   🎉 Estimated Project Completion

#### End of Q1 (March)
-   F ⏱️ Future-proof benchmarking
-   A.3 ⚡ Handshakes at the Speed of Light - **0.5 RTT data optimization**
    -   🎉 Estimated Project Completion

#### Sometime Q2
-   G 📢 Judicious Address Advertisements

#### Yet to be planned/TBD
-   D.2 📺 Universal Browser Connectivity - **[WebTransport](https://github.com/libp2p/go-libp2p/issues/1717)**
	- Dependency on Chrome needs to support new draft version of WebTransport protocol (still under development by IETF and W3C)
-   H 🧗 Clearing our tech debt
	- Address technical debt around adding more comprehensive Testground tests for non-transport features

# Roadmap Appendix
## A.⚡️Handshakes at the Speed of Light
<!--- TODO: Link to GitHub Epic -->

**Why**: Historically, libp2p has been very wasteful when it comes to round trips spent during connection establishment. This is slowing down our users, especially their TTFB (time to first byte) metrics.

**Goal**: go-libp2p optimizes its handshake latency up to the point where only increasing the speed of light would lead to further speedups. In particular, this means (in chronological order of a handshake):

1. [Early Muxer Negotiation](https://github.com/libp2p/specs/issues/426): cutting off the 1 RTT wasted on muxer negotiation
3. [Adding security protocol](https://github.com/libp2p/specs/pull/353): cutting off the 1 RTT wasted on security protocol negotiation by including the security protocol in the multiaddr
4. 0.5 RTT data optimization: using 0.5-RTT data (for TLS) / a Noise Extension to ship the list of Identify protocols, cutting of 1 RTT that many protocols spend waiting on `IdentifyWait`

## B. 🧠 Smart Dialing
<!--- TODO: Link to GitHub Epic -->

**Why**: Having a large list of transports to pick from is great. Having an advanced stack that can dial all of them is even greater. But dialing all of them at the same time wastes our, the network’s and the peer’s resources. 

**Goal**: When given a list of multiaddrs of a peer, go-libp2p is smart enough to pick the address that results in the most performant connection (for example, preferring QUIC over TCP), while also picking the address such that maximizes the likelihood of a successful handshake.

How:

1. Happy Eyeballs: implement some kind of “Happy-Eyeballs” style prioritization among all supported transports
2. Quic Blackhole detector: detection of blackholes, especially relevant to detect UDP (QUIC) blackholing
3. RTT estimation: estimation of the expected RTT of a connection based on two nodes’ IP addresses, so that Happy Eyeballs Timeouts can be set dynamically

## C. 📊 [Comprehensive Metrics](https://github.com/libp2p/go-libp2p/issues/1356)

**Why**: For far too long, go-libp2p has been a black box. This has hurt us many times, by allowing trivial bugs to go undetected for a long time ([example](https://github.com/ipfs/kubo/pull/8750)). Having metrics will allow us to track the impact of performance improvements we make over time.

**Goal**: Export a wider set of metrics across go-libp2p components and enable node operators to monitor their nodes in production. Optionally provide a sample Grafana dashboard similar to the resource manager dashboard.

**How**: This will look similar to how we already expose resource manager metrics. Metrics can be added incrementally for libp2p’s components. First milestone is having metrics for the swarm.

## D. 📺 Universal Browser Connectivity
<!--- TODO: Link to GitHub Epic -->

**Why**: A huge part of “the Web” is happening inside the browser. As a universal p2p networking stack, libp2p needs to be able to offer solutions for browser users.

**Goal**: go-libp2p ships with up-to-date WebTransport and (libp2p-) WebRTC implementations, enabled by default. This allows connections between browsers and public nodes, browsers and non-public nodes as well as two browsers.

1. [WebRTC](https://github.com/libp2p/go-libp2p/pull/1655): while browser to public node is getting close to finalized, there’ll be a push to make the other combinations work as well
2. [WebTransport](https://github.com/libp2p/go-libp2p/issues/1717): as the protocol is still under development by IETF and W3C, our implementation needs to follow. To stay up to date, we will have to move as soon as Chrome ships support for a new draft version

## E. 🧪 Future-proof Testing
<!--- TODO: Link to GitHub Epic -->

**Why**: Having lots of transports is great, and shaving off RTTs is awesome. We need to stay backwards-compatible with legacy go-libp2p implementations, and less advanced libp2p stacks.

**Goal**: We have cross-version and cross-implementation Testground coverage that makes sure that we are able to establish a libp2p connection between two nodes, in the expected number of RTTs. This makes sure that the optimizations don’t break compatibility, actually have the performance impact we expect them to have, and serves as a regression test in the future.

## F. ⏱ Future-proof Benchmarking
<!--- TODO: Link to GitHub Epic -->

**Why**: For libp2p to be competitive, it needs to delivers comparable performance to widely used protocols on the internet, namely HTTP/2 and HTTP/3.

**Goal**: We have a test suite that runs libp2p transfers between nodes located at different locations all over the world, proving that libp2p is able to achieve performance on par with HTTP. The test suite is run on a continuous basis and results are published to a public performance dashboard.

## G. 📢 Judicious Address Advertisements
<!--- TODO: Link to GitHub Epic -->

**Why**: A node that advertises lots of addresses hurts itself. Other nodes will have to try dialing a lot of addresses before they find one that actually works, dramatically increasing handshake latencies.

**Goal**: Nodes only advertise addresses that they are actually reachable at.

**How**: Unfortunately, the AutoNAT protocol can’t be used to probe the reachability of any particular address (especially due to a bug in the go-libp2p implementation deployed years ago). Most likely, we need a second version of the AutoNAT protocol.

Related discussion: [https://github.com/libp2p/go-libp2p/issues/1480](https://github.com/libp2p/go-libp2p/issues/1480)

## Libp2p Project Roadmap
Roadmap items in this document were sourced from our the overarching libp2p project roadmap: https://github.com/libp2p/specs/blob/master/ROADMAP.md
