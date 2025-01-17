// Package libp2pwebrtc implements the WebRTC transport for go-libp2p,
// as described in https://github.com/libp2p/specs/tree/master/webrtc.
package libp2pwebrtc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"

	mrand "math/rand/v2"

	"google.golang.org/protobuf/proto"

	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/sec"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"
	"github.com/libp2p/go-msgio"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/multiformats/go-multihash"

	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v4"
)

var webrtcComponent *ma.Component

func init() {
	var err error
	webrtcComponent, err = ma.NewComponent(ma.ProtocolWithCode(ma.P_WEBRTC_DIRECT).Name, "")
	if err != nil {
		log.Fatal(err)
	}
}

const (
	// handshakeChannelNegotiated is used to specify that the
	// handshake data channel does not need negotiation via DCEP.
	// A constant is used since the `DataChannelInit` struct takes
	// references instead of values.
	handshakeChannelNegotiated = true
	// handshakeChannelID is the agreed ID for the handshake data
	// channel. A constant is used since the `DataChannelInit` struct takes
	// references instead of values. We specify the type here as this
	// value is only ever copied and passed by reference
	handshakeChannelID = uint16(0)
)

// timeout values for the peerconnection
// https://github.com/pion/webrtc/blob/v3.1.50/settingengine.go#L102-L109
const (
	DefaultDisconnectedTimeout = 20 * time.Second
	DefaultFailedTimeout       = 30 * time.Second
	DefaultKeepaliveTimeout    = 15 * time.Second

	sctpReceiveBufferSize = 100_000
)

type WebRTCTransport struct {
	webrtcConfig webrtc.Configuration
	rcmgr        network.ResourceManager
	gater        connmgr.ConnectionGater
	privKey      ic.PrivKey
	noiseTpt     *noise.Transport
	localPeerId  peer.ID

	listenUDP func(network string, laddr *net.UDPAddr) (net.PacketConn, error)

	// timeouts
	peerConnectionTimeouts iceTimeouts

	// in-flight connections
	maxInFlightConnections uint32
}

var _ tpt.Transport = &WebRTCTransport{}

type Option func(*WebRTCTransport) error

type iceTimeouts struct {
	Disconnect time.Duration
	Failed     time.Duration
	Keepalive  time.Duration
}

type ListenUDPFn func(network string, laddr *net.UDPAddr) (net.PacketConn, error)

func New(privKey ic.PrivKey, psk pnet.PSK, gater connmgr.ConnectionGater, rcmgr network.ResourceManager, listenUDP ListenUDPFn, opts ...Option) (*WebRTCTransport, error) {
	if psk != nil {
		log.Error("WebRTC doesn't support private networks yet.")
		return nil, fmt.Errorf("WebRTC doesn't support private networks yet")
	}
	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}
	localPeerID, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("get local peer ID: %w", err)
	}
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
	}
	noiseTpt, err := noise.New(noise.ID, privKey, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create noise transport: %w", err)
	}
	transport := &WebRTCTransport{
		rcmgr:        rcmgr,
		gater:        gater,
		webrtcConfig: config,
		privKey:      privKey,
		noiseTpt:     noiseTpt,
		localPeerId:  localPeerID,

		listenUDP: listenUDP,
		peerConnectionTimeouts: iceTimeouts{
			Disconnect: DefaultDisconnectedTimeout,
			Failed:     DefaultFailedTimeout,
			Keepalive:  DefaultKeepaliveTimeout,
		},

		maxInFlightConnections: DefaultMaxInFlightConnections,
	}
	for _, opt := range opts {
		if err := opt(transport); err != nil {
			return nil, err
		}
	}
	return transport, nil
}

func (t *WebRTCTransport) ListenOrder() int {
	return libp2pquic.ListenOrder + 1 // We want to listen after QUIC listens so we can possibly reuse the same port.
}

func (t *WebRTCTransport) Protocols() []int {
	return []int{ma.P_WEBRTC_DIRECT}
}

func (t *WebRTCTransport) Proxy() bool {
	return false
}

func (t *WebRTCTransport) CanDial(addr ma.Multiaddr) bool {
	isValid, n := IsWebRTCDirectMultiaddr(addr)
	return isValid && n > 0
}

