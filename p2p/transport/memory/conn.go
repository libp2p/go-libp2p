package memory

import (
	"context"
	"sync"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
)

type conn struct {
	transport *transport
	scope     network.ConnManagementScope

	localPeer      peer.ID
	localMultiaddr ma.Multiaddr

	remotePeerID    peer.ID
	remotePubKey    ic.PubKey
	remoteMultiaddr ma.Multiaddr

	closed    bool
	closeOnce sync.Once
}

var _ tpt.CapableConn = &conn{}

func (c *conn) Close() error {
	c.closeOnce.Do(func() {
		c.closed = true
		c.transport.removeConn(c)
	})

	return nil
}

func (c *conn) IsClosed() bool {
	return c.closed
}

func (c *conn) OpenStream(ctx context.Context) (network.MuxedStream, error) {
	return newStream(), nil
}

func (c *conn) AcceptStream() (network.MuxedStream, error) {
	return nil, nil
}

func (c *conn) LocalPeer() peer.ID { return c.localPeer }

// RemotePeer returns the peer ID of the remote peer.
func (c *conn) RemotePeer() peer.ID { return c.remotePeerID }

// RemotePublicKey returns the public key of the remote peer.
func (c *conn) RemotePublicKey() ic.PubKey { return c.remotePubKey }

// LocalMultiaddr returns the local Multiaddr associated
func (c *conn) LocalMultiaddr() ma.Multiaddr { return c.localMultiaddr }

// RemoteMultiaddr returns the remote Multiaddr associated
func (c *conn) RemoteMultiaddr() ma.Multiaddr { return c.remoteMultiaddr }

func (c *conn) Transport() tpt.Transport {
	// TODO: return c.transport
	return nil
}

func (c *conn) Scope() network.ConnScope { return c.scope }

// ConnState is the state of security connection.
func (c *conn) ConnState() network.ConnectionState {
	return network.ConnectionState{Transport: "memory"}
}
