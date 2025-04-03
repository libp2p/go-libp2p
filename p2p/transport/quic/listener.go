package libp2pquic

import (
	"context"
	"errors"
	"net"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	p2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multiaddr/net"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/quic-go/quic-go"
)

// A listener listens for QUIC connections.
type listener struct {
	reuseListener   quicreuse.Listener
	transport       *transport
	rcmgr           network.ResourceManager
	privKey         ic.PrivKey
	localPeer       peer.ID
	localMultiaddrs map[quic.Version]ma.Multiaddr
}

func newListener(ln quicreuse.Listener, t *transport, localPeer peer.ID, key ic.PrivKey, rcmgr network.ResourceManager) (listener, error) {
	localMultiaddrs := make(map[quic.Version]ma.Multiaddr)
	for _, addr := range ln.Multiaddrs() {
		if _, err := addr.ValueForProtocol(ma.P_QUIC_V1); err == nil {
			localMultiaddrs[quic.Version1] = addr
		}
	}

	return listener{
		reuseListener:   ln,
		transport:       t,
		rcmgr:           rcmgr,
		privKey:         key,
		localPeer:       localPeer,
		localMultiaddrs: localMultiaddrs,
	}, nil
}

// Accept accepts new connections.
func (l *listener) Accept() (tpt.CapableConn, error) {
	for {
		qconn, err := l.reuseListener.Accept(context.Background())
		if err != nil {
			return nil, err
		}

		addr, err := manet.FromNetAddr(qconn.RemoteAddr())
		if err != nil {
			return nil, err
		}

		addr = addr.Encapsulate(ma.StringCast("/quic-v1"))
		addrKey := string(addr.Bytes())

		l.transport.pendingScopesMx.Lock()
		scopes, ok := l.transport.pendingScopes[addrKey]
		if !ok || len(scopes) == 0 {
			l.transport.pendingScopesMx.Unlock()
			continue
		}
		scope := scopes[0].Scope
		scopes = scopes[1:]
		if len(scopes) == 0 {
			delete(l.transport.pendingScopes, addrKey)
		} else {
			l.transport.pendingScopes[addrKey] = scopes
		}
		l.transport.pendingScopesMx.Unlock()

		c, err := l.wrapConnWithScope(qconn, scope, addr)
		if err != nil {
			log.Debugf("failed to setup connection: %s", err)
			scope.Done()
			qconn.CloseWithError(quic.ApplicationErrorCode(network.ConnResourceLimitExceeded), "")
			continue
		}
		l.transport.addConn(qconn, c)
		if l.transport.gater != nil && !(l.transport.gater.InterceptAccept(c) && l.transport.gater.InterceptSecured(network.DirInbound, c.remotePeerID, c)) {
			c.closeWithError(quic.ApplicationErrorCode(network.ConnGated), "connection gated")
			continue
		}

		// return through active hole punching if any
		key := holePunchKey{addr: qconn.RemoteAddr().String(), peer: c.remotePeerID}
		var wasHolePunch bool
		l.transport.holePunchingMx.Lock()
		holePunch, ok := l.transport.holePunching[key]
		if ok && !holePunch.fulfilled {
			holePunch.connCh <- c
			wasHolePunch = true
			holePunch.fulfilled = true
		}
		l.transport.holePunchingMx.Unlock()
		if wasHolePunch {
			continue
		}
		return c, nil
	}
}

func (l *listener) wrapConnWithScope(qconn quic.Connection, connScope network.ConnManagementScope, remoteMultiaddr ma.Multiaddr) (*conn, error) {
	// The tls.Config used to establish this connection already verified the certificate chain.
	// Since we don't have any way of knowing which tls.Config was used though,
	// we have to re-determine the peer's identity here.
	// Therefore, this is expected to never fail.
	remotePubKey, err := p2ptls.PubKeyFromCertChain(qconn.ConnectionState().TLS.PeerCertificates)
	if err != nil {
		return nil, err
	}
	remotePeerID, err := peer.IDFromPublicKey(remotePubKey)
	if err != nil {
		return nil, err
	}
	if err := connScope.SetPeer(remotePeerID); err != nil {
		log.Debugw("resource manager blocked incoming connection for peer", "peer", remotePeerID, "addr", qconn.RemoteAddr(), "error", err)
		return nil, err
	}

	localMultiaddr, found := l.localMultiaddrs[qconn.ConnectionState().Version]
	if !found {
		return nil, errors.New("unknown QUIC version:" + qconn.ConnectionState().Version.String())
	}

	return &conn{
		quicConn:        qconn,
		transport:       l.transport,
		scope:           connScope,
		localPeer:       l.localPeer,
		localMultiaddr:  localMultiaddr,
		remoteMultiaddr: remoteMultiaddr,
		remotePeerID:    remotePeerID,
		remotePubKey:    remotePubKey,
	}, nil
}

// Close closes the listener.
func (l *listener) Close() error {
	return l.reuseListener.Close()
}

// Addr returns the address of this listener.
func (l *listener) Addr() net.Addr {
	return l.reuseListener.Addr()
}
