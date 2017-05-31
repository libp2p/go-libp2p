[libp2p](https://github.com/libp2p/specs) implementation in Go.
===================

[![](https://img.shields.io/badge/made%20by-Protocol%20Labs-blue.svg?style=flat-square)](http://ipn.io)
[![](https://img.shields.io/badge/freenode-%23ipfs-blue.svg?style=flat-square)](http://webchat.freenode.net/?channels=%23ipfs)
[![GoDoc](https://godoc.org/github.com/ipfs/go-libp2p?status.svg)](https://godoc.org/github.com/libp2p/go-libp2p)
[![Build Status](https://travis-ci.org/ipfs/go-libp2p.svg?branch=master)](https://travis-ci.org/libp2p/go-libp2p)

<p align="center">
  <img src="https://github.com/libp2p/libp2p/raw/master/logo/libp2p-logo.png" />
</p>

> libp2p implementation in Go

# Description

[libp2p](https://github.com/libp2p/specs) is a networking stack and library modularized out of [The IPFS Project](https://github.com/ipfs/ipfs), and bundled separately for other tools to use.

> libp2p is the product of a long, and arduous quest of understanding -- a deep dive into the internet's network stack, and plentiful peer-to-peer protocols from the past. Building large scale peer-to-peer systems has been complex and difficult in the last 15 years, and libp2p is a way to fix that. It is a "network stack" -- a protocol suite -- that cleanly separates concerns, and enables sophisticated applications to only use the protocols they absolutely need, without giving up interoperability and upgradeability. libp2p grew out of IPFS, but it is built so that lots of people can use it, for lots of different projects.

This is the entry point for the Go implementation of `libp2p`, which is formed by multiple modules, usually accessible under the `github.com/libp2p/go-libp2p-*` namespace.

# Usage

The vast majority of `go-libp2p` modules live in their own repositories. This repository is the apex of the Go `libp2p` stack, and primarily serves to track the working-version set of all the different modules.

This is done using [Gx](https://github.com/whyrusleeping/gx) and its companion [Gx-go](https://github.com/whyrusleeping/gx-go).

The [`package.json`](https://github.com/libp2p/go-libp2p/blob/master/package.json) file maintains the current list of dependencies which, when used, provide a working version of the stack.

**Any `go-libp2p` application (including the examples in this repository) need to have their imports rewritten with Gx, otherwise compilation will fail.**

The process to get `go-libp2p` working in your project is as follows:

* Make sure you install Gx and Gx-go:

```
go get -u github.com/whyrusleeping/gx
go get -u github.com/whyrusleeping/gx-go
```

* Initialize Gx in the base folder of your project's repository. This will create a `package.json` file:

```
gx init
```

* Import `go-libp2p` to your project. This will import `go-libp2p` on its latest published version:

```
gx import github.com/libp2p/go-libp2p
```

* Download and install all the `go-libp2p` dependencies:

```
gx install --verbose --global
```

* Write your `go-libp2p` code normally and use regular imports (i.e. `import "github.com/libp2p/go-libp2p-net"`)
* Before compiling, run a Gx-go-rewrite command in the base folder of your repository: You will see that your `go-libp2p-*` imports have been rewritten to something like `gx/ipfs/QmVtMT3fD7DzQNW7hdm6Xe6KPstzcggrhNpeVZ4422UpKK/go-libp2p-net`:

```
gx-go rewrite
```

* With the imports rewritten, you can now compile your `go-libp2p`. If you don't rewrite your imports, your build will error as `go-libp2p` does not build on master.
* Before committing, it is customary to undo the rewrites with:

```
gx-go rewrite --undo
```

Most projects include these steps in a `Makefile` for convenience. [Here is an example](https://github.com/libp2p/go-libp2p-raft/blob/master/Makefile).


# Contribute

libp2p implementation in Go is a work in progress. As such, there's a few things you can do right now to help out:
 - Go through the modules below and **check out existing issues**. This would be especially useful for modules in active development. Some knowledge of IPFS/libp2p may be required, as well as the infrasture behind it - for instance, you may need to read up on p2p and more complex operations like muxing to be able to help technically.
 - **Perform code reviews**.
 - **Add tests**. There can never be enough tests.


# Examples

Examples and information on how to run them can be found on the [examples folder](examples) and their respective READMEs.

# Run tests

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
