package websocket

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"

	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-reuseport"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	ws "nhooyr.io/websocket"
)

type listener struct {
	nl     net.Listener
	server http.Server
	// The Go standard library sets the http.Server.TLSConfig no matter if this is a WS or WSS,
	// so we can't rely on checking if server.TLSConfig is set.
	isWss bool

	laddr ma.Multiaddr

	closed   chan struct{}
	incoming chan net.Conn
}

func (pwma *parsedWebsocketMultiaddr) toMultiaddr() ma.Multiaddr {
	if !pwma.isWSS {
		return pwma.restMultiaddr.Encapsulate(wsComponent)
	}

	if pwma.sni == nil {
		return pwma.restMultiaddr.Encapsulate(tlsComponent).Encapsulate(wsComponent)
	}

	return pwma.restMultiaddr.Encapsulate(tlsComponent).Encapsulate(pwma.sni).Encapsulate(wsComponent)
}

// newListener creates a new listener from a raw net.Listener.
// tlsConf may be nil (for unencrypted websockets).
func newListener(a ma.Multiaddr, tlsConf *tls.Config, reuseportAvailable bool) (*listener, error) {
	var nl net.Listener
	parsed, err := parseWebsocketMultiaddr(a)
	if err != nil {
		return nil, err
	}

	if parsed.isWSS && tlsConf == nil {
		return nil, fmt.Errorf("cannot listen on wss address %s without a tls.Config", a)
	}

	lnet, lnaddr, err := manet.DialArgs(parsed.restMultiaddr)
	if err != nil {
		return nil, err
	}
	if reuseportAvailable {
		nl, err = reuseport.Listen(lnet, lnaddr)
		if err != nil {
			nl, err = net.Listen(lnet, lnaddr)
			if err != nil {
				return nil, err
			}
		}
	} else {
		nl, err = net.Listen(lnet, lnaddr)
		if err != nil {
			return nil, err
		}
	}
	laddr, err := manet.FromNetAddr(nl.Addr())
	if err != nil {
		return nil, err
	}
	first, _ := ma.SplitFirst(a)
	// Don't resolve dns addresses.
	// We want to be able to announce domain names, so the peer can validate the TLS certificate.
	if c := first.Protocol().Code; c == ma.P_DNS || c == ma.P_DNS4 || c == ma.P_DNS6 || c == ma.P_DNSADDR {
		_, last := ma.SplitFirst(laddr)
		laddr = first.Encapsulate(last)
	}
	parsed.restMultiaddr = laddr

	ln := &listener{
		nl:       nl,
		laddr:    parsed.toMultiaddr(),
		incoming: make(chan net.Conn),
		closed:   make(chan struct{}),
	}
	ln.server = http.Server{Handler: ln}
	if parsed.isWSS {
		ln.isWss = true
		ln.server.TLSConfig = tlsConf
	}
	return ln, nil
}

func (l *listener) serve() {
	defer close(l.closed)
	if !l.isWss {
		l.server.Serve(l.nl)
	} else {
		l.server.ServeTLS(l.nl, "", "")
	}
}

func (l *listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	scheme := "ws"
	if l.isWss {
		scheme = "wss"
	}

	c, err := ws.Accept(w, r, &ws.AcceptOptions{
		// Allow requests from *all* origins.
		InsecureSkipVerify: true,
	})
	if err != nil {
		// The upgrader writes a response for us.
		return
	}

	// Set an arbitrarily large read limit since we don't actually want to limit the message size here.
	// See https://github.com/nhooyr/websocket/issues/382 for details.
	c.SetReadLimit(math.MaxInt64 - 1) // -1 because the library adds a byte for the fin frame

	select {
	case l.incoming <- conn{
		Conn: ws.NetConn(context.Background(), c, ws.MessageBinary),
		localAddr: addrWrapper{&url.URL{
			Host:   r.Context().Value(http.LocalAddrContextKey).(net.Addr).String(),
			Scheme: scheme,
		}},
		remoteAddr: addrWrapper{&url.URL{
			Host:   r.RemoteAddr,
			Scheme: scheme,
		}},
	}:
	case <-l.closed:
		c.Close(ws.StatusNormalClosure, "closed")
	}
	// The connection has been hijacked, it's safe to return.
}

func (l *listener) Accept() (manet.Conn, error) {
	select {
	case c, ok := <-l.incoming:
		if !ok {
			return nil, fmt.Errorf("listener is closed")
		}

		mnc, err := manet.WrapNetConn(c)
		if err != nil {
			c.Close()
			return nil, err
		}

		return mnc, nil
	case <-l.closed:
		return nil, fmt.Errorf("listener is closed")
	}
}

func (l *listener) Addr() net.Addr {
	return l.nl.Addr()
}

func (l *listener) Close() error {
	l.server.Close()
	err := l.nl.Close()
	<-l.closed
	return err
}

func (l *listener) Multiaddr() ma.Multiaddr {
	return l.laddr
}

type transportListener struct {
	transport.Listener
}

func (l *transportListener) Accept() (transport.CapableConn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &capableConn{CapableConn: conn}, nil
}
