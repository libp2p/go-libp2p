package kcp

import (
	"context"
	"crypto/sha1"
	"net"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/pkg/errors"
	"github.com/xtaci/kcp-go"
	"github.com/xtaci/smux"
	"golang.org/x/crypto/pbkdf2"

	logging "github.com/ipfs/go-log/v2"
	"github.com/multiformats/go-multiaddr"
	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	defaultConnectTimeout = 5 * time.Second
	keepAlivePeriod       = 30 * time.Second
	udpSocketBuffer       = 4194304
	salt                  = "kcp-go"
	key                   = "kcp-go"
	sndWnd                = 1024
	rcvWnd                = 1024
	ackNodelay            = true
	mtu                   = 1500
	smuxVersion           = 2
	smuxBufferSize        = 4194304
	streamBuffSize        = 2097152
	keepAliveInterval     = 10
	keepAliveTimeout      = 13
)

var (
	log = logging.Logger("kcp-tpt")

	// Protocol Code
	P_KCP = 482

	// KcpProtocol is the multiaddr protocol definition for this transport.
	KcpProtocol = ma.Protocol{
		Name:  "kcp",
		Code:  P_KCP,
		VCode: ma.CodeToVarint(482),
	}

	// used in addr conversion from net.Addr to manet.MultiAddr and vice versa
	baseMultiaddr multiaddr.Multiaddr
)

type listener struct {
	t   *KcpTransport
	lis *kcp.Listener
}

var _ manet.Listener = &listener{}

func (l *listener) Accept() (manet.Conn, error) {
	conn, err := l.lis.AcceptKCP()
	if err != nil {
		log.Error("Error accepting incing session, %+v", err)
		return nil, err
	}

	log.Info("Accepted session, remote address:", conn.RemoteAddr())
	conn.SetStreamMode(true)
	conn.SetWriteDelay(false)
	conn.SetNoDelay(1, 10, 2, 1)
	conn.SetMtu(mtu)
	conn.SetWindowSize(sndWnd, rcvWnd)
	conn.SetACKNoDelay(ackNodelay)

	// stream multiplex
	smuxConfig := smux.DefaultConfig()
	smuxConfig.Version = smuxVersion
	smuxConfig.MaxReceiveBuffer = smuxBufferSize
	smuxConfig.MaxStreamBuffer = streamBuffSize
	smuxConfig.KeepAliveInterval = time.Duration(keepAliveInterval) * time.Second
	smuxConfig.KeepAliveTimeout = time.Duration(keepAliveTimeout) * time.Second

	c := NewCompStream(conn)

	// // maConn is wrapping an accepted KCP session.
	smuxSess, err := smux.Server(c, smuxConfig)
	if err != nil {
		return nil, err
	}
	defer smuxSess.Close()

	stream, err := smuxSess.AcceptStream()
	if err != nil {
		if stream != nil {
			stream.Close()
		}
		smuxSess.Close()
		c.Close()
		return nil, err
	}

	streamWrap, err := manet.WrapNetConn(stream)
	if err != nil {
		if streamWrap != nil {
			streamWrap.Close()
		}
		stream.Close()
		smuxSess.Close()
		c.Close()
		return nil, err
	}

	return streamWrap, nil
}

func (l *listener) Close() error {
	return l.lis.Close()
}

func (l *listener) Addr() net.Addr {
	return l.lis.Addr()
}

func (l *listener) Multiaddr() ma.Multiaddr {
	addr, err := NetAddrToKcpMultiaddr(l.Addr())
	if err != nil {
		panic(err)
	}
	return addr
}

// KcpTransport is the TCP transport.
type KcpTransport struct {
	// Connection upgrader for upgrading insecure stream connections to
	// secure multiplex connections.
	upgrader transport.Upgrader

	// TCP connect timeout
	connectTimeout time.Duration

	// resource manager
	rcmgr network.ResourceManager
}

var _ transport.Transport = &KcpTransport{}

// NewTCPTransport creates a tcp transport object that tracks dialers and listeners
// created. It represents an entire TCP stack (though it might not necessarily be).
func NewTransport(upgrader transport.Upgrader, rcmgr network.ResourceManager) (*KcpTransport, error) {
	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}
	return &KcpTransport{
		upgrader:       upgrader,
		connectTimeout: defaultConnectTimeout, // can be set by using the WithConnectionTimeout option
		rcmgr:          rcmgr,
	}, nil
}

// CanDial returns true if this transport believes it can dial the given
// multiaddr.
func (t *KcpTransport) CanDial(addr ma.Multiaddr) bool {
	return mafmt.And(
		mafmt.UDP,
		mafmt.Base(KcpProtocol.Code),
	).Matches(addr)
}

