package autonatv2

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/test"
	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func newTestRequests(addrs []ma.Multiaddr, sendDialData bool) (reqs []Request) {
	reqs = make([]Request, len(addrs))
	for i := 0; i < len(addrs); i++ {
		reqs[i] = Request{Addr: addrs[i], SendDialData: sendDialData}
	}
	return
}

func TestServerInvalidAddrsRejected(t *testing.T) {
	c := newAutoNAT(t, nil, allowAllAddrs)
	defer c.Close()
	defer c.host.Close()

	t.Run("no transport", func(t *testing.T) {
		dialer := bhost.NewBlankHost(swarmt.GenSwarm(t, swarmt.OptDisableQUIC, swarmt.OptDisableTCP))
		an := newAutoNAT(t, dialer, allowAllAddrs)
		defer an.Close()
		defer an.host.Close()

		idAndWait(t, c, an)

		res, err := c.CheckReachability(context.Background(), newTestRequests(c.host.Addrs(), true))
		require.ErrorIs(t, err, ErrDialRefused)
		require.Equal(t, Result{}, res)
	})

	t.Run("private addrs", func(t *testing.T) {
		an := newAutoNAT(t, nil)
		defer an.Close()
		defer an.host.Close()

		idAndWait(t, c, an)

		res, err := c.CheckReachability(context.Background(), newTestRequests(c.host.Addrs(), true))
		require.ErrorIs(t, err, ErrDialRefused)
		require.Equal(t, Result{}, res)
	})
}

func TestServerDataRequest(t *testing.T) {
	// server will skip all tcp addresses
	dialer := bhost.NewBlankHost(swarmt.GenSwarm(t, swarmt.OptDisableTCP))
	// ask for dial data for quic address
	an := newAutoNAT(t, dialer, allowAllAddrs, withDataRequestPolicy(
		func(s network.Stream, dialAddr ma.Multiaddr) bool {
			if _, err := dialAddr.ValueForProtocol(ma.P_QUIC_V1); err == nil {
				return true
			}
			return false
		}),
		WithServerRateLimit(10, 10, 10),
	)
	defer an.Close()
	defer an.host.Close()

	c := newAutoNAT(t, nil, allowAllAddrs)
	defer c.Close()
	defer c.host.Close()

	idAndWait(t, c, an)

	var quicAddr, tcpAddr ma.Multiaddr
	for _, a := range c.host.Addrs() {
		if _, err := a.ValueForProtocol(ma.P_QUIC_V1); err == nil {
			quicAddr = a
		} else if _, err := a.ValueForProtocol(ma.P_TCP); err == nil {
			tcpAddr = a
		}
	}

	_, err := c.CheckReachability(context.Background(), []Request{{Addr: tcpAddr, SendDialData: true}, {Addr: quicAddr}})
	require.Error(t, err)

	res, err := c.CheckReachability(context.Background(), []Request{{Addr: quicAddr, SendDialData: true}, {Addr: tcpAddr}})
	require.NoError(t, err)

	require.Equal(t, Result{
		Idx:          0,
		Addr:         quicAddr,
		Reachability: network.ReachabilityPublic,
		Status:       pb.DialStatus_OK,
	}, res)
}

func TestServerDial(t *testing.T) {
	an := newAutoNAT(t, nil, WithServerRateLimit(10, 10, 10), allowAllAddrs)
	defer an.Close()
	defer an.host.Close()

	c := newAutoNAT(t, nil, allowAllAddrs)
	defer c.Close()
	defer c.host.Close()

	idAndWait(t, c, an)

	unreachableAddr := ma.StringCast("/ip4/1.2.3.4/tcp/2")
	hostAddrs := c.host.Addrs()

	t.Run("unreachable addr", func(t *testing.T) {
		res, err := c.CheckReachability(context.Background(),
			append([]Request{{Addr: unreachableAddr, SendDialData: true}}, newTestRequests(hostAddrs, false)...))
		require.NoError(t, err)
		require.Equal(t, Result{
			Idx:          0,
			Addr:         unreachableAddr,
			Reachability: network.ReachabilityPrivate,
			Status:       pb.DialStatus_E_DIAL_ERROR,
		}, res)
	})

	t.Run("reachable addr", func(t *testing.T) {
		res, err := c.CheckReachability(context.Background(), newTestRequests(c.host.Addrs(), false))
		require.NoError(t, err)
		require.Equal(t, Result{
			Idx:          0,
			Addr:         hostAddrs[0],
			Reachability: network.ReachabilityPublic,
			Status:       pb.DialStatus_OK,
		}, res)
	})

	t.Run("dialback error", func(t *testing.T) {
		c.host.RemoveStreamHandler(DialBackProtocol)
		res, err := c.CheckReachability(context.Background(), newTestRequests(c.host.Addrs(), false))
		require.NoError(t, err)
		require.Equal(t, Result{
			Idx:          0,
			Addr:         hostAddrs[0],
			Reachability: network.ReachabilityUnknown,
			Status:       pb.DialStatus_E_DIAL_BACK_ERROR,
		}, res)
	})
}

func TestRateLimiter(t *testing.T) {
	cl := test.NewMockClock()
	r := rateLimiter{RPM: 3, PerPeerRPM: 2, DialDataRPM: 1, now: cl.Now}

	require.True(t, r.Accept("peer1", false))

	cl.AdvanceBy(10 * time.Second)
	require.False(t, r.Accept("peer1", false)) // first request is still active
	r.CompleteRequest("peer1")

	require.True(t, r.Accept("peer1", false))
	r.CompleteRequest("peer1")

	cl.AdvanceBy(10 * time.Second)
	require.False(t, r.Accept("peer1", false))

	cl.AdvanceBy(10 * time.Second)
	require.True(t, r.Accept("peer2", false))
	r.CompleteRequest("peer2")

	cl.AdvanceBy(10 * time.Second)
	require.False(t, r.Accept("peer3", false))

	cl.AdvanceBy(21 * time.Second) // first request expired
	require.True(t, r.Accept("peer1", false))
	r.CompleteRequest("peer1")

	cl.AdvanceBy(10 * time.Second)
	require.True(t, r.Accept("peer3", true))
	r.CompleteRequest("peer3")

	cl.AdvanceBy(50 * time.Second)
	require.False(t, r.Accept("peer3", true))

	cl.AdvanceBy(11 * time.Second)
	require.True(t, r.Accept("peer3", true))
}
