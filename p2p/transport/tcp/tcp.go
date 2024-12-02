package tcp

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/net/reuseport"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse"

	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	manet "github.com/multiformats/go-multiaddr/net"
)

const defaultConnectTimeout = 5 * time.Second

var log = logging.Logger("tcp-tpt")

const keepAlivePeriod = 30 * time.Second

type canKeepAlive interface {
	SetKeepAlive(bool) error
	SetKeepAlivePeriod(time.Duration) error
}

var _ canKeepAlive = &net.TCPConn{}

// Deprecated: Use tcpreuse.ReuseportIsAvailable
var ReuseportIsAvailable = tcpreuse.ReuseportIsAvailable

func tryKeepAlive(conn net.Conn, keepAlive bool) {
	keepAliveConn, ok := conn.(canKeepAlive)
	if !ok {
		log.Errorf("Can't set TCP keepalives.")
		return
	}
	if err := keepAliveConn.SetKeepAlive(keepAlive); err != nil {
		// Sometimes we seem to get "invalid argument" results from this function on Darwin.
		// This might be due to a closed connection, but I can't reproduce that on Linux.
		//
		// But there's nothing we can do about invalid arguments, so we'll drop this to a
		// debug.
		if errors.Is(err, os.ErrInvalid) || errors.Is(err, syscall.EINVAL) {
			log.Debugw("failed to enable TCP keepalive", "error", err)
		} else {
			log.Errorw("failed to enable TCP keepalive", "error", err)
		}
		return
	}

	if runtime.GOOS != "openbsd" {
		if err := keepAliveConn.SetKeepAlivePeriod(keepAlivePeriod); err != nil {
			log.Errorw("failed set keepalive period", "error", err)
		}
	}
}

// try to set linger on the connection, if possible.
func tryLinger(conn net.Conn, sec int) {
	type canLinger interface {
		SetLinger(int) error
	}

	if lingerConn, ok := conn.(canLinger); ok {
		_ = lingerConn.SetLinger(sec)
	}
}

type tcpListener struct {
	manet.Listener
	sec int
}

func (ll *tcpListener) Accept() (manet.Conn, error) {
	c, err := ll.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tryLinger(c, ll.sec)
	tryKeepAlive(c, true)
	// We're not calling OpenConnection in the resource manager here,
	// since the manet.Conn doesn't allow us to save the scope.
	// It's the caller's (usually the p2p/net/upgrader) responsibility
	// to call the resource manager.
	return c, nil
}

type Option func(*TcpTransport) error

func DisableReuseport() Option {
	return func(tr *TcpTransport) error {
		tr.disableReuseport = true
		return nil
	}
}

func WithConnectionTimeout(d time.Duration) Option {
	return func(tr *TcpTransport) error {
		tr.connectTimeout = d
		return nil
	}
}

func WithMetrics() Option {
	return func(tr *TcpTransport) error {
		tr.enableMetrics = true
		return nil
	}
}

// TcpTransport is the TCP transport.
type TcpTransport struct {
	// Connection upgrader for upgrading insecure stream connections to
	// secure multiplex connections.
	upgrader transport.Upgrader

	disableReuseport bool // Explicitly disable reuseport.
	enableMetrics    bool

	// share and demultiplex TCP listeners across multiple transports
	sharedTcp *tcpreuse.ConnMgr

	// TCP connect timeout
	connectTimeout time.Duration

	rcmgr network.ResourceManager

	reuse reuseport.Transport

	metricsCollector *aggregatingCollector
}

var _ transport.Transport = &TcpTransport{}
var _ transport.DialUpdater = &TcpTransport{}

// NewTCPTransport creates a tcp transport object that tracks dialers and listeners
// created.
func NewTCPTransport(upgrader transport.Upgrader, rcmgr network.ResourceManager, sharedTCP *tcpreuse.ConnMgr, opts ...Option) (*TcpTransport, error) {
	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}
	tr := &TcpTransport{
		upgrader:       upgrader,
		connectTimeout: defaultConnectTimeout, // can be set by using the WithConnectionTimeout option
		rcmgr:          rcmgr,
		sharedTcp:      sharedTCP,
	}
	for _, o := range opts {
		if err := o(tr); err != nil {
			return nil, err
		}
	}
	return tr, nil
}

var dialMatcher = mafmt.And(mafmt.IP, mafmt.Base(ma.P_TCP))

