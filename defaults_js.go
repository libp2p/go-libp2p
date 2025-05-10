//go:build js
// +build js

package libp2p

import "github.com/libp2p/go-libp2p/p2p/security/noise"

// Only WebSocket and WebTransport are supported in the browser.
var DefaultTransports = ChainOptions()
var DefaultPrivateTransports = ChainOptions()

var DefaultSecurity = ChainOptions(
	Security(noise.ID, noise.New),
)