// Listen returns a listener for addr.
//
// The IP, Port combination for addr must be exclusive to this listener as a WebRTC listener cannot
// be multiplexed on the same port as other UDP based transports like QUIC and WebTransport.
// See https://github.com/libp2p/go-libp2p/issues/2446 for details.
func (t *WebRTCTransport) Listen(addr ma.Multiaddr) (tpt.Listener, error) {
	addr, wrtcComponent := ma.SplitLast(addr)
	isWebrtc := wrtcComponent.Equal(webrtcComponent)
	if !isWebrtc {
		return nil, fmt.Errorf("must listen on webrtc multiaddr")
	}
	nw, host, err := manet.DialArgs(addr)
	if err != nil {
		return nil, fmt.Errorf("listener could not fetch dialargs: %w", err)
	}
	udpAddr, err := net.ResolveUDPAddr(nw, host)
	if err != nil {
		return nil, fmt.Errorf("listener could not resolve udp address: %w", err)
	}

	socket, err := t.listenUDP(nw, udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on udp: %w", err)
	}

	listener, err := t.listenSocket(socket)
	if err != nil {
		socket.Close()
		return nil, err
	}
	return listener, nil
}

func (t *WebRTCTransport) listenSocket(socket net.PacketConn) (tpt.Listener, error) {
	listenerMultiaddr, err := manet.FromNetAddr(socket.LocalAddr())
	if err != nil {
		return nil, err
	}

	listenerFingerprint, err := t.getCertificateFingerprint()
	if err != nil {
		return nil, err
	}

	encodedLocalFingerprint, err := encodeDTLSFingerprint(listenerFingerprint)
	if err != nil {
		return nil, err
	}

	certComp, err := ma.NewComponent(ma.ProtocolWithCode(ma.P_CERTHASH).Name, encodedLocalFingerprint)
	if err != nil {
		return nil, err
	}
	listenerMultiaddr = listenerMultiaddr.AppendComponent(webrtcComponent, certComp)

	return newListener(
		t,
		listenerMultiaddr,
		socket,
		t.webrtcConfig,
	)
}

func (t *WebRTCTransport) Dial(ctx context.Context, remoteMultiaddr ma.Multiaddr, p peer.ID) (tpt.CapableConn, error) {
	scope, err := t.rcmgr.OpenConnection(network.DirOutbound, false, remoteMultiaddr)
	if err != nil {
		return nil, err
	}
	if err := scope.SetPeer(p); err != nil {
		scope.Done()
		return nil, err
	}
	conn, err := t.dial(ctx, scope, remoteMultiaddr, p)
	if err != nil {
		scope.Done()
		return nil, err
	}
	return conn, nil
}

