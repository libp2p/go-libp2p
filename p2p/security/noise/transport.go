package noise

import (
	"context"
	"net"

	"github.com/libp2p/go-libp2p/core/canonicallog"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/p2p/security/noise/pb"

	manet "github.com/multiformats/go-multiaddr/net"
)

// ID is the protocol ID for noise
const (
	ID          = "/noise"
	maxProtoNum = 100
)

var _ sec.SecureTransport = &Transport{}

// Transport implements the interface sec.SecureTransport
// https://godoc.org/github.com/libp2p/go-libp2p/core/sec#SecureConn
type Transport struct {
	localID    peer.ID
	privateKey crypto.PrivKey
	muxers     []string
}

// New creates a new Noise transport using the given private key as its
// libp2p identity key.
func New(privkey crypto.PrivKey, muxers []protocol.ID) (*Transport, error) {
	localID, err := peer.IDFromPrivateKey(privkey)
	if err != nil {
		return nil, err
	}

	smuxers := make([]string, 0, len(muxers))
	for _, muxer := range muxers {
		smuxers = append(smuxers, string(muxer))
	}

	return &Transport{
		localID:    localID,
		privateKey: privkey,
		muxers:     smuxers,
	}, nil
}

// SecureInbound runs the Noise handshake as the responder.
// If p is empty, connections from any peer are accepted.
func (t *Transport) SecureInbound(ctx context.Context, insecure net.Conn, p peer.ID) (sec.SecureConn, error) {
	responderEDH := newTransportEDH(t)
	c, err := newSecureSession(t, ctx, insecure, p, nil, nil, responderEDH, false)
	if err != nil {
		addr, maErr := manet.FromNetAddr(insecure.RemoteAddr())
		if maErr == nil {
			canonicallog.LogPeerStatus(100, p, addr, "handshake_failure", "noise", "err", err.Error())
		}
	}
	return SessionWithConnState(c, responderEDH.MatchMuxers(false)), err
}

// SecureOutbound runs the Noise handshake as the initiator.
func (t *Transport) SecureOutbound(ctx context.Context, insecure net.Conn, p peer.ID) (sec.SecureConn, error) {
	initiatorEDH := newTransportEDH(t)
	c, err := newSecureSession(t, ctx, insecure, p, nil, initiatorEDH, nil, true)
	if err != nil {
		return c, err
	}
	return SessionWithConnState(c, initiatorEDH.MatchMuxers(true)), err
}

func (t *Transport) WithSessionOptions(opts ...SessionOption) (sec.SecureTransport, error) {
	st := &SessionTransport{t: t}
	for _, opt := range opts {
		if err := opt(st); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func matchMuxers(initiatorMuxers, responderMuxers []string) string {
	for _, muxer := range responderMuxers {
		for _, initMuxer := range initiatorMuxers {
			if initMuxer == muxer {
				return muxer
			}
		}
	}
	return ""
}

type transportEarlyDataHandler struct {
	transport      *Transport
	receivedMuxers []string
}

var _ EarlyDataHandler = &transportEarlyDataHandler{}

func newTransportEDH(t *Transport) *transportEarlyDataHandler {
	return &transportEarlyDataHandler{transport: t}
}

func (i *transportEarlyDataHandler) Send(context.Context, net.Conn, peer.ID) *pb.NoiseExtensions {
	return &pb.NoiseExtensions{
		StreamMuxers: i.transport.muxers,
	}
}

func (i *transportEarlyDataHandler) Received(_ context.Context, _ net.Conn, extension *pb.NoiseExtensions) error {
	// Discard messages with size or the number of protocols exceeding extension limit for security.
	if extension != nil && len(extension.StreamMuxers) <= maxProtoNum {
		i.receivedMuxers = extension.GetStreamMuxers()
	}
	return nil
}

func (i *transportEarlyDataHandler) MatchMuxers(isInitiator bool) string {
	if isInitiator {
		return matchMuxers(i.transport.muxers, i.receivedMuxers)
	}
	return matchMuxers(i.receivedMuxers, i.transport.muxers)
}
