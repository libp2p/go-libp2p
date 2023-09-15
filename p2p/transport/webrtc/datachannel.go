package libp2pwebrtc

import (
	"context"

	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v3"
)

// only use this if the datachannels are detached, since the OnOpen callback
// will be called immediately. Only use after the peerconnection is open.
// The context should close if the peerconnection underlying the datachannel
// is closed.
func getDetachedChannel(ctx context.Context, dc *webrtc.DataChannel) (rwc datachannel.ReadWriteCloser, err error) {
	done := make(chan struct{})
	dc.OnOpen(func() {
		defer close(done)
		rwc, err = dc.Detach()
	})
	// this is safe since for detached datachannels, the peerconnection runs the onOpen
	// callback immediately if the SCTP transport is also connected.
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return
}
