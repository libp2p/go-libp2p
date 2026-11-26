package client

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// HopTagWeight is the connection manager weight for connections carrying relay hop streams
var HopTagWeight = 5

type statLimitDuration struct{}
type statLimitData struct{}

var (
	StatLimitDuration = statLimitDuration{}
	StatLimitData     = statLimitData{}
)

// closeTimeout is how long a closed Conn waits for the remote side to close
// its end, before it resets the stream to the relay.
var closeTimeout = 5 * time.Second

type Conn struct {
	stream network.Stream
	remote peer.AddrInfo
	stat   network.ConnStats

	client *Client

	closeOnce sync.Once
	closed    atomic.Bool
	readMu    sync.Mutex // held while reading from stream
}

type NetAddr struct {
	Relay  string
	Remote string
}

var _ net.Addr = (*NetAddr)(nil)

func (n *NetAddr) Network() string {
	return "libp2p-circuit-relay"
}

func (n *NetAddr) String() string {
	return fmt.Sprintf("relay[%s-%s]", n.Remote, n.Relay)
}

// Conn interface
var _ manet.Conn = (*Conn)(nil)

// Close closes the write side of the stream to the relay, and resets the
// stream once the remote side has closed its end or closeTimeout has passed.
//
// Resetting the stream right away would drop what was written last, here or
// at the relay. That is the muxer's GoAway frame with the error code, when the
// connection is closed with an error.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.untagHop()
		if err := c.stream.CloseWrite(); err != nil {
			c.stream.Reset()
			return
		}
		c.closed.Store(true)
		// Interrupt a Read in progress.
		c.stream.SetReadDeadline(time.Now())
		go c.waitForRemoteClose()
	})
	return nil
}

func (c *Conn) waitForRemoteClose() {
	// Wait for the Read that Close interrupted.
	c.readMu.Lock()
	defer c.readMu.Unlock()
	// The timeout doesn't use the read deadline, which belongs to the caller.
	c.stream.SetReadDeadline(time.Time{})
	timeout := time.AfterFunc(closeTimeout, func() { c.stream.Reset() })
	defer timeout.Stop()
	if _, err := io.Copy(io.Discard, c.stream); err != nil {
		c.stream.Reset()
		return
	}
	c.stream.Close()
}

func (c *Conn) Read(buf []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	// Check again: waitForRemoteClose may have had the lock.
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	return c.stream.Read(buf)
}

func (c *Conn) Write(buf []byte) (int, error) {
	return c.stream.Write(buf)
}

func (c *Conn) SetDeadline(t time.Time) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	return c.stream.SetDeadline(t)
}

func (c *Conn) SetReadDeadline(t time.Time) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	return c.stream.SetReadDeadline(t)
}

func (c *Conn) SetWriteDeadline(t time.Time) error {
	return c.stream.SetWriteDeadline(t)
}

// TODO: is it okay to cast c.Conn().RemotePeer() into a multiaddr? might be "user input"
func (c *Conn) RemoteMultiaddr() ma.Multiaddr {
	// TODO: We should be able to do this directly without converting to/from a string.
	relayAddr, err := ma.NewComponent(
		ma.ProtocolWithCode(ma.P_P2P).Name,
		c.stream.Conn().RemotePeer().String(),
	)
	if err != nil {
		log.Error("failed to create relay address:", "err", err)
		return ma.Join(c.stream.Conn().RemoteMultiaddr(), circuitAddr)
	}
	return ma.Join(c.stream.Conn().RemoteMultiaddr(), relayAddr.Multiaddr(), circuitAddr)
}

func (c *Conn) LocalMultiaddr() ma.Multiaddr {
	return c.stream.Conn().LocalMultiaddr()
}

func (c *Conn) LocalAddr() net.Addr {
	na, err := manet.ToNetAddr(c.stream.Conn().LocalMultiaddr())
	if err != nil {
		log.Error("failed to convert local multiaddr to net addr:", "err", err)
		return nil
	}
	return na
}

func (c *Conn) RemoteAddr() net.Addr {
	return &NetAddr{
		Relay:  c.stream.Conn().RemotePeer().String(),
		Remote: c.remote.ID.String(),
	}
}

// ConnStat interface
var _ network.ConnStat = (*Conn)(nil)

func (c *Conn) Stat() network.ConnStats {
	return c.stat
}

// tagHop tags the underlying relay connection so that it can be (somewhat) protected from the
// connection manager as it is an important connection that proxies other connections.
// This is handled here so that the user code doesnt need to bother with this and avoid
// clown shoes situations where a high value peer connection is behind a relayed connection and it is
// implicitly because the connection manager closed the underlying relay connection.
func (c *Conn) tagHop() {
	c.client.mx.Lock()
	defer c.client.mx.Unlock()

	p := c.stream.Conn().RemotePeer()
	c.client.hopCount[p]++
	if c.client.hopCount[p] == 1 {
		c.client.host.ConnManager().TagPeer(p, "relay-hop-stream", HopTagWeight)
	}
}

// untagHop removes the relay-hop-stream tag if necessary; it is invoked when a relayed connection
// is closed.
func (c *Conn) untagHop() {
	c.client.mx.Lock()
	defer c.client.mx.Unlock()

	p := c.stream.Conn().RemotePeer()
	c.client.hopCount[p]--
	if c.client.hopCount[p] == 0 {
		c.client.host.ConnManager().UntagPeer(p, "relay-hop-stream")
		delete(c.client.hopCount, p)
	}
}

type capableConnWithStat interface {
	tpt.CapableConn
	network.ConnStat
}

type capableConn struct {
	capableConnWithStat
}

var transportName = ma.ProtocolWithCode(ma.P_CIRCUIT).Name

func (c capableConn) ConnState() network.ConnectionState {
	return network.ConnectionState{
		Transport: transportName,
	}
}
