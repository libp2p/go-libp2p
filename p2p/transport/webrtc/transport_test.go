package libp2pwebrtc

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	manet "github.com/multiformats/go-multiaddr/net"

	quicproxy "github.com/quic-go/quic-go/integrationtests/tools/proxy"

	"golang.org/x/crypto/sha3"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getTransport(t *testing.T, opts ...Option) (*WebRTCTransport, peer.ID) {
	t.Helper()
	privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	require.NoError(t, err)
	rcmgr := &network.NullResourceManager{}
	transport, err := New(privKey, nil, nil, rcmgr, opts...)
	require.NoError(t, err)
	peerID, err := peer.IDFromPrivateKey(privKey)
	require.NoError(t, err)
	t.Cleanup(func() { rcmgr.Close() })
	return transport, peerID
}

func TestTransportWebRTC_CanDial(t *testing.T) {
	tr, _ := getTransport(t)
	invalid := []string{
		"/ip4/1.2.3.4/udp/1234/webrtc-direct",
		"/dns/test.test/udp/1234/webrtc-direct",
	}

	valid := []string{
		"/ip4/1.2.3.4/udp/1234/webrtc-direct/certhash/uEiAsGPzpiPGQzSlVHRXrUCT5EkTV7YFrV4VZ3hpEKTd_zg",
		"/ip6/0:0:0:0:0:0:0:1/udp/1234/webrtc-direct/certhash/uEiAsGPzpiPGQzSlVHRXrUCT5EkTV7YFrV4VZ3hpEKTd_zg",
		"/ip6/::1/udp/1234/webrtc-direct/certhash/uEiAsGPzpiPGQzSlVHRXrUCT5EkTV7YFrV4VZ3hpEKTd_zg",
		"/dns/test.test/udp/1234/webrtc-direct/certhash/uEiAsGPzpiPGQzSlVHRXrUCT5EkTV7YFrV4VZ3hpEKTd_zg",
	}

	for _, addr := range invalid {
		a := ma.StringCast(addr)
		require.Equal(t, false, tr.CanDial(a))
	}

	for _, addr := range valid {
		a := ma.StringCast(addr)
		require.Equal(t, true, tr.CanDial(a), addr)
	}
}

func TestTransportWebRTC_ListenFailsOnNonWebRTCMultiaddr(t *testing.T) {
	tr, _ := getTransport(t)
	testAddrs := []string{
		"/ip4/0.0.0.0/udp/0",
		"/ip4/0.0.0.0/tcp/0/wss",
	}
	for _, addr := range testAddrs {
		listenMultiaddr, err := ma.NewMultiaddr(addr)
		require.NoError(t, err)
		listener, err := tr.Listen(listenMultiaddr)
		require.Error(t, err)
		require.Nil(t, listener)
	}
}

// using assert inside goroutines, refer: https://github.com/stretchr/testify/issues/772#issuecomment-945166599
func TestTransportWebRTC_DialFailsOnUnsupportedHashFunction(t *testing.T) {
	tr, _ := getTransport(t)
	hash := sha3.New512()
	certhash := func() string {
		_, err := hash.Write([]byte("test-data"))
		require.NoError(t, err)
		mh, err := multihash.Encode(hash.Sum([]byte{}), multihash.SHA3_512)
		require.NoError(t, err)
		certhash, err := multibase.Encode(multibase.Base58BTC, mh)
		require.NoError(t, err)
		return certhash
	}()
	testaddr, err := ma.NewMultiaddr("/ip4/1.2.3.4/udp/1234/webrtc-direct/certhash/" + certhash)
	require.NoError(t, err)
	_, err = tr.Dial(context.Background(), testaddr, "")
	require.ErrorContains(t, err, "unsupported hash function")
}

func TestTransportWebRTC_CanListenSingle(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	tr1, connectingPeer := getTransport(t)
	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")

	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		_, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
		assert.NoError(t, err)
		close(done)
	}()

	conn, err := listener.Accept()
	require.NoError(t, err)
	require.NotNil(t, conn)

	require.Equal(t, connectingPeer, conn.RemotePeer())
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.FailNow()
	}
}

