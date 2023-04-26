// Package websocket implements a websocket based transport for go-libp2p.
package websocket

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-reuseport"

	reusetransport "github.com/libp2p/go-libp2p/p2p/net/reuseport"
	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	manet "github.com/multiformats/go-multiaddr/net"

	ws "nhooyr.io/websocket"
)

// WsFmt is multiaddr formatter for WsProtocol
var WsFmt = mafmt.And(mafmt.TCP, mafmt.Base(ma.P_WS))

var dialMatcher = mafmt.And(
	mafmt.Or(mafmt.IP, mafmt.DNS),
	mafmt.Base(ma.P_TCP),
	mafmt.Or(
		mafmt.Base(ma.P_WS),
		mafmt.And(
			mafmt.Or(
				mafmt.And(
					mafmt.Base(ma.P_TLS),
					mafmt.Base(ma.P_SNI)),
				mafmt.Base(ma.P_TLS),
			),
			mafmt.Base(ma.P_WS)),
		mafmt.Base(ma.P_WSS)))

var (
	wssComponent   = ma.StringCast("/wss")
	tlsWsComponent = ma.StringCast("/tls/ws")
	tlsComponent   = ma.StringCast("/tls")
	wsComponent    = ma.StringCast("/ws")
)

func init() {
	manet.RegisterFromNetAddr(ParseWebsocketNetAddr, "websocket")
	manet.RegisterToNetAddr(ConvertWebsocketMultiaddrToNetAddr, "ws")
	manet.RegisterToNetAddr(ConvertWebsocketMultiaddrToNetAddr, "wss")
}

type Option func(*WebsocketTransport) error

func EnableReuseport() Option {
	return func(tr *WebsocketTransport) error {
		tr.reuseport = true
		return nil
	}
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
	reuseport     bool //reuseport is disabled by default, can be enabled by passing it as an option.
	reuse         reusetransport.Transport
}

var _ transport.Transport = (*WebsocketTransport)(nil)

