package libp2pwebrtc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"sync/atomic"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/pion/datachannel"
	"github.com/pion/sctp"
	"github.com/pion/webrtc/v4"
)

var _ tpt.CapableConn = &connection{}

const maxAcceptQueueLen = 256

type errConnectionTimeout struct{}

var _ net.Error = &errConnectionTimeout{}

func (errConnectionTimeout) Error() string   { return "connection timeout" }
func (errConnectionTimeout) Timeout() bool   { return true }
func (errConnectionTimeout) Temporary() bool { return false }

var errConnClosed = errors.New("connection closed")

type dataChannel struct {
	stream  datachannel.ReadWriteCloser
	channel *webrtc.DataChannel
}

type connection struct {
	pc        *webrtc.PeerConnection
	transport *WebRTCTransport
	scope     network.ConnManagementScope

	closeOnce sync.Once
	closeErr  error

	localPeer      peer.ID
	localMultiaddr ma.Multiaddr

	remotePeer      peer.ID
	remoteKey       ic.PubKey
	remoteMultiaddr ma.Multiaddr

	m            sync.Mutex
	streams      map[uint16]*stream
	nextStreamID atomic.Int32

	acceptQueue chan dataChannel

	ctx    context.Context
	cancel context.CancelFunc
}

func newConnection(
	direction network.Direction,
	pc *webrtc.PeerConnection,
	transport *WebRTCTransport,
	scope network.ConnManagementScope,

	localPeer peer.ID,
	localMultiaddr ma.Multiaddr,

	remotePeer peer.ID,
	remoteKey ic.PubKey,
	remoteMultiaddr ma.Multiaddr,
	incomingDataChannels chan dataChannel,
	peerConnectionClosedCh chan struct{},
) (*connection, error) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &connection{
		pc:        pc,
		transport: transport,
		scope:     scope,

		localPeer:      localPeer,
		localMultiaddr: localMultiaddr,

		remotePeer:      remotePeer,
		remoteKey:       remoteKey,
		remoteMultiaddr: remoteMultiaddr,
		ctx:             ctx,
		cancel:          cancel,
		streams:         make(map[uint16]*stream),

		acceptQueue: incomingDataChannels,
	}
	switch direction {
	case network.DirInbound:
		c.nextStreamID.Store(1)
	case network.DirOutbound:
		// stream ID 0 is used for the Noise handshake stream
		c.nextStreamID.Store(2)
	}

	pc.OnConnectionStateChange(c.onConnectionStateChange)
	pc.SCTP().OnClose(func(err error) {
		if err != nil {
			c.closeWithError(fmt.Errorf("%w: %w", errConnClosed, err))
		}
		c.closeWithError(errConnClosed)
	})
	select {
	case <-peerConnectionClosedCh:
		c.Close()
		return nil, errConnClosed
	default:
	}
	return c, nil
}

// ConnState implements transport.CapableConn
func (c *connection) ConnState() network.ConnectionState {
	return network.ConnectionState{Transport: "webrtc-direct"}
}

// Close closes the underlying peerconnection.
func (c *connection) Close() error {
	c.closeWithError(errConnClosed)
	return nil
}

// CloseWithError closes the connection ignoring the error code. As there's no way to signal
// the remote peer on closing the underlying peerconnection, we ignore the error code.
func (c *connection) CloseWithError(_ network.ConnErrorCode) error {
	return c.Close()
}

// closeWithError is used to Close the connection when the underlying DTLS connection fails
func (c *connection) closeWithError(err error) {
	c.closeOnce.Do(func() {
		c.closeErr = err
		// cancel must be called after closeErr is set. This ensures interested goroutines waiting on
		// ctx.Done can read closeErr without holding the conn lock.
		c.cancel()
		// closing peerconnection will close the datachannels associated with the streams
		c.pc.Close()

		c.m.Lock()
		streams := c.streams
		c.streams = nil
		c.m.Unlock()
		for _, s := range streams {
			s.closeForShutdown(err)
		}
		c.scope.Done()
	})
}

func (c *connection) IsClosed() bool {
	return c.ctx.Err() != nil
}

