package libp2pwebrtcprivate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	pionlogger "github.com/pion/logging"

	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	libp2pwebrtc "github.com/libp2p/go-libp2p/p2p/transport/webrtc"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtcprivate/pb"
	"github.com/libp2p/go-msgio/pbio"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap/zapcore"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	name                = "webrtcprivate"
	maxMsgSize          = 4096
	connectTimeout      = time.Minute
	SignalingProtocol   = "/webrtc-signaling"
	disconnectedTimeout = 20 * time.Second
	failedTimeout       = 30 * time.Second
	keepaliveTimeout    = 15 * time.Second
	maxAcceptQueueLen   = 10
)

var (
	log        = logging.Logger("webrtcprivate")
	WebRTCAddr = ma.StringCast("/webrtc")
)

type transport struct {
	host                   host.Host
	rcmgr                  network.ResourceManager
	webrtcConfig           webrtc.Configuration
	gater                  connmgr.ConnectionGater
	maxInFlightConnections int

	mu       sync.Mutex
	listener *listener
}

var _ tpt.Transport = &transport{}

func AddTransport(h host.Host, gater connmgr.ConnectionGater, stunServers []webrtc.ICEServer) (*transport, error) {
	n, ok := h.Network().(tpt.TransportNetwork)
	if !ok {
		return nil, fmt.Errorf("%v is not a transport network", h.Network())
	}

	t, err := newTransport(h, gater, stunServers)
	if err != nil {
		return nil, err
	}

	if err := n.AddTransport(t); err != nil {
		return nil, fmt.Errorf("failed to add transport to network: %w", err)
	}

	if err := n.Listen(ma.StringCast("/webrtc")); err != nil {
		return nil, err
	}

	return t, nil
}

func newTransport(h host.Host, gater connmgr.ConnectionGater, stunServers []webrtc.ICEServer) (*transport, error) {
	// We use elliptic P-256 since it is widely supported by browsers.
	//
	// Implementation note: Testing with the browser,
	// it seems like Chromium only supports ECDSA P-256 or RSA key signatures in the webrtc TLS certificate.
	// We tried using P-228 and P-384 which caused the DTLS handshake to fail with Illegal Parameter
	//
	// Please refer to this is a list of suggested algorithms for the WebCrypto API.
	// The algorithm for generating a certificate for an RTCPeerConnection
	// must adhere to the WebCrpyto API. From my observation,
	// RSA and ECDSA P-256 is supported on almost all browsers.
	// Ed25519 is not present on the list.
	pk, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key for cert: %w", err)
	}
	cert, err := webrtc.GenerateCertificate(pk)
	if err != nil {
		return nil, fmt.Errorf("generate certificate: %w", err)
	}
	config := webrtc.Configuration{
		Certificates: []webrtc.Certificate{*cert},
		ICEServers:   stunServers,
	}

	return &transport{
		host:                   h,
		rcmgr:                  h.Network().ResourceManager(),
		webrtcConfig:           config,
		maxInFlightConnections: 16,
		gater:                  gater,
	}, nil
}

// CanDial determines if we can dial to an address
func (t *transport) CanDial(addr ma.Multiaddr) bool {
	circuit := false
	webrtc := false
	ma.ForEach(addr, func(c ma.Component) bool {
		if c.Protocol().Code == ma.P_CIRCUIT {
			circuit = true
			return true
		}
		// next element after p2p-circuit should be webrtc
		if circuit {
			webrtc = c.Protocol().Code == ma.P_WEBRTC
			return false
		}
		return true
	})
	return circuit && webrtc
}

