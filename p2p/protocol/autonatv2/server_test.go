package autonatv2

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
	"github.com/libp2p/go-msgio/pbio"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-varint"
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
	c := newAutoNAT(t, nil, allowPrivateAddrs, withAmplificationAttackPreventionDialWait(0))
	defer c.Close()
	defer c.host.Close()

	t.Run("no transport", func(t *testing.T) {
		dialer := bhost.NewBlankHost(swarmt.GenSwarm(t, swarmt.OptDisableQUIC, swarmt.OptDisableTCP))
		an := newAutoNAT(t, dialer, allowPrivateAddrs)
		defer an.Close()
		defer an.host.Close()

		idAndWait(t, c, an)

		res, err := c.GetReachability(context.Background(), newTestRequests(c.host.Addrs(), true))
		require.ErrorIs(t, err, ErrDialRefused)
		require.Equal(t, Result{}, res)
	})

	t.Run("black holed addr", func(t *testing.T) {
		dialer := bhost.NewBlankHost(swarmt.GenSwarm(
			t, swarmt.WithSwarmOpts(swarm.WithReadOnlyBlackHoleDetector())))
		an := newAutoNAT(t, dialer)
		defer an.Close()
		defer an.host.Close()

		idAndWait(t, c, an)

		res, err := c.GetReachability(context.Background(),
			[]Request{{
				Addr:         ma.StringCast("/ip4/1.2.3.4/udp/1234/quic-v1"),
				SendDialData: true,
			}})
		require.ErrorIs(t, err, ErrDialRefused)
		require.Equal(t, Result{}, res)
	})

	t.Run("private addrs", func(t *testing.T) {
		an := newAutoNAT(t, nil)
		defer an.Close()
		defer an.host.Close()

		idAndWait(t, c, an)

		res, err := c.GetReachability(context.Background(), newTestRequests(c.host.Addrs(), true))
		require.ErrorIs(t, err, ErrDialRefused)
		require.Equal(t, Result{}, res)
	})

	t.Run("relay addrs", func(t *testing.T) {
		an := newAutoNAT(t, nil)
		defer an.Close()
		defer an.host.Close()

		idAndWait(t, c, an)

		res, err := c.GetReachability(context.Background(), newTestRequests(
			[]ma.Multiaddr{ma.StringCast(fmt.Sprintf("/ip4/1.2.3.4/tcp/1/p2p/%s/p2p-circuit/p2p/%s", c.host.ID(), c.srv.dialerHost.ID()))}, true))
		require.ErrorIs(t, err, ErrDialRefused)
		require.Equal(t, Result{}, res)
	})

	t.Run("no addr", func(t *testing.T) {
		_, err := c.GetReachability(context.Background(), nil)
		require.Error(t, err)
	})

	t.Run("too many address", func(t *testing.T) {
		dialer := bhost.NewBlankHost(swarmt.GenSwarm(t, swarmt.OptDisableTCP))
		an := newAutoNAT(t, dialer, allowPrivateAddrs)
		defer an.Close()
		defer an.host.Close()

		var addrs []ma.Multiaddr
		for i := 0; i < 100; i++ {
			addrs = append(addrs, ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", 2000+i)))
		}
		addrs = append(addrs, c.host.Addrs()...)
		// The dial should still fail because we have too many addresses that the server cannot dial
		idAndWait(t, c, an)

		res, err := c.GetReachability(context.Background(), newTestRequests(addrs, true))
		require.ErrorIs(t, err, ErrDialRefused)
		require.Equal(t, Result{}, res)
	})

	t.Run("msg too large", func(t *testing.T) {
		dialer := bhost.NewBlankHost(swarmt.GenSwarm(t, swarmt.OptDisableTCP))
		an := newAutoNAT(t, dialer, allowPrivateAddrs)
		defer an.Close()
		defer an.host.Close()

		var addrs []ma.Multiaddr
		for i := 0; i < 10000; i++ {
			addrs = append(addrs, ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", 2000+i)))
		}
		addrs = append(addrs, c.host.Addrs()...)
		// The dial should still fail because we have too many addresses that the server cannot dial
		idAndWait(t, c, an)

		res, err := c.GetReachability(context.Background(), newTestRequests(addrs, true))
		require.ErrorIs(t, err, network.ErrReset)
		require.Equal(t, Result{}, res)
	})

}

func TestServerDataRequest(t *testing.T) {
	// server will skip all tcp addresses
	dialer := bhost.NewBlankHost(swarmt.GenSwarm(t, swarmt.OptDisableTCP))
	// ask for dial data for quic address
	an := newAutoNAT(t, dialer, allowPrivateAddrs, withDataRequestPolicy(
		func(s network.Stream, dialAddr ma.Multiaddr) bool {
			if _, err := dialAddr.ValueForProtocol(ma.P_QUIC_V1); err == nil {
				return true
			}
			return false
		}),
		WithServerRateLimit(10, 10, 10),
		withAmplificationAttackPreventionDialWait(0),
	)
	defer an.Close()
	defer an.host.Close()

	c := newAutoNAT(t, nil, allowPrivateAddrs)
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

	_, err := c.GetReachability(context.Background(), []Request{{Addr: tcpAddr, SendDialData: true}, {Addr: quicAddr}})
	require.Error(t, err)

	res, err := c.GetReachability(context.Background(), []Request{{Addr: quicAddr, SendDialData: true}, {Addr: tcpAddr}})
	require.NoError(t, err)

	require.Equal(t, Result{
		Addr:         quicAddr,
		Reachability: network.ReachabilityPublic,
		Status:       pb.DialStatus_OK,
	}, res)

	// Small messages should be rejected for dial data
	c.cli.dialData = c.cli.dialData[:10]
	_, err = c.GetReachability(context.Background(), []Request{{Addr: quicAddr, SendDialData: true}, {Addr: tcpAddr}})
	require.Error(t, err)
}
func TestServerDataRequestJitter(t *testing.T) {
	// server will skip all tcp addresses
	dialer := bhost.NewBlankHost(swarmt.GenSwarm(t, swarmt.OptDisableTCP))
	// ask for dial data for quic address
	an := newAutoNAT(t, dialer, allowPrivateAddrs, withDataRequestPolicy(
		func(s network.Stream, dialAddr ma.Multiaddr) bool {
			if _, err := dialAddr.ValueForProtocol(ma.P_QUIC_V1); err == nil {
				return true
			}
			return false
		}),
		WithServerRateLimit(10, 10, 10),
		withAmplificationAttackPreventionDialWait(5*time.Second),
	)
	defer an.Close()
	defer an.host.Close()

	c := newAutoNAT(t, nil, allowPrivateAddrs)
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

	for i := 0; i < 10; i++ {
		st := time.Now()
		res, err := c.GetReachability(context.Background(), []Request{{Addr: quicAddr, SendDialData: true}, {Addr: tcpAddr}})
		took := time.Since(st)
		require.NoError(t, err)

		require.Equal(t, Result{
			Addr:         quicAddr,
			Reachability: network.ReachabilityPublic,
			Status:       pb.DialStatus_OK,
		}, res)
		if took > 500*time.Millisecond {
			return
		}
	}
	t.Fatalf("expected server to delay at least 1 dial")
}

func TestServerDial(t *testing.T) {
	an := newAutoNAT(t, nil, WithServerRateLimit(10, 10, 10), allowPrivateAddrs)
	defer an.Close()
	defer an.host.Close()

	c := newAutoNAT(t, nil, allowPrivateAddrs)
	defer c.Close()
	defer c.host.Close()

	idAndWait(t, c, an)

	unreachableAddr := ma.StringCast("/ip4/1.2.3.4/tcp/2")
	hostAddrs := c.host.Addrs()

	t.Run("unreachable addr", func(t *testing.T) {
		res, err := c.GetReachability(context.Background(),
			append([]Request{{Addr: unreachableAddr, SendDialData: true}}, newTestRequests(hostAddrs, false)...))
		require.NoError(t, err)
		require.Equal(t, Result{
			Addr:         unreachableAddr,
			Reachability: network.ReachabilityPrivate,
			Status:       pb.DialStatus_E_DIAL_ERROR,
		}, res)
	})

	t.Run("reachable addr", func(t *testing.T) {
		res, err := c.GetReachability(context.Background(), newTestRequests(c.host.Addrs(), false))
		require.NoError(t, err)
		require.Equal(t, Result{
			Addr:         hostAddrs[0],
			Reachability: network.ReachabilityPublic,
			Status:       pb.DialStatus_OK,
		}, res)
		for _, addr := range c.host.Addrs() {
			res, err := c.GetReachability(context.Background(), newTestRequests([]ma.Multiaddr{addr}, false))
			require.NoError(t, err)
			require.Equal(t, Result{
				Addr:         addr,
				Reachability: network.ReachabilityPublic,
				Status:       pb.DialStatus_OK,
			}, res)
		}
	})

	t.Run("dialback error", func(t *testing.T) {
		c.host.RemoveStreamHandler(DialBackProtocol)
		res, err := c.GetReachability(context.Background(), newTestRequests(c.host.Addrs(), false))
		require.NoError(t, err)
		require.Equal(t, Result{
			Addr:         hostAddrs[0],
			Reachability: network.ReachabilityUnknown,
			Status:       pb.DialStatus_E_DIAL_BACK_ERROR,
		}, res)
	})
}

func TestRateLimiter(t *testing.T) {
	cl := test.NewMockClock()
	r := rateLimiter{RPM: 3, PerPeerRPM: 2, DialDataRPM: 1, now: cl.Now}

	require.True(t, r.Accept("peer1"))

	cl.AdvanceBy(10 * time.Second)
	require.False(t, r.Accept("peer1")) // first request is still active
	r.CompleteRequest("peer1")

	require.True(t, r.Accept("peer1"))
	r.CompleteRequest("peer1")

	cl.AdvanceBy(10 * time.Second)
	require.False(t, r.Accept("peer1"))

	cl.AdvanceBy(10 * time.Second)
	require.True(t, r.Accept("peer2"))
	r.CompleteRequest("peer2")

	cl.AdvanceBy(10 * time.Second)
	require.False(t, r.Accept("peer3"))

	cl.AdvanceBy(21 * time.Second) // first request expired
	require.True(t, r.Accept("peer1"))
	r.CompleteRequest("peer1")

	cl.AdvanceBy(10 * time.Second)
	require.True(t, r.Accept("peer3"))
	r.CompleteRequest("peer3")

	cl.AdvanceBy(50 * time.Second)
	require.True(t, r.Accept("peer3"))
	r.CompleteRequest("peer3")

	cl.AdvanceBy(1 * time.Second)
	require.False(t, r.Accept("peer3"))

	cl.AdvanceBy(10 * time.Second)
	require.True(t, r.Accept("peer3"))
}

func TestRateLimiterStress(t *testing.T) {
	cl := test.NewMockClock()
	for i := 0; i < 10; i++ {
		r := rateLimiter{RPM: 20 + i, PerPeerRPM: 10 + i, DialDataRPM: i, now: cl.Now}

		peers := make([]peer.ID, 10+i)
		for i := 0; i < len(peers); i++ {
			peers[i] = peer.ID(fmt.Sprintf("peer-%d", i))
		}
		peerSuccesses := make([]atomic.Int64, len(peers))
		var success, dialDataSuccesses atomic.Int64
		var wg sync.WaitGroup
		for k := 0; k < 5; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 2*60; i++ {
					for j, p := range peers {
						if r.Accept(p) {
							success.Add(1)
							peerSuccesses[j].Add(1)
						}
						if r.AcceptDialDataRequest(p) {
							dialDataSuccesses.Add(1)
						}
						r.CompleteRequest(p)
					}
					cl.AdvanceBy(time.Second)
				}
			}()
		}
		wg.Wait()
		if int(success.Load()) > 10*r.RPM || int(success.Load()) < 9*r.RPM {
			t.Fatalf("invalid successes, %d, expected %d-%d", success.Load(), 9*r.RPM, 10*r.RPM)
		}
		if int(dialDataSuccesses.Load()) > 10*r.DialDataRPM || int(dialDataSuccesses.Load()) < 9*r.DialDataRPM {
			t.Fatalf("invalid dial data successes, %d expected %d-%d", dialDataSuccesses.Load(), 9*r.DialDataRPM, 10*r.DialDataRPM)
		}
		for i := range peerSuccesses {
			// We cannot check the lower bound because some peers would be hitting the global rpm limit
			if int(peerSuccesses[i].Load()) > 10*r.PerPeerRPM {
				t.Fatalf("too many per peer successes, PerPeerRPM=%d", r.PerPeerRPM)
			}
		}
		cl.AdvanceBy(1 * time.Minute)
		require.True(t, r.Accept(peers[0]))
		// Assert lengths to check that we are cleaning up correctly
		require.Equal(t, len(r.reqs), 1)
		require.Equal(t, len(r.peerReqs), 1)
		require.Equal(t, len(r.peerReqs[peers[0]]), 1)
		require.Equal(t, len(r.dialDataReqs), 0)
		require.Equal(t, len(r.ongoingReqs), 1)
	}
}

