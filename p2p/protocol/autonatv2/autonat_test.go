package autonatv2

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"

	"github.com/libp2p/go-msgio/pbio"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAutoNAT(t testing.TB, dialer host.Host, opts ...AutoNATOption) *AutoNAT {
	t.Helper()
	b := eventbus.NewBus()
	h := bhost.NewBlankHost(
		swarmt.GenSwarm(t, swarmt.EventBus(b)), bhost.WithEventBus(b))
	if dialer == nil {
		dialer = bhost.NewBlankHost(
			swarmt.GenSwarm(t,
				swarmt.WithSwarmOpts(
					swarm.WithUDPBlackHoleSuccessCounter(nil),
					swarm.WithIPv6BlackHoleSuccessCounter(nil))))
	}
	an, err := New(h, dialer, opts...)
	if err != nil {
		t.Error(err)
	}
	an.Start()
	t.Cleanup(an.Close)
	return an
}

func parseAddrs(t *testing.T, msg *pb.Message) []ma.Multiaddr {
	t.Helper()
	req := msg.GetDialRequest()
	addrs := make([]ma.Multiaddr, 0)
	for _, ab := range req.Addrs {
		a, err := ma.NewMultiaddrBytes(ab)
		if err != nil {
			t.Error("invalid addr bytes", ab)
		}
		addrs = append(addrs, a)
	}
	return addrs
}

// idAndConnect identifies b to a and connects them
func idAndConnect(t testing.TB, a, b host.Host) {
	a.Peerstore().AddAddrs(b.ID(), b.Addrs(), peerstore.PermanentAddrTTL)
	a.Peerstore().AddProtocols(b.ID(), DialProtocol)

	err := a.Connect(context.Background(), peer.AddrInfo{ID: b.ID()})
	require.NoError(t, err)
}

// waitForPeer waits for a to have 1 peer in the peerMap
func waitForPeer(t testing.TB, a *AutoNAT) {
	t.Helper()
	require.Eventually(t, func() bool {
		a.mx.Lock()
		defer a.mx.Unlock()
		return a.peers.GetRand() != ""
	}, 5*time.Second, 100*time.Millisecond)
}

// idAndWait provides server address and protocol to client
func idAndWait(t testing.TB, cli *AutoNAT, srv *AutoNAT) {
	idAndConnect(t, cli.host, srv.host)
	waitForPeer(t, cli)
}

func TestAutoNATPrivateAddr(t *testing.T) {
	an := newAutoNAT(t, nil)
	res, err := an.GetReachability(context.Background(), []Request{{Addr: ma.StringCast("/ip4/192.168.0.1/udp/10/quic-v1")}})
	require.Equal(t, res, Result{})
	require.Contains(t, err.Error(), "private address cannot be verified by autonatv2")
}

func TestClientRequest(t *testing.T) {
	an := newAutoNAT(t, nil, allowPrivateAddrs)
	defer an.Close()
	defer an.host.Close()

	b := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer b.Close()
	idAndConnect(t, an.host, b)
	waitForPeer(t, an)

	addrs := an.host.Addrs()
	addrbs := make([][]byte, len(addrs))
	for i := 0; i < len(addrs); i++ {
		addrbs[i] = addrs[i].Bytes()
	}

	var receivedRequest atomic.Bool
	b.SetStreamHandler(DialProtocol, func(s network.Stream) {
		receivedRequest.Store(true)
		r := pbio.NewDelimitedReader(s, maxMsgSize)
		var msg pb.Message
		assert.NoError(t, r.ReadMsg(&msg))
		assert.NotNil(t, msg.GetDialRequest())
		assert.Equal(t, addrbs, msg.GetDialRequest().Addrs)
		s.Reset()
	})

	res, err := an.GetReachability(context.Background(), []Request{
		{Addr: addrs[0], SendDialData: true}, {Addr: addrs[1]},
	})
	require.Equal(t, res, Result{})
	require.NotNil(t, err)
	require.True(t, receivedRequest.Load())
}

