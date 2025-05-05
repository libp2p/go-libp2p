//go:build js

package libp2pwebtransport

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"syscall/js"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
)

var _ net.Addr = (*addr)(nil)

type addr struct {
	url string
}

func (addr *addr) Network() string {
	return "webtransport"
}

func (addr *addr) String() string {
	return addr.url
}

type conn struct {
	scope     network.ConnScope
	transport *transport

	wt       js.Value
	incoming js.Value

	rpid peer.ID
	rpk  ic.PubKey

	rmaddr ma.Multiaddr
	raddr  addr

	isClosed atomic.Bool
	done     bool
}

func newConn(scope network.ConnScope, t *transport, wt js.Value, rmaddr ma.Multiaddr, p peer.ID, raddr addr) *conn {
	return &conn{
		scope:     scope,
		transport: t,
		wt:        wt,
		incoming:  wt.Get("incomingBidirectionalStreams").Call("getReader"),
		rpid:      p,
		rmaddr:    rmaddr,
		raddr:     raddr,
	}
}

func (c *conn) OpenStream(ctx context.Context) (network.MuxedStream, error) {
	return c.openStream(ctx)
}

func (c *conn) openStream(ctx context.Context) (*stream, error) {
	r, err := await(ctx, c.wt.Call("createBidirectionalStream"))
	if err != nil {
		return nil, fmt.Errorf("createBidirectionalStream: %w", err)
	}

	return newStream(r[0], c), nil
}

func (c *conn) AcceptStream() (network.MuxedStream, error) {
	if c.done {
		return nil, io.EOF
	}

	r, err := await(context.Background(), c.incoming.Call("read"))
	if err != nil {
		return nil, err
	}
	o := r[0]
	s := o.Get("value")
	c.done = o.Get("done").Bool()

	return newStream(s, c), nil
}

func (c *conn) Close() error {
	c.wt.Call("close")
	_, err := await(context.Background(), c.wt.Get("closed"))
	c.isClosed.Store(true)
	return err
}

var noAddr = addr{"https://0.0.0.0" + webtransportHTTPEndpoint + "?type=noise"}

func (c *conn) RemotePeer() peer.ID           { return c.rpid }
func (c *conn) LocalPeer() peer.ID            { return c.transport.pid }
func (c *conn) RemoteMultiaddr() ma.Multiaddr { return c.rmaddr }
func (c *conn) LocalMultiaddr() ma.Multiaddr  { return webtransportMA }
func (c *conn) RemoteAddr() net.Addr          { return &c.raddr }
func (c *conn) LocalAddr() net.Addr           { return &noAddr }
func (c *conn) IsClosed() bool                { return c.isClosed.Load() }
func (c *conn) Scope() network.ConnScope      { return c.scope }
func (c *conn) Transport() tpt.Transport      { return c.transport }
func (c *conn) RemotePublicKey() ic.PubKey    { return c.rpk }

func (c *conn) ConnState() network.ConnectionState {
	return network.ConnectionState{Transport: "webtransport"}
}

func (c *conn) CloseWithError(_ network.ConnErrorCode) error {
	return c.Close()
}
