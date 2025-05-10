//go:build js
// +build js

package libp2p

// Only WebSocket and WebTransport are supported in the browser.
var DefaultTransports = ChainOptions()
var DefaultPrivateTransports = ChainOptions()