func TestClientServerError(t *testing.T) {
	an := newAutoNAT(t, nil, allowPrivateAddrs)
	defer an.Close()
	defer an.host.Close()

	b := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer b.Close()
	idAndConnect(t, an.host, b)
	waitForPeer(t, an)

	tests := []struct {
		handler  func(network.Stream)
		errorStr string
	}{
		{
			handler: func(s network.Stream) {
				s.Reset()
			},
			errorStr: "stream reset",
		},
		{
			handler: func(s network.Stream) {
				w := pbio.NewDelimitedWriter(s)
				assert.NoError(t, w.WriteMsg(
					&pb.Message{Msg: &pb.Message_DialRequest{DialRequest: &pb.DialRequest{}}}))
			},
			errorStr: "invalid msg type",
		},
		{
			handler: func(s network.Stream) {
				w := pbio.NewDelimitedWriter(s)
				assert.NoError(t, w.WriteMsg(
					&pb.Message{Msg: &pb.Message_DialResponse{
						DialResponse: &pb.DialResponse{
							Status: pb.DialResponse_E_DIAL_REFUSED,
						},
					}},
				))
			},
			errorStr: ErrDialRefused.Error(),
		},
	}

	for i, tc := range tests {
		t.Run(fmt.Sprintf("test-%d", i), func(t *testing.T) {
			b.SetStreamHandler(DialProtocol, tc.handler)
			addrs := an.host.Addrs()
			res, err := an.GetReachability(
				context.Background(),
				newTestRequests(addrs, false))
			require.Equal(t, res, Result{})
			require.NotNil(t, err)
			require.Contains(t, err.Error(), tc.errorStr)
		})
	}
}

func TestClientDataRequest(t *testing.T) {
	an := newAutoNAT(t, nil, allowPrivateAddrs)
	defer an.Close()
	defer an.host.Close()

	b := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer b.Close()
	idAndConnect(t, an.host, b)
	waitForPeer(t, an)

	tests := []struct {
		handler func(network.Stream)
		name    string
	}{
		{
			name: "provides dial data",
			handler: func(s network.Stream) {
				r := pbio.NewDelimitedReader(s, maxMsgSize)
				var msg pb.Message
				assert.NoError(t, r.ReadMsg(&msg))
				w := pbio.NewDelimitedWriter(s)
				if err := w.WriteMsg(&pb.Message{
					Msg: &pb.Message_DialDataRequest{
						DialDataRequest: &pb.DialDataRequest{
							AddrIdx:  0,
							NumBytes: 10000,
						},
					}},
				); err != nil {
					t.Error(err)
					s.Reset()
					return
				}
				var dialData []byte
				for len(dialData) < 10000 {
					if err := r.ReadMsg(&msg); err != nil {
						t.Error(err)
						s.Reset()
						return
					}
					if msg.GetDialDataResponse() == nil {
						t.Errorf("expected to receive msg of type DialDataResponse")
						s.Reset()
						return
					}
					dialData = append(dialData, msg.GetDialDataResponse().Data...)
				}
				s.Reset()
			},
		},
		{
			name: "low priority addr",
			handler: func(s network.Stream) {
				r := pbio.NewDelimitedReader(s, maxMsgSize)
				var msg pb.Message
				assert.NoError(t, r.ReadMsg(&msg))
				w := pbio.NewDelimitedWriter(s)
				if err := w.WriteMsg(&pb.Message{
					Msg: &pb.Message_DialDataRequest{
						DialDataRequest: &pb.DialDataRequest{
							AddrIdx:  1,
							NumBytes: 10000,
						},
					}},
				); err != nil {
					t.Error(err)
					s.Reset()
					return
				}
				assert.Error(t, r.ReadMsg(&msg))
				s.Reset()
			},
		},
		{
			name: "too high dial data request",
			handler: func(s network.Stream) {
				r := pbio.NewDelimitedReader(s, maxMsgSize)
				var msg pb.Message
				assert.NoError(t, r.ReadMsg(&msg))
				w := pbio.NewDelimitedWriter(s)
				if err := w.WriteMsg(&pb.Message{
					Msg: &pb.Message_DialDataRequest{
						DialDataRequest: &pb.DialDataRequest{
							AddrIdx:  0,
							NumBytes: 1 << 32,
						},
					}},
				); err != nil {
					t.Error(err)
					s.Reset()
					return
				}
				assert.Error(t, r.ReadMsg(&msg))
				s.Reset()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b.SetStreamHandler(DialProtocol, tc.handler)
			addrs := an.host.Addrs()

			res, err := an.GetReachability(
				context.Background(),
				[]Request{
					{Addr: addrs[0], SendDialData: true},
					{Addr: addrs[1]},
				})
			require.Equal(t, res, Result{})
			require.NotNil(t, err)
		})
	}
}

