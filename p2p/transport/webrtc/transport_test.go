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

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ttransport "github.com/libp2p/go-libp2p/p2p/transport/testsuite"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/sha3"
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

var (
	listenerIp net.IP
	dialerIp   net.IP
)

func TestMain(m *testing.M) {
	listenerIp, dialerIp = getListenerAndDialerIP()
	os.Exit(m.Run())
}

func TestTransportWebRTC_CanDial(t *testing.T) {
	tr, _ := getTransport(t)
	invalid := []string{
		"/ip4/1.2.3.4/udp/1234/p2p-webrtc-direct",
		"/dns/test.test/udp/1234/p2p-webrtc-direct",
	}

	valid := []string{
		"/ip4/1.2.3.4/udp/1234/p2p-webrtc-direct/certhash/uEiAsGPzpiPGQzSlVHRXrUCT5EkTV7YFrV4VZ3hpEKTd_zg",
		"/ip6/0:0:0:0:0:0:0:1/udp/1234/p2p-webrtc-direct/certhash/uEiAsGPzpiPGQzSlVHRXrUCT5EkTV7YFrV4VZ3hpEKTd_zg",
		"/ip6/::1/udp/1234/p2p-webrtc-direct/certhash/uEiAsGPzpiPGQzSlVHRXrUCT5EkTV7YFrV4VZ3hpEKTd_zg",
		"/dns/test.test/udp/1234/p2p-webrtc-direct/certhash/uEiAsGPzpiPGQzSlVHRXrUCT5EkTV7YFrV4VZ3hpEKTd_zg",
	}

	for _, addr := range invalid {
		ma, err := multiaddr.NewMultiaddr(addr)
		require.NoError(t, err)
		require.Equal(t, false, tr.CanDial(ma))
	}

	for _, addr := range valid {
		ma, err := multiaddr.NewMultiaddr(addr)
		require.NoError(t, err)
		require.Equal(t, true, tr.CanDial(ma), addr)
	}
}

func TestTransportWebRTC_ListenFailsOnNonWebRTCMultiaddr(t *testing.T) {
	tr, _ := getTransport(t)
	testAddrs := []string{
		"/ip4/0.0.0.0/udp/0",
		"/ip4/0.0.0.0/tcp/0/wss",
	}
	for _, addr := range testAddrs {
		listenMultiaddr, err := multiaddr.NewMultiaddr(addr)
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
	testaddr, err := multiaddr.NewMultiaddr("/ip4/1.2.3.4/udp/1234/p2p-webrtc-direct/certhash/" + certhash)
	require.NoError(t, err)
	_, err = tr.Dial(context.Background(), testaddr, "")
	require.ErrorContains(t, err, "unsupported hash function")
}

func TestTransportWebRTC_CanListenSingle(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	tr1, connectingPeer := getTransport(t)
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)

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

	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
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
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
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
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
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
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
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
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
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
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
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
		require.ErrorIs(t, err, ErrTimeout)

		// future deadline exceeded
		stream.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err = stream.Read([]byte{0, 0})
		require.ErrorIs(t, err, ErrTimeout)
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

		stream.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
		largeBuffer := make([]byte, 2*1024*1024)
		_, err = stream.Write(largeBuffer)
		require.ErrorIs(t, err, ErrTimeout)
	})
}

func TestTransportWebRTC_StreamWriteBufferContention(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
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

	require.ErrorIs(t, <-errC, ErrTimeout)
	require.ErrorIs(t, <-errC, ErrTimeout)

}

func TestTransportWebRTC_Read(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	tr1, connectingPeer := getTransport(t)

	createListener := func() {
		lconn, err := listener.Accept()
		require.NoError(t, err)
		require.Equal(t, connectingPeer, lconn.RemotePeer())
		stream, err := lconn.AcceptStream()
		require.NoError(t, err)
		_, _ = stream.Write(make([]byte, 2*1024))
	}

	t.Run("read partial message", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			createListener()
		}()

		conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
		require.NoError(t, err)
		stream, err := conn.OpenStream(context.Background())
		require.NoError(t, err)

		buf := make([]byte, 10)
		stream.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := stream.Read(buf)
		require.NoError(t, err)
		require.Equal(t, n, 10)

		wg.Wait()
	})

	t.Run("read zero bytes", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			createListener()
		}()

		conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
		require.NoError(t, err)
		stream, err := conn.OpenStream(context.Background())
		require.NoError(t, err)

		stream.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := stream.Read([]byte{})
		require.NoError(t, err)
		require.Equal(t, n, 0)

		wg.Wait()
	})
}