func TestReadDialData(t *testing.T) {
	for N := 30_000; N < 30_010; N++ {
		for msgSize := 100; msgSize < 256; msgSize++ {
			r, w := io.Pipe()
			msg := &pb.Message{}
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				mw := pbio.NewDelimitedWriter(w)
				err := sendDialData(make([]byte, msgSize), N, mw, msg)
				if err != nil {
					t.Error(err)
				}
				mw.Close()
			}()
			err := readDialData(N, r)
			require.NoError(t, err)
			wg.Wait()
		}

		for msgSize := 1000; msgSize < 1256; msgSize++ {
			r, w := io.Pipe()
			msg := &pb.Message{}
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				mw := pbio.NewDelimitedWriter(w)
				err := sendDialData(make([]byte, msgSize), N, mw, msg)
				if err != nil {
					t.Error(err)
				}
				mw.Close()
			}()
			err := readDialData(N, r)
			require.NoError(t, err)
			wg.Wait()
		}
	}
}

func FuzzServerDialRequest(f *testing.F) {
	a := newAutoNAT(f, nil, allowPrivateAddrs, WithServerRateLimit(math.MaxInt32, math.MaxInt32, math.MaxInt32))
	c := newAutoNAT(f, nil)
	idAndWait(f, c, a)
	// reduce the streamTimeout before running this. TODO: fix this
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := c.host.NewStream(context.Background(), a.host.ID(), DialProtocol)
		if err != nil {
			t.Fatal(err)
		}
		s.SetDeadline(time.Now().Add(10 * time.Second))
		s.Write(data)
		buf := make([]byte, 64)
		s.Read(buf) // We only care that server didn't panic
		s, err = c.host.NewStream(context.Background(), a.host.ID(), DialProtocol)
		if err != nil {
			t.Fatal(err)
		}

		n := varint.PutUvarint(buf, uint64(len(data)))
		s.SetDeadline(time.Now().Add(10 * time.Second))
		s.Write(buf[:n])
		s.Write(data)
		s.Read(buf) // We only care that server didn't panic
		s.Reset()
	})
}

func FuzzReadDialData(f *testing.F) {
	f.Fuzz(func(t *testing.T, numBytes int, data []byte) {
		readDialData(numBytes, bytes.NewReader(data))
	})
}

func BenchmarkDialData(b *testing.B) {
	b.ReportAllocs()
	const N = 100_000
	streamBuffer := make([]byte, 2*N)
	buf := bytes.NewBuffer(streamBuffer[:0])
	dialData := make([]byte, 4000)
	msg := &pb.Message{}
	w := pbio.NewDelimitedWriter(buf)
	err := sendDialData(dialData, N, w, msg)
	require.NoError(b, err)
	dialDataBuf := buf.Bytes()
	for i := 0; i < b.N; i++ {
		err = readDialData(N, bytes.NewReader(dialDataBuf))
		require.NoError(b, err)
	}
}
