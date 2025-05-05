//go:build js
// +build js

package libp2p

import (
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
	webtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
)

// Only WebSocket and WebTransport are supported in the browser.
var DefaultTransports = ChainOptions(
	Transport(ws.New),
	Transport(webtransport.New),
)
