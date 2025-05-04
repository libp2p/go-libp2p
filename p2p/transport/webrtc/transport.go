//go:build !js
// +build !js

// Package libp2pwebrtc implements the WebRTC transport for go-libp2p,
// as described in https://github.com/libp2p/specs/tree/master/webrtc.

package libp2pwebrtc

import (
	"fmt"
	"net"

	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

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
