# go-libp2p

![](https://raw.githubusercontent.com/libp2p/libp2p/master/img/logo.png)

[![](https://img.shields.io/badge/made%20by-Protocol%20Labs-blue.svg?style=flat-square)](http://ipn.io)
[![](https://img.shields.io/badge/freenode-%23ipfs-blue.svg?style=flat-square)](https://webchat.freenode.net/?channels=%23ipfs)
[![](https://img.shields.io/badge/project-libp2p-blue.svg?style=flat-square)](https://github.com/libp2p/libp2p)
[![](https://img.shields.io/badge/readme%20style-standard-brightgreen.svg?style=flat-square)](https://github.com/RichardLitt/standard-readme)
[![GoDoc](https://godoc.org/github.com/ipfs/go-libp2p?status.svg)](https://godoc.org/github.com/libp2p/go-libp2p)
[![Build Status](https://travis-ci.org/ipfs/go-libp2p.svg?branch=master)](https://travis-ci.org/libp2p/go-libp2p)

> [libp2p](https://github.com/ipfs/specs/tree/master/libp2p) implementation in Go.

[libp2p](https://github.com/libp2p/specs) is a networking stack and library modularized out of [The IPFS Project](https://github.com/ipfs/ipfs), and bundled separately for other tools to use.

libp2p is the product of a long and arduous quest of understanding -- a deep dive into the internet's network stack, and plentiful peer-to-peer protocols from the past. Building large scale peer-to-peer systems has been complex and difficult in the last 15 years, and libp2p is a way to fix that. It is a "network stack" -- a protocol suite -- that cleanly separates concerns, and enables sophisticated applications to only use the protocols they absolutely need, without giving up interoperability and upgradeability. libp2p grew out of IPFS, but it is built so that lots of people can use it, for lots of different projects.

We will be writing a set of docs, posts, tutorials, and talks to explain what p2p is, why it is tremendously useful, and how it can help your existing and new projects. But in the meantime, check out:

- [**The IPFS Network Spec**](https://github.com/ipfs/specs/tree/master/libp2p), which grew into libp2p
- [**go-libp2p implementation**](https://github.com/libp2p/go-libp2p)
- [**js-libp2p implementation**](https://github.com/libp2p/js-libp2p)

## Table of Contents

- [Install](#install)
- [Usage](#usage)
- [Examples](#examples)
- [Run tests](#run-tests)
- [Links](#links)
- [Extracting packages from go-libp2p](#extracting-packages-from-go-libp2p)
- [Contribute](#contribute)
- [License](#license)

## Install

```bash
$ go get -d github.com/libp2p/go-libp2p
$ cd $GOPATH/src/github.com/libp2p/go-libp2p
$ make
```

## Usage

`go-libp2p` repo will be a place holder for the list of Go modules that compose Go libp2p, as well as its entry point.

## Examples

Examples can be found on the [examples folder](examples).

## Run tests

```bash
$ cd $GOPATH/src/github.com/libp2p/go-libp2p
$ make deps
$ go test ./p2p/<path of module you want to run tests for>
```

## Links
- [**Specs**](https://github.com/libp2p/specs)
- [**Website**](https://github.com/libp2p/website)

## Extracting packages from go-libp2p

We want to maintain history, so we'll use git-subtree for extracting packages.

```sh
# 1) create the extracted tree (has the directory specified as -P as its root)
> cd go-libp2p/
> git subtree split -P p2p/crypto/secio/ -b libp2p-secio
62b0a5c21574bcbe06c422785cd5feff378ae5bd
# important to delete the tree now, so that outdated imports fail in step 5
> git rm -r p2p/crypto/secio/
> git commit
> cd ../

# 2) make the new repo
> mkdir go-libp2p-secio
> cd go-libp2p-secio/
> git init && git commit --allow-empty

# 3) fetch the extracted tree from the previous repo
> git remote add libp2p ../go-libp2p
> git fetch libp2p
> git reset --hard libp2p/libp2p-secio

# 4) update self import paths
> sed -someflagsidontknow 'go-libp2p/p2p/crypto/secio' 'golibp2p-secio'
> git commit

# 5) create package.json and check all imports are correct
> vim package.json
> gx --verbose install --global
> gx-go rewrite
> go test ./...
> gx-go rewrite --undo
> git commit

# 4) make the package ready
> vim README.md LICENSE
> git commit

# 5) bump the version separately
> vim package.json
> gx publish
> git add package.json .gx/
> git commit -m 'Publish 1.2.3'

# 6) clean up and push
> git remote rm libp2p
> git push origin master
```

## Maintainers

Captain: [@whyrusleeping](https://github.com/whyrusleeping)

## Contribute

libp2p implementation in Go is a work in progress. As such, there's a few things you can do right now to help out:
 - Go through the modules below and [**check out existing issues**](https://github.com/libp2p/go-libp2p/issues). This would be especially useful for modules in active development. Some knowledge of IPFS/libp2p may be required, as well as the infrasture behind it - for instance, you may need to read up on p2p and more complex operations like muxing to be able to help technically.
 - **Perform code reviews**.
 - **Add tests**. There can never be enough tests.

Feel free to join in. All are welcome. Open an [issue](https://github.com/libp2p/go-libp2p-crypto/issues)!

Check out our [contributing document](https://github.com/libp2p/community/blob/master/CONTRIBUTE.md) for more information on how we work, and about contributing in general. Please be aware that all interactions related to libp2p are subject to the IPFS [Code of Conduct](https://github.com/ipfs/community/blob/master/code-of-conduct.md).

Small note: If editing the README, please conform to the [standard-readme](https://github.com/RichardLitt/standard-readme) specification.

## License

[MIT](LICENSE) © 2014 Juan Batiz-Benet
