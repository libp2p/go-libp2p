package noise

import (
	"context"
	"net"

	"github.com/libp2p/go-libp2p-core/canonicallog"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/sec"
	manet "github.com/multiformats/go-multiaddr/net"
)

type NoiseOptions struct {
	Prologue []byte
}

var _ sec.SecureTransport = &intermediateTransport{}

// intermediate transport can be used
// to provide per-connection options
type intermediateTransport struct {
	t *Transport
	// options
	prologue []byte
}

// SecureInbound runs the Noise handshake as the responder.
// If p is empty, connections from any peer are accepted.
func (i *intermediateTransport) SecureInbound(ctx context.Context, insecure net.Conn, p peer.ID) (sec.SecureConn, error) {
	c, err := newSecureSession(i.t, ctx, insecure, p, i.prologue, false)
	if err != nil {
		addr, maErr := manet.FromNetAddr(insecure.RemoteAddr())
		if maErr == nil {
			canonicallog.LogPeerStatus(100, p, addr, "handshake_failure", "noise", "err", err.Error())
		}
	}
	return c, err
}

// SecureOutbound runs the Noise handshake as the initiator.
func (i *intermediateTransport) SecureOutbound(ctx context.Context, insecure net.Conn, p peer.ID) (sec.SecureConn, error) {
	return newSecureSession(i.t, ctx, insecure, p, i.prologue, true)
}

func (t *Transport) WithNoiseOptions(opts NoiseOptions) sec.SecureTransport {
	return &intermediateTransport{
		t:        t,
		prologue: opts.Prologue,
	}
}
