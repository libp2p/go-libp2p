package libp2pwebrtc

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/udpmux"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/pion/webrtc/v4"
)

const (
	candidateSetupTimeout = 10 * time.Second
	// This is higher than other transports(64) as there's no way to detect a peer that has gone away after
	// sending the initial connection request message(STUN Binding request). Such peers take up a goroutine
	// till connection timeout. As the number of handshakes in parallel is still guarded by the resource
	// manager, this higher number is okay.
	DefaultMaxInFlightConnections = 128
)

type connMultiaddrs struct {
	local, remote ma.Multiaddr
}

var _ network.ConnMultiaddrs = &connMultiaddrs{}

func (c *connMultiaddrs) LocalMultiaddr() ma.Multiaddr  { return c.local }
func (c *connMultiaddrs) RemoteMultiaddr() ma.Multiaddr { return c.remote }

type listener struct {
	transport *WebRTCTransport

	mux *udpmux.UDPMux

	config                    webrtc.Configuration
	localFingerprint          webrtc.DTLSFingerprint
	localFingerprintMultibase string

	localAddr      net.Addr
	localMultiaddr ma.Multiaddr

	// buffered incoming connections
	acceptQueue chan tpt.CapableConn

	// used to control the lifecycle of the listener
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var _ tpt.Listener = &listener{}

func (l *listener) Accept() (tpt.CapableConn, error) {
	select {
	case <-l.ctx.Done():
		return nil, tpt.ErrListenerClosed
	case conn := <-l.acceptQueue:
		return conn, nil
	}
}

func (l *listener) Addr() net.Addr {
	return l.localAddr
}

func (l *listener) Multiaddr() ma.Multiaddr {
	return l.localMultiaddr
}

func (l *listener) Close() error {
	select {
	case <-l.ctx.Done():
	default:
	}
	l.cancel()
	l.mux.Close()
	l.wg.Wait()
loop:
	for {
		select {
		case conn := <-l.acceptQueue:
			conn.Close()
		default:
			break loop
		}
	}
	return nil
}

// addOnConnectionStateChangeCallback adds the OnConnectionStateChange to the PeerConnection.
// The channel returned here:
// * is closed when the state changes to Connection
// * receives an error when the state changes to Failed or Closed or Disconnected
func addOnConnectionStateChangeCallback(pc *webrtc.PeerConnection) <-chan error {
	errC := make(chan error, 1)
	var once sync.Once
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch pc.ConnectionState() {
		case webrtc.PeerConnectionStateConnected:
			once.Do(func() { close(errC) })
		// PeerConnectionStateFailed happens when we fail to negotiate the connection.
		// PeerConnectionStateDisconnected happens when we disconnect immediately after connecting.
		// PeerConnectionStateClosed happens when we close the peer connection locally, not when remote closes. We don't need
		// to error in this case, but it's a no-op, so it doesn't hurt.
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			once.Do(func() {
				errC <- errors.New("peerconnection failed")
				close(errC)
			})
		}
	})
	return errC
}
