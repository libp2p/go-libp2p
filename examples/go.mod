module github.com/libp2p/go-libp2p/examples

go 1.13

require (
	github.com/gogo/protobuf v1.3.2
	github.com/google/uuid v1.2.0
	github.com/ipfs/go-datastore v0.4.5
	github.com/ipfs/go-log/v2 v2.1.3
	github.com/libp2p/go-libp2p v0.14.1
	github.com/libp2p/go-libp2p-circuit v0.4.0
	github.com/libp2p/go-libp2p-connmgr v0.2.4
	github.com/libp2p/go-libp2p-core v0.8.5
	github.com/libp2p/go-libp2p-discovery v0.5.0
	github.com/libp2p/go-libp2p-kad-dht v0.11.1
	github.com/libp2p/go-libp2p-quic-transport v0.11.2
	github.com/libp2p/go-libp2p-secio v0.2.2
	github.com/libp2p/go-libp2p-swarm v0.5.0
	github.com/libp2p/go-libp2p-tls v0.1.3
	github.com/multiformats/go-multiaddr v0.3.3
)

// Ensure that examples always use the go-libp2p version in the same git checkout.
replace github.com/libp2p/go-libp2p => ../
