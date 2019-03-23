package mocknet

import (
	"container/list"
	"sync"

	process "github.com/jbenet/goprocess"
	ic "github.com/libp2p/go-libp2p-crypto"
	inet "github.com/libp2p/go-libp2p-net"
	peer "github.com/libp2p/go-libp2p-peer"
	ma "github.com/multiformats/go-multiaddr"
)

// conn represents one side's perspective of a
// live connection between two peers.
// it goes over a particular link.
type conn struct {
	local  peer.ID
	remote peer.ID

	localAddr  ma.Multiaddr
	remoteAddr ma.Multiaddr

	localPrivKey ic.PrivKey
	remotePubKey ic.PubKey

	net     *peernet
	link    *link
	rconn   *conn // counterpart
	streams list.List
	proc    process.Process
	stat    inet.Stat

	sync.RWMutex
}

func newConn(ln, rn *peernet, l *link, dir inet.Direction) *conn {
	c := &conn{net: ln, link: l}
	c.local = ln.peer
	c.remote = rn.peer
	c.stat = inet.Stat{Direction: dir}

	c.localAddr = ln.ps.Addrs(ln.peer)[0]
	c.remoteAddr = rn.ps.Addrs(rn.peer)[0]

	c.localPrivKey = ln.ps.PrivKey(ln.peer)
	c.remotePubKey = rn.ps.PubKey(rn.peer)

	c.proc = process.WithTeardown(c.teardown)
	return c
}

func (c *conn) Close() error {
	return c.proc.Close()
}

func (c *conn) teardown() error {
	for _, s := range c.allStreams() {
		s.Reset()
	}
	c.net.removeConn(c)
	c.net.notifyAll(func(n inet.Notifiee) {
		n.Disconnected(c.net, c)
	})
	return nil
}

func (c *conn) addStream(s *stream) {
	c.Lock()
	s.conn = c
	c.streams.PushBack(s)
	c.Unlock()
}

func (c *conn) removeStream(s *stream) {
	c.Lock()
	defer c.Unlock()
	for e := c.streams.Front(); e != nil; e = e.Next() {
		if s == e.Value {
			c.streams.Remove(e)
			return
		}
	}
}

func (c *conn) allStreams() []inet.Stream {
	c.RLock()
	defer c.RUnlock()

	strs := make([]inet.Stream, 0, c.streams.Len())
	for e := c.streams.Front(); e != nil; e = e.Next() {
		s := e.Value.(*stream)
		strs = append(strs, s)
	}
	return strs
}

func (c *conn) remoteOpenedStream(s *stream) {
	c.addStream(s)
	c.net.handleNewStream(s)
	c.net.notifyAll(func(n inet.Notifiee) {
		n.OpenedStream(c.net, s)
	})
}

func (c *conn) openStream() *stream {
	sl, sr := c.link.newStreamPair()
	c.addStream(sl)
	c.net.notifyAll(func(n inet.Notifiee) {
		n.OpenedStream(c.net, sl)
	})
	c.rconn.remoteOpenedStream(sr)
	return sl
}

func (c *conn) NewStream() (inet.Stream, error) {
	log.Debugf("Conn.NewStreamWithProtocol: %s --> %s", c.local, c.remote)

	s := c.openStream()
	return s, nil
}

func (c *conn) GetStreams() []inet.Stream {
	return c.allStreams()
}

// LocalMultiaddr is the Multiaddr on this side
func (c *conn) LocalMultiaddr() ma.Multiaddr {
	return c.localAddr
}

// LocalPeer is the Peer on our side of the connection
func (c *conn) LocalPeer() peer.ID {
	return c.local
}

// LocalPrivateKey is the private key of the peer on our side.
func (c *conn) LocalPrivateKey() ic.PrivKey {
	return c.localPrivKey
}

// RemoteMultiaddr is the Multiaddr on the remote side
func (c *conn) RemoteMultiaddr() ma.Multiaddr {
	return c.remoteAddr
}

// RemotePeer is the Peer on the remote side
func (c *conn) RemotePeer() peer.ID {
	return c.remote
}

// RemotePublicKey is the private key of the peer on our side.
func (c *conn) RemotePublicKey() ic.PubKey {
	return c.remotePubKey
}

// Stat returns metadata about the connection
func (c *conn) Stat() inet.Stat {
	return c.stat
}
