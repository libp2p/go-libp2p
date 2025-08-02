//go:build !js
// +build !js

package config

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/sec/insecure"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	routed "github.com/libp2p/go-libp2p/p2p/host/routed"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2"
	circuitv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse"
	libp2pwebrtc "github.com/libp2p/go-libp2p/p2p/transport/webrtc"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/quic-go/quic-go"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

// Config describes a set of settings for a libp2p node
//
// This is *not* a stable interface. Use the options defined in the root
// package.
type Config struct {
	// UserAgent is the identifier this node will send to other peers when
	// identifying itself, e.g. via the identify protocol.
	//
	// Set it via the UserAgent option function.
	UserAgent string

	// ProtocolVersion is the protocol version that identifies the family
	// of protocols used by the peer in the Identify protocol. It is set
	// using the [ProtocolVersion] option.
	ProtocolVersion string

	PeerKey crypto.PrivKey

	QUICReuse          []fx.Option
	Transports         []fx.Option
	Muxers             []tptu.StreamMuxer
	SecurityTransports []Security
	Insecure           bool
	PSK                pnet.PSK

	DialTimeout time.Duration

	RelayCustom bool
	Relay       bool // should the relay transport be used

	EnableRelayService bool // should we run a circuitv2 relay (if publicly reachable)
	RelayServiceOpts   []relayv2.Option

	ListenAddrs     []ma.Multiaddr
	AddrsFactory    bhost.AddrsFactory
	ConnectionGater connmgr.ConnectionGater

	ConnManager     connmgr.ConnManager
	ResourceManager network.ResourceManager

	NATManager NATManagerC
	Peerstore  peerstore.Peerstore
	Reporter   metrics.Reporter

	MultiaddrResolver network.MultiaddrDNSResolver

	DisablePing bool

	Routing RoutingC

	EnableAutoRelay bool
	AutoRelayOpts   []autorelay.Option
	AutoNATConfig

	EnableHolePunching  bool
	HolePunchingOptions []holepunch.Option

	DisableMetrics       bool
	PrometheusRegisterer prometheus.Registerer

	DialRanker network.DialRanker

	SwarmOpts []swarm.Option

	DisableIdentifyAddressDiscovery bool

	EnableAutoNATv2 bool

	UDPBlackHoleSuccessCounter        *swarm.BlackHoleSuccessCounter
	CustomUDPBlackHoleSuccessCounter  bool
	IPv6BlackHoleSuccessCounter       *swarm.BlackHoleSuccessCounter
	CustomIPv6BlackHoleSuccessCounter bool

	UserFxOptions []fx.Option

	ShareTCPListener bool
}

