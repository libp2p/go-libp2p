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
	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"

	"github.com/libp2p/go-msgio"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v3"
	"google.golang.org/protobuf/proto"
)

var _ tpt.CapableConn = &connection{}

const maxAcceptQueueLen = 10

const maxDataChannelID = 1 << 10

type errConnectionTimeout struct{}

var _ net.Error = &errConnectionTimeout{}

func (errConnectionTimeout) Error() string   { return "connection timeout" }
func (errConnectionTimeout) Timeout() bool   { return true }
func (errConnectionTimeout) Temporary() bool { return false }

type dataChannel struct {
	stream  datachannel.ReadWriteCloser
	channel *webrtc.DataChannel
}

type connection struct {
	pc        *webrtc.PeerConnection
	transport *WebRTCTransport
	scope     network.ConnManagementScope

	closeErr error

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

		acceptQueue: make(chan dataChannel, maxAcceptQueueLen),
	}
	switch direction {
	case network.DirInbound:
		c.nextStreamID.Store(1)
	case network.DirOutbound:
		// stream ID 0 is used for the Noise handshake stream
		c.nextStreamID.Store(2)
	}

	pc.OnConnectionStateChange(c.onConnectionStateChange)
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if c.IsClosed() {
			return
		}
		// Limit the number of streams, since we're not able to actually properly close them.
		// See https://github.com/libp2p/specs/issues/575 for details.
		if *dc.ID() > maxDataChannelID {
			c.Close()
			return
		}
		dc.OnOpen(func() {
			rwc, err := dc.Detach()
			if err != nil {
				log.Warnf("could not detach datachannel: id: %d", *dc.ID())
				return
			}
			select {
			case c.acceptQueue <- dataChannel{rwc, dc}:
			default:
				log.Warnf("connection busy, rejecting stream")
				b, _ := proto.Marshal(&pb.Message{Flag: pb.Message_RESET.Enum()})
				w := msgio.NewWriter(rwc)
				w.WriteMsg(b)
				rwc.Close()
			}
		})
	})

	return c, nil
}

// ConnState implements transport.CapableConn
func (c *connection) ConnState() network.ConnectionState {
	return network.ConnectionState{Transport: "webrtc-direct"}
}

// Close closes the underlying peerconnection.
func (c *connection) Close() error {
	if c.IsClosed() {
		return nil
	}

	c.m.Lock()
	defer c.m.Unlock()
	c.scope.Done()
	c.closeErr = errors.New("connection closed")
	c.cancel()
	return c.pc.Close()
}

func (c *connection) IsClosed() bool {
	select {
	case <-c.ctx.Done():
		return true
	default:
		return false
	}
}

func (c *connection) OpenStream(ctx context.Context) (network.MuxedStream, error) {
	if c.IsClosed() {
		return nil, c.closeErr
	}

	id := c.nextStreamID.Add(2) - 2
	if id > math.MaxUint16 {
		return nil, errors.New("exhausted stream ID space")
	}
	// Limit the number of streams, since we're not able to actually properly close them.
	// See https://github.com/libp2p/specs/issues/575 for details.
	if id > maxDataChannelID {
		c.Close()
		return c.OpenStream(ctx)
	}

	streamID := uint16(id)
	dc, err := c.pc.CreateDataChannel("", &webrtc.DataChannelInit{ID: &streamID})
	if err != nil {
		return nil, err
	}
	rwc, err := c.detachChannel(ctx, dc)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	str := newStream(dc, rwc, func() { c.removeStream(streamID) })
	if err := c.addStream(str); err != nil {
		str.Close()
		return nil, err
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
			str.Close()
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
		// reset any streams
		if c.IsClosed() {
			return
		}
		c.m.Lock()
		defer c.m.Unlock()
		c.closeErr = errConnectionTimeout{}
		for k, str := range c.streams {
			str.setCloseError(c.closeErr)
			delete(c.streams, k)
		}
		c.cancel()
		c.scope.Done()
		c.pc.Close()
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
func (c *connection) detachChannel(ctx context.Context, dc *webrtc.DataChannel) (rwc datachannel.ReadWriteCloser, err error) {
	done := make(chan struct{})
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
	}
	return
}

// A note on these setters and why they are needed:
//
// The connection object sets up receiving datachannels (streams) from the remote peer.
// Please consider the XX noise handshake pattern from a peer A to peer B as described at:
// https://noiseexplorer.com/patterns/XX/
//
// The initiator A completes the noise handshake before B.
// This would allow A to create new datachannels before B has set up the callbacks to process incoming datachannels.
// This would create a situation where A has successfully created a stream but B is not aware of it.
// Moving the construction of the connection object before the noise handshake eliminates this issue,
// as callbacks have been set up for both peers.
//
// This could lead to a case where streams are created during the noise handshake,
// and the handshake fails. In this case, we would close the underlying peerconnection.

// only used during connection setup
func (c *connection) setRemotePeer(id peer.ID) {
	c.remotePeer = id
}

// only used during connection setup
func (c *connection) setRemotePublicKey(key ic.PubKey) {
	c.remoteKey = key
}
