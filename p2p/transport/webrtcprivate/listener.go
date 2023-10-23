package libp2pwebrtcprivate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	libp2pwebrtc "github.com/libp2p/go-libp2p/p2p/transport/webrtc"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtcprivate/pb"
	"github.com/libp2p/go-msgio/pbio"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/pion/webrtc/v3"
)

type listener struct {
	transport     *transport
	connQueue     chan tpt.CapableConn
	inflightQueue chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
}

var _ tpt.Listener = &listener{}

type NetAddr struct{}

var _ net.Addr = NetAddr{}

func (n NetAddr) Network() string {
	return "libp2p-webrtc"
}

func (n NetAddr) String() string {
	return "/webrtc"
}

// Accept implements transport.Listener.
func (l *listener) Accept() (tpt.CapableConn, error) {
	if l.ctx.Err() != nil {
		return nil, tpt.ErrListenerClosed
	}
	select {
	case c := <-l.connQueue:
		return c, nil
	case <-l.ctx.Done():
		return nil, tpt.ErrListenerClosed
	}
}

// Addr implements transport.Listener. The returned address always returns libp2p-webrtc:/webrtc
func (l *listener) Addr() net.Addr {
	return NetAddr{}
}

func (l *listener) Close() error {
	l.transport.RemoveListener(l)
	l.cancel()
	return nil
}

func (*listener) Multiaddr() ma.Multiaddr {
	return ma.StringCast("/webrtc")
}

