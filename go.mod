module github.com/libp2p/go-libp2p

go 1.12

require (
	github.com/gogo/protobuf v1.3.1
	github.com/ipfs/go-cid v0.0.5
	github.com/ipfs/go-detect-race v0.0.1
	github.com/ipfs/go-ipfs-util v0.0.1
	github.com/ipfs/go-log v1.0.4
	github.com/jbenet/go-cienv v0.1.0
	github.com/jbenet/goprocess v0.1.4
	github.com/libp2p/go-conn-security-multistream v0.2.0
	github.com/libp2p/go-eventbus v0.1.0
	github.com/libp2p/go-libp2p-autonat v0.2.1
	github.com/libp2p/go-libp2p-blankhost v0.1.4
	github.com/libp2p/go-libp2p-circuit v0.2.1
	github.com/libp2p/go-libp2p-core v0.5.5
	github.com/libp2p/go-libp2p-discovery v0.3.0
	github.com/libp2p/go-libp2p-loggables v0.1.0
	github.com/libp2p/go-libp2p-mplex v0.2.3
	github.com/libp2p/go-libp2p-nat v0.0.6
	github.com/libp2p/go-libp2p-netutil v0.1.0
	github.com/libp2p/go-libp2p-peerstore v0.2.3
	github.com/libp2p/go-libp2p-secio v0.2.2
	github.com/libp2p/go-libp2p-swarm v0.2.4-0.20200413072055-fb9362ad8f18
	github.com/libp2p/go-libp2p-testing v0.1.1
	github.com/libp2p/go-libp2p-transport-upgrader v0.2.1-0.20200513191341-03eaee6978e2
	github.com/libp2p/go-libp2p-yamux v0.2.7
	github.com/libp2p/go-stream-muxer-multistream v0.3.0
	github.com/libp2p/go-tcp-transport v0.2.0
	github.com/libp2p/go-ws-transport v0.3.0
	github.com/multiformats/go-multiaddr v0.2.2
	github.com/multiformats/go-multiaddr-dns v0.2.0
	github.com/multiformats/go-multiaddr-net v0.1.5
	github.com/multiformats/go-multibase v0.0.2 // indirect
	github.com/multiformats/go-multistream v0.1.1
	github.com/stretchr/testify v1.5.1
	github.com/whyrusleeping/mdns v0.0.0-20190826153040-b9b60ed33aa9
	golang.org/x/crypto v0.0.0-20200510223506-06a226fb4e37 // indirect
	golang.org/x/sys v0.0.0-20200513112337-417ce2331b5c // indirect
)

replace github.com/libp2p/go-libp2p-swarm => ../go-libp2p-swarm
