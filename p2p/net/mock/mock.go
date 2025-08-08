package mocknet

import (
	logging "github.com/libp2p/go-libp2p/gologshim"
)

var log = logging.Logger("mocknet")

// WithNPeers constructs a Mocknet with N peers.
func WithNPeers(n int) (Mocknet, error) {
	m := New()
	for i := 0; i < n; i++ {
		if _, err := m.GenPeer(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// FullMeshLinked constructs a Mocknet with full mesh of Links.
// This means that all the peers **can** connect to each other
// (not that they already are connected. you can use m.ConnectAll())
func FullMeshLinked(n int) (Mocknet, error) {
	m, err := WithNPeers(n)
	if err != nil {
		return nil, err
	}

	if err := m.LinkAll(); err != nil {
		return nil, err
	}
	return m, nil
}

// FullMeshConnected constructs a Mocknet with full mesh of Connections.
// This means that all the peers have dialed and are ready to talk to
// each other.
func FullMeshConnected(n int) (Mocknet, error) {
	m, err := FullMeshLinked(n)
	if err != nil {
		return nil, err
	}

	if err := m.ConnectAllButSelf(); err != nil {
		return nil, err
	}
	return m, nil
}
