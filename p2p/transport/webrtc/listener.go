package libp2pwebrtc

import (
	"context"
	"crypto"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/internal"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/udpmux"

	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"

	"github.com/pion/ice/v2"
	pionlogger "github.com/pion/logging"
	"github.com/pion/webrtc/v3"
)

var _ tpt.Listener = &listener{}

const (
	candidateSetupTimeout         = 20 * time.Second
	DefaultMaxInFlightConnections = 10
)

type candidateAddr struct {
	ufrag string
	raddr *net.UDPAddr
}

type listener struct {
	transport *WebRTCTransport

	mux ice.UDPMux

	config                    webrtc.Configuration
	localFingerprint          webrtc.DTLSFingerprint
	localFingerprintMultibase string

	localAddr      net.Addr
	localMultiaddr ma.Multiaddr

	// buffered incoming connections
	acceptQueue chan tpt.CapableConn

	// Accepting a connection requires instantiating a peerconnection
	// and a noise connection which is expensive. We therefore limit
	// the number of in-flight connection requests. A connection
	// is considered to be in flight from the instant it is handled
	// until it is dequeued by a call to Accept, or errors out in some
	// way.
	inFlightInputQueue     chan candidateAddr
	maxInFlightConnections uint32

	// used to control the lifecycle of the listener
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newListener(transport *WebRTCTransport, laddr ma.Multiaddr, socket net.PacketConn, config webrtc.Configuration) (*listener, error) {
	localFingerprints, err := config.Certificates[0].GetFingerprints()
	if err != nil {
		return nil, err
	}

	localMh, err := hex.DecodeString(strings.ReplaceAll(localFingerprints[0].Value, ":", ""))
	if err != nil {
		return nil, err
	}
	localMhBuf, err := multihash.Encode(localMh, multihash.SHA2_256)
	if err != nil {
		return nil, err
	}
	localFpMultibase, err := multibase.Encode(multibase.Base64url, localMhBuf)
	if err != nil {
		return nil, err
	}

	inFlightQueueCh := make(chan candidateAddr, 1)

	ctx, cancel := context.WithCancel(context.Background())
	mux := udpmux.NewUDPMux(socket, func(ufrag string, addr net.Addr) bool {
		// Push to the inFlightQueue asynchronously to avoid blocking the mux goroutine
		// on candidates being processed. This can cause new connections to fail at high
		// throughput but will allow packets for existing connections to be processed.
		select {
		case inFlightQueueCh <- candidateAddr{ufrag: ufrag, raddr: addr.(*net.UDPAddr)}:
			return true
		default:
			log.Debug("candidate chan full, dropping incoming candidate")
			return false
		}
	})

	l := &listener{
		mux:                       mux,
		transport:                 transport,
		config:                    config,
		localFingerprint:          localFingerprints[0],
		localFingerprintMultibase: localFpMultibase,
		localMultiaddr:            laddr,
		ctx:                       ctx,
		cancel:                    cancel,
		localAddr:                 socket.LocalAddr(),
		acceptQueue:               make(chan tpt.CapableConn, transport.maxInFlightConnections-1),
		inFlightInputQueue:        inFlightQueueCh,
		maxInFlightConnections:    transport.maxInFlightConnections,
	}

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.inFlightWorker()
	}()

	return l, err
}

func (l *listener) inFlightWorker() {
	for {
		select {
		case <-l.ctx.Done():
			return

		case addr := <-l.inFlightInputQueue:
			if !l.handleInFlightInput(&addr) {
				return
			}
		}
	}
}

func (l *listener) handleInFlightInput(addr *candidateAddr) bool {
	ctx, cancel := context.WithTimeout(l.ctx, candidateSetupTimeout)
	defer cancel()

	conn, err := l.handleCandidate(ctx, addr)
	if err != nil {
		log.Debugf("could not accept connection: %s: %v", addr.ufrag, err)
		return false
	}
	select {
	case <-ctx.Done():
		log.Warn("could not push connection: ctx done")
		conn.Close()
	case l.acceptQueue <- conn:
		// block until the connection is accepted,
		// or until we are done, this effectively blocks our in flight from continuing to progress
	}

	return true
}

func (l *listener) handleCandidate(ctx context.Context, addr *candidateAddr) (tpt.CapableConn, error) {
	remoteMultiaddr, err := manet.FromNetAddr(addr.raddr)
	if err != nil {
		return nil, err
	}
	scope, err := l.transport.rcmgr.OpenConnection(network.DirInbound, false, remoteMultiaddr)
	if err != nil {
		return nil, err
	}
	conn, err := l.setupConnection(ctx, scope, remoteMultiaddr, addr)
	if err != nil {
		scope.Done()
		return nil, err
	}
	return conn, nil
}

