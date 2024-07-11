package gostream

import (
	"context"
	"io"
	"net"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// conn is an implementation of net.Conn which wraps
// libp2p streams.
type conn struct {
	network.Stream
	ignoreEOF bool
}

func (c *conn) Read(b []byte) (int, error) {
	n, err := c.Stream.Read(b)
	if err != nil && c.ignoreEOF && err == io.EOF {
		return n, nil
	}
	return n, err
}

// newConn creates a conn given a libp2p stream
func newConn(s network.Stream, ignoreEOF bool) net.Conn {
	return &conn{s, ignoreEOF}
}

// LocalAddr returns the local network address.
func (c *conn) LocalAddr() net.Addr {
	return &addr{c.Stream.Conn().LocalPeer()}
}

// RemoteAddr returns the remote network address.
func (c *conn) RemoteAddr() net.Addr {
	return &addr{c.Stream.Conn().RemotePeer()}
}

// Dial opens a stream to the destination address
// (which should parseable to a peer ID) using the given
// host and returns it as a standard net.Conn.
func Dial(ctx context.Context, h host.Host, pid peer.ID, tag protocol.ID) (net.Conn, error) {
	s, err := h.NewStream(ctx, pid, tag)
	if err != nil {
		return nil, err
	}
	return newConn(s, false), nil
}