func (l *listener) handleSignalingStream(s network.Stream) {
	select {
	case l.inflightQueue <- struct{}{}:
		defer func() { <-l.inflightQueue }()
	case <-l.ctx.Done():
		s.Reset()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	defer s.Close()

	remoteAddr := s.Conn().RemoteMultiaddr().Encapsulate(WebRTCAddr)
	scope, err := l.transport.rcmgr.OpenConnection(network.DirInbound, true, remoteAddr)
	if err != nil {
		s.Reset()
		log.Debug("failed to create connection scope:", err)
		return
	}
	if err := scope.SetPeer(s.Conn().RemotePeer()); err != nil {
		s.Reset()
		log.Debugf("resource manager blocked incoming conn from peer %s: %s", s.Conn().RemotePeer(), err)
		return
	}

	if err := s.Scope().SetService(name); err != nil {
		s.Reset()
		log.Debugf("error attaching stream to /webrtc listener: %s", err)
		return
	}

	if err := s.Scope().ReserveMemory(2*maxMsgSize, network.ReservationPriorityAlways); err != nil {
		s.Reset()
		log.Debugf("error reserving memory for /webrtc signaling stream: %s", err)
		return
	}
	defer s.Scope().ReleaseMemory(maxMsgSize)

	s.SetDeadline(time.Now().Add(connectTimeout))

	if l.transport.gater != nil {
		localAddr := s.Conn().LocalMultiaddr().Encapsulate(WebRTCAddr)
		if !l.transport.gater.InterceptAccept(&libp2pwebrtc.ConnMultiaddrs{Local: localAddr, Remote: remoteAddr}) {
			log.Debug("gater disallowed accepting connection from %s at %s", s.Conn().RemotePeer(), remoteAddr)
			s.Reset()
		}
	}

	conn, err := l.setupConnection(ctx, s, scope)
	if err != nil {
		s.Reset()
		scope.Done()
		log.Debug("failed to establish connection with %s: %s", s.Conn().RemotePeer(), err)
		return
	}

	if l.transport.gater != nil && !l.transport.gater.InterceptSecured(network.DirInbound, s.Conn().RemotePeer(), conn) {
		s.Reset()
		conn.Close()
		log.Debugf("conn gater refused connection to addr: %s", conn.RemoteMultiaddr())
		return
	}
	// Close the stream before we wait for the connection to be accepted
	s.Close()
	select {
	case l.connQueue <- conn:
	case <-l.ctx.Done():
		conn.Close()
		log.Debug("listener closed: dropping conn from %s", s.Conn().RemotePeer())
	}
}

func (l *listener) setupConnection(ctx context.Context, s network.Stream, scope network.ConnManagementScope) (_ tpt.CapableConn, err error) {
	var pc *webrtc.PeerConnection
	defer func() {
		if err != nil {
			pc.Close()
		}
	}()

	pc, err = l.transport.NewPeerConnection()
	if err != nil {
		err = fmt.Errorf("error creating a webrtc.PeerConnection: %w", err)
		log.Debug(err)
		return nil, err
	}
	dataChannelQueue := libp2pwebrtc.SetupDataChannelQueue(pc, maxAcceptQueueLen)

	r := pbio.NewDelimitedReader(s, maxMsgSize)
	w := pbio.NewDelimitedWriter(s)

	// register peerconnection state update callback
	connectionState := make(chan webrtc.PeerConnectionState, 1)
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected, webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			// We only use the first state written to connectionState.
			select {
			case connectionState <- state:
			default:
			}
		}
	})

	// register local ICE Candidate found callback
	writeErr := make(chan error, 1)
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		b, err := json.Marshal(candidate.ToJSON())
		if err != nil {
			// We only want to write a single error on this channel
			select {
			case writeErr <- fmt.Errorf("failed to marshal candidate to JSON: %w", err):
			default:
			}
			return
		}
		data := string(b)

		msg := &pb.Message{
			Type: pb.Message_ICE_CANDIDATE.Enum(),
			Data: &data,
		}
		if err := w.WriteMsg(msg); err != nil {
			// We only want to write a single error on this channel
			select {
			case writeErr <- fmt.Errorf("write candidate failed: %w", err):
			default:
			}
		}
	})

	// de-register candidate callback
	defer pc.OnICECandidate(func(_ *webrtc.ICECandidate) {})

	// read an incoming offer
	var msg pb.Message
	if err := r.ReadMsg(&msg); err != nil {
		err = fmt.Errorf("failed to read offer: %w", err)
		return nil, err
	}
	if msg.Type == nil || *msg.Type != pb.Message_SDP_OFFER {
		err = fmt.Errorf("invalid message: msg.Type expected %s got %s", pb.Message_SDP_OFFER, msg.Type)
		return nil, err
	}
	if msg.Data == nil || *msg.Data == "" {
		err = errors.New("invalid message: empty data")
		return nil, err
	}
	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: *msg.Data}
	if err := pc.SetRemoteDescription(offer); err != nil {
		err = fmt.Errorf("failed to set remote description: %w", err)
		return nil, err
	}

	// send an answer
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create answer: %w", err)
	}
	msg = pb.Message{
		Type: pb.Message_SDP_ANSWER.Enum(),
		Data: &answer.SDP,
	}
	if err := w.WriteMsg(&msg); err != nil {
		return nil, fmt.Errorf("failed to write answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return nil, fmt.Errorf("failed to set local description: %w", err)
	}

	readErr := make(chan error, 1)
	// start a goroutine to read candidates
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			err := r.ReadMsg(&msg)
			if err == io.EOF {
				// remote has done writing
				return
			}
			if err != nil {
				readErr <- fmt.Errorf("failed to read candidate: %w", err)
				return
			}

			if msg.Type == nil || *msg.Type != pb.Message_ICE_CANDIDATE {
				readErr <- fmt.Errorf("invalid message: msg.Type expected %s got %s", pb.Message_ICE_CANDIDATE, msg.Type)
				return
			}
			// Ignore without Debuging on empty message.
			// Pion has a case where OnCandidate callback may be called with a nil
			// candidate
			if msg.Data == nil || *msg.Data == "" {
				log.Debugf("received empty candidate from %s", s.Conn().RemotePeer())
				continue
			}

			var init webrtc.ICECandidateInit
			if err := json.Unmarshal([]byte(*msg.Data), &init); err != nil {
				readErr <- fmt.Errorf("failed to unmarshal ice candidate %w", err)
				return
			}
			if err := pc.AddICECandidate(init); err != nil {
				readErr <- fmt.Errorf("failed to add ice candidate: %w", err)
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		pc.Close()
		return nil, ctx.Err()
	case err := <-writeErr:
		pc.Close()
		return nil, fmt.Errorf("error writing candidate: %w", err)
	case err := <-readErr:
		pc.Close()
		return nil, fmt.Errorf("error reading candidate: %w", err)
	case state := <-connectionState:
		switch state {
		default:
			pc.Close()
			return nil, fmt.Errorf("failed to establish webrtc.PeerConnection, state: %s", state)
		case webrtc.PeerConnectionStateConnected:
		}
	}

	localAddr, err := getLocalConnectionAddress(pc)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to get connection addresses: %w", err)
	}
	remoteAddr := s.Conn().RemoteMultiaddr().Encapsulate(WebRTCAddr)

	conn, err := libp2pwebrtc.NewWebRTCConnection(
		network.DirInbound,
		pc,
		l.transport,
		scope,
		l.transport.host.ID(),
		localAddr,
		s.Conn().RemotePeer(),
		l.transport.host.Peerstore().PubKey(s.Conn().RemotePeer()), // we have the public key from the relayed connection
		remoteAddr,
		dataChannelQueue,
	)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("error establishing tpt.CapableConn: %w", err)
	}
	return conn, nil
}
