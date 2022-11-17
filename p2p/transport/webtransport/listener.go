package libp2pwebtransport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/security/noise/pb"

	"github.com/lucas-clemente/quic-go/http3"
	"github.com/marten-seemann/webtransport-go"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var errClosed = errors.New("closed")

const queueLen = 16
const handshakeTimeout = 10 * time.Second

type listener struct {
	transport       *transport
	tlsConf         *tls.Config
	isStaticTLSConf bool

	server webtransport.Server

	ctx       context.Context
	ctxCancel context.CancelFunc

	serverClosed chan struct{} // is closed when server.Serve returns

	addr      net.Addr
	multiaddr ma.Multiaddr

	queue chan tpt.CapableConn
}

var _ tpt.Listener = &listener{}

func newListener(laddr ma.Multiaddr, t *transport, tlsConf *tls.Config) (tpt.Listener, error) {
	network, addr, err := manet.DialArgs(laddr)
	if err != nil {
		return nil, err
	}
	udpAddr, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		return nil, err
	}
	udpConn, err := net.ListenUDP(network, udpAddr)
	if err != nil {
		return nil, err
	}
	localMultiaddr, err := toWebtransportMultiaddr(udpConn.LocalAddr())
	if err != nil {
		return nil, err
	}
	isStaticTLSConf := tlsConf != nil
	if tlsConf == nil {
		tlsConf = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return t.certManager.GetConfig(), nil
		}}
	}
	ln := &listener{
		transport:       t,
		tlsConf:         tlsConf,
		isStaticTLSConf: isStaticTLSConf,
		queue:           make(chan tpt.CapableConn, queueLen),
		serverClosed:    make(chan struct{}),
		addr:            udpConn.LocalAddr(),
		multiaddr:       localMultiaddr,
		server: webtransport.Server{
			H3: http3.Server{
				QuicConfig: t.quicConfig,
				TLSConfig:  tlsConf,
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
		defer func() { udpConn.Close() }()
		if err := ln.server.Serve(udpConn); err != nil {
			// TODO: only output if the server hasn't been closed
			log.Debugw("serving failed", "addr", udpConn.LocalAddr(), "error", err)
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

	sess, err := l.server.Upgrade(w, r)
	if err != nil {
		log.Debugw("upgrade failed", "error", err)
		// TODO: think about the status code to use here
		w.WriteHeader(500)
		connScope.Done()
		return
	}
	ctx, cancel := context.WithTimeout(l.ctx, handshakeTimeout)
	sconn, err := l.handshake(ctx, sess)
	if err != nil {
		cancel()
		log.Debugw("handshake failed", "error", err)
		sess.CloseWithError(1, "")
		connScope.Done()
		return
	}
	cancel()

	if l.transport.gater != nil && !l.transport.gater.InterceptSecured(network.DirInbound, sconn.RemotePeer(), sconn) {
		// TODO: can we close with a specific error here?
		sess.CloseWithError(errorCodeConnectionGating, "")
		connScope.Done()
		return
	}

	if err := connScope.SetPeer(sconn.RemotePeer()); err != nil {
		log.Debugw("resource manager blocked incoming connection for peer", "peer", sconn.RemotePeer(), "addr", r.RemoteAddr, "error", err)
		sess.CloseWithError(1, "")
		connScope.Done()
		return
	}

	conn := newConn(l.transport, sess, sconn, connScope)
	l.transport.addConn(sess, conn)
	select {
	case l.queue <- conn:
	default:
		log.Debugw("accept queue full, dropping incoming connection", "peer", sconn.RemotePeer(), "addr", r.RemoteAddr, "error", err)
		sess.CloseWithError(1, "")
		connScope.Done()
	}
}

func (l *listener) Accept() (tpt.CapableConn, error) {
	select {
	case <-l.ctx.Done():
		return nil, errClosed
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

func (l *listener) Multiaddrs() []ma.Multiaddr {
	if l.transport.certManager == nil {
		return []ma.Multiaddr{l.multiaddr}
	}
	return []ma.Multiaddr{l.multiaddr.Encapsulate(l.transport.certManager.AddrComponent())}
}

func (l *listener) Close() error {
	l.ctxCancel()
	err := l.server.Close()
	<-l.serverClosed
	return err
}