func TestClientDialBacks(t *testing.T) {
	an := newAutoNAT(t, nil, allowPrivateAddrs)
	defer an.Close()
	defer an.host.Close()

	b := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer b.Close()
	idAndConnect(t, an.host, b)
	waitForPeer(t, an)

	dialerHost := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer dialerHost.Close()

	readReq := func(r pbio.Reader) ([]ma.Multiaddr, uint64, error) {
		var msg pb.Message
		if err := r.ReadMsg(&msg); err != nil {
			return nil, 0, err
		}
		if msg.GetDialRequest() == nil {
			return nil, 0, errors.New("no dial request in msg")
		}
		addrs := parseAddrs(t, &msg)
		return addrs, msg.GetDialRequest().GetNonce(), nil
	}

	writeNonce := func(addr ma.Multiaddr, nonce uint64) error {
		pid := an.host.ID()
		dialerHost.Peerstore().AddAddr(pid, addr, peerstore.PermanentAddrTTL)
		defer func() {
			dialerHost.Network().ClosePeer(pid)
			dialerHost.Peerstore().RemovePeer(pid)
			dialerHost.Peerstore().ClearAddrs(pid)
		}()
		as, err := dialerHost.NewStream(context.Background(), pid, DialBackProtocol)
		if err != nil {
			return err
		}
		w := pbio.NewDelimitedWriter(as)
		if err := w.WriteMsg(&pb.DialBack{Nonce: nonce}); err != nil {
			return err
		}
		as.CloseWrite()
		data := make([]byte, 1)
		as.Read(data)
		as.Close()
		return nil
	}

	tests := []struct {
		name    string
		handler func(network.Stream)
		success bool
	}{
		{
			name: "correct dial attempt",
			handler: func(s network.Stream) {
				r := pbio.NewDelimitedReader(s, maxMsgSize)
				w := pbio.NewDelimitedWriter(s)

				addrs, nonce, err := readReq(r)
				if err != nil {
					s.Reset()
					t.Error(err)
					return
				}
				if err := writeNonce(addrs[1], nonce); err != nil {
					s.Reset()
					t.Error(err)
					return
				}
				w.WriteMsg(&pb.Message{
					Msg: &pb.Message_DialResponse{
						DialResponse: &pb.DialResponse{
							Status:     pb.DialResponse_OK,
							DialStatus: pb.DialStatus_OK,
							AddrIdx:    1,
						},
					},
				})
				s.Close()
			},
			success: true,
		},
		{
			name: "no dial attempt",
			handler: func(s network.Stream) {
				r := pbio.NewDelimitedReader(s, maxMsgSize)
				if _, _, err := readReq(r); err != nil {
					s.Reset()
					t.Error(err)
					return
				}
				resp := &pb.DialResponse{
					Status:     pb.DialResponse_OK,
					DialStatus: pb.DialStatus_OK,
					AddrIdx:    0,
				}
				w := pbio.NewDelimitedWriter(s)
				w.WriteMsg(&pb.Message{
					Msg: &pb.Message_DialResponse{
						DialResponse: resp,
					},
				})
				s.Close()
			},
			success: false,
		},
		{
			name: "invalid reported address",
			handler: func(s network.Stream) {
				r := pbio.NewDelimitedReader(s, maxMsgSize)
				addrs, nonce, err := readReq(r)
				if err != nil {
					s.Reset()
					t.Error(err)
					return
				}

				if err := writeNonce(addrs[1], nonce); err != nil {
					s.Reset()
					t.Error(err)
					return
				}

				w := pbio.NewDelimitedWriter(s)
				w.WriteMsg(&pb.Message{
					Msg: &pb.Message_DialResponse{
						DialResponse: &pb.DialResponse{
							Status:     pb.DialResponse_OK,
							DialStatus: pb.DialStatus_OK,
							AddrIdx:    0,
						},
					},
				})
				s.Close()
			},
			success: false,
		},
		{
			name: "invalid nonce",
			handler: func(s network.Stream) {
				r := pbio.NewDelimitedReader(s, maxMsgSize)
				addrs, nonce, err := readReq(r)
				if err != nil {
					s.Reset()
					t.Error(err)
					return
				}
				if err := writeNonce(addrs[0], nonce-1); err != nil {
					s.Reset()
					t.Error(err)
					return
				}
				w := pbio.NewDelimitedWriter(s)
				w.WriteMsg(&pb.Message{
					Msg: &pb.Message_DialResponse{
						DialResponse: &pb.DialResponse{
							Status:     pb.DialResponse_OK,
							DialStatus: pb.DialStatus_OK,
							AddrIdx:    0,
						},
					},
				})
				s.Close()
			},
			success: false,
		},
		{
			name: "invalid addr index",
			handler: func(s network.Stream) {
				r := pbio.NewDelimitedReader(s, maxMsgSize)
				_, _, err := readReq(r)
				if err != nil {
					s.Reset()
					t.Error(err)
					return
				}
				w := pbio.NewDelimitedWriter(s)
				w.WriteMsg(&pb.Message{
					Msg: &pb.Message_DialResponse{
						DialResponse: &pb.DialResponse{
							Status:     pb.DialResponse_OK,
							DialStatus: pb.DialStatus_OK,
							AddrIdx:    10,
						},
					},
				})
				s.Close()
			},
			success: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addrs := an.host.Addrs()
			b.SetStreamHandler(DialProtocol, tc.handler)
			res, err := an.GetReachability(
				context.Background(),
				[]Request{
					{Addr: addrs[0], SendDialData: true},
					{Addr: addrs[1]},
				})
			if !tc.success {
				require.Error(t, err)
				require.Equal(t, Result{}, res)
			} else {
				require.NoError(t, err)
				require.Equal(t, res.Reachability, network.ReachabilityPublic)
				require.Equal(t, res.Status, pb.DialStatus_OK)
			}
		})
	}
}

