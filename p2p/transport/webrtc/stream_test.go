package libp2pwebrtc

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"
	"github.com/libp2p/go-msgio/pbio"
	"google.golang.org/protobuf/proto"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/pion/datachannel"
	"github.com/pion/sctp"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type detachedChan struct {
	rwc datachannel.ReadWriteCloser
	dc  *webrtc.DataChannel
}

func getDetachedDataChannels(t *testing.T) (detachedChan, detachedChan) {
	s := webrtc.SettingEngine{}
	s.SetIncludeLoopbackCandidate(true)
	s.DetachDataChannels()
	api := webrtc.NewAPI(webrtc.WithSettingEngine(s))

	offerPC, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { offerPC.Close() })
	offerRWCChan := make(chan detachedChan, 1)
	offerDC, err := offerPC.CreateDataChannel("data", nil)
	require.NoError(t, err)
	offerDC.OnOpen(func() {
		rwc, err := offerDC.Detach()
		require.NoError(t, err)
		offerRWCChan <- detachedChan{rwc: rwc, dc: offerDC}
	})

	answerPC, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)

	answerChan := make(chan detachedChan, 1)
	answerPC.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() {
			rwc, err := dc.Detach()
			require.NoError(t, err)
			answerChan <- detachedChan{rwc: rwc, dc: dc}
		})
	})
	t.Cleanup(func() { answerPC.Close() })

	// Set ICE Candidate handlers. As soon as a PeerConnection has gathered a candidate send it to the other peer
	answerPC.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			require.NoError(t, offerPC.AddICECandidate(candidate.ToJSON()))
		}
	})
	offerPC.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			require.NoError(t, answerPC.AddICECandidate(candidate.ToJSON()))
		}
	})

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	offerPC.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed {
			t.Log("peer connection failed on offerer")
		}
	})

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	answerPC.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed {
			t.Log("peer connection failed on answerer")
		}
	})

	// Now, create an offer
	offer, err := offerPC.CreateOffer(nil)
	require.NoError(t, err)
	require.NoError(t, answerPC.SetRemoteDescription(offer))
	require.NoError(t, offerPC.SetLocalDescription(offer))

	answer, err := answerPC.CreateAnswer(nil)
	require.NoError(t, err)
	require.NoError(t, offerPC.SetRemoteDescription(answer))
	require.NoError(t, answerPC.SetLocalDescription(answer))

	return <-answerChan, <-offerRWCChan
}