// WithListenerMaxInFlightConnections sets the maximum number of connections that are in-flight, i.e
// they are being negotiated, or are waiting to be accepted.
func WithListenerMaxInFlightConnections(m uint32) Option {
	return func(t *WebRTCTransport) error {
		if m == 0 {
			t.maxInFlightConnections = DefaultMaxInFlightConnections
		} else {
			t.maxInFlightConnections = m
		}
		return nil
	}
}

func TestTransportWebRTC_CanListenMultiple(t *testing.T) {
	count := 3
	tr, listeningPeer := getTransport(t, WithListenerMaxInFlightConnections(uint32(count)))

	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	go func() {
		for i := 0; i < count; i++ {
			conn, err := listener.Accept()
			assert.NoError(t, err)
			assert.NotNil(t, conn)
		}
		wg.Wait()
		cancel()
	}()

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctr, _ := getTransport(t)
			conn, err := ctr.Dial(ctx, listener.Multiaddr(), listeningPeer)
			select {
			case <-ctx.Done():
			default:
				assert.NoError(t, err)
				assert.NotNil(t, conn)
			}
		}()
	}

	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out")
	}

}

func TestTransportWebRTC_CanCreateSuccessiveConnections(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)
	count := 2

	go func() {
		for i := 0; i < count; i++ {
			ctr, _ := getTransport(t)
			conn, err := ctr.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
			require.NoError(t, err)
			require.Equal(t, conn.RemotePeer(), listeningPeer)
		}
	}()

	for i := 0; i < count; i++ {
		_, err := listener.Accept()
		require.NoError(t, err)
	}
}

func TestTransportWebRTC_ListenerCanCreateStreams(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	tr1, connectingPeer := getTransport(t)
	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	streamChan := make(chan network.MuxedStream)
	go func() {
		conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
		require.NoError(t, err)
		t.Logf("connection opened by dialer")
		stream, err := conn.AcceptStream()
		require.NoError(t, err)
		t.Logf("dialer accepted stream")
		streamChan <- stream
	}()

	conn, err := listener.Accept()
	require.NoError(t, err)
	t.Logf("listener accepted connection")
	require.Equal(t, connectingPeer, conn.RemotePeer())

	stream, err := conn.OpenStream(context.Background())
	require.NoError(t, err)
	t.Logf("listener opened stream")
	_, err = stream.Write([]byte("test"))
	require.NoError(t, err)

	var str network.MuxedStream
	select {
	case str = <-streamChan:
	case <-time.After(3 * time.Second):
		t.Fatal("stream opening timed out")
	}
	buf := make([]byte, 100)
	stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := str.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "test", string(buf[:n]))

}

func TestTransportWebRTC_DialerCanCreateStreams(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	tr1, connectingPeer := getTransport(t)
	done := make(chan struct{})

	go func() {
		lconn, err := listener.Accept()
		require.NoError(t, err)
		require.Equal(t, connectingPeer, lconn.RemotePeer())

		stream, err := lconn.AcceptStream()
		require.NoError(t, err)
		buf := make([]byte, 100)
		n, err := stream.Read(buf)
		require.NoError(t, err)
		require.Equal(t, "test", string(buf[:n]))

		done <- struct{}{}
	}()

	go func() {
		conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
		require.NoError(t, err)
		t.Logf("dialer opened connection")
		stream, err := conn.OpenStream(context.Background())
		require.NoError(t, err)
		t.Logf("dialer opened stream")
		_, err = stream.Write([]byte("test"))
		require.NoError(t, err)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}

}

