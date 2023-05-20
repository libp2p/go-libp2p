//go:build !js

package libp2p

import (
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
	webtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
)

// DefaultTransports are the default libp2p transports.
//
// Use this option when you want to *extend* the set of transports used by
// libp2p instead of replacing them.
var DefaultTransports = ChainOptions(
	Transport(tcp.NewTCPTransport),
	Transport(quic.NewTransport),
	Transport(ws.New),
	Transport(webtransport.New),
)

// DefaultPrivateTransports are the default libp2p transports when a PSK is supplied.
//
// Use this option when you want to *extend* the set of transports used by
// libp2p instead of replacing them.
var DefaultPrivateTransports = ChainOptions(
	Transport(tcp.NewTCPTransport),
	Transport(ws.New),
)

// DefaultListenAddrs configures libp2p to use default listen address.
var DefaultListenAddrs = makeDefaultListenAddrs(
	"/ip4/0.0.0.0/tcp/0",
	"/ip4/0.0.0.0/udp/0/quic",
	"/ip4/0.0.0.0/udp/0/quic-v1",
	"/ip4/0.0.0.0/udp/0/quic-v1/webtransport",
	"/ip6/::/tcp/0",
	"/ip6/::/udp/0/quic",
	"/ip6/::/udp/0/quic-v1",
	"/ip6/::/udp/0/quic-v1/webtransport",
)
