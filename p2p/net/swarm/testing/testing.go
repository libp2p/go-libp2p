package testing

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/sec/insecure"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

type config struct {
	disableReuseport bool
	dialOnly         bool
	disableTCP       bool
	disableQUIC      bool
	connectionGater  connmgr.ConnectionGater
	sk               crypto.PrivKey
	swarmOpts        []swarm.Option
	eventBus         event.Bus
	clock
}

type clock interface {
	Now() time.Time
}

type realclock struct{}

func (rc realclock) Now() time.Time {
	return time.Now()
}

// Option is an option that can be passed when constructing a test swarm.
type Option func(testing.TB, *config)

// WithClock sets the clock to use for this swarm
func WithClock(clock clock) Option {
	return func(_ testing.TB, c *config) {
		c.clock = clock
	}
}

func WithSwarmOpts(swarmOpts ...swarm.Option) Option {
	return func(_ testing.TB, c *config) {
		c.swarmOpts = swarmOpts
	}
}

// OptDisableReuseport disables reuseport in this test swarm.
var OptDisableReuseport Option = func(_ testing.TB, c *config) {
	c.disableReuseport = true
}

// OptDialOnly prevents the test swarm from listening.
var OptDialOnly Option = func(_ testing.TB, c *config) {
	c.dialOnly = true
}

// OptDisableTCP disables TCP.
var OptDisableTCP Option = func(_ testing.TB, c *config) {
	c.disableTCP = true
}

// OptDisableQUIC disables QUIC.
var OptDisableQUIC Option = func(_ testing.TB, c *config) {
	c.disableQUIC = true
}

// OptConnGater configures the given connection gater on the test
func OptConnGater(cg connmgr.ConnectionGater) Option {
	return func(_ testing.TB, c *config) {
		c.connectionGater = cg
	}
}

// OptPeerPrivateKey configures the peer private key which is then used to derive the public key and peer ID.
func OptPeerPrivateKey(sk crypto.PrivKey) Option {
	return func(_ testing.TB, c *config) {
		c.sk = sk
	}
}

func EventBus(b event.Bus) Option {
	return func(_ testing.TB, c *config) {
		c.eventBus = b
	}
}

// GenUpgrader creates a new connection upgrader for use with this swarm.
func GenUpgrader(t testing.TB, n *swarm.Swarm, connGater connmgr.ConnectionGater, opts ...tptu.Option) transport.Upgrader {
	id := n.LocalPeer()
	pk := n.Peerstore().PrivKey(id)
	st := insecure.NewWithIdentity(insecure.ID, id, pk)

	u, err := tptu.New([]sec.SecureTransport{st}, []tptu.StreamMuxer{{ID: yamux.ID, Muxer: yamux.DefaultTransport}}, nil, nil, connGater, opts...)
	require.NoError(t, err)
	return u
}

// GenSwarm generates a new test swarm.
func GenSwarm(t testing.TB, opts ...Option) *swarm.Swarm {
	var cfg config
	cfg.clock = realclock{}
	for _, o := range opts {
		o(t, &cfg)
	}

	var priv crypto.PrivKey
	if cfg.sk == nil {
		var err error
		priv, _, err = crypto.GenerateEd25519Key(rand.Reader)
		require.NoError(t, err)
	} else {
		priv = cfg.sk
	}
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)

	ps, err := pstoremem.NewPeerstore(pstoremem.WithClock(cfg.clock))
	require.NoError(t, err)
	ps.AddPubKey(id, priv.GetPublic())
	ps.AddPrivKey(id, priv)
	t.Cleanup(func() { ps.Close() })

	swarmOpts := cfg.swarmOpts
	swarmOpts = append(swarmOpts, swarm.WithMetrics(metrics.NewBandwidthCounter()))
	if cfg.connectionGater != nil {
		swarmOpts = append(swarmOpts, swarm.WithConnectionGater(cfg.connectionGater))
	}

	eventBus := cfg.eventBus
	if eventBus == nil {
		eventBus = eventbus.NewBus()
	}
	s, err := swarm.NewSwarm(id, ps, eventBus, swarmOpts...)
	require.NoError(t, err)

	upgrader := GenUpgrader(t, s, cfg.connectionGater)

	if !cfg.disableTCP {
		var tcpOpts []tcp.Option
		if cfg.disableReuseport {
			tcpOpts = append(tcpOpts, tcp.DisableReuseport())
		}
		tcpTransport, err := tcp.NewTCPTransport(upgrader, nil, nil, tcpOpts...)
		require.NoError(t, err)
		if err := s.AddTransport(tcpTransport); err != nil {
			t.Fatal(err)
		}
		if !cfg.dialOnly {
			if err := s.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !cfg.disableQUIC {
		reuse, err := quicreuse.NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{})
		if err != nil {
			t.Fatal(err)
		}
		quicTransport, err := libp2pquic.NewTransport(priv, reuse, nil, cfg.connectionGater, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddTransport(quicTransport); err != nil {
			t.Fatal(err)
		}
		if !cfg.dialOnly {
			if err := s.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !cfg.dialOnly {
		s.Peerstore().AddAddrs(id, s.ListenAddresses(), peerstore.PermanentAddrTTL)
	}
	return s
}

// DivulgeAddresses adds swarm a's addresses to swarm b's peerstore.
func DivulgeAddresses(a, b network.Network) {
	id := a.LocalPeer()
	addrs := a.Peerstore().Addrs(id)
	b.Peerstore().AddAddrs(id, addrs, peerstore.PermanentAddrTTL)
}

// MockConnectionGater is a mock connection gater to be used by the tests.
type MockConnectionGater struct {
	Dial     func(p peer.ID, addr ma.Multiaddr) bool
	PeerDial func(p peer.ID) bool
	Accept   func(c network.ConnMultiaddrs) bool
	Secured  func(network.Direction, peer.ID, network.ConnMultiaddrs) bool
	Upgraded func(c network.Conn) (bool, control.DisconnectReason)
}

func DefaultMockConnectionGater() *MockConnectionGater {
	m := &MockConnectionGater{}
	m.Dial = func(p peer.ID, addr ma.Multiaddr) bool {
		return true
	}

	m.PeerDial = func(p peer.ID) bool {
		return true
	}

	m.Accept = func(c network.ConnMultiaddrs) bool {
		return true
	}

	m.Secured = func(network.Direction, peer.ID, network.ConnMultiaddrs) bool {
		return true
	}

	m.Upgraded = func(c network.Conn) (bool, control.DisconnectReason) {
		return true, 0
	}

	return m
}

func (m *MockConnectionGater) InterceptAddrDial(p peer.ID, addr ma.Multiaddr) (allow bool) {
	return m.Dial(p, addr)
}

func (m *MockConnectionGater) InterceptPeerDial(p peer.ID) (allow bool) {
	return m.PeerDial(p)
}

func (m *MockConnectionGater) InterceptAccept(c network.ConnMultiaddrs) (allow bool) {
	return m.Accept(c)
}

func (m *MockConnectionGater) InterceptSecured(d network.Direction, p peer.ID, c network.ConnMultiaddrs) (allow bool) {
	return m.Secured(d, p, c)
}

func (m *MockConnectionGater) InterceptUpgraded(tc network.Conn) (allow bool, reason control.DisconnectReason) {
	return m.Upgraded(tc)
}
