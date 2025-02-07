package memory

import (
	"context"
	"log"
	"sync"
	"sync/atomic"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
)

type conn struct {
	id    int64
	rconn *conn

	scope     network.ConnManagementScope
	listener  *listener
	transport *transport

	localPeer      peer.ID
	localMultiaddr ma.Multiaddr

	remotePeerID    peer.ID
	remotePubKey    ic.PubKey
	remoteMultiaddr ma.Multiaddr

	mu sync.Mutex

	closed    atomic.Bool
	closeOnce sync.Once

	streamC chan *stream
	streams map[int64]network.MuxedStream
}

var _ tpt.CapableConn = &conn{}

func newConnection(
	t *transport,
	s *stream,
	localPeer peer.ID,
	localMultiaddr ma.Multiaddr,
	remotePubKey ic.PubKey,
	remotePeer peer.ID,
	remoteMultiaddr ma.Multiaddr,
) *conn {
	c := &conn{
		id:              connCounter.Add(1),
		transport:       t,
		localPeer:       localPeer,
		localMultiaddr:  localMultiaddr,
		remotePubKey:    remotePubKey,
		remotePeerID:    remotePeer,
		remoteMultiaddr: remoteMultiaddr,
		streamC:         make(chan *stream, 1),
		streams:         make(map[int64]network.MuxedStream),
	}

	c.addStream(s.id, s)
	return c
}

func (c *conn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		go c.rconn.Close()
		c.teardown()
	})

	return nil
}

func (c *conn) IsClosed() bool {
	return c.closed.Load()
}

func (c *conn) OpenStream(ctx context.Context) (network.MuxedStream, error) {
	sl, sr := newStreamPair()
	sl.conn = c
	c.addStream(sl.id, sl)
	log.Println("opening stream", sl.id, sr.id)

	c.rconn.streamC <- sr
	return sl, nil
}

func (c *conn) AcceptStream() (network.MuxedStream, error) {
	in := <-c.streamC
	in.conn = c
	c.addStream(in.id, in)
	return in, nil
}

func (c *conn) LocalPeer() peer.ID { return c.localPeer }

// RemotePeer returns the peer ID of the remote peer.
func (c *conn) RemotePeer() peer.ID { return c.remotePeerID }

// RemotePublicKey returns the public pkey of the remote peer.
func (c *conn) RemotePublicKey() ic.PubKey { return c.remotePubKey }

// LocalMultiaddr returns the local Multiaddr associated
func (c *conn) LocalMultiaddr() ma.Multiaddr { return c.localMultiaddr }

// RemoteMultiaddr returns the remote Multiaddr associated
func (c *conn) RemoteMultiaddr() ma.Multiaddr { return c.remoteMultiaddr }

func (c *conn) Transport() tpt.Transport {
	return c.transport
}

func (c *conn) Scope() network.ConnScope {
	return c.scope
}

// ConnState is the state of security connection.
func (c *conn) ConnState() network.ConnectionState {
	return network.ConnectionState{Transport: "memory"}
}

func (c *conn) addStream(id int64, stream network.MuxedStream) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.streams[id] = stream
}

func (c *conn) removeStream(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.streams, id)
}

func (c *conn) teardown() {
	for id, s := range c.streams {
		log.Println("tearing down stream", id)
		s.Reset()
	}

	// TODO: remove self from listener
}
