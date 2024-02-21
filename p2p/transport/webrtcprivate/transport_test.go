package libp2pwebrtcprivate

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// relayedHost is a webrtc enabled host with a relay reservation
type relayedHost struct {
	webrtcHost
	// R is the relay host
	R host.Host
	// Addr is the reachable /webrtc address
	Addr ma.Multiaddr
}

func (r *relayedHost) Close() {
	r.R.Close()
	r.webrtcHost.Close()
}

type webrtcHost struct {
	host.Host
	// T is the webrtc transport used by the host
	T *transport
}

func newWebRTCHost(t *testing.T) *webrtcHost {
	as := swarmt.GenSwarm(t)
	a := blankhost.NewBlankHost(as)
	upg := swarmt.GenUpgrader(t, as, nil)
	err := client.AddTransport(a, upg)
	require.NoError(t, err)
	ta, err := newTransport(a, nil, nil)
	require.NoError(t, err)
	return &webrtcHost{
		Host: a,
		T:    ta,
	}
}

func newRelayedHost(t *testing.T) *relayedHost {
	rh := blankhost.NewBlankHost(swarmt.GenSwarm(t))
	rr := relay.DefaultResources()
	rr.MaxCircuits = 100
	_, err := relay.New(rh, relay.WithResources(rr))
	require.NoError(t, err)

	ps := swarmt.GenSwarm(t)
	p := blankhost.NewBlankHost(ps)
	upg := swarmt.GenUpgrader(t, ps, nil)
	client.AddTransport(p, upg)
	_, err = client.Reserve(context.Background(), p, peer.AddrInfo{ID: rh.ID(), Addrs: rh.Addrs()})
	require.NoError(t, err)
	tp, err := newTransport(p, nil, nil)
	require.NoError(t, err)
	return &relayedHost{
		webrtcHost: webrtcHost{
			Host: p,
			T:    tp,
		},
		R:    rh,
		Addr: ma.StringCast(fmt.Sprintf("%s/p2p/%s/p2p-circuit/webrtc/", rh.Addrs()[0], rh.ID())),
	}
}

func TestSingleDial(t *testing.T) {
	a := newWebRTCHost(t)
	b := newRelayedHost(t)
	defer b.Close()
	defer a.Close()

	l, err := b.T.Listen(ma.StringCast("/webrtc"))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ca, err := a.T.Dial(ctx, b.Addr, b.ID())
	require.NoError(t, err)

	cb, err := l.Accept()
	require.NoError(t, err)
	sa, err := ca.OpenStream(ctx)
	require.NoError(t, err)
	sb, err := cb.AcceptStream()
	require.NoError(t, err)
	sa.Write([]byte("hello world"))
	recv := make([]byte, 24)
	n, err := sb.Read(recv)
	require.NoError(t, err)
	require.Equal(t, "hello world", string(recv[:n]))

	ca.Close()
	cb.Close()
}

func TestConnectionProperties(t *testing.T) {
	a := newWebRTCHost(t)
	b := newRelayedHost(t)
	defer b.Close()
	defer a.Close()

	l, err := b.T.Listen(ma.StringCast("/webrtc"))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ca, err := a.T.Dial(ctx, b.Addr, b.ID())
	require.NoError(t, err)

	cb, err := l.Accept()
	require.NoError(t, err)

	t.Run("Addresses", func(t *testing.T) {
		testAddr := func(addr ma.Multiaddr) {
			_, err := addr.ValueForProtocol(ma.P_UDP)
			require.NoError(t, err)
			_, err = addr.ValueForProtocol(ma.P_WEBRTC)
			require.NoError(t, err)
		}
		testAddr(ca.LocalMultiaddr())
		testAddr(ca.RemoteMultiaddr())
		testAddr(cb.LocalMultiaddr())
		testAddr(cb.RemoteMultiaddr())
	})

	t.Run("ConnectionState", func(t *testing.T) {
		require.Equal(t, network.ConnectionState{Transport: "webrtc"}, ca.ConnState())
		require.Equal(t, network.ConnectionState{Transport: "webrtc"}, cb.ConnState())
	})

}

