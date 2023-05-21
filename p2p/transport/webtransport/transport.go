package libp2pwebtransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/sec"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/security/noise/pb"

	"github.com/benbjohnson/clock"
	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
)

var log = logging.Logger("webtransport")

const webtransportHTTPEndpoint = "/.well-known/libp2p-webtransport"

const errorCodeConnectionGating = 0x47415445 // GATE in ASCII

const certValidity = 14 * 24 * time.Hour

type Option func(*transport) error

func WithClock(cl clock.Clock) Option {
	return func(t *transport) error {
		t.clock = cl
		return nil
	}
}

var _ tpt.Transport = &transport{}

func new(key ic.PrivKey, psk pnet.PSK, gater connmgr.ConnectionGater, rcmgr network.ResourceManager, beforeOpt func(*transport), opts ...Option) (*transport, error) {
	if len(psk) > 0 {
		log.Error("WebTransport doesn't support private networks yet.")
		return nil, errors.New("WebTransport doesn't support private networks yet")
	}
	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}
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
	if beforeOpt != nil {
		beforeOpt(t)
	}
	for _, opt := range opts {
		if err := opt(t); err != nil {
			return nil, err
		}
	}
	n, err := noise.New(noise.ID, key, nil)
	if err != nil {
		return nil, err
	}
	t.noise = n
	return t, nil
}

func (t *transport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (tpt.CapableConn, error) {
	scope, err := t.rcmgr.OpenConnection(network.DirOutbound, false, raddr)
	if err != nil {
		log.Debugw("resource manager blocked outgoing connection", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}

	c, err := t.dialWithScope(ctx, raddr, p, scope)
	if err != nil {
		scope.Done()
		return nil, err
	}

	return c, nil
}

func (t *transport) dialWithScope(ctx context.Context, raddr ma.Multiaddr, p peer.ID, scope network.ConnManagementScope) (tpt.CapableConn, error) {
	certHashes, err := extractCertHashes(raddr)
	if err != nil {
		return nil, err
	}

	if len(certHashes) == 0 {
		return nil, errors.New("can't dial webtransport without certhashes")
	}

	sni, _ := extractSNI(raddr)

	if err := scope.SetPeer(p); err != nil {
		log.Debugw("resource manager blocked outgoing connection for peer", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}

	maddr, _ := ma.SplitFunc(raddr, func(c ma.Component) bool { return c.Protocol().Code == ma.P_WEBTRANSPORT })
	return t.dial(ctx, maddr, p, sni, certHashes, scope)
}

func (t *transport) verifyChallengeOnOutboundConnection(ctx context.Context, conn net.Conn, p peer.ID, certHashes []multihash.DecodedMultihash) (sec.SecureConn, error) {
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
	c, err := n.SecureOutbound(ctx, conn, p)
	if err != nil {
		return nil, err
	}
	if err = c.Close(); err != nil {
		return nil, err
	}
	// The Noise handshake _should_ guarantee that our verification callback is called.
	// Double-check just in case.
	if !verified {
		return nil, errors.New("didn't verify")
	}

	return c, nil
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
	ok, _ := IsWebtransportMultiaddr(addr)
	return ok
}

func (t *transport) Protocols() []int {
	return []int{ma.P_WEBTRANSPORT}
}

func (t *transport) Proxy() bool {
	return false
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
func (t *transport) Resolve(_ context.Context, maddr ma.Multiaddr) ([]ma.Multiaddr, error) {
	sni, foundSniComponent := extractSNI(maddr)

	if foundSniComponent || sni == "" {
		// The multiaddr already had an sni field, we can keep using it. Or we don't have any sni like thing
		return []ma.Multiaddr{maddr}, nil
	}

	beforeQuicMA, afterIncludingQuicMA := ma.SplitFunc(maddr, func(c ma.Component) bool {
		return c.Protocol().Code == ma.P_QUIC_V1
	})
	quicComponent, afterQuicMA := ma.SplitFirst(afterIncludingQuicMA)
	sniComponent, err := ma.NewComponent(ma.ProtocolWithCode(ma.P_SNI).Name, sni)
	if err != nil {
		return nil, err
	}
	return []ma.Multiaddr{beforeQuicMA.Encapsulate(quicComponent).Encapsulate(sniComponent).Encapsulate(afterQuicMA)}, nil
}