func TestTransportWebRTC_DialerCanCreateStreamsMultiple(t *testing.T) {
	count := 5
	tr, listeningPeer := getTransport(t)
	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	tr1, connectingPeer := getTransport(t)
	done := make(chan struct{})

	go func() {
		lconn, err := listener.Accept()
		require.NoError(t, err)
		require.Equal(t, connectingPeer, lconn.RemotePeer())
		var wg sync.WaitGroup

		for i := 0; i < count; i++ {
			stream, err := lconn.AcceptStream()
			require.NoError(t, err)
			wg.Add(1)
			go func() {
				defer wg.Done()
				buf := make([]byte, 100)
				n, err := stream.Read(buf)
				require.NoError(t, err)
				require.Equal(t, "test", string(buf[:n]))
				_, err = stream.Write([]byte("test"))
				require.NoError(t, err)
			}()
		}

		wg.Wait()
		done <- struct{}{}
	}()

	conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
	require.NoError(t, err)
	t.Logf("dialer opened connection")

	for i := 0; i < count; i++ {
		idx := i
		go func() {
			stream, err := conn.OpenStream(context.Background())
			require.NoError(t, err)
			t.Logf("dialer opened stream: %d", idx)
			buf := make([]byte, 100)
			_, err = stream.Write([]byte("test"))
			require.NoError(t, err)
			n, err := stream.Read(buf)
			require.NoError(t, err)
			require.Equal(t, "test", string(buf[:n]))
		}()
		if i%10 == 0 && i > 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	select {
	case <-done:
	case <-time.After(100 * time.Second):
		t.Fatal("timed out")
	}
}

func TestTransportWebRTC_Deadline(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)
	tr1, connectingPeer := getTransport(t)

	t.Run("SetReadDeadline", func(t *testing.T) {
		go func() {
			lconn, err := listener.Accept()
			require.NoError(t, err)
			require.Equal(t, connectingPeer, lconn.RemotePeer())
			_, err = lconn.AcceptStream()
			require.NoError(t, err)
		}()

		conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
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
			require.NoError(t, err)
			require.Equal(t, connectingPeer, lconn.RemotePeer())
			_, err = lconn.AcceptStream()
			require.NoError(t, err)
		}()

		conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
		require.NoError(t, err)
		stream, err := conn.OpenStream(context.Background())
		require.NoError(t, err)

		stream.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		largeBuffer := make([]byte, 2*1024*1024)
		_, err = stream.Write(largeBuffer)
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)

		stream.SetWriteDeadline(time.Now().Add(-200 * time.Millisecond))
		smallBuffer := make([]byte, 1024)
		_, err = stream.Write(smallBuffer)
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)

	})
}

func TestTransportWebRTC_StreamWriteBufferContention(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	tr1, connectingPeer := getTransport(t)

	for i := 0; i < 2; i++ {
		go func() {
			lconn, err := listener.Accept()
			require.NoError(t, err)
			require.Equal(t, connectingPeer, lconn.RemotePeer())
			_, err = lconn.AcceptStream()
			require.NoError(t, err)
		}()

	}

	conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
	require.NoError(t, err)

	errC := make(chan error)
	// writers
	for i := 0; i < 2; i++ {
		go func() {
			stream, err := conn.OpenStream(context.Background())
			require.NoError(t, err)

			stream.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
			largeBuffer := make([]byte, 2*1024*1024)
			_, err = stream.Write(largeBuffer)
			errC <- err
		}()
	}

	require.ErrorIs(t, <-errC, os.ErrDeadlineExceeded)
	require.ErrorIs(t, <-errC, os.ErrDeadlineExceeded)
}

func TestTransportWebRTC_RemoteReadsAfterClose(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	tr1, _ := getTransport(t)

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

	conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
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

func TestTransportWebRTC_RemoteReadsAfterClose2(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	tr1, _ := getTransport(t)

	awaitStreamClosure := make(chan struct{})
	readBytesResult := make(chan int)
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

		<-awaitStreamClosure
		buf := make([]byte, 16)
		n, err := stream.Read(buf)
		if err != nil {
			done <- err
			return
		}
		readBytesResult <- n
		close(done)
	}()

	conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
	require.NoError(t, err)
	// create a stream
	stream, err := conn.OpenStream(context.Background())
	require.NoError(t, err)
	_, err = stream.Write([]byte{1, 2, 3, 4})
	require.NoError(t, err)
	err = stream.Close()
	require.NoError(t, err)
	// signal stream closure
	close(awaitStreamClosure)
	require.Equal(t, <-readBytesResult, 4)
}

func TestTransportWebRTC_Close(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	tr1, connectingPeer := getTransport(t)

	t.Run("RemoteClosesStream", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			lconn, err := listener.Accept()
			require.NoError(t, err)
			require.Equal(t, connectingPeer, lconn.RemotePeer())
			stream, err := lconn.AcceptStream()
			require.NoError(t, err)
			time.Sleep(100 * time.Millisecond)
			_ = stream.Close()

		}()

		buf := make([]byte, 2)

		conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
		require.NoError(t, err)
		stream, err := conn.OpenStream(context.Background())
		require.NoError(t, err)

		err = stream.SetReadDeadline(time.Now().Add(2 * time.Second))
		require.NoError(t, err)
		_, err = stream.Read(buf)
		require.ErrorIs(t, err, io.EOF)

		wg.Wait()
	})
}