func TestMultipleDials(t *testing.T) {
	a := newWebRTCHost(t)
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			b := newRelayedHost(t)
			defer b.Close()

			l, err := b.T.Listen(ma.StringCast("/webrtc"))
			if !assert.NoError(t, err) {
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ca, err := a.T.Dial(ctx, b.Addr, b.ID())
			if !assert.NoError(t, err) {
				return
			}

			cb, err := l.Accept()
			if !assert.NoError(t, err) {
				return
			}

			sa, err := ca.OpenStream(ctx)
			if !assert.NoError(t, err) {
				return
			}
			sb, err := cb.AcceptStream()
			if !assert.NoError(t, err) {
				return
			}
			sa.Write([]byte("hello world"))
			recv := make([]byte, 24)
			n, err := sb.Read(recv)
			if !assert.NoError(t, err) {
				return
			}
			if !assert.Equal(t, "hello world", string(recv[:n])) {
				return
			}
			wg.Done()
		}()
	}
	wg.Wait()
}

func TestMultipleDialsAndListeners(t *testing.T) {
	const N = 5
	var hosts []*relayedHost
	for i := 0; i < N; i++ {
		hosts = append(hosts, newRelayedHost(t))
		l, err := hosts[i].T.Listen(ma.StringCast("/webrtc"))
		require.NoError(t, err)
		defer hosts[i].Close()
		defer l.Close()
	}
	var wg sync.WaitGroup

	dialAndPing := func(h *relayedHost, raddr ma.Multiaddr, p peer.ID) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ca, err := h.T.Dial(ctx, raddr, p)
		if !assert.NoError(t, err) {
			return
		}
		defer ca.Close()
		sa, err := ca.OpenStream(ctx)
		if !assert.NoError(t, err) {
			return
		}
		defer sa.Close()
		sa.Write([]byte("hello world"))
		recv := make([]byte, 24)
		n, err := sa.Read(recv)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.Equal(t, "hello world", string(recv[:n])) {
			return
		}
	}

	acceptAndPong := func(r *relayedHost) {
		cb, err := r.T.listener.Accept()
		if !assert.NoError(t, err) {
			return
		}

		sb, err := cb.AcceptStream()
		if !assert.NoError(t, err) {
			return
		}
		defer sb.Close()

		recv := make([]byte, 24)
		n, err := sb.Read(recv)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.Equal(t, "hello world", string(recv[:n])) {
			return
		}
		sb.Write(recv[:n])
	}

	for i := 0; i < N; i++ {
		for j := i + 1; j < N; j++ {
			wg.Add(1)
			go func(i, j int) {
				go acceptAndPong(hosts[j])
				dialAndPing(hosts[i], hosts[j].Addr, hosts[j].ID())
				wg.Done()
			}(i, j)
		}
	}
	wg.Wait()
}

func TestDialerCanCreateStreams(t *testing.T) {
	a := newWebRTCHost(t)
	b := newRelayedHost(t)
	listener, err := b.T.Listen(ma.StringCast("/webrtc"))
	require.NoError(t, err)

	aC := make(chan bool)
	go func() {
		defer close(aC)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := a.T.Dial(ctx, b.Addr, b.ID())
		if !assert.NoError(t, err) {
			return
		}
		s, err := conn.AcceptStream()
		if !assert.NoError(t, err) {
			return
		}
		recv := make([]byte, 24)
		n, err := s.Read(recv)
		if !assert.NoError(t, err) {
			return
		}
		_, err = s.Write(recv[:n])
		if !assert.NoError(t, err) {
			return
		}
		s.Close()
	}()

	bC := make(chan bool)
	go func() {
		defer close(bC)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := listener.Accept()
		if !assert.NoError(t, err) {
			return
		}
		s, err := conn.OpenStream(ctx)
		if !assert.NoError(t, err) {
			return
		}
		defer s.Close()

		_, err = s.Write([]byte("hello world"))
		if !assert.NoError(t, err) {
			return
		}

		recv := make([]byte, 24)
		n, err := s.Read(recv)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.Equal(t, "hello world", string(recv[:n])) {
			return
		}
	}()

	select {
	case <-aC:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout")
	}
	select {
	case <-bC:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout")
	}
}

