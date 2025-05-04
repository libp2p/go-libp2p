//go:build js
// +build js

package libp2pwebrtc

import (
	"fmt"

	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
)

func (t *WebRTCTransport) Listen(addr ma.Multiaddr) (tpt.Listener, error) {
	return nil, fmt.Errorf("listening is not supported in the browser")
}