func (cfg *Config) addTransports() ([]fx.Option, error) {
	fxopts := []fx.Option{
		fx.WithLogger(func() fxevent.Logger { return getFXLogger() }),
		fx.Provide(fx.Annotate(tptu.New, fx.ParamTags(`name:"security"`))),
		fx.Supply(cfg.Muxers),
		fx.Provide(func() connmgr.ConnectionGater { return cfg.ConnectionGater }),
		fx.Provide(func() pnet.PSK { return cfg.PSK }),
		fx.Provide(func() network.ResourceManager { return cfg.ResourceManager }),
		fx.Provide(func(upgrader transport.Upgrader) *tcpreuse.ConnMgr {
			if !cfg.ShareTCPListener {
				return nil
			}
			return tcpreuse.NewConnMgr(tcpreuse.EnvReuseportVal, upgrader)
		}),
		fx.Provide(func(cm *quicreuse.ConnManager, sw *swarm.Swarm) libp2pwebrtc.ListenUDPFn {
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
	fxopts = append(fxopts, cfg.Transports...)
	if cfg.Insecure {
		fxopts = append(fxopts,
			fx.Provide(
				fx.Annotate(
					func(id peer.ID, priv crypto.PrivKey) []sec.SecureTransport {
						return []sec.SecureTransport{insecure.NewWithIdentity(insecure.ID, id, priv)}
					},
					fx.ResultTags(`name:"security"`),
				),
			),
		)
	} else {
		// fx groups are unordered, but we need to preserve the order of the security transports
		// First of all, we construct the security transports that are needed,
		// and save them to a group call security_unordered.
		for _, s := range cfg.SecurityTransports {
			fxName := fmt.Sprintf(`name:"security_%s"`, s.ID)
			fxopts = append(fxopts, fx.Supply(fx.Annotate(s.ID, fx.ResultTags(fxName))))
			fxopts = append(fxopts,
				fx.Provide(fx.Annotate(
					s.Constructor,
					fx.ParamTags(fxName),
					fx.As(new(sec.SecureTransport)),
					fx.ResultTags(`group:"security_unordered"`),
				)),
			)
		}
		// Then we consume the group security_unordered, and order them by the user's preference.
		fxopts = append(fxopts, fx.Provide(
			fx.Annotate(
				func(secs []sec.SecureTransport) ([]sec.SecureTransport, error) {
					if len(secs) != len(cfg.SecurityTransports) {
						return nil, errors.New("inconsistent length for security transports")
					}
					t := make([]sec.SecureTransport, 0, len(secs))
					for _, s := range cfg.SecurityTransports {
						for _, st := range secs {
							if s.ID != st.ID() {
								continue
							}
							t = append(t, st)
						}
					}
					return t, nil
				},
				fx.ParamTags(`group:"security_unordered"`),
				fx.ResultTags(`name:"security"`),
			)))
	}

	fxopts = append(fxopts, fx.Provide(PrivKeyToStatelessResetKey))
	fxopts = append(fxopts, fx.Provide(PrivKeyToTokenGeneratorKey))
	if cfg.QUICReuse != nil {
		fxopts = append(fxopts, cfg.QUICReuse...)
	} else {
		fxopts = append(fxopts,
			fx.Provide(func(key quic.StatelessResetKey, tokenGenerator quic.TokenGeneratorKey, rcmgr network.ResourceManager, lifecycle fx.Lifecycle) (*quicreuse.ConnManager, error) {
				opts := []quicreuse.Option{
					quicreuse.ConnContext(func(ctx context.Context, clientInfo *quic.ClientInfo) (context.Context, error) {
						// even if creating the quic maddr fails, let the rcmgr decide what to do with the connection
						addr, err := quicreuse.ToQuicMultiaddr(clientInfo.RemoteAddr, quic.Version1)
						if err != nil {
							addr = nil
						}
						scope, err := rcmgr.OpenConnection(network.DirInbound, false, addr)
						if err != nil {
							return ctx, err
						}
						ctx = network.WithConnManagementScope(ctx, scope)
						context.AfterFunc(ctx, func() {
							scope.Done()
						})
						return ctx, nil
					}),
					quicreuse.VerifySourceAddress(func(addr net.Addr) bool {
						return rcmgr.VerifySourceAddress(addr)
					}),
				}
				if !cfg.DisableMetrics {
					opts = append(opts, quicreuse.EnableMetrics(cfg.PrometheusRegisterer))
				}
				cm, err := quicreuse.NewConnManager(key, tokenGenerator, opts...)
				if err != nil {
					return nil, err
				}
				lifecycle.Append(fx.StopHook(cm.Close))
				return cm, nil
			}),
		)
	}

	fxopts = append(fxopts, fx.Invoke(
		fx.Annotate(
			func(swrm *swarm.Swarm, tpts []transport.Transport) error {
				for _, t := range tpts {
					if err := swrm.AddTransport(t); err != nil {
						return err
					}
				}
				return nil
			},
			fx.ParamTags("", `group:"transport"`),
		)),
	)
	if cfg.Relay {
		fxopts = append(fxopts, fx.Invoke(circuitv2.AddTransport))
	}
	return fxopts, nil
}

// NewNode constructs a new libp2p Host from the Config.
//
// This function consumes the config. Do not reuse it (really!).
func (cfg *Config) NewNode() (host.Host, error) {

	validateErr := cfg.validate()
	if validateErr != nil {
		if cfg.ResourceManager != nil {
			cfg.ResourceManager.Close()
		}
		if cfg.ConnManager != nil {
			cfg.ConnManager.Close()
		}
		if cfg.Peerstore != nil {
			cfg.Peerstore.Close()
		}

		return nil, validateErr
	}

	if !cfg.DisableMetrics {
		rcmgr.MustRegisterWith(cfg.PrometheusRegisterer)
	}

	fxopts := []fx.Option{
		fx.Provide(func() event.Bus {
			return eventbus.NewBus(eventbus.WithMetricsTracer(eventbus.NewMetricsTracer(eventbus.WithRegisterer(cfg.PrometheusRegisterer))))
		}),
		fx.Provide(func() crypto.PrivKey {
			return cfg.PeerKey
		}),
		// Make sure the swarm constructor depends on the quicreuse.ConnManager.
		// That way, the ConnManager will be started before the swarm, and more importantly,
		// the swarm will be stopped before the ConnManager.
		fx.Provide(func(eventBus event.Bus, _ *quicreuse.ConnManager, lifecycle fx.Lifecycle) (*swarm.Swarm, error) {
			sw, err := cfg.makeSwarm(eventBus, !cfg.DisableMetrics)
			if err != nil {
				return nil, err
			}
			lifecycle.Append(fx.Hook{
				OnStart: func(context.Context) error {
					// TODO: This method succeeds if listening on one address succeeds. We
					// should probably fail if listening on *any* addr fails.
					return sw.Listen(cfg.ListenAddrs...)
				},
				OnStop: func(context.Context) error {
					return sw.Close()
				},
			})
			return sw, nil
		}),
		fx.Provide(func() (*autonatv2.AutoNAT, error) {
			if !cfg.EnableAutoNATv2 {
				return nil, nil
			}
			ah, err := cfg.makeAutoNATV2Host()
			if err != nil {
				return nil, err
			}
			var mt autonatv2.MetricsTracer
			if !cfg.DisableMetrics {
				mt = autonatv2.NewMetricsTracer(cfg.PrometheusRegisterer)
			}
			autoNATv2, err := autonatv2.New(ah, autonatv2.WithMetricsTracer(mt))
			if err != nil {
				return nil, fmt.Errorf("failed to create autonatv2: %w", err)
			}
			return autoNATv2, nil
		}),
		fx.Provide(cfg.newBasicHost),
		fx.Provide(func(bh *bhost.BasicHost) identify.IDService {
			return bh.IDService()
		}),
		fx.Provide(func(bh *bhost.BasicHost) host.Host {
			return bh
		}),
		fx.Provide(func(h *swarm.Swarm) peer.ID { return h.LocalPeer() }),
	}
	transportOpts, err := cfg.addTransports()
	if err != nil {
		return nil, err
	}
	fxopts = append(fxopts, transportOpts...)

	// Configure routing
	if cfg.Routing != nil {
		fxopts = append(fxopts,
			fx.Provide(cfg.Routing),
			fx.Provide(func(h host.Host, router routing.PeerRouting) *routed.RoutedHost {
				return routed.Wrap(h, router)
			}),
		)
	}

	// enable autorelay
	fxopts = append(fxopts,
		fx.Invoke(func(h *bhost.BasicHost, lifecycle fx.Lifecycle) error {
			if cfg.EnableAutoRelay {
				if !cfg.DisableMetrics {
					mt := autorelay.WithMetricsTracer(
						autorelay.NewMetricsTracer(autorelay.WithRegisterer(cfg.PrometheusRegisterer)))
					mtOpts := []autorelay.Option{mt}
					cfg.AutoRelayOpts = append(mtOpts, cfg.AutoRelayOpts...)
				}

				ar, err := autorelay.NewAutoRelay(h, cfg.AutoRelayOpts...)
				if err != nil {
					return err
				}
				lifecycle.Append(fx.StartStopHook(ar.Start, ar.Close))
				return nil
			}
			return nil
		}),
	)

	var bh *bhost.BasicHost
	fxopts = append(fxopts, fx.Invoke(func(bho *bhost.BasicHost) { bh = bho }))
	fxopts = append(fxopts, fx.Invoke(func(h *bhost.BasicHost, lifecycle fx.Lifecycle) {
		lifecycle.Append(fx.StartHook(h.Start))
	}))

	var rh *routed.RoutedHost
	if cfg.Routing != nil {
		fxopts = append(fxopts, fx.Invoke(func(bho *routed.RoutedHost) { rh = bho }))
	}

	fxopts = append(fxopts, cfg.UserFxOptions...)

	app := fx.New(fxopts...)
	if err := app.Start(context.Background()); err != nil {
		return nil, err
	}

	if err := cfg.addAutoNAT(bh); err != nil {
		app.Stop(context.Background())
		if cfg.Routing != nil {
			rh.Close()
		} else {
			bh.Close()
		}
		return nil, err
	}

	if cfg.Routing != nil {
		return &closableRoutedHost{
			closableBasicHost: closableBasicHost{
				App:       app,
				BasicHost: bh,
			},
			RoutedHost: rh,
		}, nil
	}
	return &closableBasicHost{App: app, BasicHost: bh}, nil
}