func (t *WebRTCTransport) dial(ctx context.Context, scope network.ConnManagementScope, remoteMultiaddr ma.Multiaddr, p peer.ID) (tConn tpt.CapableConn, err error) {
	var w webRTCConnection
	defer func() {
		if err != nil {
			if w.PeerConnection != nil {
				_ = w.PeerConnection.Close()
			}
			if tConn != nil {
				_ = tConn.Close()
				tConn = nil
			}
		}
	}()

	remoteMultihash, err := decodeRemoteFingerprint(remoteMultiaddr)
	if err != nil {
		return nil, fmt.Errorf("decode fingerprint: %w", err)
	}
	remoteHashFunction, ok := getSupportedSDPHash(remoteMultihash.Code)
	if !ok {
		return nil, fmt.Errorf("unsupported hash function: %w", nil)
	}

	rnw, rhost, err := manet.DialArgs(remoteMultiaddr)
	if err != nil {
		return nil, fmt.Errorf("generate dial args: %w", err)
	}

	raddr, err := net.ResolveUDPAddr(rnw, rhost)
	if err != nil {
		return nil, fmt.Errorf("resolve udp address: %w", err)
	}

	// Instead of encoding the local fingerprint we
	// generate a random UUID as the connection ufrag.
	// The only requirement here is that the ufrag and password
	// must be equal, which will allow the server to determine
	// the password using the STUN message.
	ufrag := genUfrag()

	settingEngine := webrtc.SettingEngine{
		LoggerFactory: pionLoggerFactory,
	}
	settingEngine.SetICECredentials(ufrag, ufrag)
	settingEngine.DetachDataChannels()
	// use the first best address candidate
	settingEngine.SetPrflxAcceptanceMinWait(0)
	settingEngine.SetICETimeouts(
		t.peerConnectionTimeouts.Disconnect,
		t.peerConnectionTimeouts.Failed,
		t.peerConnectionTimeouts.Keepalive,
	)
	// By default, webrtc will not collect candidates on the loopback address.
	// This is disallowed in the ICE specification. However, implementations
	// do not strictly follow this, for eg. Chrome gathers TCP loopback candidates.
	// If you run pion on a system with only the loopback interface UP,
	// it will not connect to anything.
	settingEngine.SetIncludeLoopbackCandidate(true)
	settingEngine.SetSCTPMaxReceiveBufferSize(sctpReceiveBufferSize)
	if err := scope.ReserveMemory(sctpReceiveBufferSize, network.ReservationPriorityMedium); err != nil {
		return nil, err
	}

	w, err = newWebRTCConnection(settingEngine, t.webrtcConfig)
	if err != nil {
		return nil, fmt.Errorf("instantiating peer connection failed: %w", err)
	}

	errC := addOnConnectionStateChangeCallback(w.PeerConnection)

	// do offer-answer exchange
	offer, err := w.PeerConnection.CreateOffer(nil)
	if err != nil {
		return nil, fmt.Errorf("create offer: %w", err)
	}

	err = w.PeerConnection.SetLocalDescription(offer)
	if err != nil {
		return nil, fmt.Errorf("set local description: %w", err)
	}

	answerSDPString, err := createServerSDP(raddr, ufrag, *remoteMultihash)
	if err != nil {
		return nil, fmt.Errorf("render server SDP: %w", err)
	}

	answer := webrtc.SessionDescription{SDP: answerSDPString, Type: webrtc.SDPTypeAnswer}
	err = w.PeerConnection.SetRemoteDescription(answer)
	if err != nil {
		return nil, fmt.Errorf("set remote description: %w", err)
	}

	// await peerconnection opening
	select {
	case err := <-errC:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, errors.New("peerconnection opening timed out")
	}

	// We are connected, run the noise handshake
	detached, err := detachHandshakeDataChannel(ctx, w.HandshakeDataChannel)
	if err != nil {
		return nil, err
	}
	channel := newStream(w.HandshakeDataChannel, detached, func() {})

	remotePubKey, err := t.noiseHandshake(ctx, w.PeerConnection, channel, p, remoteHashFunction, false)
	if err != nil {
		return nil, err
	}

	// Setup local and remote address for the connection
	cp, err := w.HandshakeDataChannel.Transport().Transport().ICETransport().GetSelectedCandidatePair()
	if cp == nil {
		return nil, errors.New("ice connection did not have selected candidate pair: nil result")
	}
	if err != nil {
		return nil, fmt.Errorf("ice connection did not have selected candidate pair: error: %w", err)
	}
	// the local address of the selected candidate pair should be the local address for the connection
	localAddr, err := manet.FromNetAddr(&net.UDPAddr{IP: net.ParseIP(cp.Local.Address), Port: int(cp.Local.Port)})
	if err != nil {
		return nil, err
	}
	remoteMultiaddrWithoutCerthash, _ := ma.SplitFunc(remoteMultiaddr, func(c ma.Component) bool { return c.Protocol().Code == ma.P_CERTHASH })

	conn, err := newConnection(
		network.DirOutbound,
		w.PeerConnection,
		t,
		scope,
		t.localPeerId,
		localAddr,
		p,
		remotePubKey,
		remoteMultiaddrWithoutCerthash,
		w.IncomingDataChannels,
		w.PeerConnectionClosedCh,
	)
	if err != nil {
		return nil, err
	}

	if t.gater != nil && !t.gater.InterceptSecured(network.DirOutbound, p, conn) {
		return nil, fmt.Errorf("secured connection gated")
	}
	return conn, nil
}

