package libp2pwebtransport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/security/noise/pb"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const queueLen = 16
const handshakeTimeout = 10 * time.Second

type connKey struct{}

type listener struct {
	transport       *transport
	isStaticTLSConf bool
	reuseListener   quicreuse.Listener

	server webtransport.Server

	ctx       context.Context
	ctxCancel context.CancelFunc

	serverClosed chan struct{} // is closed when server.Serve returns

	addr      net.Addr
	multiaddr ma.Multiaddr

	queue chan tpt.CapableConn

	mx           sync.Mutex
	pendingConns map[*quic.Conn]*negotiatingConn
}

var _ tpt.Listener = &listener{}

func newListener(reuseListener quicreuse.Listener, t *transport, isStaticTLSConf bool) (tpt.Listener, error) {
	localMultiaddr, err := toWebtransportMultiaddr(reuseListener.Addr())
	if err != nil {
		return nil, err
	}

	ln := &listener{
		reuseListener:   reuseListener,
		transport:       t,
		isStaticTLSConf: isStaticTLSConf,
		queue:           make(chan tpt.CapableConn, queueLen),
		serverClosed:    make(chan struct{}),
		addr:            reuseListener.Addr(),
		multiaddr:       localMultiaddr,
		server: webtransport.Server{
			H3: &http3.Server{
				ConnContext: func(ctx context.Context, c *quic.Conn) context.Context {
					return context.WithValue(ctx, connKey{}, c)
				},
				EnableDatagrams: true,
			},
			CheckOrigin: func(_ *http.Request) bool { return true },
		},
		pendingConns: make(map[*quic.Conn]*negotiatingConn),
	}
	ln.ctx, ln.ctxCancel = context.WithCancel(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc(webtransportHTTPEndpoint, ln.httpHandler)
	ln.server.H3.Handler = mux
	webtransport.ConfigureHTTP3Server(ln.server.H3)
	go func() {
		defer close(ln.serverClosed)
		for {
			conn, err := ln.reuseListener.Accept(context.Background())
			if err != nil {
				log.Debug("serving failed", "addr", ln.Addr(), "error", err)
				return
			}
			err = ln.startHandshake(conn)
			if err != nil {
				log.Debug("failed to start handshake", "error", err)
				continue
			}
			go ln.server.ServeQUICConn(conn)
		}
	}()
	return ln, nil
}

func (l *listener) startHandshake(conn *quic.Conn) error {
	ctx, cancel := context.WithTimeout(l.ctx, handshakeTimeout)
	stopHandshakeTimeout := context.AfterFunc(ctx, func() {
		log.Debug("failed to handshake on conn", "remote_addr", conn.RemoteAddr())
		conn.CloseWithError(1, "")
		l.mx.Lock()
		delete(l.pendingConns, conn)
		l.mx.Unlock()
	})
	l.mx.Lock()
	defer l.mx.Unlock()
	// don't add to map if the context is already cancelled
	if ctx.Err() != nil {
		cancel()
		return ctx.Err()
	}
	l.pendingConns[conn] = &negotiatingConn{
		Conn:                 conn,
		ctx:                  ctx,
		cancel:               cancel,
		stopHandshakeTimeout: stopHandshakeTimeout,
	}
	return nil
}

// negotiatingConn is a wrapper around a *quic.Conn that lets us wrap it in
// our own context for the duration of the upgrade process. Upgrading a quic
// connection to an h3 connection to a webtransport session.
type negotiatingConn struct {
	*quic.Conn
	ctx    context.Context
	cancel context.CancelFunc
	// stopHandshakeTimeout is a function that stops triggering the handshake timeout. Returns true if the handshake timeout was not triggered.
	stopHandshakeTimeout func() bool
	err                  error
}

func (c *negotiatingConn) StopHandshakeTimeout() error {
	defer c.cancel()
	if c.stopHandshakeTimeout != nil {
		// cancel the handshake timeout function
		if !c.stopHandshakeTimeout() {
			c.err = errTimeout
		}
		c.stopHandshakeTimeout = nil
	}
	if c.err != nil {
		return c.err
	}
	return nil
}

var errTimeout = errors.New("timeout")

func (l *listener) httpHandler(w http.ResponseWriter, r *http.Request) {
	typ, ok := r.URL.Query()["type"]
	if !ok || len(typ) != 1 || typ[0] != "noise" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	remoteMultiaddr, err := stringToWebtransportMultiaddr(r.RemoteAddr)
	if err != nil {
		// This should never happen.
		log.Error("converting remote address failed", "remote", r.RemoteAddr, "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if l.transport.gater != nil && !l.transport.gater.InterceptAccept(&connMultiaddrs{local: l.multiaddr, remote: remoteMultiaddr}) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	connScope, err := network.UnwrapConnManagementScope(r.Context())
	if err != nil {
		connScope = nil
		// Don't error here.
		// Setup scope if we don't have scope from quicreuse.
		// This is better than failing so that users that don't use quicreuse.ConnContext option with the resource
		// manager still work correctly.
	}
	if connScope == nil {
		connScope, err = l.transport.rcmgr.OpenConnection(network.DirInbound, false, remoteMultiaddr)
		if err != nil {
			log.Debug("resource manager blocked incoming connection", "addr", r.RemoteAddr, "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	err = l.httpHandlerWithConnScope(w, r, connScope)
	if err != nil {
		connScope.Done()
	}
}

func (l *listener) httpHandlerWithConnScope(w http.ResponseWriter, r *http.Request, connScope network.ConnManagementScope) error {
	sess, err := l.server.Upgrade(w, r)
	if err != nil {
		log.Debug("upgrade failed", "error", err)
		// TODO: think about the status code to use here
		w.WriteHeader(500)
		return err
	}
	ctx, cancel := context.WithTimeout(l.ctx, handshakeTimeout)
	sconn, err := l.handshake(ctx, sess)
	if err != nil {
		cancel()
		log.Debug("handshake failed", "error", err)
		sess.CloseWithError(1, "")
		return err
	}
	cancel()

	if l.transport.gater != nil && !l.transport.gater.InterceptSecured(network.DirInbound, sconn.RemotePeer(), sconn) {
		// TODO: can we close with a specific error here?
		sess.CloseWithError(errorCodeConnectionGating, "")
		return errors.New("gater blocked connection")
	}

	if err := connScope.SetPeer(sconn.RemotePeer()); err != nil {
		log.Debug("resource manager blocked incoming connection for peer", "peer", sconn.RemotePeer(), "addr", r.RemoteAddr, "error", err)
		sess.CloseWithError(1, "")
		return err
	}

	connVal := r.Context().Value(connKey{})
	if connVal == nil {
		log.Error("missing conn from context")
		sess.CloseWithError(1, "")
		return errors.New("invalid context")
	}
	qconn := connVal.(*quic.Conn)

	l.mx.Lock()
	nconn, ok := l.pendingConns[qconn]
	delete(l.pendingConns, qconn)
	l.mx.Unlock()
	if !ok {
		log.Debug("handshake timed out", "remote_addr", r.RemoteAddr)
		sess.CloseWithError(1, "")
		return errTimeout
	}
	if err := nconn.StopHandshakeTimeout(); err != nil {
		log.Debug("handshake timed out", "remote_addr", r.RemoteAddr)
		sess.CloseWithError(1, "")
		return err
	}

	conn := newConn(l.transport, sess, sconn, connScope, qconn)
	l.transport.addConn(qconn, conn)
	select {
	case l.queue <- conn:
	default:
		log.Debug("accept queue full, dropping incoming connection", "peer", sconn.RemotePeer(), "addr", r.RemoteAddr, "error", err)
		conn.Close()
		return errors.New("accept queue full")
	}

	return nil
}

func (l *listener) Accept() (tpt.CapableConn, error) {
	select {
	case <-l.ctx.Done():
		return nil, tpt.ErrListenerClosed
	case c := <-l.queue:
		return c, nil
	}
}

func (l *listener) handshake(ctx context.Context, sess *webtransport.Session) (*connSecurityMultiaddrs, error) {
	local, err := toWebtransportMultiaddr(sess.LocalAddr())
	if err != nil {
		return nil, fmt.Errorf("error determiniting local addr: %w", err)
	}
	remote, err := toWebtransportMultiaddr(sess.RemoteAddr())
	if err != nil {
		return nil, fmt.Errorf("error determiniting remote addr: %w", err)
	}

	str, err := sess.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	var earlyData [][]byte
	if !l.isStaticTLSConf {
		earlyData = l.transport.certManager.SerializedCertHashes()
	}

	n, err := l.transport.noise.WithSessionOptions(noise.EarlyData(
		nil,
		newEarlyDataSender(&pb.NoiseExtensions{WebtransportCerthashes: earlyData}),
	))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Noise session: %w", err)
	}
	c, err := n.SecureInbound(ctx, webtransportStream{Stream: str, wsess: sess}, "")
	if err != nil {
		return nil, err
	}

	return &connSecurityMultiaddrs{
		ConnSecurity:   c,
		ConnMultiaddrs: &connMultiaddrs{local: local, remote: remote},
	}, nil
}

func (l *listener) Addr() net.Addr {
	return l.addr
}

func (l *listener) Multiaddr() ma.Multiaddr {
	if l.transport.certManager == nil {
		return l.multiaddr
	}
	return l.multiaddr.Encapsulate(l.transport.certManager.AddrComponent())
}

func (l *listener) Close() error {
	l.ctxCancel()
	l.reuseListener.Close()
	err := l.server.Close()
	<-l.serverClosed
loop:
	for {
		select {
		case conn := <-l.queue:
			conn.Close()
		default:
			break loop
		}
	}
	return err
}
