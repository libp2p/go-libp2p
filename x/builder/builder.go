// package builder is an alternative and experimental way to build a go-libp2p
// node and services.
// See the commit details for history and motivation
package builder

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/internal/limits"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/observedaddrs"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	routed "github.com/libp2p/go-libp2p/p2p/host/routed"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	netconnmgr "github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse"
	libp2pwebrtc "github.com/libp2p/go-libp2p/p2p/transport/webrtc"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
	libp2pwebtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
	mstream "github.com/multiformats/go-multistream"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/hkdf"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/quic-go/quic-go"

	"git.sr.ht/~marcopolo/di"
)

// SetDefaultServiceLimits sets the default limits for bundled libp2p services
func SetDefaultServiceLimits(config *rcmgr.ScalingLimitConfig) {
	limits.SetDefaultServiceLimits(config)
}

// Lifecycle can be used to register start functions and closers. Services
// should not cause any side-effects on instantiation. Instead, services should
// have a start function that executes side effects (such as spawning a worker
// goroutine).
type Lifecycle struct {
	startFns []func() error
	closers  []io.Closer
}

func (l *Lifecycle) OnStart(fn func() error) {
	l.startFns = append(l.startFns, fn)
}

func (l *Lifecycle) OnClose(c io.Closer) {
	l.closers = append(l.closers, c)
}

