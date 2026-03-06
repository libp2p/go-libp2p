package mixnet

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// StreamUpgrader provides a libp2p-friendly stream upgrade surface for Mixnet.
// This matches the PRD Stream_Upgrader responsibilities.
type StreamUpgrader interface {
	UpgradeOutbound(ctx context.Context, dest peer.ID, proto protocol.ID) (*MixStream, error)
	UpgradeInbound(ctx context.Context) (*MixStream, error)
}

// MixnetStreamUpgrader implements StreamUpgrader using Mixnet circuits.
type MixnetStreamUpgrader struct {
	mixnet *Mixnet
}

// NewStreamUpgrader constructs a StreamUpgrader for Mixnet.
func NewStreamUpgrader(m *Mixnet) *MixnetStreamUpgrader {
	return &MixnetStreamUpgrader{mixnet: m}
}

// UpgradeOutbound establishes circuits and returns a MixStream.
// The proto argument is stored in the returned stream for caller reference.
func (u *MixnetStreamUpgrader) UpgradeOutbound(ctx context.Context, dest peer.ID, proto protocol.ID) (*MixStream, error) {
	s, err := u.mixnet.OpenStream(ctx, dest)
	if err != nil {
		return nil, err
	}
	s.proto = string(proto)
	return s, nil
}

// UpgradeInbound waits for the next inbound mixnet session and returns a MixStream.
func (u *MixnetStreamUpgrader) UpgradeInbound(ctx context.Context) (*MixStream, error) {
	return u.mixnet.AcceptStream(ctx)
}
