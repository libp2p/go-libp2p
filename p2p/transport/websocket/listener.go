package websocket

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	ws "github.com/gorilla/websocket"
	logging "github.com/libp2p/go-libp2p/gologshim"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var log = logging.Logger("websocket-transport")

type listener struct {
	netListener *httpNetListener
	server      http.Server
	wsUpgrader  ws.Upgrader
	// The Go standard library sets the http.Server.TLSConfig no matter if this is a WS or WSS,
	// so we can't rely on checking if server.TLSConfig is set.
	isWss bool

	laddr ma.Multiaddr

	incoming chan *Conn

	closeOnce sync.Once
	closeErr  error
	closed    chan struct{}
	wsurl     *url.URL

	// httpHandler serves any request that is not a WebSocket upgrade.
	// Nil means non-upgrade requests get a 404 (the historical behaviour).
	// See [WithHTTPHandler] for the full rationale.
	httpHandler http.Handler
}

var _ transport.GatedMaListener = &listener{}

func (pwma *parsedWebsocketMultiaddr) toMultiaddr() ma.Multiaddr {
	if !pwma.isWSS {
		return pwma.restMultiaddr.AppendComponent(wsComponent)
	}

	if pwma.sni == nil {
		return pwma.restMultiaddr.AppendComponent(tlsComponent, wsComponent)
	}

	return pwma.restMultiaddr.AppendComponent(tlsComponent, pwma.sni, wsComponent)
}

// newListener creates a new listener from a raw net.Listener.
// tlsConf may be nil (for unencrypted websockets).
// httpHandler may be nil; when non-nil it serves every request that is
// not a WebSocket upgrade.
func newListener(a ma.Multiaddr, tlsConf *tls.Config, sharedTcp *tcpreuse.ConnMgr, upgrader transport.Upgrader, handshakeTimeout time.Duration, httpHandler http.Handler, serverConfig func(*http.Server)) (*listener, error) {
	parsed, err := parseWebsocketMultiaddr(a)
	if err != nil {
		return nil, err
	}

	if parsed.isWSS && tlsConf == nil {
		return nil, fmt.Errorf("cannot listen on wss address %s without a tls.Config", a)
	}

	var gmal transport.GatedMaListener
	if sharedTcp == nil {
		mal, err := manet.Listen(parsed.restMultiaddr)
		if err != nil {
			return nil, err
		}
		gmal = upgrader.GateMaListener(mal)
	} else {
		var connType tcpreuse.DemultiplexedConnType
		if parsed.isWSS {
			connType = tcpreuse.DemultiplexedConnType_TLS
		} else {
			connType = tcpreuse.DemultiplexedConnType_HTTP
		}
		gmal, err = sharedTcp.DemultiplexedListen(parsed.restMultiaddr, connType)
		if err != nil {
			return nil, err
		}
	}

	// laddr has the correct port in case we listened on port 0
	laddr := gmal.Multiaddr()

	// Don't resolve dns addresses.
	// We want to be able to announce domain names, so the peer can validate the TLS certificate.
	first, _ := ma.SplitFirst(a)
	if c := first.Protocol().Code; c == ma.P_DNS || c == ma.P_DNS4 || c == ma.P_DNS6 || c == ma.P_DNSADDR {
		_, last := ma.SplitFirst(laddr)
		laddr = first.Encapsulate(last)
	}
	parsed.restMultiaddr = laddr

	listenAddr := parsed.toMultiaddr()
	wsurl, err := parseMultiaddr(listenAddr)
	if err != nil {
		gmal.Close()
		return nil, fmt.Errorf("failed to parse multiaddr to URL: %v: %w", listenAddr, err)
	}
	ln := &listener{
		netListener: &httpNetListener{
			GatedMaListener:  gmal,
			handshakeTimeout: handshakeTimeout,
		},
		laddr:    parsed.toMultiaddr(),
		incoming: make(chan *Conn),
		closed:   make(chan struct{}),
		isWss:    parsed.isWSS,
		wsurl:    wsurl,
		wsUpgrader: ws.Upgrader{
			// Allow requests from *all* origins.
			CheckOrigin: func(_ *http.Request) bool {
				return true
			},
			HandshakeTimeout: handshakeTimeout,
		},
		httpHandler: httpHandler,
	}
	ln.server = http.Server{
		Handler: ln,
		// Use LevelDebug for http.Server errors (TLS handshake failures, connection issues).
		// These are operational noise from misbehaving/buggy remote clients, not server errors.
		ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelDebug),
		ConnContext: ln.ConnContext,
		TLSConfig:   tlsConf,
	}
	if httpHandler != nil {
		// A fallback request may run far longer than the handshake-timeout
		// (HTTP/2 streams, HTTP/1.1 keep-alive), so bound connections with
		// server-side timeouts. IdleTimeout caps idle keep-alive on both
		// h1 and h2. ReadHeaderTimeout guards only h1 against slow-header
		// clients; Go's HTTP/2 server ignores it. ReadTimeout and
		// WriteTimeout are left unset on purpose, as they apply per request
		// and would truncate large streamed responses.
		ln.server.ReadHeaderTimeout = handshakeTimeout
		ln.server.IdleTimeout = defaultHTTPIdleTimeout
		if !parsed.isWSS {
			// On a plaintext /ws listener, accept HTTP/2 cleartext (h2c)
			// next to HTTP/1.1 so reverse proxies that speak h2c to
			// backends (Caddy, Traefik, nginx) get HTTP/2 multiplexing.
			// On /tls/ws, h2 is negotiated via ALPN inside ServeTLS.
			p := new(http.Protocols)
			p.SetHTTP1(true)
			p.SetUnencryptedHTTP2(true)
			ln.server.Protocols = p
		}
		// Let the caller tune timeouts and HTTP/2 settings; see
		// [WithHTTPServerConfig]. The transport re-asserts the fields it
		// must own afterwards so the hook cannot break request dispatch.
		if serverConfig != nil {
			serverConfig(&ln.server)
		}
		ln.server.Handler = ln
		ln.server.ConnContext = ln.ConnContext
		ln.server.TLSConfig = tlsConf
	}
	// RFC 8441 (WebSocket-over-HTTP/2) is intentionally not advertised: the
	// server never enables SETTINGS_ENABLE_CONNECT_PROTOCOL, so browsers fall
	// back to HTTP/1.1 for wss://, where gorilla/websocket can hijack the
	// connection. Go gates that setting behind a process-global
	// GODEBUG=http2xconnect=1 (golang/go#71128), which a library must not set.
	// Dispatch in ServeHTTP defers the "is this a WebSocket upgrade?" decision
	// to ws.IsWebSocketUpgrade, so if a future gorilla and Go add server-side
	// ext-CONNECT support, this listener upgrades to it without code changes.
	return ln, nil
}

