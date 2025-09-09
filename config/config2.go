package config

import (
	"crypto/rand"
	"net"
	"slices"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/di"
	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	netconnmgr "github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	libp2pwebrtc "github.com/libp2p/go-libp2p/p2p/transport/webrtc"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
	libp2pwebtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
	mstream "github.com/multiformats/go-multistream"
	"github.com/prometheus/client_golang/prometheus"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/quic-go/quic-go"
)

type ListenAddrs []ma.Multiaddr

type SwarmConfig struct {
	ListenAddrs                 ListenAddrs
	UDPBlackHoleSuccessCounter  *swarm.BlackHoleSuccessCounter
	IPv6BlackHoleSuccessCounter *swarm.BlackHoleSuccessCounter

	Swarm di.Provide[*swarm.Swarm]
}

type IdentifyConfig struct {
	// UserAgent is the identifier this node will send to other peers when
	// identifying itself, e.g. via the identify protocol.
	//
	// Set it via the UserAgent option function.
	UserAgent string

	// ProtocolVersion is the protocol version that identifies the family
	// of protocols used by the peer in the Identify protocol. It is set
	// using the [ProtocolVersion] option.
	ProtocolVersion string
}

type UpgraderConfig struct {
	// Upgrader is the upgrader used to upgrade connections to the libp2p
	// protocol. It is set using the *TODO* option.
	Upgrader di.Provide[transport.Upgrader]

	UpgraderOptions []di.Provide[tptu.Option]

	Muxers   []tptu.StreamMuxer
	Security []di.Provide[sec.SecureTransport]
}

type DialConfig struct {
	DialTimeout di.Provide[time.Duration]
}

type BasicHostConfig struct {
	AddrsFactory bhost.AddrsFactory
}

type MetricsConfig struct {
	PrometheusRegisterer prometheus.Registerer
}

type TransportsConfig struct {
	Transports []di.Provide[transport.Transport]

	ListenUDPFn di.Provide[libp2pwebrtc.ListenUDPFn]
}

type QUICConfig struct {
	QUICReuse di.Provide[*quicreuse.ConnManager]

	StatelessResetKey func(crypto.PrivKey) (quic.StatelessResetKey, error)
	TokenKey          func(crypto.PrivKey) (quic.TokenGeneratorKey, error)
}

type AutoNatPrivKey crypto.PrivKey
type AutoNatPeerStore peerstore.Peerstore
type AutoNATHost host.Host
type AutoNatConfig struct {
	PrivateKey func() (AutoNatPrivKey, error)
	PeerStore  di.Provide[AutoNatPeerStore]

	AutoNATV2Host di.Provide[AutoNATHost]
}

type Config2 struct {
	Lifecycle func() *Lifecycle

	IdentifyConfig

	ResourceManager di.Provide[network.ResourceManager]

	EventBus    func() event.Bus
	Peerstore   func() (peerstore.Peerstore, error)
	ConnManager func() (connmgr.ConnManager, error)

	PrivateKey func() (crypto.PrivKey, error)
	PeerID     func(crypto.PrivKey) (peer.ID, error)

	QUICConfig
	TransportsConfig
	SwarmConfig

	ConnGater di.Provide[connmgr.ConnectionGater]

	UpgraderConfig
	DialConfig
	MetricsConfig

	AutoNatConfig

	SideEffects []di.Provide[di.SideEffect]

	Host di.Provide[host.Host]
}

