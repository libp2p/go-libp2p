package libp2pwebrtc

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
)

type detachedChan struct {
	rwc datachannel.ReadWriteCloser
	dc  *webrtc.DataChannel
}

func getDetachedDataChannels(t *testing.T) (detachedChan, detachedChan) {
	s := webrtc.SettingEngine{}
	s.DetachDataChannels()
	api := webrtc.NewAPI(webrtc.WithSettingEngine(s))

	offerPC, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { offerPC.Close() })
	offerRWCChan := make(chan datachannel.ReadWriteCloser, 1)
	offerDC, err := offerPC.CreateDataChannel("data", nil)
	require.NoError(t, err)
	offerDC.OnOpen(func() {
		rwc, err := offerDC.Detach()
		require.NoError(t, err)
		offerRWCChan <- rwc
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

	return <-answerChan, detachedChan{rwc: <-offerRWCChan, dc: offerDC}
}

func TestStreamSimpleReadWriteClose(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	var clientDone, serverDone bool
	clientStr := newStream(client.dc, client.rwc, func() { clientDone = true })
	serverStr := newStream(server.dc, server.rwc, func() { serverDone = true })

	// send a foobar from the client
	n, err := clientStr.Write([]byte("foobar"))
	require.NoError(t, err)
	require.Equal(t, 6, n)
	require.NoError(t, clientStr.CloseWrite())
	// writing after closing should error
	_, err = clientStr.Write([]byte("foobar"))
	require.Error(t, err)
	require.False(t, clientDone)

	// now read all the data on the server side
	b, err := io.ReadAll(serverStr)
	require.NoError(t, err)
	require.Equal(t, []byte("foobar"), b)
	// reading again should give another io.EOF
	n, err = serverStr.Read(make([]byte, 10))
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)
	require.False(t, serverDone)

	// send something back
	_, err = serverStr.Write([]byte("lorem ipsum"))
	require.NoError(t, err)
	require.NoError(t, serverStr.CloseWrite())
	require.True(t, serverDone)
	// and read it at the client
	require.False(t, clientDone)
	b, err = io.ReadAll(clientStr)
	require.NoError(t, err)
	require.Equal(t, []byte("lorem ipsum"), b)
	require.True(t, clientDone)
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
	time.Sleep(50 * time.Millisecond) // give the Read call some time to hit the loop
	require.NoError(t, clientStr.Close())
	select {
	case err := <-errChan:
		require.ErrorIs(t, err, network.ErrReset)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout")
	}
}

func TestStreamResets(t *testing.T) {
	client, server := getDetachedDataChannels(t)

	var clientDone, serverDone bool
	clientStr := newStream(client.dc, client.rwc, func() { clientDone = true })
	serverStr := newStream(server.dc, server.rwc, func() { serverDone = true })

	// send a foobar from the client
	_, err := clientStr.Write([]byte("foobar"))
	require.NoError(t, err)
	_, err = serverStr.Write([]byte("lorem ipsum"))
	require.NoError(t, err)
	require.NoError(t, clientStr.Reset()) // resetting resets both directions
	require.True(t, clientDone)
	// attempting to write more data should result in a reset error
	_, err = clientStr.Write([]byte("foobar"))
	require.ErrorIs(t, err, network.ErrReset)
	// read what the server sent
	b, err := io.ReadAll(clientStr)
	require.Empty(t, b)
	require.ErrorIs(t, err, network.ErrReset)

	// read the data on the server side
	require.False(t, serverDone)
	b, err = io.ReadAll(serverStr)
	require.Equal(t, []byte("foobar"), b)
	require.ErrorIs(t, err, network.ErrReset)
	require.Eventually(t, func() bool {
		_, err := serverStr.Write([]byte("foobar"))
		return errors.Is(err, network.ErrReset)
	}, time.Second, 50*time.Millisecond)
	require.True(t, serverDone)
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