func (l *listener) serve() {
	defer close(l.closed)
	if !l.isWss {
		l.server.Serve(l.netListener)
	} else {
		l.server.ServeTLS(l.netListener, "", "")
	}
}

type connKey struct{}

func (l *listener) ConnContext(ctx context.Context, c net.Conn) context.Context {
	// prefer `*tls.Conn` over `(interface{NetConn() net.Conn})` in case `manet.Conn` is extended
	// to support a `NetConn() net.Conn` method.
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	if nc, ok := c.(*negotiatingConn); ok {
		return context.WithValue(ctx, connKey{}, nc)
	}
	log.Error("BUG: expected net.Conn of type *websocket.negotiatingConn", "got_type", fmt.Sprintf("%T", c))
	// might as well close the connection as there's no way to proceed now.
	c.Close()
	return ctx
}

func (l *listener) extractConnFromContext(ctx context.Context) (*negotiatingConn, error) {
	c := ctx.Value(connKey{})
	if c == nil {
		return nil, fmt.Errorf("expected *websocket.negotiatingConn in context: got nil")
	}
	nc, ok := c.(*negotiatingConn)
	if !ok {
		return nil, fmt.Errorf("expected *websocket.negotiatingConn in context: got %T", c)
	}
	return nc, nil
}

func (l *listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Route non-WebSocket requests to the fallback handler if one is set.
	// This is what lets a /tls/ws listener share a port with an ordinary
	// HTTPS site; see [WithHTTPHandler]. The handler is invoked
	// for every HTTP version the listener accepts (h1 and h2 over TLS,
	// h1 and h2c on plaintext) so callers do not have to pre-screen
	// clients by HTTP version.
	if !ws.IsWebSocketUpgrade(r) {
		if l.httpHandler == nil {
			http.NotFound(w, r)
			return
		}
		// Disarm the handshake-timeout that would otherwise close the
		// underlying connection 15s after Accept. The fallback handler may
		// run for longer than that (HTTP/2 streams, HTTP/1.1 keep-alive),
		// so the timer must go away once we know a request has arrived.
		// Unwrap serializes through sync.Once, so concurrent h2 streams
		// on the same TCP connection all see the same result.
		if nc, err := l.extractConnFromContext(r.Context()); err == nil {
			if _, err := nc.Unwrap(); err != nil {
				// The handshake-timeout fired between Accept and now; the
				// connection is already closed. Nothing useful to send.
				log.Debug("connection timed out before fallback request", "remote_addr", r.RemoteAddr)
				return
			}
		}
		l.httpHandler.ServeHTTP(w, r)
		return
	}

	c, err := l.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// The upgrader writes a response for us.
		return
	}
	nc, err := l.extractConnFromContext(r.Context())
	if err != nil {
		c.Close()
		w.WriteHeader(500)
		log.Error("BUG: failed to extract conn from context", "remote_addr", r.RemoteAddr, "err", err)
		return
	}

	cs, err := nc.Unwrap()
	if err != nil {
		c.Close()
		w.WriteHeader(500)
		log.Debug("connection timed out", "remote_addr", r.RemoteAddr)
		return
	}

	conn := newConn(c, l.isWss, cs.Scope)
	if conn == nil {
		c.Close()
		w.WriteHeader(500)
		return
	}

	select {
	case l.incoming <- conn:
	case <-l.closed:
		conn.Close()
	}
	// The connection has been hijacked, it's safe to return.
}