func (t *transport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (tpt.CapableConn, error) {
	// Connect to the peer on the circuit address
	relayAddr := getRelayAddr(raddr)
	// We drop the ForceDirectDial option as we need a relayed connection before we can
	// setup a direct connection
	ctx = network.WithoutForceDirectDial(ctx)
	// We need this for the signaling stream
	ctx = network.WithUseTransient(ctx, "webrtcprivate dial")
	err := t.host.Connect(ctx, peer.AddrInfo{ID: p, Addrs: []ma.Multiaddr{relayAddr}})
	if err != nil {
		return nil, fmt.Errorf("failed to open %s stream: %w", SignalingProtocol, err)
	}

	scope, err := t.rcmgr.OpenConnection(network.DirOutbound, true, raddr)
	if err != nil {
		log.Debugw("resource manager blocked outgoing connection", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}
	if err := scope.SetPeer(p); err != nil {
		return nil, err
	}

	c, err := t.dialWithScope(ctx, p, scope)
	if err != nil {
		scope.Done()
		log.Debug(err)
		return nil, err
	}
	return c, nil
}

func (t *transport) dialWithScope(ctx context.Context, p peer.ID, scope network.ConnManagementScope) (tpt.CapableConn, error) {
	s, err := t.host.NewStream(ctx, p, SignalingProtocol)
	if err != nil {
		return nil, fmt.Errorf("error opening stream %s: %w", SignalingProtocol, err)
	}

	if err := s.Scope().SetService(name); err != nil {
		s.Reset()
		return nil, fmt.Errorf("error attaching signaling stream to %s transport: %w", name, err)
	}

	if err := s.Scope().ReserveMemory(2*maxMsgSize, network.ReservationPriorityAlways); err != nil {
		s.Reset()
		return nil, fmt.Errorf("error reserving memory for signaling stream: %w", err)
	}
	defer s.Scope().ReleaseMemory(maxMsgSize)
	defer s.Close()

	deadline := time.Now().Add(connectTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	s.SetDeadline(deadline)

	conn, err := t.setupConnection(ctx, s, scope)
	if err != nil {
		s.Reset()
		return nil, fmt.Errorf("error establishing webrtc.PeerConnection: %w", err)
	}

	if t.gater != nil && !t.gater.InterceptSecured(network.DirOutbound, p, conn) {
		conn.Close()
		s.Reset()
		return nil, fmt.Errorf("conn gater refused connection to addr: %s", conn.RemoteMultiaddr())
	}
	return conn, nil
}

func (t *transport) setupConnection(ctx context.Context, s network.Stream, scope network.ConnManagementScope) (tpt.CapableConn, error) {
	r := pbio.NewDelimitedReader(s, maxMsgSize)
	w := pbio.NewDelimitedWriter(s)

	pc, err := t.NewPeerConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to create webrtc.PeerConnection: %w", err)
	}

	dataChannelQueue := libp2pwebrtc.SetupDataChannelQueue(pc, maxAcceptQueueLen)

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
		// The callback can be called with a nil pointer
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
		msg := pb.Message{
			Type: pb.Message_ICE_CANDIDATE.Enum(),
			Data: &data,
		}
		if err = w.WriteMsg(&msg); err != nil {
			// We only want to write a single error on this channel
			select {
			case writeErr <- fmt.Errorf("failed to write candidate: %w", err):
			default:
			}
		}
	})

	// de-register candidate callback
	defer pc.OnICECandidate(func(_ *webrtc.ICECandidate) {})

	// We initialise a data channel otherwise the offer will have no ICE components
	// https://stackoverflow.com/a/38872920/759687
	// We use out-of-band negotiation(negotiated=true), to ensure that this channel doesn't
	// get accepted as a stream in AcceptStream on the remote side
	negotiated := true
	// Any value here is fine since this will be closed on connection establishment. We use 0 as
	// it is also used for the /webrtc-direct handshake channel
	var initStreamID uint16
	dc, err := pc.CreateDataChannel("init", &webrtc.DataChannelInit{Negotiated: &negotiated, ID: &initStreamID})
	if err != nil {
		return nil, fmt.Errorf("failed to create data channel: %w", err)
	}
	defer dc.Close()

	// create an offer
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create offer: %w", err)
	}
	msg := pb.Message{
		Type: pb.Message_SDP_OFFER.Enum(),
		Data: &offer.SDP,
	}
	// send offer to peer
	if err := w.WriteMsg(&msg); err != nil {
		return nil, fmt.Errorf("failed to write to stream: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return nil, fmt.Errorf("failed to set local description: %w", err)
	}

	// read an incoming answer
	if err := r.ReadMsg(&msg); err != nil {
		return nil, fmt.Errorf("failed to read from stream: %w", err)
	}
	if msg.Type == nil || *msg.Type != pb.Message_SDP_ANSWER {
		return nil, fmt.Errorf("invalid message: expected %s, got %s", pb.Message_SDP_ANSWER, msg.Type)
	}
	if msg.Data == nil || *msg.Data == "" {
		return nil, fmt.Errorf("invalid message: empty answer")
	}
	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  *msg.Data,
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		return nil, fmt.Errorf("failed to set remote description: %w", err)
	}

	readErr := make(chan error, 1)
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	// start a goroutine to read candidates
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}

			err := r.ReadMsg(&msg)
			if err == io.EOF {
				return
			}
			if err != nil {
				readErr <- fmt.Errorf("read failed: %w", err)
				return
			}
			if msg.Type == nil || *msg.Type != pb.Message_ICE_CANDIDATE {
				readErr <- fmt.Errorf("invalid message: expected %s got %s", pb.Message_ICE_CANDIDATE, msg.Type)
				return
			}
			// Ignore without erroring on empty message.
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
	case err := <-readErr:
		pc.Close()
		return nil, err
	case err := <-writeErr:
		pc.Close()
		return nil, err
	case state := <-connectionState:
		switch state {
		default:
			pc.Close()
			return nil, fmt.Errorf("conn establishment failed, state: %s", state)
		case webrtc.PeerConnectionStateConnected:
		}
	}
	localAddr, remoteAddr, err := getConnectionAddresses(pc)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to get connection addresses: %w", err)
	}

	conn, err := libp2pwebrtc.NewWebRTCConnection(
		network.DirOutbound,
		pc,
		t,
		scope,
		t.host.ID(),
		localAddr,
		s.Conn().RemotePeer(),
		t.host.Network().Peerstore().PubKey(s.Conn().RemotePeer()), // we have the pubkey from the relayed connection
		remoteAddr,
		dataChannelQueue,
	)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to create tpt.CapableConn: %w", err)
	}
	return conn, nil
}