func (l *Lifecycle) Start() error {
	for _, fn := range l.startFns {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}

func (l *Lifecycle) Close() error {
	var errs []error
	for _, c := range l.closers {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type ListenAddrs []ma.Multiaddr

type SwarmConfig struct {
	ListenAddrs                 ListenAddrs
	ReadOnlyBlackHoleDetector   bool
	UDPBlackHoleSuccessCounter  *swarm.BlackHoleSuccessCounter
	IPv6BlackHoleSuccessCounter *swarm.BlackHoleSuccessCounter
	MultiaddrDNSResolver        di.Optional[network.MultiaddrDNSResolver]

	// Opts can be used by the user to set swarm options
	Opts  []swarm.Option
	Swarm di.Provide[*swarm.Swarm]
}

// IdentifyConfig specifies the configuration of the Identify Service
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
	// protocol.
	Upgrader di.Provide[transport.Upgrader]

	UpgraderOptions []di.Provide[tptu.Option]

	Muxers   []tptu.StreamMuxer
	Security []di.Provide[sec.SecureTransport]
}

type DialConfig struct {
	DialTimeout time.Duration
	DialRanker  di.Optional[network.DialRanker]
}

type MetricsConfig struct {
	BandwidthReporter    metrics.Reporter
	PrometheusRegisterer prometheus.Registerer
}

// UDPTransportsConfig specifies the concrete transports that run on top of TCP
type TCPTransportsConfig struct {
	SharedTCPConnMuxer di.Provide[*tcpreuse.ConnMgr]

	TCPOpts      []tcp.Option
	TcpTransport di.Provide[*tcp.TcpTransport]

	WsOpts      []ws.Option
	WSTransport di.Provide[*ws.WebsocketTransport]
}

// UDPTransportsConfig specifies the concrete transports that run on top of UDP
type UDPTransportsConfig struct {
	QUICConfig

	ListenUDPFn di.Provide[libp2pwebrtc.ListenUDPFn]

	QUICTransport di.Provide[*libp2pquic.Transport]

	WebTransportOpts      []libp2pwebtransport.Option
	WebTransportTransport di.Provide[*libp2pwebtransport.Transport]

	WebRTCOpts      []libp2pwebrtc.Option
	WebRTCTransport di.Provide[*libp2pwebrtc.WebRTCTransport]
}

type TransportsConfig struct {
	TCPTransportsConfig
	UDPTransportsConfig

	PSK di.Provide[pnet.PSK]

	// Transports controls what transports are actually instantiated and used.
	// While {TCP,UDP}TransportsConfig defines the concrete transports, they
	// will not be instantiated unless they are provided here.
	//
	// This is because `di` is lazy, and won't call a constructor unless that
	// constructor's return value is used.
	Transports di.Provide[[]transport.Transport]
}

type AutoRelayConfig struct {
	Enabled bool
	Opts    []autorelay.Option
}

type QUICConfig struct {
	QUICReuse     di.Provide[*quicreuse.ConnManager]
	QUICReuseOpts []quicreuse.Option

	StatelessResetKey func(crypto.PrivKey) (quic.StatelessResetKey, error)
	TokenKey          func(crypto.PrivKey) (quic.TokenGeneratorKey, error)
}

// use new types for AutoNAT specific properties when the type overlap with the
// host type.

type AutoNatPrivKey crypto.PrivKey
type AutoNatPeerStore peerstore.Peerstore
type AutoNATHost host.Host
type AutoNatConfig struct {
	PrivateKey func() (AutoNatPrivKey, error)
	Peerstore  di.Provide[AutoNatPeerStore]

	Host di.Provide[AutoNATHost]
	Opts []autonatv2.AutoNATOption

	AutoNAT di.Provide[*autonatv2.AutoNAT]
}

type RoutingC func(host.Host) (routing.PeerRouting, error)
type BasicHostConfig struct {
	AddrsFactory         di.Optional[bhost.AddrsFactory]
	NATManager           func(network.Network) bhost.NATManager
	ObservedAddrsManager di.Provide[bhost.ObservedAddrsManager]

	EnablePing          bool
	EnableHolePunching  bool
	HolePunchingOptions []holepunch.Option

	Routing di.Optional[RoutingC]

	EnableRelay      bool
	RelayServiceOpts []relayv2.Option

	EnableMetrics bool
}

type Config struct {
	Logger    *slog.Logger
	Lifecycle func() *Lifecycle

	IdentifyConfig

	ResourceManager di.Provide[network.ResourceManager]

	EventBus    func() event.Bus
	Peerstore   func() (peerstore.Peerstore, error)
	ConnManager func(l *Lifecycle) (connmgr.ConnManager, error)

	PrivateKey func() (crypto.PrivKey, error)
	PeerID     func(crypto.PrivKey) (peer.ID, error)

	TransportsConfig
	SwarmConfig

	ConnGater di.Provide[connmgr.ConnectionGater]

	UpgraderConfig
	DialConfig
	MetricsConfig

	AutoRelayConfig
	BasicHostConfig
	AutoNatConfig

	// SideEffects is used to do some extra processing on instantiated objects
	// without producing loops.
	// For example if A needs to be linked to B, but B requires C on
	// instantiation, and C requires A. We can't link A to B in A's constructor
	// (A would depend on B would depend on C would depend on A).
	//
	// With SideEffects we can construct A and B separately, and then introduce them in another step.
	// - Build A
	// - Build B
	// - Link A to B in a side effect.
	//
	// The di.SideEffect is not a special type. Any type would work here.
	// di.SideEffect is chosen for convention.
	SideEffects []di.Provide[di.SideEffect]

	Host di.Provide[host.Host]
}

func PrivKeyToStatelessResetKey(key crypto.PrivKey) (quic.StatelessResetKey, error) {
	const statelessResetKeyInfo = "libp2p quic stateless reset key"
	var statelessResetKey quic.StatelessResetKey
	keyBytes, err := key.Raw()
	if err != nil {
		return statelessResetKey, err
	}
	keyReader := hkdf.New(sha256.New, keyBytes, nil, []byte(statelessResetKeyInfo))
	if _, err := io.ReadFull(keyReader, statelessResetKey[:]); err != nil {
		return statelessResetKey, err
	}
	return statelessResetKey, nil
}

func PrivKeyToTokenGeneratorKey(key crypto.PrivKey) (quic.TokenGeneratorKey, error) {
	const tokenGeneratorKeyInfo = "libp2p quic token generator key"
	var tokenKey quic.TokenGeneratorKey
	keyBytes, err := key.Raw()
	if err != nil {
		return tokenKey, err
	}
	keyReader := hkdf.New(sha256.New, keyBytes, nil, []byte(tokenGeneratorKeyInfo))
	if _, err := io.ReadFull(keyReader, tokenKey[:]); err != nil {
		return tokenKey, err
	}
	return tokenKey, nil
}

var DefaultTransports = TransportsConfig{
	TCPTransportsConfig: TCPTransportsConfig{
		SharedTCPConnMuxer: di.MustProvide[*tcpreuse.ConnMgr](
			func(cfg TCPTransportsConfig, upgrader transport.Upgrader) *tcpreuse.ConnMgr {
				if cfg.TcpTransport.Nil || cfg.WSTransport.Nil {
					return nil
				}
				return tcpreuse.NewConnMgr(tcpreuse.EnvReuseportVal, upgrader)
			},
		),

		TCPOpts:      []tcp.Option{},
		TcpTransport: di.MustProvide[*tcp.TcpTransport](tcp.NewTCPTransport),

		WsOpts:      []ws.Option{},
		WSTransport: di.MustProvide[*ws.WebsocketTransport](ws.New),
	},

	UDPTransportsConfig: UDPTransportsConfig{
		QUICConfig: QUICConfig{
			QUICReuse: di.MustProvide[*quicreuse.ConnManager](
				func(
					l *Lifecycle,
					statelessResetKey quic.StatelessResetKey,
					tokenKey quic.TokenGeneratorKey,
					opts []quicreuse.Option,
				) (*quicreuse.ConnManager, error) {
					cm, err := quicreuse.NewConnManager(statelessResetKey, tokenKey, opts...)
					if err != nil {
						return nil, err
					}
					l.OnClose(cm)
					return cm, nil
				}),
			StatelessResetKey: PrivKeyToStatelessResetKey,
			TokenKey:          PrivKeyToTokenGeneratorKey,
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
		QUICTransport: di.MustProvide[*libp2pquic.Transport](libp2pquic.NewTransport),

		WebTransportOpts:      []libp2pwebtransport.Option{},
		WebTransportTransport: di.MustProvide[*libp2pwebtransport.Transport](libp2pwebtransport.New),

		WebRTCOpts:      []libp2pwebrtc.Option{},
		WebRTCTransport: di.MustProvide[*libp2pwebrtc.WebRTCTransport](libp2pwebrtc.New),
	},

	// We don't support PSKs by default
	PSK: di.MustProvide[pnet.PSK](nil),

	Transports: di.MustProvide[[]transport.Transport](
		func(
			tcp *tcp.TcpTransport,
			ws *ws.WebsocketTransport,
			quic *libp2pquic.Transport,
			wt *libp2pwebtransport.Transport,
			webrtc *libp2pwebrtc.WebRTCTransport,
		) (tpts []transport.Transport, err error) {
			if tcp != nil {
				tpts = append(tpts, tcp)
			}
			if ws != nil {
				tpts = append(tpts, ws)
			}
			if quic != nil {
				tpts = append(tpts, quic)
			}
			if wt != nil {
				tpts = append(tpts, wt)
			}
			if webrtc != nil {
				tpts = append(tpts, webrtc)
			}
			return tpts, nil
		},
	),
}

var DefaultConfig = Config{
	Logger:    slog.Default(),
	Lifecycle: func() *Lifecycle { return &Lifecycle{} },

	IdentifyConfig: IdentifyConfig{},

	ResourceManager: di.MustProvide[network.ResourceManager](func(l *Lifecycle) (network.ResourceManager, error) {
		// Default memory limit: 1/8th of total memory, minimum 128MB, maximum 1GB
		limits := rcmgr.DefaultLimits
		SetDefaultServiceLimits(&limits)
		r, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(limits.AutoScale()))
		if err != nil {
			return nil, err
		}
		l.OnClose(r)

		return r, nil
	}),

	EventBus: func() event.Bus { return eventbus.NewBus() },
	Peerstore: func() (peerstore.Peerstore, error) {
		return pstoremem.NewPeerstore()
	},
	ConnManager: func(l *Lifecycle) (connmgr.ConnManager, error) {

		cm, err := netconnmgr.NewConnManager(160, 192)
		if err != nil {
			return nil, err
		}
		l.OnClose(cm)
		return cm, nil
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
			k crypto.PrivKey,
			ps peerstore.Peerstore,
			b event.Bus,
			listenAddrs ListenAddrs,

			swarmConfig SwarmConfig,
			dialConfig DialConfig,
			UDPBlackHoleSuccessCounter *swarm.BlackHoleSuccessCounter,
			IPv6BlackHoleSuccessCounter *swarm.BlackHoleSuccessCounter,
			rcmgr network.ResourceManager,
			multiaddrResolver di.Optional[network.MultiaddrDNSResolver],
			dialRanker di.Optional[network.DialRanker],
			metricsCfg MetricsConfig,
			gater connmgr.ConnectionGater,
		) (*swarm.Swarm, error) {
			if ps == nil {
				return nil, fmt.Errorf("no peerstore specified")
			}
			if err := ps.AddPrivKey(p, k); err != nil {
				return nil, err
			}
			if err := ps.AddPubKey(p, k.GetPublic()); err != nil {
				return nil, err
			}

			opts := slices.Clone(swarmConfig.Opts)
			opts = append(opts,
				swarm.WithUDPBlackHoleSuccessCounter(UDPBlackHoleSuccessCounter),
				swarm.WithIPv6BlackHoleSuccessCounter(IPv6BlackHoleSuccessCounter),
				swarm.WithDialTimeout(dialConfig.DialTimeout),
				swarm.WithResourceManager(rcmgr),
			)
			if multiaddrResolver.IsSome {
				opts = append(opts, swarm.WithMultiaddrResolver(multiaddrResolver.Unwrap()))
			}
			if dialRanker.IsSome {
				opts = append(opts, swarm.WithDialRanker(dialRanker.Unwrap()))
			}
			if gater != nil {
				opts = append(opts, swarm.WithConnectionGater(gater))
			}
			if metricsCfg.PrometheusRegisterer != nil {
				opts = append(opts,
					swarm.WithMetricsTracer(swarm.NewMetricsTracer(swarm.WithRegisterer(metricsCfg.PrometheusRegisterer))))
			}
			if metricsCfg.BandwidthReporter != nil {
				opts = append(opts, swarm.WithMetrics(metricsCfg.BandwidthReporter))
			}
			if swarmConfig.ReadOnlyBlackHoleDetector {
				opts = append(opts, swarm.WithReadOnlyBlackHoleDetector())
			}

			s, err := swarm.NewSwarm(p, ps, b, opts...)
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
	DialConfig: DialConfig{
		DialTimeout: 10 * time.Second,
	},
	MetricsConfig: MetricsConfig{
		PrometheusRegisterer: prometheus.DefaultRegisterer,
	},
	AutoRelayConfig: AutoRelayConfig{
		Enabled: false,
	},
	BasicHostConfig: BasicHostConfig{
		ObservedAddrsManager: di.MustProvide[bhost.ObservedAddrsManager](
			func(l *Lifecycle, eventBus event.Bus, s *swarm.Swarm) (bhost.ObservedAddrsManager, error) {
				o, err := observedaddrs.NewManager(eventBus, s)
				if err != nil {
					return nil, err
				}
				l.OnStart(func() error {
					o.Start(s)
					return nil
				})
				l.OnClose(o)
				return o, nil
			}),
	},

	AutoNatConfig: AutoNatConfig{
		PrivateKey: func() (AutoNatPrivKey, error) {
			priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
			return AutoNatPrivKey(priv), err
		},
		Peerstore: di.MustProvide[AutoNatPeerStore](
			func(l *Lifecycle) (AutoNatPeerStore, error) {
				ps, err := pstoremem.NewPeerstore()
				l.OnClose(ps)
				return AutoNatPeerStore(ps), err
			},
		),
		Opts: []autonatv2.AutoNATOption{},

		AutoNAT: di.MustProvide[*autonatv2.AutoNAT](
			func(
				prometheusRegisterer prometheus.Registerer,
				autonatHost AutoNATHost,
				autonatOptions []autonatv2.AutoNATOption,
			) (*autonatv2.AutoNAT, error) {
				if prometheusRegisterer != nil {
					mt := autonatv2.NewMetricsTracer(prometheusRegisterer)
					autonatOptions = append(
						[]autonatv2.AutoNATOption{autonatv2.WithMetricsTracer(mt)},
						autonatOptions...,
					)
				}
				autoNATv2, err := autonatv2.New(autonatHost, autonatOptions...)
				if err != nil {
					return nil, fmt.Errorf("failed to create autonatv2: %w", err)
				}
				return autoNATv2, nil
			},
		),
		Host: di.MustProvide[AutoNATHost](
			func(
				config Config,
				k AutoNatPrivKey,
				ps AutoNatPeerStore,
				l *Lifecycle,
			) (AutoNATHost, error) {
				// Use the same provided config, but override some
				autonatCfg := config
				autonatCfg.ListenAddrs = nil
				autonatCfg.Peerstore = func() (peerstore.Peerstore, error) {
					return ps, nil
				}
				autonatCfg.PrivateKey = func() (crypto.PrivKey, error) {
					return k, nil
				}
				autonatCfg.Lifecycle = func() *Lifecycle {
					// Use the same lifecycle as our parent config
					return l
				}
				autonatCfg.ReadOnlyBlackHoleDetector = true
				autonatCfg.Host = di.MustProvide[host.Host](func(
					l *Lifecycle,
					swarm *swarm.Swarm,
				) host.Host {
					mux := mstream.NewMultistreamMuxer[protocol.ID]()
					h := &blankhost.BlankHost{
						N:       swarm,
						M:       mux,
						ConnMgr: connmgr.NullConnMgr{},
						E:       nil,
						// Don't need this for autonat
						SkipInitSignedRecord: true,
					}
					l.OnStart(func() error {
						return h.Start()
					})
					l.OnClose(h)
					return h
				})
				type Result struct {
					Host host.Host
					_    []di.SideEffect
				}

				res, err := di.New[Result](autonatCfg)
				return res.Host, err
			},
		),
	},

	SideEffects: []di.Provide[di.SideEffect]{
		di.MustProvide[di.SideEffect](func(metricsRegisterer prometheus.Registerer) (di.SideEffect, error) {
			rcmgr.MustRegisterWith(metricsRegisterer)
			return di.SideEffect{}, nil
		}),
		di.MustProvide[di.SideEffect](func(logger *slog.Logger, rcmgr network.ResourceManager, cmgr connmgr.ConnManager) (di.SideEffect, error) {
			if l, ok := rcmgr.(connmgr.GetConnLimiter); ok {
				err := cmgr.CheckLimit(l)
				if err != nil {
					logger.Warn("rcmgr limit conflicts with connmgr limit", "err", err)
				}
			}
			return di.SideEffect{}, nil
		}),
		di.MustProvide[di.SideEffect](func(s *swarm.Swarm, tpts []transport.Transport) (di.SideEffect, error) {
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
		identifyConfig IdentifyConfig,
		basicHostConfig BasicHostConfig,
		observedAddrManager bhost.ObservedAddrsManager,
		metricsConfig MetricsConfig,
		autoRelayConfig AutoRelayConfig,
		network *swarm.Swarm,
		connmgr connmgr.ConnManager,
		autonat *autonatv2.AutoNAT,
		routingC di.Optional[RoutingC],
		eventBus event.Bus,
	) (h host.Host, err error) {
		bh, err := bhost.NewHost(network, &bhost.HostOpts{
			EventBus:             eventBus,
			ConnManager:          connmgr,
			AddrsFactory:         basicHostConfig.AddrsFactory.Val,
			NATManager:           basicHostConfig.NATManager,
			EnablePing:           basicHostConfig.EnablePing,
			UserAgent:            identifyConfig.UserAgent,
			ProtocolVersion:      identifyConfig.ProtocolVersion,
			EnableHolePunching:   basicHostConfig.EnableHolePunching,
			HolePunchingOptions:  basicHostConfig.HolePunchingOptions,
			EnableRelayService:   basicHostConfig.EnableRelay,
			RelayServiceOpts:     basicHostConfig.RelayServiceOpts,
			EnableMetrics:        metricsConfig.PrometheusRegisterer != nil,
			PrometheusRegisterer: metricsConfig.PrometheusRegisterer,
			ObservedAddrsManager: observedAddrManager,
			AutoNATv2:            autonat,
		})
		if err != nil {
			return nil, err
		}
		l.OnStart(func() error {
			bh.Start()
			return nil
		})
		l.OnClose(bh)
		h = bh

		if routingC.IsSome {
			router, err := routingC.Val(h)
			if err != nil {
				return nil, err
			}

			h = routed.Wrap(bh, router)
		}

		if autoRelayConfig.Enabled {
			autorelayOpts := autoRelayConfig.Opts
			if metricsConfig.PrometheusRegisterer != nil {
				mt := autorelay.WithMetricsTracer(
					autorelay.NewMetricsTracer(autorelay.WithRegisterer(metricsConfig.PrometheusRegisterer)))
				mtOpts := []autorelay.Option{mt}
				autorelayOpts = append(mtOpts, autoRelayConfig.Opts...)
			}
			ar, err := autorelay.NewAutoRelay(h, autorelayOpts...)
			if err != nil {
				return nil, err
			}
			l.OnStart(func() error {
				ar.Start()
				return nil
			})
			l.OnClose(ar)
		}

		return h, nil
	}),
}

type hostWithLifecycle struct {
	host.Host
	lifecycle io.Closer
}

func (h *hostWithLifecycle) Close() error {
	return h.lifecycle.Close()
}

// NewHost is a helper function for the common case of constructing host that
// will clean up the lifecycle of all instantiated objects when closed.
func NewHost(config Config) (host.Host, error) {
	type Result struct {
		L    *Lifecycle
		_    []di.SideEffect
		Host host.Host
	}
	r, err := di.New[Result](config)
	if err != nil {
		return nil, err
	}

	if err := r.L.Start(); err != nil {
		return nil, err
	}

	return &hostWithLifecycle{r.Host, r.L}, nil
}
