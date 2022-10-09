package libp2pwebtransport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/p2p/security/noise/pb"

	"github.com/benbjohnson/clock"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/lucas-clemente/quic-go/http3"
	"github.com/marten-seemann/webtransport-go"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/multiformats/go-multihash"
)

var log = logging.Logger("webtransport")

const webtransportHTTPEndpoint = "/.well-known/libp2p-webtransport"

const certValidity = 14 * 24 * time.Hour

type Option func(*transport) error

func WithClock(cl clock.Clock) Option {
	return func(t *transport) error {
		t.clock = cl
		return nil
	}
}

// WithTLSConfig sets a tls.Config used for listening.
// When used, the certificate from that config will be used, and no /certhash will be added to the listener's multiaddr.
// This is most useful when running a listener that has a valid (CA-signed) certificate.
func WithTLSConfig(c *tls.Config) Option {
	return func(t *transport) error {
		t.staticTLSConf = c
		return nil
	}
}

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

type transport struct {
	privKey ic.PrivKey
	pid     peer.ID
	clock   clock.Clock

	rcmgr network.ResourceManager
	gater connmgr.ConnectionGater

	listenOnce    sync.Once
	listenOnceErr error
	certManager   *certManager
	staticTLSConf *tls.Config
	tlsClientConf *tls.Config

	noise *noise.Transport
}

var _ tpt.Transport = &transport{}
var _ tpt.Resolver = &transport{}
var _ io.Closer = &transport{}

func New(key ic.PrivKey, gater connmgr.ConnectionGater, rcmgr network.ResourceManager, opts ...Option) (tpt.Transport, error) {
	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return nil, err
	}
	t := &transport{
		pid:     id,
		privKey: key,
		rcmgr:   rcmgr,
		gater:   gater,
		clock:   clock.New(),
	}
	for _, opt := range opts {
		if err := opt(t); err != nil {
			return nil, err
		}
	}
	n, err := noise.New(key)
	if err != nil {
		return nil, err
	}
	t.noise = n
	return t, nil
}