func TestDialerCanCreateStreamsMultiple(t *testing.T) {
	count := 5
	a := newWebRTCHost(t)
	b := newRelayedHost(t)
	listener, err := b.T.Listen(WebRTCAddr)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		lconn, err := listener.Accept()
		if !assert.NoError(t, err) {
			return
		}
		if !assert.Equal(t, a.ID(), lconn.RemotePeer()) {
			return
		}
		var wg sync.WaitGroup

		for i := 0; i < count; i++ {
			stream, err := lconn.AcceptStream()
			if !assert.NoError(t, err) {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				buf := make([]byte, 100)
				n, err := stream.Read(buf)
				if !assert.NoError(t, err) {
					return
				}
				if !assert.Equal(t, "test", string(buf[:n])) {
					return
				}
				_, err = stream.Write([]byte("test"))
				if !assert.NoError(t, err) {
					return
				}
			}()
		}

		wg.Wait()
		done <- struct{}{}
	}()

	conn, err := a.T.Dial(context.Background(), b.Addr, b.ID())
	require.NoError(t, err)

	for i := 0; i < count; i++ {
		idx := i
		go func() {
			stream, err := conn.OpenStream(context.Background())
			if !assert.NoError(t, err) {
				return
			}
			t.Logf("dialer opened stream: %d", idx)
			buf := make([]byte, 100)
			_, err = stream.Write([]byte("test"))
			if !assert.NoError(t, err) {
				return
			}
			n, err := stream.Read(buf)
			if !assert.NoError(t, err) {
				return
			}
			if !assert.Equal(t, "test", string(buf[:n])) {
				return
			}
		}()
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("timed out")
	}
}

func TestMaxInflightQueue(t *testing.T) {
	b := newRelayedHost(t)
	defer b.Close()
	count := 3
	b.T.maxInFlightConnections = count
	listener, err := b.T.Listen(WebRTCAddr)
	require.NoError(t, err)
	defer listener.Close()

	var success, failure atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < count+1; i++ {
		wg.Add(1)
		go func() {
			a := newWebRTCHost(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := a.T.Dial(ctx, b.Addr, b.ID())
			if err == nil {
				success.Add(1)
			} else {
				failure.Add(1)
			}
			wg.Done()
		}()
	}
	wg.Wait()
	require.Equal(t, 1, int(failure.Load()))
	require.Equal(t, count, int(success.Load()))
}

func TestRemoteReadsAfterClose(t *testing.T) {
	b := newRelayedHost(t)
	listener, err := b.T.Listen(WebRTCAddr)
	require.NoError(t, err)

	a := newWebRTCHost(t)

	done := make(chan error)
	go func() {
		lconn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		stream, err := lconn.AcceptStream()
		if err != nil {
			done <- err
			return
		}
		_, err = stream.Write([]byte{1, 2, 3, 4})
		if err != nil {
			done <- err
			return
		}
		err = stream.Close()
		if err != nil {
			done <- err
			return
		}
		close(done)
	}()

	conn, err := a.T.Dial(context.Background(), b.Addr, b.ID())
	require.NoError(t, err)
	// create a stream
	stream, err := conn.OpenStream(context.Background())

	require.NoError(t, err)
	// require write and close to complete
	require.NoError(t, <-done)

	stream.SetReadDeadline(time.Now().Add(5 * time.Second))

	buf := make([]byte, 10)
	n, err := stream.Read(buf)
	require.NoError(t, err)
	require.Equal(t, n, 4)
}

func TestStreamDeadline(t *testing.T) {
	b := newRelayedHost(t)
	listener, err := b.T.Listen(WebRTCAddr)
	require.NoError(t, err)
	a := newWebRTCHost(t)

	t.Run("SetReadDeadline", func(t *testing.T) {
		go func() {
			lconn, err := listener.Accept()
			if !assert.NoError(t, err) {
				return
			}
			_, err = lconn.AcceptStream()
			if !assert.NoError(t, err) {
				return
			}
		}()

		conn, err := a.T.Dial(context.Background(), b.Addr, b.ID())
		require.NoError(t, err)
		stream, err := conn.OpenStream(context.Background())
		require.NoError(t, err)

		// deadline set to the past
		stream.SetReadDeadline(time.Now().Add(-200 * time.Millisecond))
		_, err = stream.Read([]byte{0, 0})
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)

		// future deadline exceeded
		stream.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err = stream.Read([]byte{0, 0})
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	})

	t.Run("SetWriteDeadline", func(t *testing.T) {
		go func() {
			lconn, err := listener.Accept()
			if !assert.NoError(t, err) {
				return
			}
			_, err = lconn.AcceptStream()
			if !assert.NoError(t, err) {
				return
			}
		}()

		conn, err := a.T.Dial(context.Background(), b.Addr, b.ID())
		require.NoError(t, err)
		stream, err := conn.OpenStream(context.Background())
		require.NoError(t, err)

		stream.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
		time.Sleep(201 * time.Millisecond)
		largeBuffer := make([]byte, 2*1024*1024)
		_, err = stream.Write(largeBuffer)
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	})
}