func TestTransportWebRTC_RemoteReadsAfterClose(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
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
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
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
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	tr1, connectingPeer := getTransport(t)

	t.Run("StreamCanCloseWhenReadActive", func(t *testing.T) {
		done := make(chan struct{})

		go func() {
			lconn, err := listener.Accept()
			require.NoError(t, err)
			t.Logf("listener accepted connection")
			require.Equal(t, connectingPeer, lconn.RemotePeer())
			done <- struct{}{}
		}()

		conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
		require.NoError(t, err)
		t.Logf("dialer opened connection")
		stream, err := conn.OpenStream(context.Background())
		require.NoError(t, err)

		time.AfterFunc(100*time.Millisecond, func() {
			err := stream.Close()
			require.NoError(t, err)
		})

		_, err = stream.Read(make([]byte, 19))
		require.ErrorIs(t, err, ErrTimeout)

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("timed out")
		}
	})

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

func TestTransportWebRTC_ReceiveFlagsAfterReadClosed(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	tr1, connectingPeer := getTransport(t)
	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		lconn, err := listener.Accept()
		require.NoError(t, err)
		t.Logf("listener accepted connection")
		require.Equal(t, connectingPeer, lconn.RemotePeer())
		stream, err := lconn.AcceptStream()
		require.NoError(t, err)
		n, err := stream.Read(make([]byte, 10))
		require.NoError(t, err)
		require.Equal(t, 10, n)
		// stop reader
		err = stream.Reset()
		require.NoError(t, err)
		done <- struct{}{}
	}()

	conn, err := tr1.Dial(context.Background(), listener.Multiaddr(), listeningPeer)
	require.NoError(t, err)
	t.Logf("dialer opened connection")
	stream, err := conn.OpenStream(context.Background())
	require.NoError(t, err)

	err = stream.CloseRead()
	require.NoError(t, err)
	_, err = stream.Read([]byte{0})
	require.ErrorIs(t, err, io.EOF)
	_, err = stream.Write(make([]byte, 10))
	require.NoError(t, err)
	<-done
	_, err = stream.Write(make([]byte, 2*1024*1024))
	// can be an error closed or timeout
	require.Error(t, err)

	wg.Wait()
}