func TestEventSubscription(t *testing.T) {
	an := newAutoNAT(t, nil)
	defer an.host.Close()

	b := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer b.Close()
	c := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer c.Close()

	idAndConnect(t, an.host, b)
	require.Eventually(t, func() bool {
		an.mx.Lock()
		defer an.mx.Unlock()
		return len(an.peers.peers) == 1
	}, 5*time.Second, 100*time.Millisecond)

	idAndConnect(t, an.host, c)
	require.Eventually(t, func() bool {
		an.mx.Lock()
		defer an.mx.Unlock()
		return len(an.peers.peers) == 2
	}, 5*time.Second, 100*time.Millisecond)

	an.host.Network().ClosePeer(b.ID())
	require.Eventually(t, func() bool {
		an.mx.Lock()
		defer an.mx.Unlock()
		return len(an.peers.peers) == 1
	}, 5*time.Second, 100*time.Millisecond)

	an.host.Network().ClosePeer(c.ID())
	require.Eventually(t, func() bool {
		an.mx.Lock()
		defer an.mx.Unlock()
		return len(an.peers.peers) == 0
	}, 5*time.Second, 100*time.Millisecond)
}

func TestPeersMap(t *testing.T) {
	emptyPeerID := peer.ID("")

	t.Run("single_item", func(t *testing.T) {
		p := newPeersMap()
		p.Put("peer1")
		p.Delete("peer1")
		p.Put("peer1")
		require.Equal(t, peer.ID("peer1"), p.GetRand())
		p.Delete("peer1")
		require.Equal(t, emptyPeerID, p.GetRand())
	})

	t.Run("multiple_items", func(t *testing.T) {
		p := newPeersMap()
		require.Equal(t, emptyPeerID, p.GetRand())

		allPeers := make(map[peer.ID]bool)
		for i := 0; i < 20; i++ {
			pid := peer.ID(fmt.Sprintf("peer-%d", i))
			allPeers[pid] = true
			p.Put(pid)
		}
		foundPeers := make(map[peer.ID]bool)
		for i := 0; i < 1000; i++ {
			pid := p.GetRand()
			require.NotEqual(t, emptyPeerID, p)
			require.True(t, allPeers[pid])
			foundPeers[pid] = true
			if len(foundPeers) == len(allPeers) {
				break
			}
		}
		for pid := range allPeers {
			p.Delete(pid)
		}
		require.Equal(t, emptyPeerID, p.GetRand())
	})
}

