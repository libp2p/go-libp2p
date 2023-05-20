//go:build js

package libp2p

import (
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
)

// DefaultTransport has been trimmed down to what works in the browser.
var DefaultTransports = ChainOptions(
	// TODO(@Jorropo): If the wasm experiment is doing good, write shims for webtransport and webrtc.
	Transport(ws.New),
)

// DefaultPrivateTransports has been trimmed down to what works in the browser.
var DefaultPrivateTransports = ChainOptions(
	Transport(ws.New),
)

// DefaultListenAddrs is sadly empty, we could maybe prop it up if webrtc in browser support is added.
var DefaultListenAddrs = makeDefaultListenAddrs()
