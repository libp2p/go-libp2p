//go:build !js

package websocket

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	ws "github.com/gorilla/websocket"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// Default gorilla upgrader
var upgrader = ws.Upgrader{
	// Allow requests from *all* origins.
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// WithTLSClientConfig sets a TLS client configuration on the WebSocket Dialer. Only
// relevant for non-browser usages.
//
// Some useful use cases include setting InsecureSkipVerify to `true`, or
// setting user-defined trusted CA certificates.
func WithTLSClientConfig(c *tls.Config) Option {
	return func(t *WebsocketTransport) error {
		t.tlsClientConf = c
		return nil
	}
}

// WithTLSConfig sets a TLS configuration for the WebSocket listener.
func WithTLSConfig(conf *tls.Config) Option {
	return func(t *WebsocketTransport) error {
		t.tlsConf = conf
		return nil
	}
}

// WebsocketTransport is the actual go-libp2p transport
type WebsocketTransport struct {
	upgrader transport.Upgrader
	rcmgr    network.ResourceManager

	tlsClientConf *tls.Config
	tlsConf       *tls.Config
}

// osNew is for initialisation code that os specific.
func (t *WebsocketTransport) osNew() {
	t.tlsClientConf = &tls.Config{}
}

func (t *WebsocketTransport) maDial(ctx context.Context, raddr ma.Multiaddr) (manet.Conn, error) {
	wsurl, err := parseMultiaddr(raddr)
	if err != nil {
		return nil, err
	}
	isWss := wsurl.Scheme == "wss"
	dialer := ws.Dialer{HandshakeTimeout: 30 * time.Second}
	if isWss {
		sni := ""
		sni, err = raddr.ValueForProtocol(ma.P_SNI)
		if err != nil {
			sni = ""
		}

		if sni != "" {
			copytlsClientConf := t.tlsClientConf.Clone()
			copytlsClientConf.ServerName = sni
			dialer.TLSClientConfig = copytlsClientConf
			ipAddr := wsurl.Host
			// Setting the NetDial because we already have the resolved IP address, so we don't want to do another resolution.
			// We set the `.Host` to the sni field so that the host header gets properly set.
			dialer.NetDial = func(network, address string) (net.Conn, error) {
				tcpAddr, err := net.ResolveTCPAddr(network, ipAddr)
				if err != nil {
					return nil, err
				}
				return net.DialTCP("tcp", nil, tcpAddr)
			}
			wsurl.Host = sni + ":" + wsurl.Port()
		} else {
			dialer.TLSClientConfig = t.tlsClientConf
		}
	}

	wscon, _, err := dialer.DialContext(ctx, wsurl.String(), nil)
	if err != nil {
		return nil, err
	}

	mnc, err := manet.WrapNetConn(NewConn(wscon, isWss))
	if err != nil {
		wscon.Close()
		return nil, err
	}
	return mnc, nil
}

func (t *WebsocketTransport) maListen(a ma.Multiaddr) (manet.Listener, error) {
	l, err := newListener(a, t.tlsConf)
	if err != nil {
		return nil, err
	}
	go l.serve()
	return l, nil
}

func (t *WebsocketTransport) Listen(a ma.Multiaddr) (transport.Listener, error) {
	malist, err := t.maListen(a)
	if err != nil {
		return nil, err
	}
	return &transportListener{Listener: t.upgrader.UpgradeListener(t, malist)}, nil
}