func TestAreAddrsConsistency(t *testing.T) {
	c := &client{
		normalizeMultiaddr: func(a ma.Multiaddr) ma.Multiaddr {
			for {
				rest, l := ma.SplitLast(a)
				if _, err := l.ValueForProtocol(ma.P_CERTHASH); err != nil {
					return a
				}
				a = rest
			}
		},
	}
	tests := []struct {
		name      string
		localAddr ma.Multiaddr
		dialAddr  ma.Multiaddr
		success   bool
	}{
		{
			name:      "simple match",
			localAddr: ma.StringCast("/ip4/192.168.0.1/tcp/12345"),
			dialAddr:  ma.StringCast("/ip4/1.2.3.4/tcp/23232"),
			success:   true,
		},
		{
			name:      "nat64",
			localAddr: ma.StringCast("/ip6/1::1/tcp/12345"),
			dialAddr:  ma.StringCast("/ip4/1.2.3.4/tcp/23232"),
			success:   false,
		},
		{
			name:      "simple mismatch",
			localAddr: ma.StringCast("/ip4/192.168.0.1/tcp/12345"),
			dialAddr:  ma.StringCast("/ip4/1.2.3.4/udp/23232/quic-v1"),
			success:   false,
		},
		{
			name:      "quic-vs-webtransport",
			localAddr: ma.StringCast("/ip4/192.168.0.1/udp/12345/quic-v1"),
			dialAddr:  ma.StringCast("/ip4/1.2.3.4/udp/123/quic-v1/webtransport"),
			success:   false,
		},
		{
			name:      "webtransport-certhash",
			localAddr: ma.StringCast("/ip4/192.168.0.1/udp/12345/quic-v1/webtransport"),
			dialAddr:  ma.StringCast("/ip4/1.2.3.4/udp/123/quic-v1/webtransport/certhash/uEgNmb28"),
			success:   true,
		},
		{
			name:      "dns",
			localAddr: ma.StringCast("/dns/lib.p2p/udp/12345/quic-v1"),
			dialAddr:  ma.StringCast("/ip6/1::1/udp/123/quic-v1/"),
			success:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if c.areAddrsConsistent(tc.localAddr, tc.dialAddr) != tc.success {
				wantStr := "match"
				if !tc.success {
					wantStr = "mismatch"
				}
				t.Errorf("expected %s between\nlocal addr: %s\ndial addr:  %s", wantStr, tc.localAddr, tc.dialAddr)
			}
		})
	}

}