// assertDataChannelOpen checks if the datachannel is open.
// It sends empty messages on the data channel to check if the channel is still open.
// The control message reader goroutine depends on exclusive access to datachannel.Read
// so we have to depend on Write to determine whether the channel has been closed.
func assertDataChannelOpen(t *testing.T, dc *datachannel.DataChannel) {
	t.Helper()
	emptyMsg := &pb.Message{}
	msg, err := proto.Marshal(emptyMsg)
	if err != nil {
		t.Fatal("unexpected mashalling error", err)
	}
	for i := 0; i < 3; i++ {
		_, err := dc.Write(msg)
		if err != nil {
			t.Fatal("unexpected write err: ", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// assertDataChannelClosed checks if the datachannel is closed.
// It sends empty messages on the data channel to check if the channel has been closed.
// The control message reader goroutine depends on exclusive access to datachannel.Read
// so we have to depend on Write to determine whether the channel has been closed.
func assertDataChannelClosed(t *testing.T, dc *datachannel.DataChannel) {
	t.Helper()
	emptyMsg := &pb.Message{}
	msg, err := proto.Marshal(emptyMsg)
	if err != nil {
		t.Fatal("unexpected mashalling error", err)
	}
	for i := 0; i < 5; i++ {
		_, err := dc.Write(msg)
		if err != nil {
			if errors.Is(err, sctp.ErrStreamClosed) {
				return
			} else {
				t.Fatal("unexpected write err: ", err)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestStreamSimpleReadWriteClose(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	var clientDone, serverDone atomic.Bool
	clientStr := newStream(client.dc, client.rwc, func() { clientDone.Store(true) })
	serverStr := newStream(server.dc, server.rwc, func() { serverDone.Store(true) })

	// send a foobar from the client
	n, err := clientStr.Write([]byte("foobar"))
	require.NoError(t, err)
	require.Equal(t, 6, n)
	require.NoError(t, clientStr.CloseWrite())
	// writing after closing should error
	_, err = clientStr.Write([]byte("foobar"))
	require.Error(t, err)
	require.False(t, clientDone.Load())

	// now read all the data on the server side
	b, err := io.ReadAll(serverStr)
	require.NoError(t, err)
	require.Equal(t, []byte("foobar"), b)
	// reading again should give another io.EOF
	n, err = serverStr.Read(make([]byte, 10))
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)
	require.False(t, serverDone.Load())

	// send something back
	_, err = serverStr.Write([]byte("lorem ipsum"))
	require.NoError(t, err)
	require.NoError(t, serverStr.CloseWrite())

	// and read it at the client
	require.False(t, clientDone.Load())
	b, err = io.ReadAll(clientStr)
	require.NoError(t, err)
	require.Equal(t, []byte("lorem ipsum"), b)

	// stream is only cleaned up on calling Close or Reset
	clientStr.Close()
	serverStr.Close()
	require.Eventually(t, func() bool { return clientDone.Load() }, 5*time.Second, 100*time.Millisecond)
	// Need to call Close for cleanup. Otherwise the FIN_ACK is never read
	require.NoError(t, serverStr.Close())
	require.Eventually(t, func() bool { return serverDone.Load() }, 5*time.Second, 100*time.Millisecond)
}

func TestStreamPartialReads(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	clientStr := newStream(client.dc, client.rwc, func() {})
	serverStr := newStream(server.dc, server.rwc, func() {})

	_, err := serverStr.Write([]byte("foobar"))
	require.NoError(t, err)
	require.NoError(t, serverStr.CloseWrite())

	n, err := clientStr.Read([]byte{}) // empty read
	require.NoError(t, err)
	require.Zero(t, n)
	b := make([]byte, 3)
	n, err = clientStr.Read(b)
	require.Equal(t, 3, n)
	require.NoError(t, err)
	require.Equal(t, []byte("foo"), b)
	b, err = io.ReadAll(clientStr)
	require.NoError(t, err)
	require.Equal(t, []byte("bar"), b)
}

func TestStreamSkipEmptyFrames(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	clientStr := newStream(client.dc, client.rwc, func() {})
	serverStr := newStream(server.dc, server.rwc, func() {})

	for i := 0; i < 10; i++ {
		require.NoError(t, serverStr.writer.WriteMsg(&pb.Message{}))
	}
	require.NoError(t, serverStr.writer.WriteMsg(&pb.Message{Message: []byte("foo")}))
	for i := 0; i < 10; i++ {
		require.NoError(t, serverStr.writer.WriteMsg(&pb.Message{}))
	}
	require.NoError(t, serverStr.writer.WriteMsg(&pb.Message{Message: []byte("bar")}))
	for i := 0; i < 10; i++ {
		require.NoError(t, serverStr.writer.WriteMsg(&pb.Message{}))
	}
	require.NoError(t, serverStr.writer.WriteMsg(&pb.Message{Flag: pb.Message_FIN.Enum()}))

	var read []byte
	var count int
	for i := 0; i < 100; i++ {
		b := make([]byte, 10)
		count++
		n, err := clientStr.Read(b)
		read = append(read, b[:n]...)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}
	require.LessOrEqual(t, count, 3, "should've taken a maximum of 3 reads")
	require.Equal(t, []byte("foobar"), read)
}

func TestStreamReadReturnsOnClose(t *testing.T) {
	client, _ := getDetachedDataChannels(t)

	clientStr := newStream(client.dc, client.rwc, func() {})
	errChan := make(chan error, 1)
	go func() {
		_, err := clientStr.Read([]byte{0})
		errChan <- err
	}()
	time.Sleep(100 * time.Millisecond) // give the Read call some time to hit the loop
	require.NoError(t, clientStr.Close())
	select {
	case err := <-errChan:
		require.ErrorIs(t, err, network.ErrReset)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout")
	}

	_, err := clientStr.Read([]byte{0})
	require.ErrorIs(t, err, network.ErrReset)
}

func TestStreamResets(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	var clientDone, serverDone atomic.Bool
	clientStr := newStream(client.dc, client.rwc, func() { clientDone.Store(true) })
	serverStr := newStream(server.dc, server.rwc, func() { serverDone.Store(true) })

	// send a foobar from the client
	_, err := clientStr.Write([]byte("foobar"))
	require.NoError(t, err)
	_, err = serverStr.Write([]byte("lorem ipsum"))
	require.NoError(t, err)
	require.NoError(t, clientStr.Reset()) // resetting resets both directions
	require.True(t, clientDone.Load())
	// attempting to write more data should result in a reset error
	_, err = clientStr.Write([]byte("foobar"))
	require.ErrorIs(t, err, network.ErrReset)
	// read what the server sent
	b, err := io.ReadAll(clientStr)
	require.Empty(t, b)
	require.ErrorIs(t, err, network.ErrReset)

	// read the data on the server side
	require.False(t, serverDone.Load())
	b, err = io.ReadAll(serverStr)
	require.Equal(t, []byte("foobar"), b)
	require.ErrorIs(t, err, network.ErrReset)
	require.Eventually(t, func() bool {
		_, err := serverStr.Write([]byte("foobar"))
		return errors.Is(err, network.ErrReset)
	}, time.Second, 50*time.Millisecond)
	serverStr.Close()
	require.Eventually(t, func() bool {
		return serverDone.Load()
	}, time.Second, 50*time.Millisecond)
}

func TestStreamReadDeadlineAsync(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	clientStr := newStream(client.dc, client.rwc, func() {})
	serverStr := newStream(server.dc, server.rwc, func() {})

	timeout := 100 * time.Millisecond
	if os.Getenv("CI") != "" {
		timeout *= 5
	}
	start := time.Now()
	clientStr.SetReadDeadline(start.Add(timeout))
	_, err := clientStr.Read([]byte{0})
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	took := time.Since(start)
	require.GreaterOrEqual(t, took, timeout)
	require.LessOrEqual(t, took, timeout*3/2)
	// repeated calls should return immediately
	start = time.Now()
	_, err = clientStr.Read([]byte{0})
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	require.LessOrEqual(t, time.Since(start), timeout/3)
	// clear the deadline
	clientStr.SetReadDeadline(time.Time{})
	_, err = serverStr.Write([]byte("foobar"))
	require.NoError(t, err)
	_, err = clientStr.Read([]byte{0})
	require.NoError(t, err)
	require.LessOrEqual(t, time.Since(start), timeout/3)
}

func TestStreamWriteDeadlineAsync(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	clientStr := newStream(client.dc, client.rwc, func() {})
	serverStr := newStream(server.dc, server.rwc, func() {})
	_ = serverStr

	b := make([]byte, 1024)
	rand.Read(b)
	start := time.Now()
	timeout := 100 * time.Millisecond
	if os.Getenv("CI") != "" {
		timeout *= 5
	}
	clientStr.SetWriteDeadline(start.Add(timeout))
	var hitDeadline bool
	for i := 0; i < 2000; i++ {
		if _, err := clientStr.Write(b); err != nil {
			t.Logf("wrote %d kB", i)
			require.ErrorIs(t, err, os.ErrDeadlineExceeded)
			hitDeadline = true
			break
		}
	}
	require.True(t, hitDeadline)
	took := time.Since(start)
	require.GreaterOrEqual(t, took, timeout)
	require.LessOrEqual(t, took, timeout*3/2)
}

func TestStreamReadAfterClose(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	clientStr := newStream(client.dc, client.rwc, func() {})
	serverStr := newStream(server.dc, server.rwc, func() {})

	serverStr.Close()
	b := make([]byte, 1)
	_, err := clientStr.Read(b)
	require.Equal(t, io.EOF, err)
	_, err = clientStr.Read(nil)
	require.Equal(t, io.EOF, err)

	client, server = getDetachedDataChannels(t)

	clientStr = newStream(client.dc, client.rwc, func() {})
	serverStr = newStream(server.dc, server.rwc, func() {})

	serverStr.Reset()
	b = make([]byte, 1)
	_, err = clientStr.Read(b)
	require.ErrorIs(t, err, network.ErrReset)
	_, err = clientStr.Read(nil)
	require.ErrorIs(t, err, network.ErrReset)
}

func TestStreamCloseAfterFINACK(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	done := make(chan bool, 1)
	clientStr := newStream(client.dc, client.rwc, func() { done <- true })
	serverStr := newStream(server.dc, server.rwc, func() {})

	go func() {
		err := clientStr.Close()
		assert.NoError(t, err)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("Close should signal OnDone immediately")
	}

	// Reading FIN_ACK on server should trigger data channel close on the client
	b := make([]byte, 1)
	_, err := serverStr.Read(b)
	require.Error(t, err)
	require.ErrorIs(t, err, io.EOF)
	assertDataChannelClosed(t, client.rwc.(*datachannel.DataChannel))
}

// TestStreamFinAckAfterStopSending tests that FIN_ACK is sent even after the write half
// of the stream is closed.
func TestStreamFinAckAfterStopSending(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	done := make(chan bool, 1)
	clientStr := newStream(client.dc, client.rwc, func() { done <- true })
	serverStr := newStream(server.dc, server.rwc, func() {})

	go func() {
		clientStr.CloseRead()
		clientStr.Write([]byte("hello world"))
		done <- true
		err := clientStr.Close()
		assert.NoError(t, err)
	}()
	<-done

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Errorf("Close should signal onDone immediately")
	}

	// serverStr has write half closed and read half open
	// serverStr should still send FIN_ACK
	b := make([]byte, 24)
	_, err := serverStr.Read(b)
	require.NoError(t, err)
	serverStr.Close() // Sends stop_sending, fin
	assertDataChannelClosed(t, server.rwc.(*datachannel.DataChannel))
	assertDataChannelClosed(t, client.rwc.(*datachannel.DataChannel))
}

func TestStreamConcurrentClose(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	start := make(chan bool, 2)
	done := make(chan bool, 2)
	clientStr := newStream(client.dc, client.rwc, func() { done <- true })
	serverStr := newStream(server.dc, server.rwc, func() { done <- true })

	go func() {
		start <- true
		clientStr.Close()
	}()
	go func() {
		start <- true
		serverStr.Close()
	}()
	<-start
	<-start

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("concurrent close should succeed quickly")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("concurrent close should succeed quickly")
	}

	// Wait for FIN_ACK AND datachannel close
	assertDataChannelClosed(t, client.rwc.(*datachannel.DataChannel))
	assertDataChannelClosed(t, server.rwc.(*datachannel.DataChannel))

}

func TestStreamResetAfterClose(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	done := make(chan bool, 2)
	clientStr := newStream(client.dc, client.rwc, func() { done <- true })
	clientStr.Close()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Close should run cleanup immediately")
	}
	// The server data channel should still be open
	assertDataChannelOpen(t, server.rwc.(*datachannel.DataChannel))
	clientStr.Reset()
	// Reset closes the datachannels
	assertDataChannelClosed(t, server.rwc.(*datachannel.DataChannel))
	assertDataChannelClosed(t, client.rwc.(*datachannel.DataChannel))
	select {
	case <-done:
		t.Fatalf("onDone should not be called twice")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestStreamDataChannelCloseOnFINACK(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	done := make(chan bool, 1)
	clientStr := newStream(client.dc, client.rwc, func() { done <- true })

	clientStr.Close()

	select {
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Close should run cleanup immediately")
	case <-done:
	}

	// sending FIN_ACK closes the datachannel
	serverWriter := pbio.NewDelimitedWriter(server.rwc)
	err := serverWriter.WriteMsg(&pb.Message{Flag: pb.Message_FIN_ACK.Enum()})
	require.NoError(t, err)

	assertDataChannelClosed(t, server.rwc.(*datachannel.DataChannel))
	assertDataChannelClosed(t, client.rwc.(*datachannel.DataChannel))
}

func TestStreamChunking(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	clientStr := newStream(client.dc, client.rwc, func() {})
	serverStr := newStream(server.dc, server.rwc, func() {})

	const N = (16 << 10) + 1000
	go func() {
		data := make([]byte, N)
		_, err := clientStr.Write(data)
		require.NoError(t, err)
	}()

	data := make([]byte, N)
	n, err := serverStr.Read(data)
	require.NoError(t, err)
	require.LessOrEqual(t, n, 16<<10)

	nn, err := serverStr.Read(data)
	require.NoError(t, err)
	require.Equal(t, nn+n, N)
}
