package quicreuse

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/quic-go/quic-go"
	quiclogging "github.com/quic-go/quic-go/logging"
)

type ConnManager struct {
	reuseUDP4       *reuse
	reuseUDP6       *reuse
	enableReuseport bool
	enableMetrics   bool

	serverConfig *quic.Config
	clientConfig *quic.Config

	quicListenersMu sync.Mutex
	quicListeners   map[string]quicListenerEntry

	srk      quic.StatelessResetKey
	tokenKey quic.TokenGeneratorKey
}

type quicListenerEntry struct {
	refCount int
	ln       *quicListener
}

func NewConnManager(statelessResetKey quic.StatelessResetKey, tokenKey quic.TokenGeneratorKey, opts ...Option) (*ConnManager, error) {
	cm := &ConnManager{
		enableReuseport: true,
		quicListeners:   make(map[string]quicListenerEntry),
		srk:             statelessResetKey,
		tokenKey:        tokenKey,
	}
	for _, o := range opts {
		if err := o(cm); err != nil {
			return nil, err
		}
	}

	quicConf := quicConfig.Clone()

	quicConf.Tracer = func(ctx context.Context, p quiclogging.Perspective, ci quic.ConnectionID) *quiclogging.ConnectionTracer {
		var tracer *quiclogging.ConnectionTracer
		if qlogTracerDir != "" {
			tracer = qloggerForDir(qlogTracerDir, p, ci)
		}
		return tracer
	}
	serverConfig := quicConf.Clone()

	cm.clientConfig = quicConf
	cm.serverConfig = serverConfig
	if cm.enableReuseport {
		cm.reuseUDP4 = newReuse(&statelessResetKey, &tokenKey)
		cm.reuseUDP6 = newReuse(&statelessResetKey, &tokenKey)
	}
	return cm, nil
}

func (c *ConnManager) getReuse(network string) (*reuse, error) {
	switch network {
	case "udp4":
		return c.reuseUDP4, nil
	case "udp6":
		return c.reuseUDP6, nil
	default:
		return nil, errors.New("invalid network: must be either udp4 or udp6")
	}
}

func (c *ConnManager) ListenQUIC(addr ma.Multiaddr, tlsConf *tls.Config, allowWindowIncrease func(conn quic.Connection, delta uint64) bool) (Listener, error) {
	netw, host, err := manet.DialArgs(addr)
	if err != nil {
		return nil, err
	}
	laddr, err := net.ResolveUDPAddr(netw, host)
	if err != nil {
		return nil, err
	}

	c.quicListenersMu.Lock()
	defer c.quicListenersMu.Unlock()

	key := laddr.String()
	entry, ok := c.quicListeners[key]
	if !ok {
		tr, err := c.transportForListen(netw, laddr)
		if err != nil {
			return nil, err
		}
		ln, err := newQuicListener(tr, c.serverConfig)
		if err != nil {
			return nil, err
		}
		key = tr.LocalAddr().String()
		entry = quicListenerEntry{ln: ln}
	}
	l, err := entry.ln.Add(tlsConf, allowWindowIncrease, func() { c.onListenerClosed(key) })
	if err != nil {
		if entry.refCount <= 0 {
			entry.ln.Close()
		}
		return nil, err
	}
	entry.refCount++
	c.quicListeners[key] = entry
	return l, nil
}

func (c *ConnManager) onListenerClosed(key string) {
	c.quicListenersMu.Lock()
	defer c.quicListenersMu.Unlock()

	entry := c.quicListeners[key]
	entry.refCount = entry.refCount - 1
	if entry.refCount <= 0 {
		delete(c.quicListeners, key)
		entry.ln.Close()
	} else {
		c.quicListeners[key] = entry
	}
}

func (c *ConnManager) transportForListen(network string, laddr *net.UDPAddr) (refCountedQuicTransport, error) {
	if c.enableReuseport {
		reuse, err := c.getReuse(network)
		if err != nil {
			return nil, err
		}
		return reuse.TransportForListen(network, laddr)
	}

	conn, err := net.ListenUDP(network, laddr)
	if err != nil {
		return nil, err
	}
	return &singleOwnerTransport{
		packetConn: conn,
		Transport: quic.Transport{
			Conn:              conn,
			StatelessResetKey: &c.srk,
			TokenGeneratorKey: &c.tokenKey,
		},
	}, nil
}

func (c *ConnManager) DialQUIC(ctx context.Context, raddr ma.Multiaddr, tlsConf *tls.Config, allowWindowIncrease func(conn quic.Connection, delta uint64) bool) (quic.Connection, error) {
	naddr, v, err := FromQuicMultiaddr(raddr)
	if err != nil {
		return nil, err
	}
	netw, _, err := manet.DialArgs(raddr)
	if err != nil {
		return nil, err
	}

	quicConf := c.clientConfig.Clone()
	quicConf.AllowConnectionWindowIncrease = allowWindowIncrease

	if v == quic.Version1 {
		// The endpoint has explicit support for QUIC v1, so we'll only use that version.
		quicConf.Versions = []quic.VersionNumber{quic.Version1}
	} else {
		return nil, errors.New("unknown QUIC version")
	}

	tr, err := c.TransportForDial(netw, naddr)
	if err != nil {
		return nil, err
	}
	conn, err := tr.Dial(ctx, naddr, tlsConf, quicConf)
	if err != nil {
		tr.DecreaseCount()
		return nil, err
	}
	return conn, nil
}

func (c *ConnManager) TransportForDial(network string, raddr *net.UDPAddr) (refCountedQuicTransport, error) {
	if c.enableReuseport {
		reuse, err := c.getReuse(network)
		if err != nil {
			return nil, err
		}
		return reuse.TransportForDial(network, raddr)
	}

	var laddr *net.UDPAddr
	switch network {
	case "udp4":
		laddr = &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	case "udp6":
		laddr = &net.UDPAddr{IP: net.IPv6zero, Port: 0}
	}
	conn, err := net.ListenUDP(network, laddr)
	if err != nil {
		return nil, err
	}
	return &singleOwnerTransport{Transport: quic.Transport{Conn: conn, StatelessResetKey: &c.srk}, packetConn: conn}, nil
}

func (c *ConnManager) Protocols() []int {
	return []int{ma.P_QUIC_V1}
}

func (c *ConnManager) Close() error {
	if !c.enableReuseport {
		return nil
	}
	if err := c.reuseUDP6.Close(); err != nil {
		return err
	}
	return c.reuseUDP4.Close()
}

func (c *ConnManager) ClientConfig() *quic.Config {
	return c.clientConfig
}