func (t *transport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (tpt.CapableConn, error) {
	_, addr, err := manet.DialArgs(raddr)
	if err != nil {
		return nil, err
	}
	certHashes, err := extractCertHashes(raddr)
	if err != nil {
		return nil, err
	}

	sni, _ := extractSNI(raddr)

	scope, err := t.rcmgr.OpenConnection(network.DirOutbound, false, raddr)
	if err != nil {
		log.Debugw("resource manager blocked outgoing connection", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}
	if err := scope.SetPeer(p); err != nil {
		log.Debugw("resource manager blocked outgoing connection for peer", "peer", p, "addr", raddr, "error", err)
		scope.Done()
		return nil, err
	}

	sess, err := t.dial(ctx, addr, sni, certHashes)
	if err != nil {
		scope.Done()
		return nil, err
	}
	sconn, err := t.upgrade(ctx, sess, p, certHashes)
	if err != nil {
		sess.Close()
		scope.Done()
		return nil, err
	}
	if t.gater != nil && !t.gater.InterceptSecured(network.DirOutbound, p, sconn) {
		// TODO: can we close with a specific error here?
		sess.Close()
		scope.Done()
		return nil, fmt.Errorf("secured connection gated")
	}

	return newConn(t, sess, sconn, scope), nil
}

func (t *transport) dial(ctx context.Context, addr string, sni string, certHashes []multihash.DecodedMultihash) (*webtransport.Session, error) {
	url := fmt.Sprintf("https://%s%s?type=noise", addr, webtransportHTTPEndpoint)
	var tlsConf *tls.Config
	if t.tlsClientConf != nil {
		tlsConf = t.tlsClientConf.Clone()
	} else {
		tlsConf = &tls.Config{}
	}

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
	dialer := webtransport.Dialer{
		RoundTripper: &http3.RoundTripper{TLSClientConfig: tlsConf},
	}
	rsp, sess, err := dialer.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	if rsp.StatusCode < 200 || rsp.StatusCode > 299 {
		return nil, fmt.Errorf("invalid response status code: %d", rsp.StatusCode)
	}
	return sess, err
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

	// Now run a Noise handshake (using early data) and get all the certificate hashes from the server.
	// We will verify that the certhashes we used to dial is a subset of the certhashes we received from the server.
	var verified bool
	n, err := t.noise.WithSessionOptions(noise.EarlyData(newEarlyDataReceiver(func(b *pb.NoiseExtensions) error {
		decodedCertHashes, err := decodeCertHashesFromProtobuf(b.WebtransportCerthashes)
		if err != nil {
			return err
		}
		for _, sent := range certHashes {
			var found bool
			for _, rcvd := range decodedCertHashes {
				if sent.Code == rcvd.Code && bytes.Equal(sent.Digest, rcvd.Digest) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("missing cert hash: %v", sent)
			}
		}
		verified = true
		return nil
	}), nil))
	if err != nil {
		return nil, fmt.Errorf("failed to create Noise transport: %w", err)
	}
	c, err := n.SecureOutbound(ctx, &webtransportStream{Stream: str, wsess: sess}, p)
	if err != nil {
		return nil, err
	}
	// The Noise handshake _should_ guarantee that our verification callback is called.
	// Double-check just in case.
	if !verified {
		return nil, errors.New("didn't verify")
	}
	return &connSecurityMultiaddrs{
		ConnSecurity:   c,
		ConnMultiaddrs: &connMultiaddrs{local: local, remote: remote},
	}, nil
}

func decodeCertHashesFromProtobuf(b [][]byte) ([]multihash.DecodedMultihash, error) {
	hashes := make([]multihash.DecodedMultihash, 0, len(b))
	for _, h := range b {
		dh, err := multihash.Decode(h)
		if err != nil {
			return nil, fmt.Errorf("failed to decode hash: %w", err)
		}
		hashes = append(hashes, *dh)
	}
	return hashes, nil
}

func (t *transport) CanDial(addr ma.Multiaddr) bool {
	var numHashes int
	ma.ForEach(addr, func(c ma.Component) bool {
		if c.Protocol().Code == ma.P_CERTHASH {
			numHashes++
		}
		return true
	})
	// Remove the /certhash components from the multiaddr.
	// If the multiaddr doesn't contain any certhashes, the node might have a CA-signed certificate.
	for i := 0; i < numHashes; i++ {
		addr, _ = ma.SplitLast(addr)
	}
	return webtransportMatcher.Matches(addr)
}

func (t *transport) Listen(laddr ma.Multiaddr) (tpt.Listener, error) {
	if !webtransportMatcher.Matches(laddr) {
		return nil, fmt.Errorf("cannot listen on non-WebTransport addr: %s", laddr)
	}
	if t.staticTLSConf == nil {
		t.listenOnce.Do(func() {
			t.certManager, t.listenOnceErr = newCertManager(t.clock)
		})
		if t.listenOnceErr != nil {
			return nil, t.listenOnceErr
		}
	}
	return newListener(laddr, t, t.noise, t.certManager, t.staticTLSConf, t.gater, t.rcmgr)
}

func (t *transport) Protocols() []int {
	return []int{ma.P_WEBTRANSPORT}
}

func (t *transport) Proxy() bool {
	return false
}

func (t *transport) Close() error {
	t.listenOnce.Do(func() {})
	if t.certManager != nil {
		return t.certManager.Close()
	}
	return nil
}

// extractSNI returns what the SNI should be for the given maddr. If there is an
// SNI component in the multiaddr, then it will be returned and
// foundSniComponent will be true. If there's no SNI component, but there is a
// DNS-like component, then that will be returned for the sni and
// foundSniComponent will be false (since we didn't find an actual sni component).
func extractSNI(maddr ma.Multiaddr) (sni string, foundSniComponent bool) {
	ma.ForEach(maddr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_SNI:
			sni = c.Value()
			foundSniComponent = true
			return false
		case ma.P_DNS, ma.P_DNS4, ma.P_DNS6, ma.P_DNSADDR:
			sni = c.Value()
			// Keep going in case we find an `sni` component
			return true
		}
		return true
	})
	return sni, foundSniComponent
}

// Resolve implements transport.Resolver
func (t *transport) Resolve(ctx context.Context, maddr ma.Multiaddr) ([]ma.Multiaddr, error) {
	sni, foundSniComponent := extractSNI(maddr)

	if foundSniComponent || sni == "" {
		// The multiaddr already had an sni field, we can keep using it. Or we don't have any sni like thing
		return []ma.Multiaddr{maddr}, nil
	}

	beforeQuicMA, afterIncludingQuicMA := ma.SplitFunc(maddr, func(c ma.Component) bool {
		return c.Protocol().Code == ma.P_QUIC
	})
	quicComponent, afterQuicMA := ma.SplitFirst(afterIncludingQuicMA)
	sniComponent, err := ma.NewComponent(ma.ProtocolWithCode(ma.P_SNI).Name, sni)
	if err != nil {
		return nil, err
	}
	return []ma.Multiaddr{beforeQuicMA.Encapsulate(quicComponent).Encapsulate(sniComponent).Encapsulate(afterQuicMA)}, nil
}
