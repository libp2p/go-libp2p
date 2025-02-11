
<h1 align="center">
  <a href="https://libp2p.io/"><img width="250" src="https://github.com/libp2p/libp2p/blob/master/logo/black-bg-2.png?raw=true" alt="libp2p hex logo" /></a>
</h1>

<h3 align="center">The Go implementation of the libp2p Networking Stack.</h3>

<p align="center">
  <a href="http://protocol.ai"><img src="https://img.shields.io/badge/made%20by-Protocol%20Labs-blue.svg?style=flat-square" /></a>
  <a href="http://libp2p.io/"><img src="https://img.shields.io/badge/project-libp2p-yellow.svg?style=flat-square" /></a>
  <a href="https://pkg.go.dev/github.com/libp2p/go-libp2p"><img src="https://pkg.go.dev/badge/github.com/libp2p/go-libp2p.svg" alt="Go Reference"></a>
  <a href="https://discuss.libp2p.io"><img src="https://img.shields.io/discourse/https/discuss.libp2p.io/posts.svg"/></a>
  <a href="https://marcopolo.github.io/FlakyTests/"><img src="https://marcopolo.github.io/FlakyTests/current-score.svg"/></a>
</p>

# Table of Contents <!-- omit in toc -->
- [Background](#background)
- [Usage](#usage)
  - [Examples](#examples)
  - [Dashboards](#dashboards)
- [Contribute](#contribute)
  - [Supported Go Versions](#supported-go-versions)
- [Notable Users](#notable-users)

# Background

[libp2p](https://github.com/libp2p/specs) is a networking stack and library modularized out of [The IPFS Project](https://github.com/ipfs/ipfs), and bundled separately for other tools to use.
>
libp2p is the product of a long, and arduous quest of understanding -- a deep dive into the internet's network stack, and plentiful peer-to-peer protocols from the past. Building large-scale peer-to-peer systems has been complex and difficult in the last 15 years, and libp2p is a way to fix that. It is a "network stack" -- a protocol suite -- that cleanly separates concerns, and enables sophisticated applications to only use the protocols they absolutely need, without giving up interoperability and upgradeability. libp2p grew out of IPFS, but it is built so that lots of people can use it, for lots of different projects.

To learn more, check out the following resources:
- [**Our documentation**](https://docs.libp2p.io)
- [**Our community discussion forum**](https://discuss.libp2p.io)
- [**The libp2p Specification**](https://github.com/libp2p/specs)
- [**js-libp2p implementation**](https://github.com/libp2p/js-libp2p)
- [**rust-libp2p implementation**](https://github.com/libp2p/rust-libp2p)

# Usage

This repository (`go-libp2p`) serves as the entrypoint to the universe of packages that compose the Go implementation of the libp2p stack.

You can start using go-libp2p in your Go application simply by adding imports from our repos, e.g.:

```go
import "github.com/libp2p/go-libp2p"
```

## Examples

Examples can be found in the [examples folder](examples).

## Dashboards

We provide prebuilt Grafana dashboards so that applications can better monitor libp2p in production.
You can find the [dashboard JSON files here](https://github.com/libp2p/go-libp2p/tree/master/dashboards).

We also have live [Public Dashboards](https://github.com/libp2p/go-libp2p/tree/master/dashboards/README.md#public-dashboards) that you can check out to see real time monitoring in action.


# Contribute

go-libp2p is MIT-licensed open source software. We welcome contributions big and small! Take a look at the [community contributing notes](https://github.com/ipfs/community/blob/master/CONTRIBUTING.md). Please make sure to check the [issues](https://github.com/libp2p/go-libp2p/issues). Search the closed ones before reporting things, and help us with the open ones.

Guidelines:

- read the [libp2p spec](https://github.com/libp2p/specs)
- ask questions or talk about things in our [discussion forums](https://discuss.libp2p.io), or open an [issue](https://github.com/libp2p/go-libp2p/issues) for bug reports, or #libp2p-implementers on [Filecoin slack](https://filecoin.io/slack).
- ensure you are able to contribute (no legal issues please -- we use the DCO)
- get in touch with @libp2p/go-libp2p-maintainers about how best to contribute
- No drive-by contributions seeking to collect airdrops.
  - Many projects aim to reward contributors to common goods. Great. However,
    this creates an unfortunate incentive for low-effort PRs, submitted solely to
    claim rewards. These PRs consume maintainers’ time and energy to triage, with
    little to no impact on end users. If we suspect this is the intent of a PR,
    we may close it without comment. If you believe this was done in error,
    contact us via email. Reference this README section and explain why your PR
    is not a “drive-by contribution.”
- have fun!

There's a few things you can do right now to help out:
 - **Perform code reviews**.
 - **Add tests**. There can never be enough tests.
 - Go through the modules below and **check out existing issues**. This would
   be especially useful for modules in active development. Some knowledge of
   IPFS/libp2p may be required, as well as the infrastructure behind it - for
   instance, you may need to read up on p2p and more complex operations like
   muxing to be able to help technically.


## Supported Go Versions

We test against and support the two most recent major releases of Go. This is
informed by Go's own [security policy](https://go.dev/doc/security/policy).

# Notable Users
Some notable users of go-libp2p are:
- [Kubo](https://github.com/ipfs/kubo) - The original Go implementation of IPFS
- [Lotus](https://github.com/filecoin-project/lotus) - An implementation of the Filecoin protocol
- [Drand](https://github.com/drand/drand) - A distributed random beacon daemon
- [Prysm](https://github.com/prysmaticlabs/prysm) - An Ethereum Beacon Chain consensus client built by [Prysmatic Labs](https://prysmaticlabs.com/)
- [Berty](https://github.com/berty/berty) - An open, secure, offline-first, peer-to-peer and zero trust messaging app.
- [Wasp](https://github.com/iotaledger/wasp) - A node that runs IOTA Smart Contracts built by the [IOTA Foundation](https://www.iota.org/)
- [Mina](https://github.com/minaprotocol/mina) - A lightweight, constant-sized blockchain that runs zero-knowledge smart contracts
- [Polygon Edge](https://github.com/0xPolygon/polygon-edge) - A modular, extensible framework for building Ethereum compatible networks
- [Celestia Node](https://github.com/celestiaorg/celestia-node) - The Go implementation of Celestia's data availability nodes
- [Status go](https://github.com/status-im/status-go) - Status bindings for go-ethereum, built by [Status.im](https://status.im/)
- [Flow](https://github.com/onflow/flow-go) - A blockchain built to support games, apps, and digital assets built by [Dapper Labs](https://www.dapperlabs.com/)
- [Swarm Bee](https://github.com/ethersphere/bee) - A client for connecting to the [Swarm network](https://www.ethswarm.org/)
- [MultiversX Node](https://github.com/multiversx/mx-chain-go) - The Go implementation of the MultiversX network protocol
- [Sonr](https://github.com/sonr-io/sonr) - A platform to integrate DID Documents, WebAuthn, and IPFS and manage digital identity and assets.
- [EdgeVPN](https://github.com/mudler/edgevpn) - A decentralized, immutable, portable VPN and reverse proxy over p2p.
- [Kairos](https://github.com/kairos-io/kairos) - A Kubernetes-focused, Cloud Native Linux meta-distribution.
- [Oasis Core](https://github.com/oasisprotocol/oasis-core) - The consensus and runtime layers of the [Oasis protocol](https://oasisprotocol.org/).
- [Spacemesh](https://github.com/spacemeshos/go-spacemesh/) - The Go implementation of the [Spacemesh protocol](https://spacemesh.io/), a novel layer one blockchain
- [Tau](https://github.com/taubyte/tau/) - Open source distributed Platform as a Service (PaaS)

Please open a pull request if you want your project (min. 250 GitHub stars) to be added here.
