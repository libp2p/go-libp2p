package websocket

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"go.uber.org/zap"

	ws "github.com/gorilla/websocket"
	logging "github.com/ipfs/go-log/v2"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var log = logging.Logger("websocket-transport")
var stdLog = zap.NewStdLog(log.Desugar())

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
func newListener(a ma.Multiaddr, tlsConf *tls.Config, sharedTcp *tcpreuse.ConnMgr, upgrader transport.Upgrader, handshakeTimeout time.Duration) (*listener, error) {
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
	}
	ln.server = http.Server{Handler: ln, ErrorLog: stdLog, ConnContext: ln.ConnContext, TLSConfig: tlsConf}
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
	log.Errorf("BUG: expected net.Conn of type *websocket.negotiatingConn: got %T", c)
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
	c, err := l.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// The upgrader writes a response for us.
		return
	}
	nc, err := l.extractConnFromContext(r.Context())
	if err != nil {
		c.Close()
		w.WriteHeader(500)
		log.Errorf("BUG: failed to extract conn from context: RemoteAddr: %s: err: %s", r.RemoteAddr, err)
		return
	}

	cs, err := nc.Unwrap()
	if err != nil {
		c.Close()
		w.WriteHeader(500)
		log.Debugf("connection timed out from: %s", r.RemoteAddr)
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
			log.Errorf("BUG: scope non-nil when err is non nil: %v", err)
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
			log.Debugf("handshake timeout for conn from: %s", conn.RemoteAddr())
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
	ctx       context.Context
	cancelCtx context.CancelFunc
	stopClose func() bool
}

// Close closes the negotiating conn and the underlying connWithScope
// This will be called in case the tls handshake or websocket upgrade fails.
func (c *negotiatingConn) Close() error {
	defer c.cancelCtx()
	if c.stopClose != nil {
		c.stopClose()
	}
	return c.connWithScope.Close()
}

func (c *negotiatingConn) Unwrap() (connWithScope, error) {
	defer c.cancelCtx()
	if c.stopClose != nil {
		if !c.stopClose() {
			return connWithScope{}, errors.New("timed out")
		}
		c.stopClose = nil
	}
	return c.connWithScope, nil
}