var DefaultTransports = TransportsConfig{
	Transports: []di.Provide[transport.Transport]{
		di.MustProvide[transport.Transport](func(
			upgrader transport.Upgrader,
			rcmgr network.ResourceManager,
			// TODO add this:
			// sharedTCP *tcpreuse.ConnMgr,
			// TODO add support for tcp options
			// opts []tcp.Option,
		) (transport.Transport, error) {
			return tcp.NewTCPTransport(upgrader, rcmgr, nil)
		}),

		di.MustProvide[transport.Transport](func(
			key crypto.PrivKey,
			connManager *quicreuse.ConnManager,
			gater connmgr.ConnectionGater,
			rcmgr network.ResourceManager,
		) (transport.Transport, error) {
			t, err := libp2pquic.NewTransport(key, connManager, nil, gater, rcmgr)
			return t, err
		}),

		di.MustProvide[transport.Transport](func(
			u transport.Upgrader,
			rcmgr network.ResourceManager,
			// sharedTCP *tcpreuse.ConnMgr,
			// TODO add support for websocket options
			// opts []ws.Option,
		) (transport.Transport, error) {
			return ws.New(u, rcmgr, nil)
		}),

		di.MustProvide[transport.Transport](func(
			key crypto.PrivKey,
			connManager *quicreuse.ConnManager,
			gater connmgr.ConnectionGater,
			rcmgr network.ResourceManager,
		) (transport.Transport, error) {
			return libp2pwebtransport.New(key, nil, connManager, gater, rcmgr)
		}),

		di.MustProvide[transport.Transport](func(
			key crypto.PrivKey,
			gater connmgr.ConnectionGater,
			rcmgr network.ResourceManager,
			listenUDP libp2pwebrtc.ListenUDPFn,
			// opts []libp2pwebrtc.Option
		) (transport.Transport, error) {
			return libp2pwebrtc.New(key, nil, gater, rcmgr, listenUDP)
		}),
	},

	ListenUDPFn: di.MustProvide[libp2pwebrtc.ListenUDPFn](func(
		cm *quicreuse.ConnManager,
		sw *swarm.Swarm,
	) libp2pwebrtc.ListenUDPFn {
		hasQuicAddrPortFor := func(network string, laddr *net.UDPAddr) bool {
			quicAddrPorts := map[string]struct{}{}
			for _, addr := range sw.ListenAddresses() {
				if _, err := addr.ValueForProtocol(ma.P_QUIC_V1); err == nil {
					netw, addr, err := manet.DialArgs(addr)
					if err != nil {
						return false
					}
					quicAddrPorts[netw+"_"+addr] = struct{}{}
				}
			}
			_, ok := quicAddrPorts[network+"_"+laddr.String()]
			return ok
		}

		return func(network string, laddr *net.UDPAddr) (net.PacketConn, error) {
			if hasQuicAddrPortFor(network, laddr) {
				return cm.SharedNonQUICPacketConn(network, laddr)
			}
			return net.ListenUDP(network, laddr)
		}

	}),
}