func (c *connection) OpenStream(ctx context.Context) (network.MuxedStream, error) {
	if c.IsClosed() {
		return nil, c.closeErr
	}

	id := c.nextStreamID.Add(2) - 2
	if id > math.MaxUint16 {
		return nil, errors.New("exhausted stream ID space")
	}
	streamID := uint16(id)
	dc, err := c.pc.CreateDataChannel("", &webrtc.DataChannelInit{ID: &streamID})
	if err != nil {
		return nil, err
	}
	rwc, err := c.detachChannel(ctx, dc)
	if err != nil {
		// There's a race between webrtc.SCTP.OnClose callback and the underlying
		// association closing. It's nicer to close the connection here.
		if errors.Is(err, sctp.ErrStreamClosed) {
			c.closeWithError(errConnClosed)
			return nil, c.closeErr
		}
		dc.Close()
		return nil, fmt.Errorf("detach channel failed for stream(%d): %w", streamID, err)
	}
	str := newStream(dc, rwc, func() { c.removeStream(streamID) })
	if err := c.addStream(str); err != nil {
		str.Reset()
		return nil, fmt.Errorf("failed to add stream(%d) to connection: %w", streamID, err)
	}
	return str, nil
}

func (c *connection) AcceptStream() (network.MuxedStream, error) {
	select {
	case <-c.ctx.Done():
		return nil, c.closeErr
	case dc := <-c.acceptQueue:
		str := newStream(dc.channel, dc.stream, func() { c.removeStream(*dc.channel.ID()) })
		if err := c.addStream(str); err != nil {
			str.Reset()
			return nil, err
		}
		return str, nil
	}
}

func (c *connection) LocalPeer() peer.ID            { return c.localPeer }
func (c *connection) RemotePeer() peer.ID           { return c.remotePeer }
func (c *connection) RemotePublicKey() ic.PubKey    { return c.remoteKey }
func (c *connection) LocalMultiaddr() ma.Multiaddr  { return c.localMultiaddr }
func (c *connection) RemoteMultiaddr() ma.Multiaddr { return c.remoteMultiaddr }
func (c *connection) Scope() network.ConnScope      { return c.scope }
func (c *connection) Transport() tpt.Transport      { return c.transport }

func (c *connection) addStream(str *stream) error {
	c.m.Lock()
	defer c.m.Unlock()
	if c.streams == nil {
		return c.closeErr
	}
	if _, ok := c.streams[str.id]; ok {
		return errors.New("stream ID already exists")
	}
	c.streams[str.id] = str
	return nil
}

func (c *connection) removeStream(id uint16) {
	c.m.Lock()
	defer c.m.Unlock()
	delete(c.streams, id)
}

func (c *connection) onConnectionStateChange(state webrtc.PeerConnectionState) {
	if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
		c.closeWithError(errConnectionTimeout{})
	}
}

// detachChannel detaches an outgoing channel by taking into account the context
// passed to `OpenStream` as well the closure of the underlying peerconnection
//
// The underlying SCTP stream for a datachannel implements a net.Conn interface.
// However, the datachannel creates a goroutine which continuously reads from
// the SCTP stream and surfaces the data using an OnMessage callback.
//
// The actual abstractions are as follows: webrtc.DataChannel
// wraps pion.DataChannel, which wraps sctp.Stream.
//
// The goroutine for reading, Detach method,
// and the OnMessage callback are present at the webrtc.DataChannel level.
// Detach provides us abstracted access to the underlying pion.DataChannel,
// which allows us to issue Read calls to the datachannel.
// This was desired because it was not feasible to introduce backpressure
// with the OnMessage callbacks. The tradeoff is a change in the semantics of
// the OnOpen callback, and having to force close Read locally.
func (c *connection) detachChannel(ctx context.Context, dc *webrtc.DataChannel) (datachannel.ReadWriteCloser, error) {
	done := make(chan struct{})

	var rwc datachannel.ReadWriteCloser
	var err error
	// OnOpen will return immediately for detached datachannels
	// refer: https://github.com/pion/webrtc/blob/7ab3174640b3ce15abebc2516a2ca3939b5f105f/datachannel.go#L278-L282
	dc.OnOpen(func() {
		rwc, err = dc.Detach()
		// this is safe since the function should return instantly if the peerconnection is closed
		close(done)
	})
	select {
	case <-c.ctx.Done():
		return nil, c.closeErr
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
		return rwc, err
	}
}
