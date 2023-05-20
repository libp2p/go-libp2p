//go:build js

package websocket

import (
	"context"
	"errors"
	"syscall/js"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// WebsocketTransport is the actual go-libp2p transport
type WebsocketTransport struct {
	upgrader transport.Upgrader
	rcmgr    network.ResourceManager
}

func (t *WebsocketTransport) osNew() {}

func (t *WebsocketTransport) maDial(ctx context.Context, raddr ma.Multiaddr) (manet.Conn, error) {
	wsurl, err := parseMultiaddr(raddr)
	if err != nil {
		return nil, err
	}

	rawConn := js.Global().Get("WebSocket").New(wsurl.String())
	conn := NewConn(rawConn)
	if err := conn.waitForOpen(); err != nil {
		conn.Close()
		return nil, err
	}
	mnc, err := manet.WrapNetConn(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}

	return mnc, nil
}

func (t *WebsocketTransport) Listen(a ma.Multiaddr) (transport.Listener, error) {
	return nil, errors.New("Listen not implemented when GOOS=js.")
}