// CanDial returns true if this transport believes it can dial the given
// multiaddr.
func (t *TcpTransport) CanDial(addr ma.Multiaddr) bool {
	return dialMatcher.Matches(addr)
}

func (t *TcpTransport) maDial(ctx context.Context, raddr ma.Multiaddr) (manet.Conn, error) {
	// Apply the deadline iff applicable
	if t.connectTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.connectTimeout)
		defer cancel()
	}

	if t.sharedTcp != nil {
		return t.sharedTcp.DialContext(ctx, raddr)
	}

	if t.UseReuseport() {
		return t.reuse.DialContext(ctx, raddr)
	}
	var d manet.Dialer
	return d.DialContext(ctx, raddr)
}

// Dial dials the peer at the remote address.
func (t *TcpTransport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (transport.CapableConn, error) {
	return t.DialWithUpdates(ctx, raddr, p, nil)
}

func (t *TcpTransport) DialWithUpdates(ctx context.Context, raddr ma.Multiaddr, p peer.ID, updateChan chan<- transport.DialUpdate) (transport.CapableConn, error) {
	connScope, err := t.rcmgr.OpenConnection(network.DirOutbound, true, raddr)
	if err != nil {
		log.Debugw("resource manager blocked outgoing connection", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}

	c, err := t.dialWithScope(ctx, raddr, p, connScope, updateChan)
	if err != nil {
		connScope.Done()
		return nil, err
	}
	return c, nil
}

func (t *TcpTransport) dialWithScope(ctx context.Context, raddr ma.Multiaddr, p peer.ID, connScope network.ConnManagementScope, updateChan chan<- transport.DialUpdate) (transport.CapableConn, error) {
	if err := connScope.SetPeer(p); err != nil {
		log.Debugw("resource manager blocked outgoing connection for peer", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}
	conn, err := t.maDial(ctx, raddr)
	if err != nil {
		return nil, err
	}
	// Set linger to 0 so we never get stuck in the TIME-WAIT state. When
	// linger is 0, connections are _reset_ instead of closed with a FIN.
	// This means we can immediately reuse the 5-tuple and reconnect.
	tryLinger(conn, 0)
	tryKeepAlive(conn, true)
	c := conn
	if t.enableMetrics {
		var err error
		c, err = newTracingConn(conn, t.metricsCollector, true)
		if err != nil {
			return nil, err
		}
	}
	if updateChan != nil {
		select {
		case updateChan <- transport.DialUpdate{Kind: transport.UpdateKindHandshakeProgressed, Addr: raddr}:
		default:
			// It is better to skip the update than to delay upgrading the connection
		}
	}
	direction := network.DirOutbound
	if ok, isClient, _ := network.GetSimultaneousConnect(ctx); ok && !isClient {
		direction = network.DirInbound
	}
	return t.upgrader.Upgrade(ctx, t, c, direction, p, connScope)
}

// UseReuseport returns true if reuseport is enabled and available.
func (t *TcpTransport) UseReuseport() bool {
	return !t.disableReuseport && tcpreuse.ReuseportIsAvailable()
}

func (t *TcpTransport) unsharedMAListen(laddr ma.Multiaddr) (manet.Listener, error) {
	if t.UseReuseport() {
		return t.reuse.Listen(laddr)
	}
	return manet.Listen(laddr)
}

// Listen listens on the given multiaddr.
func (t *TcpTransport) Listen(laddr ma.Multiaddr) (transport.Listener, error) {
	var list manet.Listener
	var err error

	if t.sharedTcp == nil {
		list, err = t.unsharedMAListen(laddr)
	} else {
		list, err = t.sharedTcp.DemultiplexedListen(laddr, tcpreuse.DemultiplexedConnType_MultistreamSelect)
	}
	if err != nil {
		return nil, err
	}

	if t.enableMetrics {
		list = newTracingListener(&tcpListener{list, 0}, t.metricsCollector)
	}
	return t.upgrader.UpgradeListener(t, list), nil
}

// Protocols returns the list of terminal protocols this transport can dial.
func (t *TcpTransport) Protocols() []int {
	return []int{ma.P_TCP}
}

// Proxy always returns false for the TCP transport.
func (t *TcpTransport) Proxy() bool {
	return false
}

func (t *TcpTransport) String() string {
	return "TCP"
}