func genUfrag() string {
	const (
		uFragAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"
		uFragPrefix   = "libp2p+webrtc+v1/"
		uFragIdLength = 32
		uFragLength   = len(uFragPrefix) + uFragIdLength
	)

	seed := [32]byte{}
	rand.Read(seed[:])
	r := mrand.New(mrand.New(mrand.NewChaCha8(seed)))
	b := make([]byte, uFragLength)
	for i := 0; i < len(uFragPrefix); i++ {
		b[i] = uFragPrefix[i]
	}
	for i := len(uFragPrefix); i < uFragLength; i++ {
		b[i] = uFragAlphabet[r.IntN(len(uFragAlphabet))]
	}
	return string(b)
}

func (t *WebRTCTransport) getCertificateFingerprint() (webrtc.DTLSFingerprint, error) {
	fps, err := t.webrtcConfig.Certificates[0].GetFingerprints()
	if err != nil {
		return webrtc.DTLSFingerprint{}, err
	}
	return fps[0], nil
}

func (t *WebRTCTransport) generateNoisePrologue(pc *webrtc.PeerConnection, hash crypto.Hash, inbound bool) ([]byte, error) {
	raw := pc.SCTP().Transport().GetRemoteCertificate()
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		return nil, err
	}

	// NOTE: should we want we can fork the cert code as well to avoid
	// all the extra allocations due to unneeded string interspersing (hex)
	localFp, err := t.getCertificateFingerprint()
	if err != nil {
		return nil, err
	}

	remoteFpBytes, err := parseFingerprint(cert, hash)
	if err != nil {
		return nil, err
	}

	localFpBytes, err := decodeInterspersedHexFromASCIIString(localFp.Value)
	if err != nil {
		return nil, err
	}

	localEncoded, err := multihash.Encode(localFpBytes, multihash.SHA2_256)
	if err != nil {
		log.Debugf("could not encode multihash for local fingerprint")
		return nil, err
	}
	remoteEncoded, err := multihash.Encode(remoteFpBytes, multihash.SHA2_256)
	if err != nil {
		log.Debugf("could not encode multihash for remote fingerprint")
		return nil, err
	}

	result := []byte("libp2p-webrtc-noise:")
	if inbound {
		result = append(result, remoteEncoded...)
		result = append(result, localEncoded...)
	} else {
		result = append(result, localEncoded...)
		result = append(result, remoteEncoded...)
	}
	return result, nil
}

func (t *WebRTCTransport) noiseHandshake(ctx context.Context, pc *webrtc.PeerConnection, s *stream, peer peer.ID, hash crypto.Hash, inbound bool) (ic.PubKey, error) {
	prologue, err := t.generateNoisePrologue(pc, hash, inbound)
	if err != nil {
		return nil, fmt.Errorf("generate prologue: %w", err)
	}
	opts := make([]noise.SessionOption, 0, 2)
	opts = append(opts, noise.Prologue(prologue))
	if peer == "" {
		opts = append(opts, noise.DisablePeerIDCheck())
	}
	sessionTransport, err := t.noiseTpt.WithSessionOptions(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate Noise transport: %w", err)
	}
	var secureConn sec.SecureConn
	if inbound {
		secureConn, err = sessionTransport.SecureOutbound(ctx, netConnWrapper{s}, peer)
		if err != nil {
			return nil, fmt.Errorf("failed to secure inbound connection: %w", err)
		}
	} else {
		secureConn, err = sessionTransport.SecureInbound(ctx, netConnWrapper{s}, peer)
		if err != nil {
			return nil, fmt.Errorf("failed to secure outbound connection: %w", err)
		}
	}
	return secureConn.RemotePublicKey(), nil
}

func (t *WebRTCTransport) AddCertHashes(addr ma.Multiaddr) (ma.Multiaddr, bool) {
	listenerFingerprint, err := t.getCertificateFingerprint()
	if err != nil {
		return nil, false
	}

	encodedLocalFingerprint, err := encodeDTLSFingerprint(listenerFingerprint)
	if err != nil {
		return nil, false
	}

	certComp, err := ma.NewComponent(ma.ProtocolWithCode(ma.P_CERTHASH).Name, encodedLocalFingerprint)
	if err != nil {
		return nil, false
	}
	return addr.AppendComponent(certComp), true
}

type netConnWrapper struct {
	*stream
}

