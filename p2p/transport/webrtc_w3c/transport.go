package webrtc_w3c

import (
	"context"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	wrtc "github.com/libp2p/go-libp2p/p2p/transport/webrtc"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/pion/webrtc/v3"
)

var log = logging.Logger("webrtc_w3c")

var _ tpt.Transport = &Client{}

const MULTIADDR_PROTOCOL = "/webrtc-w3c"
const SIGNALING_PROTOCOL = "/webrtc-signaling"

type Client struct {
	ctx    context.Context
	host   host.Host
	config webrtc.Configuration
}

// CanDial implements transport.Transport
func (*Client) CanDial(addr ma.Multiaddr) bool {
	panic("unimplemented")
}

// Dial implements transport.Transport
func (c *Client) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (tpt.CapableConn, error) {
	scope, err := c.host.Network().ResourceManager().OpenConnection(network.DirOutbound, false, raddr)
	if err != nil {
		return nil, err
	}
	baseAddr, err := getBaseMultiaddr(raddr)
	if err != nil {
		return nil, err
	}

	remotePubKey := c.host.Peerstore().PubKey(p)
	var localAddress ma.Multiaddr
	if len(c.host.Addrs()) != 0 {
		localAddress = c.host.Addrs()[0]
	}
	// ensure there is an addr to connect to the remote peer
	c.host.Peerstore().AddAddr(p, baseAddr, peerstore.TempAddrTTL)

	// create a new stream to the remote
	stream, err := c.host.NewStream(ctx, p, SIGNALING_PROTOCOL)
	if err != nil {
		return nil, err
	}
	// attempt webrtc connection
	peerConnection, err := connect(ctx, c.config, stream)
	return wrtc.NewWebRTCConnection(
		network.DirOutbound,
		peerConnection,
		c,
		scope,
		c.host.ID(),
		localAddress,
		p,
		remotePubKey,
		raddr,
	)
}

// Listen implements transport.Transport
func (*Client) Listen(laddr ma.Multiaddr) (tpt.Listener, error) {
	panic("unimplemented")
}

// Protocols implements transport.Transport
func (*Client) Protocols() []int {
	return []int{}
}

// Proxy implements transport.Transport
func (*Client) Proxy() bool {
	return false
}

func handleIncomingConnections(stream network.Stream) {}