var DefaultConfig = Config2{
	Lifecycle: func() *Lifecycle { return &Lifecycle{} },

	IdentifyConfig: IdentifyConfig{},

	ResourceManager: di.MustProvide[network.ResourceManager](func() (network.ResourceManager, error) {
		// Default memory limit: 1/8th of total memory, minimum 128MB, maximum 1GB
		limits := rcmgr.DefaultLimits
		// TODO
		// SetDefaultServiceLimits(&limits)
		return rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(limits.AutoScale()))
	}),

	QUICConfig: QUICConfig{
		QUICReuse: di.MustProvide[*quicreuse.ConnManager](func(l *Lifecycle, statelessResetKey quic.StatelessResetKey, tokenKey quic.TokenGeneratorKey) (*quicreuse.ConnManager, error) {
			return quicreuse.NewConnManager(statelessResetKey, tokenKey)
		}),
		StatelessResetKey: PrivKeyToStatelessResetKey,
		TokenKey:          PrivKeyToTokenGeneratorKey,
	},

	EventBus: func() event.Bus { return eventbus.NewBus() },
	Peerstore: func() (peerstore.Peerstore, error) {
		return pstoremem.NewPeerstore()
	},
	ConnManager: func() (connmgr.ConnManager, error) {
		return netconnmgr.NewConnManager(160, 192)
	},

	PrivateKey: func() (crypto.PrivKey, error) {
		priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
		return priv, err
	},
	PeerID: peer.IDFromPrivateKey,

	TransportsConfig: DefaultTransports,
	SwarmConfig: SwarmConfig{
		UDPBlackHoleSuccessCounter:  &swarm.BlackHoleSuccessCounter{N: 100, MinSuccesses: 5, Name: "UDP"},
		IPv6BlackHoleSuccessCounter: &swarm.BlackHoleSuccessCounter{N: 100, MinSuccesses: 5, Name: "IPv6"},
		ListenAddrs: []ma.Multiaddr{
			di.Must(ma.NewMultiaddr("/ip4/0.0.0.0/tcp/0")),
			di.Must(ma.NewMultiaddr("/ip4/0.0.0.0/udp/0/quic-v1")),
			di.Must(ma.NewMultiaddr("/ip4/0.0.0.0/udp/0/quic-v1/webtransport")),
			di.Must(ma.NewMultiaddr("/ip4/0.0.0.0/udp/0/webrtc-direct")),
			di.Must(ma.NewMultiaddr("/ip6/::/tcp/0")),
			di.Must(ma.NewMultiaddr("/ip6/::/udp/0/quic-v1")),
			di.Must(ma.NewMultiaddr("/ip6/::/udp/0/quic-v1/webtransport")),
			di.Must(ma.NewMultiaddr("/ip6/::/udp/0/webrtc-direct")),
		},
		Swarm: di.MustProvide[*swarm.Swarm](func(
			l *Lifecycle,
			p peer.ID,
			ps peerstore.Peerstore,
			b event.Bus,

			UDPBlackHoleSuccessCounter *swarm.BlackHoleSuccessCounter,
			IPv6BlackHoleSuccessCounter *swarm.BlackHoleSuccessCounter,

			listenAddrs ListenAddrs,
		) (*swarm.Swarm, error) {
			swarmOpts := []swarm.Option{
				swarm.WithUDPBlackHoleSuccessCounter(UDPBlackHoleSuccessCounter),
				swarm.WithIPv6BlackHoleSuccessCounter(IPv6BlackHoleSuccessCounter),
			}
			s, err := swarm.NewSwarm(p, ps, b, swarmOpts...)
			if err != nil {
				return nil, err
			}

			l.OnStart(func() error {
				return s.Listen(slices.Clone(listenAddrs)...)
			})
			l.OnClose(s)

			return s, nil
		}),
	},

	ConnGater: di.MustProvide[connmgr.ConnectionGater](nil),
	UpgraderConfig: UpgraderConfig{
		Muxers: []tptu.StreamMuxer{
			{ID: yamux.ID, Muxer: yamux.DefaultTransport},
		},
		Security: []di.Provide[sec.SecureTransport]{
			di.MustProvide[sec.SecureTransport](func(
				privkey crypto.PrivKey, muxers []tptu.StreamMuxer) (sec.SecureTransport, error) {
				return libp2ptls.New(libp2ptls.ID, privkey, muxers)
			}),
			di.MustProvide[sec.SecureTransport](func(
				privkey crypto.PrivKey, muxers []tptu.StreamMuxer) (sec.SecureTransport, error) {
				return noise.New(noise.ID, privkey, muxers)
			}),
		},
		UpgraderOptions: []di.Provide[tptu.Option]{},
		Upgrader: di.MustProvide[transport.Upgrader](func(
			security []sec.SecureTransport,
			muxers []tptu.StreamMuxer,
			rcmgr network.ResourceManager,
			connGater connmgr.ConnectionGater,
			upgraderOpts []tptu.Option,
		) (transport.Upgrader, error) {
			// No PSK. Use a different config for PSK
			return tptu.New(
				security, muxers, nil, rcmgr, connGater, upgraderOpts...,
			)
		}),
	},
	DialConfig: DialConfig{},
	MetricsConfig: MetricsConfig{
		PrometheusRegisterer: prometheus.DefaultRegisterer,
	},

	AutoNatConfig: AutoNatConfig{
		PrivateKey: func() (AutoNatPrivKey, error) {
			priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
			return AutoNatPrivKey(priv), err
		},
		PeerStore: di.MustProvide[AutoNatPeerStore](
			func() (AutoNatPeerStore, error) {
				ps, err := pstoremem.NewPeerstore()
				return AutoNatPeerStore(ps), err
			},
		),

		AutoNATV2Host: di.MustProvide[AutoNATHost](
			func(k AutoNatPrivKey, ps AutoNatPeerStore, swarmCfg SwarmConfig, tptConfig TransportsConfig) (AutoNATHost, error) {
				panic("todo")
			},
		),
	},

	SideEffects: []di.Provide[di.SideEffect]{
		di.MustSideEffect(func(s *swarm.Swarm, tpts []transport.Transport) (di.SideEffect, error) {
			for _, t := range tpts {
				err := s.AddTransport(t)
				if err != nil {
					return di.SideEffect{}, err
				}
			}
			return di.SideEffect{}, nil
		}),
	},

	Host: di.MustProvide[host.Host](func(
		l *Lifecycle,
		network *swarm.Swarm,
		connmgr connmgr.ConnManager,
		b event.Bus,
	) (host.Host, error) {
		mux := mstream.NewMultistreamMuxer[protocol.ID]()
		h := &blankhost.BlankHost{
			N:       network,
			M:       mux,
			ConnMgr: connmgr,
			E:       b,
			// Users can do this manually, but can't opt out of it otherwise.
			SkipInitSignedRecord: true,
		}
		l.OnStart(func() error {
			return h.Start()
		})
		l.OnClose(h)
		return h, nil

	}),
}