func (netConnWrapper) LocalAddr() net.Addr  { return nil }
func (netConnWrapper) RemoteAddr() net.Addr { return nil }
func (w netConnWrapper) Close() error {
	// Close called while running the security handshake is an error and we should Reset the
	// stream in that case rather than gracefully closing
	w.stream.Reset()
	return nil
}

// detachHandshakeDataChannel detaches the handshake data channel
func detachHandshakeDataChannel(ctx context.Context, dc *webrtc.DataChannel) (datachannel.ReadWriteCloser, error) {
	done := make(chan struct{})
	var rwc datachannel.ReadWriteCloser
	var err error
	dc.OnOpen(func() {
		defer close(done)
		rwc, err = dc.Detach()
	})
	// this is safe since for detached datachannels, the peerconnection runs the onOpen
	// callback immediately if the SCTP transport is also connected.
	select {
	case <-done:
		return rwc, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// webRTCConnection holds the webrtc.PeerConnection with the handshake channel and the queue for
// incoming data channels created by the peer.
//
// When creating a webrtc.PeerConnection, It is important to set the OnDataChannel handler upfront
// before connecting with the peer. If the handler's set up after connecting with the peer, there's
// a small window of time where datachannels created by the peer may not surface to us and cause a
// memory leak.
type webRTCConnection struct {
	PeerConnection         *webrtc.PeerConnection
	HandshakeDataChannel   *webrtc.DataChannel
	IncomingDataChannels   chan dataChannel
	PeerConnectionClosedCh chan struct{}
}

func newWebRTCConnection(settings webrtc.SettingEngine, config webrtc.Configuration) (webRTCConnection, error) {
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settings))
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return webRTCConnection{}, fmt.Errorf("failed to create peer connection: %w", err)
	}

	negotiated, id := handshakeChannelNegotiated, handshakeChannelID
	handshakeDataChannel, err := pc.CreateDataChannel("", &webrtc.DataChannelInit{
		Negotiated: &negotiated,
		ID:         &id,
	})
	if err != nil {
		pc.Close()
		return webRTCConnection{}, fmt.Errorf("failed to create handshake channel: %w", err)
	}

	incomingDataChannels := make(chan dataChannel, maxAcceptQueueLen)
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() {
			rwc, err := dc.Detach()
			if err != nil {
				log.Warnf("could not detach datachannel: id: %d", *dc.ID())
				return
			}
			select {
			case incomingDataChannels <- dataChannel{rwc, dc}:
			default:
				log.Warnf("connection busy, rejecting stream")
				b, _ := proto.Marshal(&pb.Message{Flag: pb.Message_RESET.Enum()})
				w := msgio.NewWriter(rwc)
				w.WriteMsg(b)
				rwc.Close()
			}
		})
	})

	connectionClosedCh := make(chan struct{}, 1)
	pc.SCTP().OnClose(func(err error) {
		// We only need one message. Closing a connection is a problem as pion might invoke the callback more than once.
		select {
		case connectionClosedCh <- struct{}{}:
		default:
		}
	})
	return webRTCConnection{
		PeerConnection:         pc,
		HandshakeDataChannel:   handshakeDataChannel,
		IncomingDataChannels:   incomingDataChannels,
		PeerConnectionClosedCh: connectionClosedCh,
	}, nil
}

// IsWebRTCDirectMultiaddr returns whether addr is a /webrtc-direct multiaddr with the count of certhashes
// in addr
func IsWebRTCDirectMultiaddr(addr ma.Multiaddr) (bool, int) {
	var foundUDP, foundWebRTC bool
	certHashCount := 0
	ma.ForEach(addr, func(c ma.Component) bool {
		if !foundUDP {
			if c.Protocol().Code == ma.P_UDP {
				foundUDP = true
			}
			return true
		}
		if !foundWebRTC && foundUDP {
			// protocol after udp must be webrtc-direct
			if c.Protocol().Code != ma.P_WEBRTC_DIRECT {
				return false
			}
			foundWebRTC = true
			return true
		}
		if foundWebRTC {
			if c.Protocol().Code == ma.P_CERTHASH {
				certHashCount++
			} else {
				return false
			}
		}
		return true
	})
	return foundUDP && foundWebRTC, certHashCount
}
