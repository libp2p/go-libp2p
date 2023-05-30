//go:build !js

package libp2pwebtransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"

	"github.com/benbjohnson/clock"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/multiformats/go-multihash"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

// WithTLSClientConfig sets a custom tls.Config used for dialing.
// This option is most useful for setting a custom tls.Config.RootCAs certificate pool.
// When dialing a multiaddr that contains a /certhash component, this library will set InsecureSkipVerify and
// overwrite the VerifyPeerCertificate callback.
func WithTLSClientConfig(c *tls.Config) Option {
	return func(t *transport) error {
		t.tlsClientConf = c
		return nil
	}
}

var _ tpt.Resolver = &transport{}
var _ io.Closer = &transport{}

type transport struct {
	privKey ic.PrivKey
	pid     peer.ID
	clock   clock.Clock

	connManager *quicreuse.ConnManager
	rcmgr       network.ResourceManager
	gater       connmgr.ConnectionGater

	listenOnce     sync.Once
	listenOnceErr  error
	certManager    *certManager
	hasCertManager atomic.Bool // set to true once the certManager is initialized
	staticTLSConf  *tls.Config
	tlsClientConf  *tls.Config

	noise *noise.Transport

	connMx sync.Mutex
	conns  map[uint64]*conn // using quic-go's ConnectionTracingKey as map key
}

func New(key ic.PrivKey, psk pnet.PSK, connManager *quicreuse.ConnManager, gater connmgr.ConnectionGater, rcmgr network.ResourceManager, opts ...Option) (tpt.Transport, error) {
	return new(key, psk, gater, rcmgr, func(t *transport) {
		t.connManager = connManager
		t.conns = map[uint64]*conn{}
	}, opts...)
}

func (t *transport) Close() error {
	t.listenOnce.Do(func() {})
	if t.certManager != nil {
		return t.certManager.Close()
	}
	return nil
}

func (t *transport) dial(ctx context.Context, addr ma.Multiaddr, p peer.ID, sni string, certHashes []multihash.DecodedMultihash, scope network.ConnManagementScope) (tpt.CapableConn, error) {
	_, raddr, err := manet.DialArgs(addr)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("https://%s%s?type=noise", raddr, webtransportHTTPEndpoint)

	var tlsConf *tls.Config
	if t.tlsClientConf != nil {
		tlsConf = t.tlsClientConf.Clone()
	} else {
		tlsConf = &tls.Config{}
	}
	tlsConf.NextProtos = append(tlsConf.NextProtos, http3.NextProtoH3)

	if sni != "" {
		tlsConf.ServerName = sni
	}

	if len(certHashes) > 0 {
		// This is not insecure. We verify the certificate ourselves.
		// See https://www.w3.org/TR/webtransport/#certificate-hashes.
		tlsConf.InsecureSkipVerify = true
		tlsConf.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyRawCerts(rawCerts, certHashes)
		}
	}
	qconn, err := t.connManager.DialQUIC(ctx, addr, tlsConf, t.allowWindowIncrease)
	if err != nil {
		return nil, err
	}
	dialer := webtransport.Dialer{
		RoundTripper: &http3.RoundTripper{
			Dial: func(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (quic.EarlyConnection, error) {
				return qconn.(quic.EarlyConnection), nil
			},
		},
	}
	rsp, sess, err := dialer.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	if rsp.StatusCode < 200 || rsp.StatusCode > 299 {
		return nil, fmt.Errorf("invalid response status code: %d", rsp.StatusCode)
	}

	sconn, err := t.upgrade(ctx, sess, p, certHashes)
	if err != nil {
		sess.CloseWithError(1, "")
		return nil, err
	}
	if t.gater != nil && !t.gater.InterceptSecured(network.DirOutbound, p, sconn) {
		sess.CloseWithError(errorCodeConnectionGating, "")
		return nil, fmt.Errorf("secured connection gated")
	}
	conn := newConn(t, sess, sconn, scope)
	t.addConn(sess, conn)
	return conn, nil
}

func (t *transport) upgrade(ctx context.Context, sess *webtransport.Session, p peer.ID, certHashes []multihash.DecodedMultihash) (*connSecurityMultiaddrs, error) {
	local, err := toWebtransportMultiaddr(sess.LocalAddr())
	if err != nil {
		return nil, fmt.Errorf("error determining local addr: %w", err)
	}
	remote, err := toWebtransportMultiaddr(sess.RemoteAddr())
	if err != nil {
		return nil, fmt.Errorf("error determining remote addr: %w", err)
	}

	str, err := sess.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	defer str.Close()

	c, err := t.verifyChallengeOnOutboundConnection(ctx, &webtransportStream{Stream: str, wsess: sess}, p, certHashes)
	if err != nil {
		return nil, err
	}
	return &connSecurityMultiaddrs{
		ConnSecurity:   c,
		ConnMultiaddrs: &connMultiaddrs{local: local, remote: remote},
	}, nil
}

func (t *transport) Listen(laddr ma.Multiaddr) (tpt.Listener, error) {
	isWebTransport, _ := IsWebtransportMultiaddr(laddr)
	if !isWebTransport {
		return nil, fmt.Errorf("cannot listen on non-WebTransport addr: %s", laddr)
	}
	if t.staticTLSConf == nil {
		t.listenOnce.Do(func() {
			t.certManager, t.listenOnceErr = newCertManager(t.privKey, t.clock)
			t.hasCertManager.Store(true)
		})
		if t.listenOnceErr != nil {
			return nil, t.listenOnceErr
		}
	} else {
		return nil, errors.New("static TLS config not supported on WebTransport")
	}
	tlsConf := t.staticTLSConf.Clone()
	if tlsConf == nil {
		tlsConf = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return t.certManager.GetConfig(), nil
		}}
	}
	tlsConf.NextProtos = append(tlsConf.NextProtos, http3.NextProtoH3)

	ln, err := t.connManager.ListenQUIC(laddr, tlsConf, t.allowWindowIncrease)
	if err != nil {
		return nil, err
	}
	return newListener(ln, t, t.staticTLSConf != nil)
}

func (t *transport) allowWindowIncrease(conn quic.Connection, size uint64) bool {
	t.connMx.Lock()
	defer t.connMx.Unlock()

	c, ok := t.conns[conn.Context().Value(quic.ConnectionTracingKey).(uint64)]
	if !ok {
		return false
	}
	return c.allowWindowIncrease(size)
}

func (t *transport) addConn(sess *webtransport.Session, c *conn) {
	t.connMx.Lock()
	t.conns[sess.Context().Value(quic.ConnectionTracingKey).(uint64)] = c
	t.connMx.Unlock()
}

func (t *transport) removeConn(sess *webtransport.Session) {
	t.connMx.Lock()
	delete(t.conns, sess.Context().Value(quic.ConnectionTracingKey).(uint64))
	t.connMx.Unlock()
}

// AddCertHashes adds the current certificate hashes to a multiaddress.
// If called before Listen, it's a no-op.
func (t *transport) AddCertHashes(m ma.Multiaddr) (ma.Multiaddr, bool) {
	if !t.hasCertManager.Load() {
		return m, false
	}
	return m.Encapsulate(t.certManager.AddrComponent()), true
}
