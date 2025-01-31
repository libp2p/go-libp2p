package websocket

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	ws "github.com/gorilla/websocket"
)

// GracefulCloseTimeout is the time to wait trying to gracefully close a
// connection before simply cutting it.
var GracefulCloseTimeout = 100 * time.Millisecond

// Conn implements net.Conn interface for gorilla/websocket.
type Conn struct {
	*ws.Conn
	secure             bool
	DefaultMessageType int
	reader             io.Reader
	closeOnceVal       func() error
	laddr              ma.Multiaddr
	raddr              ma.Multiaddr

	readLock, writeLock sync.Mutex
}

var _ net.Conn = (*Conn)(nil)
var _ manet.Conn = (*Conn)(nil)

// NewConn creates a Conn given a regular gorilla/websocket Conn.
//
// Deprecated: There's no reason to use this method externally. It'll be unexported in a future release.
func NewConn(raw *ws.Conn, secure bool) *Conn {
	lna := NewAddrWithScheme(raw.LocalAddr().String(), secure)
	laddr, err := manet.FromNetAddr(lna)
	if err != nil {
		log.Errorf("BUG: invalid localaddr on websocket conn", raw.LocalAddr())
		return nil
	}

	rna := NewAddrWithScheme(raw.RemoteAddr().String(), secure)
	raddr, err := manet.FromNetAddr(rna)
	if err != nil {
		log.Errorf("BUG: invalid remoteaddr on websocket conn", raw.RemoteAddr())
		return nil
	}

	c := &Conn{
		Conn:               raw,
		secure:             secure,
		DefaultMessageType: ws.BinaryMessage,
		laddr:              laddr,
		raddr:              raddr,
	}
	c.closeOnceVal = sync.OnceValue(c.closeOnceFn)
	return c
}

// LocalMultiaddr implements manet.Conn.
func (c *Conn) LocalMultiaddr() ma.Multiaddr {
	return c.laddr
}

// RemoteMultiaddr implements manet.Conn.
func (c *Conn) RemoteMultiaddr() ma.Multiaddr {
	return c.raddr
}

func (c *Conn) Read(b []byte) (int, error) {
	c.readLock.Lock()
	defer c.readLock.Unlock()

	if c.reader == nil {
		if err := c.prepNextReader(); err != nil {
			return 0, err
		}
	}

	for {
		n, err := c.reader.Read(b)
		switch err {
		case io.EOF:
			c.reader = nil

			if n > 0 {
				return n, nil
			}

			if err := c.prepNextReader(); err != nil {
				return 0, err
			}

			// explicitly looping
		default:
			return n, err
		}
	}
}

func (c *Conn) prepNextReader() error {
	t, r, err := c.Conn.NextReader()
	if err != nil {
		if wserr, ok := err.(*ws.CloseError); ok {
			if wserr.Code == 1000 || wserr.Code == 1005 {
				return io.EOF
			}
		}
		return err
	}

	if t == ws.CloseMessage {
		return io.EOF
	}

	c.reader = r
	return nil
}

func (c *Conn) Write(b []byte) (n int, err error) {
	c.writeLock.Lock()
	defer c.writeLock.Unlock()

	if err := c.Conn.WriteMessage(c.DefaultMessageType, b); err != nil {
		return 0, err
	}

	return len(b), nil
}

func (c *Conn) Scope() network.ConnManagementScope {
	nc := c.NetConn()
	if sc, ok := nc.(interface {
		Scope() network.ConnManagementScope
	}); ok {
		return sc.Scope()
	}
	return nil
}

// Close closes the connection.
// subsequent and concurrent calls will return the same error value.
// This method is thread-safe.
func (c *Conn) Close() error {
	return c.closeOnceVal()
}

func (c *Conn) closeOnceFn() error {
	err1 := c.Conn.WriteControl(
		ws.CloseMessage,
		ws.FormatCloseMessage(ws.CloseNormalClosure, "closed"),
		time.Now().Add(GracefulCloseTimeout),
	)
	err2 := c.Conn.Close()
	return errors.Join(err1, err2)
}

func (c *Conn) LocalAddr() net.Addr {
	return NewAddrWithScheme(c.Conn.LocalAddr().String(), c.secure)
}

func (c *Conn) RemoteAddr() net.Addr {
	return NewAddrWithScheme(c.Conn.RemoteAddr().String(), c.secure)
}

func (c *Conn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}

	return c.SetWriteDeadline(t)
}

func (c *Conn) SetReadDeadline(t time.Time) error {
	// Don't lock when setting the read deadline. That would prevent us from
	// interrupting an in-progress read.
	return c.Conn.SetReadDeadline(t)
}

func (c *Conn) SetWriteDeadline(t time.Time) error {
	// Unlike the read deadline, we need to lock when setting the write
	// deadline.

	c.writeLock.Lock()
	defer c.writeLock.Unlock()

	return c.Conn.SetWriteDeadline(t)
}

type capableConn struct {
	transport.CapableConn
}

func (c *capableConn) ConnState() network.ConnectionState {
	cs := c.CapableConn.ConnState()
	cs.Transport = "websocket"
	return cs
}