func (t *transport) Listen(laddr ma.Multiaddr) (tpt.Listener, error) {
	if _, err := laddr.ValueForProtocol(ma.P_WEBRTC); err != nil {
		return nil, fmt.Errorf("invalid listen multiaddr: %s", laddr)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.listener != nil {
		return nil, errors.New("already listening on /webrtc")
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &listener{
		transport:     t,
		connQueue:     make(chan tpt.CapableConn),
		inflightQueue: make(chan struct{}, t.maxInFlightConnections),
		ctx:           ctx,
		cancel:        cancel,
	}
	t.listener = l
	t.host.SetStreamHandler(SignalingProtocol, l.handleSignalingStream)
	return l, nil
}

func (t *transport) RemoveListener(l *listener) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.listener == l {
		t.listener = nil
		t.host.RemoveStreamHandler(SignalingProtocol)
	}
}

func (*transport) Protocols() []int {
	return []int{ma.P_WEBRTC}
}

func (*transport) Proxy() bool {
	return false
}

func (t *transport) NewPeerConnection() (*webrtc.PeerConnection, error) {
	loggerFactory := pionlogger.NewDefaultLoggerFactory()
	logLevel := pionlogger.LogLevelDisabled
	switch log.Level() {
	case zapcore.DebugLevel:
		logLevel = pionlogger.LogLevelDebug
	case zapcore.InfoLevel:
		logLevel = pionlogger.LogLevelInfo
	case zapcore.WarnLevel:
		logLevel = pionlogger.LogLevelWarn
	case zapcore.ErrorLevel:
		logLevel = pionlogger.LogLevelError
	}
	loggerFactory.DefaultLogLevel = logLevel
	s := webrtc.SettingEngine{LoggerFactory: loggerFactory}
	s.SetICETimeouts(disconnectedTimeout, failedTimeout, keepaliveTimeout)
	s.DetachDataChannels()
	s.SetIncludeLoopbackCandidate(true)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(s))
	return api.NewPeerConnection(t.webrtcConfig)
}

// getRelayAddr removes /webrtc from addr and returns a circuit v2 only address
func getRelayAddr(addr ma.Multiaddr) ma.Multiaddr {
	first, rest := ma.SplitFunc(addr, func(c ma.Component) bool {
		return c.Protocol().Code == ma.P_WEBRTC
	})
	// remove /webrtc prefix
	_, rest = ma.SplitFirst(rest)
	if rest == nil {
		return first
	}
	return first.Encapsulate(rest)
}

// getConnectionAddresses provides multiaddresses on the two sides of the connection pc
func getConnectionAddresses(pc *webrtc.PeerConnection) (local ma.Multiaddr, remote ma.Multiaddr, err error) {
	if pc.SCTP() == nil {
		return nil, nil, errors.New("no sctp transport")
	}
	if pc.SCTP().Transport() == nil {
		return nil, nil, errors.New("no dtls transport")
	}
	if pc.SCTP().Transport().ICETransport() == nil {
		return nil, nil, errors.New("no ice transport")
	}
	cp, err := pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
	if cp == nil || err != nil {
		return nil, nil, fmt.Errorf("invalid candidate pair %s: %w", cp, err)
	}

	localAddr, err := manet.FromNetAddr(&net.UDPAddr{IP: net.ParseIP(cp.Local.Address), Port: int(cp.Local.Port)})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to infer local address from candidate %s: %w", cp, err)
	}
	localAddr = localAddr.Encapsulate(WebRTCAddr)

	remoteAddr, err := manet.FromNetAddr(&net.UDPAddr{IP: net.ParseIP(cp.Remote.Address), Port: int(cp.Remote.Port)})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to infer remote address from candidate %s: %w", cp, err)
	}
	remoteAddr = remoteAddr.Encapsulate(WebRTCAddr)

	return localAddr, remoteAddr, nil
}