func (l *listener) setupConnection(
	ctx context.Context, scope network.ConnManagementScope,
	remoteMultiaddr ma.Multiaddr, addr *candidateAddr,
) (tConn tpt.CapableConn, err error) {
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

	settingEngine := webrtc.SettingEngine{}

	// suppress pion logs
	loggerFactory := pionlogger.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel = pionlogger.LogLevelWarn
	settingEngine.LoggerFactory = loggerFactory

	settingEngine.SetAnsweringDTLSRole(webrtc.DTLSRoleServer)
	settingEngine.SetICECredentials(addr.ufrag, addr.ufrag)
	settingEngine.SetLite(true)
	settingEngine.SetICEUDPMux(l.mux)
	settingEngine.SetIncludeLoopbackCandidate(true)
	settingEngine.DisableCertificateFingerprintVerification(true)
	settingEngine.SetICETimeouts(
		l.transport.peerConnectionTimeouts.Disconnect,
		l.transport.peerConnectionTimeouts.Failed,
		l.transport.peerConnectionTimeouts.Keepalive,
	)
	settingEngine.DetachDataChannels()

	api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))

	pc, err = api.NewPeerConnection(l.config)
	if err != nil {
		return nil, err
	}

	negotiated, id := handshakeChannelNegotiated, handshakeChannelID
	rawDatachannel, err := pc.CreateDataChannel("", &webrtc.DataChannelInit{
		Negotiated: &negotiated,
		ID:         &id,
	})
	if err != nil {
		return nil, err
	}

	errC := awaitPeerConnectionOpen(addr.ufrag, pc)
	// we infer the client sdp from the incoming STUN connectivity check
	// by setting the ice-ufrag equal to the incoming check.
	clientSdpString := internal.RenderClientSDP(addr.raddr, addr.ufrag)
	clientSdp := webrtc.SessionDescription{SDP: clientSdpString, Type: webrtc.SDPTypeOffer}
	pc.SetRemoteDescription(clientSdp)

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return nil, err
	}

	err = pc.SetLocalDescription(answer)
	if err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errC:
		if err != nil {
			return nil, fmt.Errorf("peer connection error: %w", err)
		}

	}

	rwc, err := getDetachedChannel(ctx, rawDatachannel)
	if err != nil {
		return nil, err
	}

	handshakeChannel := newStream(nil, rawDatachannel, rwc, l.localAddr, addr.raddr)
	// The connection is instantiated before performing the Noise handshake. This is
	// to handle the case where the remote is faster and attempts to initiate a stream
	// before the ondatachannel callback can be set.
	conn, err := NewWebRTCConnection(
		network.DirInbound,
		pc,
		l.transport,
		scope,
		l.transport.localPeerId,
		l.localMultiaddr,
		"",  // remotePeer
		nil, // remoteKey
		remoteMultiaddr,
	)
	if err != nil {
		return nil, err
	}

	// we do not yet know A's peer ID so accept any inbound
	secureConn, err := l.transport.noiseHandshake(ctx, pc, handshakeChannel, "", crypto.SHA256, true)
	if err != nil {
		return nil, err
	}

	// earliest point where we know the remote's peerID
	err = scope.SetPeer(secureConn.RemotePeer())
	if err != nil {
		return nil, err
	}

	conn.setRemotePeer(secureConn.RemotePeer())
	conn.setRemotePublicKey(secureConn.RemotePublicKey())

	return conn, err
}

func (l *listener) Accept() (tpt.CapableConn, error) {
	select {
	case <-l.ctx.Done():
		return nil, os.ErrClosed
	case conn := <-l.acceptQueue:
		return conn, nil
	}
}

func (l *listener) Close() error {
	select {
	case <-l.ctx.Done():
	default:
		l.cancel()
		l.wg.Wait()
	}
	return nil
}

func (l *listener) Addr() net.Addr {
	return l.localAddr
}

func (l *listener) Multiaddr() ma.Multiaddr {
	return l.localMultiaddr
}

func awaitPeerConnectionOpen(ufrag string, pc *webrtc.PeerConnection) <-chan error {
	errC := make(chan error, 1)
	var once sync.Once
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			once.Do(func() { close(errC) })
		case webrtc.PeerConnectionStateFailed:
			once.Do(func() {
				errC <- fmt.Errorf("peerconnection failed: %s", ufrag)
				close(errC)
			})
		case webrtc.PeerConnectionStateDisconnected:
			// the connection can move to a disconnected state and back to a connected state without ICE renegotiation.
			// This could happen when underlying UDP packets are lost, and therefore the connection moves to the disconnected state.
			// If the connection then receives packets on the connection, it can move back to the connected state.
			// If no packets are received until the failed timeout is triggered, the connection moves to the failed state.
			log.Warn("peerconnection disconnected")
		}
	})
	return errC
}