func TestTransportWebRTC_PeerConnectionDTLSFailed(t *testing.T) {
	// test multihash
	encoded, err := hex.DecodeString("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	require.NoError(t, err)

	testMultihash := &multihash.DecodedMultihash{
		Code:   multihash.SHA2_256,
		Name:   multihash.Codes[multihash.SHA2_256],
		Digest: encoded,
		Length: len(encoded),
	}

	tr, listeningPeer := getTransport(t)
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	tr1, _ := getTransport(t)

	go listener.Accept()

	badMultiaddr, _ := multiaddr.SplitFunc(listener.Multiaddr(), func(component multiaddr.Component) bool {
		return component.Protocol().Code == multiaddr.P_CERTHASH
	})

	encodedCerthash, err := multihash.Encode(testMultihash.Digest, testMultihash.Code)
	require.NoError(t, err)
	badEncodedCerthash, err := multibase.Encode(multibase.Base58BTC, encodedCerthash)
	require.NoError(t, err)
	badCerthash, err := multiaddr.NewMultiaddr(fmt.Sprintf("/certhash/%s", badEncodedCerthash))
	require.NoError(t, err)
	badMultiaddr = badMultiaddr.Encapsulate(badCerthash)

	conn, err := tr1.Dial(context.Background(), badMultiaddr, listeningPeer)
	require.Nil(t, conn)
	t.Log(err)
	require.Error(t, err)

	require.ErrorContains(t, err, "failed")
}

func TestTransportWebRTC_StreamResetOnPeerConnectionFailure(t *testing.T) {
	tr, listeningPeer := getTransport(t)
	tr.peerConnectionTimeouts.Disconnect = 2 * time.Second
	tr.peerConnectionTimeouts.Failed = 3 * time.Second
	tr.peerConnectionTimeouts.Keepalive = time.Second

	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
	lsnr, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)

	tr1, connectingPeer := getTransport(t)
	tr1.peerConnectionTimeouts.Disconnect = 2 * time.Second
	tr1.peerConnectionTimeouts.Failed = 3 * time.Second
	tr1.peerConnectionTimeouts.Keepalive = time.Second

	done := make(chan struct{})
	go func() {
		lconn, err := lsnr.Accept()
		require.NoError(t, err)
		require.Equal(t, connectingPeer, lconn.RemotePeer())

		stream, err := lconn.AcceptStream()
		require.NoError(t, err)
		_, err = stream.Write([]byte("test"))
		require.NoError(t, err)
		// force close the mux
		lsnr.(*listener).mux.Close()
		// stream.Write can keep buffering data until failure,
		// so we need to loop on writing.
		for {
			_, err := stream.Write([]byte("test"))
			if err != nil {
				assert.ErrorIs(t, err, network.ErrReset)
				close(done)
				return
			}
		}
	}()

	dialctx, dialcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dialcancel()
	conn, err := tr1.Dial(dialctx, lsnr.Multiaddr(), listeningPeer)
	require.NoError(t, err)
	stream, err := conn.OpenStream(dialctx)
	require.NoError(t, err)
	_, err = io.ReadAll(stream)
	require.Error(t, err)

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out")
	}
}

func TestTransportWebRTC_MaxInFlightRequests(t *testing.T) {
	count := uint32(3)
	tr, listeningPeer := getTransport(t,
		WithListenerMaxInFlightConnections(count),
	)
	tr.peerConnectionTimeouts.Disconnect = 8 * time.Second
	tr.peerConnectionTimeouts.Failed = 10 * time.Second
	tr.peerConnectionTimeouts.Keepalive = 5 * time.Second
	listenMultiaddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp))
	require.NoError(t, err)
	listener, err := tr.Listen(listenMultiaddr)
	require.NoError(t, err)
	defer listener.Close()

	dialerCount := count + 2

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	start := make(chan struct{})
	ready := make(chan struct{}, dialerCount)
	var success uint32
	for i := 0; uint32(i) < dialerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dialer, _ := getTransport(t)
			dialer.peerConnectionTimeouts.Disconnect = 8 * time.Second
			dialer.peerConnectionTimeouts.Failed = 10 * time.Second
			dialer.peerConnectionTimeouts.Keepalive = 5 * time.Second
			ready <- struct{}{}
			<-start
			_, err := dialer.Dial(ctx, listener.Multiaddr(), listeningPeer)
			if err == nil {
				atomic.AddUint32(&success, 1)
			} else {
				t.Logf("dial error: %v", err)
			}
		}()
	}

	for i := 0; uint32(i) < dialerCount; i++ {
		<-ready
	}
	close(start)
	wg.Wait()
	successCount := atomic.LoadUint32(&success)
	require.Equal(t, count, successCount)
}

// TestWebrtcTransport implements the standard go-libp2p transport test.
// It's a test that however not works for many transports, and neither does it for WebRTC.
//
// Reason it doens't work for WebRTC is that it opens too many streams too rapidly, which
// in a regular environment would be seen as an attack that we wish to block/stop.
//
// Leaving it here for now only for documentation purposes,
// and to make it explicitly clear this test doesn't work for WebRTC.
func TestWebrtcTransport(t *testing.T) {
	t.Skip("This test does not work for WebRTC due to the way it is setup, see comments for more explanation")
	ta, _ := getTransport(t)
	tb, _ := getTransport(t)
	ttransport.SubtestTransport(t, ta, tb, fmt.Sprintf("/ip4/%s/udp/0/p2p-webrtc-direct", listenerIp), "peerA")
}