func New(u transport.Upgrader, rcmgr network.ResourceManager, opts ...Option) (*WebsocketTransport, error) {
	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}

	t := &WebsocketTransport{
		upgrader:      u,
		rcmgr:         rcmgr,
		tlsClientConf: &tls.Config{},
	}
	for _, opt := range opts {
		if err := opt(t); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (t *WebsocketTransport) CanDial(a ma.Multiaddr) bool {
	return dialMatcher.Matches(a)
}

func (t *WebsocketTransport) Protocols() []int {
	return []int{ma.P_WS, ma.P_WSS}
}

func (t *WebsocketTransport) Proxy() bool {
	return false
}

func (t *WebsocketTransport) Resolve(ctx context.Context, maddr ma.Multiaddr) ([]ma.Multiaddr, error) {
	parsed, err := parseWebsocketMultiaddr(maddr)
	if err != nil {
		return nil, err
	}

	if !parsed.isWSS {
		// No /tls/ws component, this isn't a secure websocket multiaddr. We can just return it here
		return []ma.Multiaddr{maddr}, nil
	}

	if parsed.sni == nil {
		var err error
		// We don't have an sni component, we'll use dns/dnsaddr
		ma.ForEach(parsed.restMultiaddr, func(c ma.Component) bool {
			switch c.Protocol().Code {
			case ma.P_DNS, ma.P_DNS4, ma.P_DNS6, ma.P_DNSADDR:
				// err shouldn't happen since this means we couldn't parse a dns hostname for an sni value.
				parsed.sni, err = ma.NewComponent("sni", c.Value())
				return false
			}
			return true
		})
		if err != nil {
			return nil, err
		}
	}

	if parsed.sni == nil {
		// we didn't find anything to set the sni with. So we just return the given multiaddr
		return []ma.Multiaddr{maddr}, nil
	}

	return []ma.Multiaddr{parsed.toMultiaddr()}, nil
}

func (t *WebsocketTransport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (transport.CapableConn, error) {
	connScope, err := t.rcmgr.OpenConnection(network.DirOutbound, true, raddr)
	if err != nil {
		return nil, err
	}
	c, err := t.dialWithScope(ctx, raddr, p, connScope)
	if err != nil {
		connScope.Done()
		return nil, err
	}
	return c, nil
}

func (t *WebsocketTransport) dialWithScope(ctx context.Context, raddr ma.Multiaddr, p peer.ID, connScope network.ConnManagementScope) (transport.CapableConn, error) {
	macon, err := t.maDial(ctx, raddr)
	if err != nil {
		return nil, err
	}
	conn, err := t.upgrader.Upgrade(ctx, t, macon, network.DirOutbound, p, connScope)
	if err != nil {
		return nil, err
	}
	return &capableConn{CapableConn: conn}, nil
}

func (t *WebsocketTransport) maDial(ctx context.Context, raddr ma.Multiaddr) (manet.Conn, error) {
	wsurl, err := parseMultiaddr(raddr)
	if err != nil {
		return nil, err
	}
	isWss := wsurl.Scheme == "wss"
	wsurlCopy := *wsurl
	remoteAddr := addrWrapper{URL: &wsurlCopy}
	localAddrChan := make(chan addrWrapper, 1)

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var conn manet.Conn
			var err error
			if t.UseReuseport() {
				conn, err = t.reuse.DialContext(ctx, raddr)
			} else {
				var d manet.Dialer
				conn, err = d.DialContext(ctx, raddr)
			}
			if err != nil {
				close(localAddrChan)
				return nil, err
			}
			localAddrChan <- addrWrapper{URL: &url.URL{Host: conn.LocalAddr().String(), Scheme: wsurl.Scheme}}
			return conn, nil
		},
	}
	dialer := http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	if isWss {
		sni := ""
		sni, err = raddr.ValueForProtocol(ma.P_SNI)
		if err != nil {
			sni = ""
		}

		if sni != "" {
			copytlsClientConf := t.tlsClientConf.Clone()
			copytlsClientConf.ServerName = sni
			transport.TLSClientConfig = copytlsClientConf
			ipAddr := wsurl.Host
			// Setting the Dial because we already have the resolved IP address, so we don't want to do another resolution.
			// We set the `.Host` to the sni field so that the host header gets properly set.
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				tcpAddr, err := net.ResolveTCPAddr(network, ipAddr)
				if err != nil {
					close(localAddrChan)
					return nil, err
				}
				conn, err := net.DialTCP("tcp", nil, tcpAddr)
				if err != nil {
					close(localAddrChan)
					return nil, err
				}
				localAddrChan <- addrWrapper{URL: &url.URL{Host: conn.LocalAddr().String(), Scheme: wsurl.Scheme}}
				return conn, nil
			}
			wsurl.Host = sni + ":" + wsurl.Port()
		} else {
			transport.TLSClientConfig = t.tlsClientConf
		}
	}

	wscon, _, err := ws.Dial(ctx, wsurl.String(), &ws.DialOptions{
		HTTPClient: &dialer,
	})
	if err != nil {
		return nil, err
	}

	// We need the local address of this connection, and afaict there's no other
	// way of getting it besides hooking into the dial context func.
	localAdddr, ok := <-localAddrChan
	if !ok {
		wscon.Close(ws.StatusNormalClosure, "closed. no local address")
		return nil, fmt.Errorf("failed to get local address")
	}

	// Set an arbitrarily large read limit since we don't actually want to limit the message size here.
	wscon.SetReadLimit(math.MaxInt64 - 1) // -1 because the library adds a byte for the fin frame
	mnc, err := manet.WrapNetConn(
		conn{
			Conn:       ws.NetConn(context.Background(), wscon, ws.MessageBinary),
			localAddr:  localAdddr,
			remoteAddr: remoteAddr,
		})
	if err != nil {
		wscon.Close(ws.StatusNormalClosure, "closed. err")
		return nil, err
	}
	return mnc, nil
}

func (t *WebsocketTransport) maListen(a ma.Multiaddr) (manet.Listener, error) {
	l, err := newListener(a, t.tlsConf, t.reuseport)
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

// UseReuseport returns true if reuseport is enabled and available.
func (t *WebsocketTransport) UseReuseport() bool {
	return t.reuseport && reuseport.Available()
}