// Dial dials the peer at the remote address.
func (t *KcpTransport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (transport.CapableConn, error) {
	connScope, err := t.rcmgr.OpenConnection(network.DirOutbound, true, raddr)
	if err != nil {
		log.Debugw("resource manager blocked outgoing connection", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}

	if err := connScope.SetPeer(p); err != nil {
		log.Debugw("resource manager blocked outgoing connection for peer", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}
	// Apply the deadline iff applicable
	if t.connectTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.connectTimeout)
		defer cancel()
	}

	pass := pbkdf2.Key([]byte(key), []byte(salt), 4096, 32, sha1.New)
	b, err := kcp.NewAESBlockCrypt(pass)
	if err != nil {
		return nil, err
	}

	dialAddr, err := KcpMultiaddrToNetAddr(raddr)
	if err != nil {
		return nil, err
	}

	conn, err := kcp.DialWithOptions(dialAddr.String(), b, 10, 3) // data shard, parity shard

	conn.SetStreamMode(true)
	conn.SetWriteDelay(false)
	conn.SetNoDelay(1, 10, 2, 1)
	conn.SetMtu(mtu)
	conn.SetWindowSize(sndWnd, rcvWnd)
	conn.SetACKNoDelay(ackNodelay)

	if err := conn.SetReadBuffer(udpSocketBuffer); err != nil {
		log.Error("SetReadBuffer:", err)
	}
	if err := conn.SetWriteBuffer(udpSocketBuffer); err != nil {
		log.Error("SetWriteBuffer:", err)
	}

	// stream multiplex
	smuxConfig := smux.DefaultConfig()
	smuxConfig.Version = smuxVersion
	smuxConfig.MaxReceiveBuffer = smuxBufferSize
	smuxConfig.MaxStreamBuffer = streamBuffSize
	smuxConfig.KeepAliveInterval = time.Duration(keepAliveInterval) * time.Second
	smuxConfig.KeepAliveTimeout = time.Duration(keepAliveTimeout) * time.Second

	if err := smux.VerifyConfig(smuxConfig); err != nil {
		log.Fatalf("%+v", err)
		return nil, err
	}

	smuxSession, err := smux.Client(NewCompStream(conn), smuxConfig)

	stream, err := smuxSession.AcceptStream()
	if err != nil {
		if stream != nil {
			stream.Close()
		}
		smuxSession.Close()
		conn.Close()
		return nil, err
	}

	streamWrap, err := manet.WrapNetConn(stream)
	if err != nil {
		if streamWrap != nil {
			streamWrap.Close()
		}
		stream.Close()
		conn.Close()
		return nil, err
	}

	direction := network.DirOutbound
	if ok, isClient, _ := network.GetSimultaneousConnect(ctx); ok && !isClient {
		direction = network.DirInbound
	}

	c, err := t.upgrader.Upgrade(ctx, t, streamWrap, direction, p, connScope)
	if err != nil {
		connScope.Done()
		return nil, err
	}

	return c, nil
}

// Listen listens on the given multiaddr.
func (t *KcpTransport) Listen(laddr ma.Multiaddr) (transport.Listener, error) {
	pass := pbkdf2.Key([]byte(key), []byte(salt), 4096, 32, sha1.New)
	b, err := kcp.NewAESBlockCrypt(pass)
	if err != nil {
		return nil, err
	}

	listenAddr, err := KcpMultiaddrToNetAddr(laddr)
	if err != nil {
		return nil, err
	}

	lis, err := kcp.ListenWithOptions(listenAddr.String(), b, 10, 3)
	if err != nil {
		return nil, err
	}

	if err := lis.SetReadBuffer(udpSocketBuffer); err != nil {
		log.Error("SetReadBuffer:", err)
	}
	if err := lis.SetWriteBuffer(udpSocketBuffer); err != nil {
		log.Error("SetWriteBuffer:", err)
	}

	list := &listener{
		t:   t,
		lis: lis,
	}

	return t.upgrader.UpgradeListener(t, list), nil
}

// Protocols returns the list of terminal protocols this transport can dial.
func (t *KcpTransport) Protocols() []int {
	return []int{P_KCP}
}

// Proxy always returns false for the TCP transport.
func (t *KcpTransport) Proxy() bool {
	return false
}

func (t *KcpTransport) String() string {
	return "KCP"
}

// KcpMultiaddrToNetAddr converts a kcp multiaddress to a net.addr.
func KcpMultiaddrToNetAddr(maddr multiaddr.Multiaddr) (net.Addr, error) {
	if baseMultiaddr.String() == "" {
		bAddr, err := multiaddr.NewMultiaddr("/kcp")
		if err != nil {
			return nil, err
		}
		baseMultiaddr = bAddr
	}

	protos := maddr.Protocols()
	if last := protos[len(protos)-1]; last.Name != "kcp" {
		return nil, errors.Errorf("not a kcp multiaddr: %s", maddr.String())
	}

	maddrBase := maddr.Decapsulate(baseMultiaddr)
	return manet.ToNetAddr(maddrBase)
}

// NetAddrToKcpMultiaddr converts a net address to a kcp multiaddress.addr.
func NetAddrToKcpMultiaddr(addr net.Addr) (multiaddr.Multiaddr, error) {
	if baseMultiaddr.String() == "" {
		bAddr, err := multiaddr.NewMultiaddr("/kcp")
		if err != nil {
			return nil, err
		}
		baseMultiaddr = bAddr
	}

	maddrBase, err := manet.FromNetAddr(addr)
	if err != nil {
		return nil, err
	}

	return maddrBase.Encapsulate(baseMultiaddr), nil
}
