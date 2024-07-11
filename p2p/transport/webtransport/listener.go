package libp2pwebtransport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
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

// negotiatingConn is a wrapper around a quic.Connection that lets us wrap it in
// our own context for the duration of the upgrade process. Upgrading a quic
// connection to an h3 connection to a webtransport session.
type negotiatingConn struct {
	quic.Connection
	ctx    context.Context
	cancel context.CancelFunc
	// stopClose is a function that stops the connection from being closed when
	// the context is done. Returns true if the connection close function was
	// not called.
	stopClose func() bool
	err       error
}

func (c *negotiatingConn) Unwrap() (quic.Connection, error) {
	defer c.cancel()
	if c.stopClose != nil {
		// unwrap the first time
		if !c.stopClose() {
			c.err = errTimeout
		}
		c.stopClose = nil
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.Connection, nil
}

func wrapConn(ctx context.Context, c quic.Connection, handshakeTimeout time.Duration) *negotiatingConn {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	stopClose := context.AfterFunc(ctx, func() {
		log.Debugf("failed to handshake on conn: %s", c.RemoteAddr())
		c.CloseWithError(1, "")
	})
	return &negotiatingConn{
		Connection: c,
		ctx:        ctx,
		cancel:     cancel,
		stopClose:  stopClose,
	}
}

var errTimeout = errors.New("timeout")

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
			H3: http3.Server{
				ConnContext: func(ctx context.Context, c quic.Connection) context.Context {
					return context.WithValue(ctx, connKey{}, c)
				},
			},
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
	ln.ctx, ln.ctxCancel = context.WithCancel(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc(webtransportHTTPEndpoint, ln.httpHandler)
	ln.server.H3.Handler = mux
	go func() {
		defer close(ln.serverClosed)
		for {
			conn, err := ln.reuseListener.Accept(context.Background())
			if err != nil {
				log.Debugw("serving failed", "addr", ln.Addr(), "error", err)
				return
			}
			wrapped := wrapConn(ln.ctx, conn, t.handshakeTimeout)
			go ln.server.ServeQUICConn(wrapped)
		}
	}()
	return ln, nil
}

func (l *listener) httpHandler(w http.ResponseWriter, r *http.Request) {
	typ, ok := r.URL.Query()["type"]
	if !ok || len(typ) != 1 || typ[0] != "noise" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	remoteMultiaddr, err := stringToWebtransportMultiaddr(r.RemoteAddr)
	if err != nil {
		// This should never happen.
		log.Errorw("converting remote address failed", "remote", r.RemoteAddr, "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if l.transport.gater != nil && !l.transport.gater.InterceptAccept(&connMultiaddrs{local: l.multiaddr, remote: remoteMultiaddr}) {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	connScope, err := l.transport.rcmgr.OpenConnection(network.DirInbound, false, remoteMultiaddr)
	if err != nil {
		log.Debugw("resource manager blocked incoming connection", "addr", r.RemoteAddr, "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	err = l.httpHandlerWithConnScope(w, r, connScope)
	if err != nil {
		connScope.Done()
	}
}

func (l *listener) httpHandlerWithConnScope(w http.ResponseWriter, r *http.Request, connScope network.ConnManagementScope) error {
	sess, err := l.server.Upgrade(w, r)
	if err != nil {
		log.Debugw("upgrade failed", "error", err)
		// TODO: think about the status code to use here
		w.WriteHeader(500)
		return err
	}
	ctx, cancel := context.WithTimeout(l.ctx, handshakeTimeout)
	sconn, err := l.handshake(ctx, sess)
	if err != nil {
		cancel()
		log.Debugw("handshake failed", "error", err)
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
		log.Debugw("resource manager blocked incoming connection for peer", "peer", sconn.RemotePeer(), "addr", r.RemoteAddr, "error", err)
		sess.CloseWithError(1, "")
		return err
	}

	connVal := r.Context().Value(connKey{})
	if connVal == nil {
		log.Errorf("missing conn from context")
		sess.CloseWithError(1, "")
		return errors.New("invalid context")
	}
	nconn, ok := connVal.(*negotiatingConn)
	if !ok {
		log.Errorf("unexpected connection in context. invalid conn type: %T", nconn)
		sess.CloseWithError(1, "")
		return errors.New("invalid context")
	}
	qconn, err := nconn.Unwrap()
	if err != nil {
		log.Debugf("handshake timed out: %s", r.RemoteAddr)
		sess.CloseWithError(1, "")
		return err
	}

	conn := newConn(l.transport, sess, sconn, connScope, qconn)
	l.transport.addConn(sess, conn)
	select {
	case l.queue <- conn:
	default:
		log.Debugw("accept queue full, dropping incoming connection", "peer", sconn.RemotePeer(), "addr", r.RemoteAddr, "error", err)
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
	c, err := n.SecureInbound(ctx, &webtransportStream{Stream: str, wsess: sess}, "")
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