func TestTransportWebRTC_PeerConnectionDTLSFailed(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	ln, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)
	defer ln.Close()

	encoded, err := hex.DecodeString("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	require.NoError(t, err)
	encodedCerthash, err := multihash.Encode(encoded, multihash.SHA2_256)
	require.NoError(t, err)
	badEncodedCerthash, err := multibase.Encode(multibase.Base58BTC, encodedCerthash)
	require.NoError(t, err)
	badCerthash, err := ma.NewMultiaddr(fmt.Sprintf("/certhash/%s", badEncodedCerthash))
	require.NoError(t, err)
	badMultiaddr, _ := ma.SplitFunc(ln.Multiaddr(), func(c ma.Component) bool { return c.Protocol().Code == ma.P_CERTHASH })
	badMultiaddr = badMultiaddr.Encapsulate(badCerthash)

	tr1, _ := getTransport(t)
	conn, err := tr1.Dial(context.Background(), badMultiaddr, listeningPeer)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed")
	require.Nil(t, conn)
}

func TestConnectionTimeoutOnListener(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	tr.peerConnectionTimeouts.Disconnect = 100 * time.Millisecond
	tr.peerConnectionTimeouts.Failed = 150 * time.Millisecond
	tr.peerConnectionTimeouts.Keepalive = 50 * time.Millisecond

	listenMultiaddr := ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct")
	ln, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)
	defer ln.Close()

	var drop atomic.Bool
	proxy, err := quicproxy.NewQuicProxy("127.0.0.1:0", &quicproxy.Opts{
		RemoteAddr: fmt.Sprintf("127.0.0.1:%d", ln.Addr().(*net.UDPAddr).Port),
		DropPacket: func(quicproxy.Direction, []byte) bool { return drop.Load() },
	})
	require.NoError(t, err)
	defer proxy.Close()

	tr1, connectingPeer := getTransport(t)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		addr, err := manet.FromNetAddr(proxy.LocalAddr())
		require.NoError(t, err)
		_, webrtcComponent := ma.SplitFunc(ln.Multiaddr(), func(c ma.Component) bool { return c.Protocol().Code == ma.P_WEBRTC_DIRECT })
		addr = addr.Encapsulate(webrtcComponent)
		conn, err := tr1.Dial(ctx, addr, listeningPeer)
		require.NoError(t, err)
		str, err := conn.OpenStream(ctx)
		require.NoError(t, err)
		str.Write([]byte("foobar"))
	}()

	conn, err := ln.Accept()
	require.NoError(t, err)
	require.Equal(t, connectingPeer, conn.RemotePeer())

	str, err := conn.AcceptStream()
	require.NoError(t, err)
	_, err = str.Write([]byte("test"))
	require.NoError(t, err)
	// start dropping all packets
	drop.Store(true)
	start := time.Now()
	// TODO: return timeout errors here
	for {
		if _, err := str.Write([]byte("test")); err != nil {
			require.True(t, os.IsTimeout(err))
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("timeout")
		}
		// make sure to not write too often, we don't want to fill the flow control window
		time.Sleep(5 * time.Millisecond)
	}
	// make sure that accepting a stream also returns an error...
	_, err = conn.AcceptStream()
	require.True(t, os.IsTimeout(err))
	// ... as well as opening a new stream
	_, err = conn.OpenStream(context.Background())
	require.True(t, os.IsTimeout(err))
}

func TestMaxInFlightRequests(t *testing.T) {
	const count = 3
	tr, listeningPeer := getTransport(t,
		WithListenerMaxInFlightConnections(count),
	)
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct"))
	require.NoError(t, err)
	defer ln.Close()

	var wg sync.WaitGroup
	var success, fails atomic.Int32
	for i := 0; i < count+1; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dialer, _ := getTransport(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := dialer.Dial(ctx, ln.Multiaddr(), listeningPeer); err == nil {
				success.Add(1)
			} else {
				t.Log("failed to dial:", err)
				fails.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, count, int(success.Load()), "expected exactly 3 dial successes")
	require.Equal(t, 1, int(fails.Load()), "expected exactly 1 dial failure")
}