func (l *listener) Accept() (manet.Conn, network.ConnManagementScope, error) {
	select {
	case c, ok := <-l.incoming:
		if !ok {
			return nil, nil, transport.ErrListenerClosed
		}
		return c, c.Scope, nil
	case <-l.closed:
		return nil, nil, transport.ErrListenerClosed
	}
}

func (l *listener) Addr() net.Addr {
	return &Addr{URL: l.wsurl}
}

func (l *listener) Close() error {
	l.closeOnce.Do(func() {
		err1 := l.netListener.Close()
		err2 := l.server.Close()
		<-l.closed
		l.closeErr = errors.Join(err1, err2)
	})
	return l.closeErr
}

func (l *listener) Multiaddr() ma.Multiaddr {
	return l.laddr
}

// httpNetListener is a net.Listener that adapts a transport.GatedMaListener to a net.Listener.
// It wraps the manet.Conn, and the Scope from the underlying gated listener in a connWithScope.
type httpNetListener struct {
	transport.GatedMaListener
	handshakeTimeout time.Duration
}

var _ net.Listener = &httpNetListener{}

func (l *httpNetListener) Accept() (net.Conn, error) {
	conn, scope, err := l.GatedMaListener.Accept()
	if err != nil {
		if scope != nil {
			log.Error("BUG: scope non-nil when err is non nil", "error", err)
			scope.Done()
		}
		return nil, err
	}
	connWithScope := connWithScope{
		Conn:  conn,
		Scope: scope,
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.handshakeTimeout)
	return &negotiatingConn{
		connWithScope: connWithScope,
		ctx:           ctx,
		cancelCtx:     cancel,
		stopClose: context.AfterFunc(ctx, func() {
			connWithScope.Close()
			log.Debug("handshake timeout for conn", "remote_addr", conn.RemoteAddr())
		}),
	}, nil
}

type connWithScope struct {
	net.Conn
	Scope network.ConnManagementScope
}

func (c connWithScope) Close() error {
	c.Scope.Done()
	return c.Conn.Close()
}

type negotiatingConn struct {
	connWithScope
	ctx        context.Context
	cancelCtx  context.CancelFunc
	stopClose  func() bool
	disarmOnce sync.Once
	timedOut   bool // written once inside disarmOnce, read after
}

// disarm stops the handshake-timeout AfterFunc set up in Accept and reports
// whether the connection is still alive. Concurrent callers see the same
// result, which the HTTP/2 fallback path needs when several streams share
// one *negotiatingConn.
func (c *negotiatingConn) disarm() (alive bool) {
	c.disarmOnce.Do(func() {
		if c.stopClose != nil {
			c.timedOut = !c.stopClose()
			c.stopClose = nil
		}
	})
	return !c.timedOut
}

// Close closes the negotiating conn and the underlying connWithScope.
// Called when the TLS handshake or WebSocket upgrade fails.
func (c *negotiatingConn) Close() error {
	defer c.cancelCtx()
	c.disarm()
	return c.connWithScope.Close()
}

func (c *negotiatingConn) Unwrap() (connWithScope, error) {
	defer c.cancelCtx()
	if !c.disarm() {
		return connWithScope{}, errors.New("timed out")
	}
	return c.connWithScope, nil
}