func TestCanDial(t *testing.T) {
	a := newWebRTCHost(t)
	defer a.Close()
	b := newWebRTCHost(t)

	tests := []struct {
		addr    ma.Multiaddr
		canDial bool
	}{
		{
			addr:    ma.StringCast(fmt.Sprintf("/ip4/1.2.3.4/tcp/1234/p2p/%s/p2p-circuit/", b.ID())),
			canDial: false,
		},
		{
			addr:    ma.StringCast(fmt.Sprintf("/ip4/1.2.3.4/tcp/1234/p2p/%s/p2p-circuit/webrtc", b.ID())),
			canDial: true,
		},
		{
			addr:    ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/udp/1234/quic-v1/p2p/%s/p2p-circuit/", b.ID())),
			canDial: false,
		},
		{
			addr:    ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/udp/1234/quic-v1/p2p/%s/p2p-circuit/webrtc/", b.ID())),
			canDial: true,
		},
		{
			addr:    ma.StringCast("/ip4/1.2.3.4/tcp/1234/webrtc"),
			canDial: false,
		},
		{
			addr:    ma.StringCast(fmt.Sprintf("/ip4/1.2.3.4/tcp/1234/p2p/%s/webrtc/", b.ID())),
			canDial: false,
		},
		{
			addr:    ma.StringCast("/ip4/1.2.3.4/tcp/1234/"),
			canDial: false,
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			require.Equal(t, tt.canDial, a.T.CanDial(tt.addr), "args: %s", tt.addr)
		})
	}
}

func TestCanListenTwice(t *testing.T) {
	b := newRelayedHost(t)
	defer b.Close()
	listener, err := b.T.Listen(WebRTCAddr)
	require.NoError(t, err)
	a := newWebRTCHost(t)
	defer a.Close()

	ca, err := a.T.Dial(context.Background(), b.Addr, b.ID())
	require.NoError(t, err)
	cb, err := listener.Accept()
	require.NoError(t, err)
	ca.Close()
	cb.Close()
	listener.Close()
	_, err = listener.Accept()
	require.Error(t, err)

	listener, err = b.T.Listen(WebRTCAddr)
	require.NoError(t, err)
	ca, err = a.T.Dial(context.Background(), b.Addr, b.ID())
	require.NoError(t, err)
	cb, err = listener.Accept()
	require.NoError(t, err)
	ca.Close()
	cb.Close()
}
