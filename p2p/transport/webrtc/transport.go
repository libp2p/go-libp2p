// Package libp2pwebrtc implements the WebRTC transport for go-libp2p,
// as described in https://github.com/libp2p/specs/tree/master/webrtc.
//
// At this point, this package is EXPERIMENTAL, and the WebRTC transport is not enabled by default.
// While we're fairly confident that the implementation correctly implements the specification,
// we're not making any guarantees regarding its security (especially regarding resource exhaustion attacks).
// Fixes, even for security-related issues, will be conducted in the open.
//
// Experimentation is encouraged. Please open an issue if you encounter any problems with this transport.
//
// The udpmux subpackage contains the logic for multiplexing multiple WebRTC (ICE)
// connections over a single UDP socket.
package libp2pwebrtc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	mrand "golang.org/x/exp/rand"

	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/sec"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/security/noise"

	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/multiformats/go-multihash"

	pionlogger "github.com/pion/logging"
	"github.com/pion/webrtc/v3"
)

var log = logging.Logger("webrtc-transport")

var dialMatcher = mafmt.And(mafmt.UDP, mafmt.Base(ma.P_WEBRTC_DIRECT), mafmt.Base(ma.P_CERTHASH))

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
)

type WebRTCTransport struct {
	webrtcConfig webrtc.Configuration
	rcmgr        network.ResourceManager
	gater        connmgr.ConnectionGater
	privKey      ic.PrivKey
	noiseTpt     *noise.Transport
	localPeerId  peer.ID

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

func New(privKey ic.PrivKey, psk pnet.PSK, gater connmgr.ConnectionGater, rcmgr network.ResourceManager, opts ...Option) (*WebRTCTransport, error) {
	if psk != nil {
		log.Error("WebRTC doesn't support private networks yet.")
		return nil, fmt.Errorf("WebRTC doesn't support private networks yet")
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

func (t *WebRTCTransport) Protocols() []int {
	return []int{ma.P_WEBRTC_DIRECT}
}

func (t *WebRTCTransport) Proxy() bool {
	return false
}

func (t *WebRTCTransport) CanDial(addr ma.Multiaddr) bool {
	return dialMatcher.Matches(addr)
}

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

	socket, err := net.ListenUDP(nw, udpAddr)
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

func (t *WebRTCTransport) listenSocket(socket *net.UDPConn) (tpt.Listener, error) {
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
	listenerMultiaddr = listenerMultiaddr.Encapsulate(webrtcComponent).Encapsulate(certComp)

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
	var pc *webrtc.PeerConnection
	defer func() {
		if err != nil {
			if pc != nil {
				_ = pc.Close()
			}
			if tConn != nil {
				_ = tConn.Close()
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

	settingEngine := webrtc.SettingEngine{}
	// suppress pion logs
	loggerFactory := pionlogger.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel = pionlogger.LogLevelDisabled
	settingEngine.LoggerFactory = loggerFactory

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
	// it will not connect to anything. However, if it has any other interface
	// (even a private one, eg. 192.168.0.0/16), it will gather candidates on it and
	// will be able to connect to other pion instances on the same interface.
	settingEngine.SetIncludeLoopbackCandidate(true)

	api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))

	pc, err = api.NewPeerConnection(t.webrtcConfig)
	if err != nil {
		return nil, fmt.Errorf("instantiate peerconnection: %w", err)
	}

	errC := addOnConnectionStateChangeCallback(pc)
	// We need to set negotiated = true for this channel on both
	// the client and server to avoid DCEP errors.
	negotiated, id := handshakeChannelNegotiated, handshakeChannelID
	rawHandshakeChannel, err := pc.CreateDataChannel("", &webrtc.DataChannelInit{
		Negotiated: &negotiated,
		ID:         &id,
	})
	if err != nil {
		return nil, fmt.Errorf("create datachannel: %w", err)
	}

	// do offer-answer exchange
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return nil, fmt.Errorf("create offer: %w", err)
	}

	err = pc.SetLocalDescription(offer)
	if err != nil {
		return nil, fmt.Errorf("set local description: %w", err)
	}

	answerSDPString, err := createServerSDP(raddr, ufrag, *remoteMultihash)
	if err != nil {
		return nil, fmt.Errorf("render server SDP: %w", err)
	}

	answer := webrtc.SessionDescription{SDP: answerSDPString, Type: webrtc.SDPTypeAnswer}
	err = pc.SetRemoteDescription(answer)
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

	detached, err := getDetachedChannel(ctx, rawHandshakeChannel)
	if err != nil {
		return nil, err
	}
	// set the local address from the candidate pair
	cp, err := rawHandshakeChannel.Transport().Transport().ICETransport().GetSelectedCandidatePair()
	if cp == nil {
		return nil, errors.New("ice connection did not have selected candidate pair: nil result")
	}
	if err != nil {
		return nil, fmt.Errorf("ice connection did not have selected candidate pair: error: %w", err)
	}

	channel := newStream(rawHandshakeChannel, detached, func() {})
	// the local address of the selected candidate pair should be the
	// local address for the connection, since different datachannels
	// are multiplexed over the same SCTP connection
	localAddr, err := manet.FromNetAddr(&net.UDPAddr{IP: net.ParseIP(cp.Local.Address), Port: int(cp.Local.Port)})
	if err != nil {
		return nil, err
	}

	remoteMultiaddrWithoutCerthash, _ := ma.SplitFunc(remoteMultiaddr, func(c ma.Component) bool { return c.Protocol().Code == ma.P_CERTHASH })

	// we can only know the remote public key after the noise handshake,
	// but need to set up the callbacks on the peerconnection
	conn, err := newConnection(
		network.DirOutbound,
		pc,
		t,
		scope,
		t.localPeerId,
		localAddr,
		p,
		nil,
		remoteMultiaddrWithoutCerthash,
	)
	if err != nil {
		return nil, err
	}

	remotePubKey, err := t.noiseHandshake(ctx, pc, channel, p, remoteHashFunction, false)
	if err != nil {
		return conn, err
	}
	if t.gater != nil && !t.gater.InterceptSecured(network.DirOutbound, p, conn) {
		conn.Close()
		return nil, fmt.Errorf("secured connection gated")
	}
	conn.setRemotePublicKey(remotePubKey)
	return conn, nil
}

func genUfrag() string {
	const (
		uFragAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"
		uFragPrefix   = "libp2p+webrtc+v1/"
		uFragIdLength = 32
		uFragIdOffset = len(uFragPrefix)
		uFragLength   = uFragIdOffset + uFragIdLength
	)

	seed := [8]byte{}
	rand.Read(seed[:])
	r := mrand.New(mrand.NewSource(binary.BigEndian.Uint64(seed[:])))
	b := make([]byte, uFragLength)
	for i := uFragIdOffset; i < uFragLength; i++ {
		b[i] = uFragAlphabet[r.Intn(len(uFragAlphabet))]
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

func (t *WebRTCTransport) noiseHandshake(ctx context.Context, pc *webrtc.PeerConnection, datachannel *stream, peer peer.ID, hash crypto.Hash, inbound bool) (ic.PubKey, error) {
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
		secureConn, err = sessionTransport.SecureOutbound(ctx, fakeStreamConn{datachannel}, peer)
		if err != nil {
			return nil, fmt.Errorf("failed to secure inbound connection: %w", err)
		}
	} else {
		secureConn, err = sessionTransport.SecureInbound(ctx, fakeStreamConn{datachannel}, peer)
		if err != nil {
			return nil, fmt.Errorf("failed to secure outbound connection: %w", err)
		}
	}
	return secureConn.RemotePublicKey(), nil
}

type fakeStreamConn struct{ *stream }

func (fakeStreamConn) LocalAddr() net.Addr  { return nil }
func (fakeStreamConn) RemoteAddr() net.Addr { return nil }
