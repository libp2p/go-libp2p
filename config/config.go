// Package config defines an easy to use configuration struct to build a libp2p
// host.
//
// As a user, change the config by using `Option`.
// Defaults are defined by defaults.go in the root.
//
// As a maintainer, here are a couple of rules:
//
// Config:
//   - Define a typed Fx provider. If the object needs to be started and stopped
//     it should include a lifecycle.
//
// Ownership:
//
// Lifecycles:
package config

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
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
	"github.com/libp2p/go-libp2p/core/sec/insecure"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/host/autonat"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	basichost "github.com/libp2p/go-libp2p/p2p/host/basic"
	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/quic-go/quic-go"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

// AddrsFactory is a function that takes a set of multiaddrs we're listening on and
// returns the set of multiaddrs we should advertise to the network.
type AddrsFactory = bhost.AddrsFactory

// NATManagerC is a NATManager constructor.
type NATManagerC func(network.Network) bhost.NATManager

type RoutingC func(host.Host) (routing.PeerRouting, error)

// AutoNATConfig defines the AutoNAT behavior for the libp2p host.
type AutoNATConfig struct {
	ForceReachability   *network.Reachability
	EnableService       bool
	ThrottleGlobalLimit int
	ThrottlePeerLimit   int
	ThrottleInterval    time.Duration
}

type Security struct {
	ID          protocol.ID
	Constructor interface{}
}

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

	PeerKey TypedFxOption[crypto.PrivKey]

	QUICReuse          TypedFxOption[*quicreuse.ConnManager]
	Transports         []fx.Option
	Muxers             []tptu.StreamMuxer
	SecurityTransports []Security

	PSK pnet.PSK

	// Insecure is Deprecated
	Insecure bool

	DialTimeout time.Duration

	RelayCustom bool
	Relay       bool // should the relay transport be used

	EnableRelayService bool // should we run a circuitv2 relay (if publicly reachable)
	RelayServiceOpts   []relayv2.Option

	ListenAddrs     []ma.Multiaddr
	AddrsFactory    bhost.AddrsFactory
	ConnectionGater connmgr.ConnectionGater

	ConnManager     TypedFxOption[connmgr.ConnManager]
	ResourceManager TypedFxOption[network.ResourceManager]

	NATManager NATManagerC
	Peerstore  TypedFxOption[peerstore.Peerstore]
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

func (cfg *Config) newSwarm(peerkey crypto.PrivKey, eventBus event.Bus, rcmgr network.ResourceManager, peerstore peerstore.Peerstore) (*swarm.Swarm, error) {
	// Check this early. Prevents us from even *starting* without verifying this.
	if pnet.ForcePrivateNetwork && len(cfg.PSK) == 0 {
		log.Error("tried to create a libp2p node with no Private" +
			" Network Protector but usage of Private Networks" +
			" is forced by the environment")
		// Note: This is *also* checked the upgrader itself, so it'll be
		// enforced even *if* you don't use the libp2p constructor.
		return nil, pnet.ErrNotInPrivateNetwork
	}

	if peerkey == nil {
		return nil, fmt.Errorf("no peer key specified")
	}

	// Obtain Peer ID from public key
	pid, err := peer.IDFromPublicKey(peerkey.GetPublic())
	if err != nil {
		return nil, err
	}

	if err := peerstore.AddPrivKey(pid, peerkey); err != nil {
		return nil, err
	}
	if err := peerstore.AddPubKey(pid, peerkey.GetPublic()); err != nil {
		return nil, err
	}

	opts := append(cfg.SwarmOpts,
		swarm.WithUDPBlackHoleSuccessCounter(cfg.UDPBlackHoleSuccessCounter),
		swarm.WithIPv6BlackHoleSuccessCounter(cfg.IPv6BlackHoleSuccessCounter),
	)
	if cfg.Reporter != nil {
		opts = append(opts, swarm.WithMetrics(cfg.Reporter))
	}
	if cfg.ConnectionGater != nil {
		opts = append(opts, swarm.WithConnectionGater(cfg.ConnectionGater))
	}
	if cfg.DialTimeout != 0 {
		opts = append(opts, swarm.WithDialTimeout(cfg.DialTimeout))
	}
	if cfg.ResourceManager != nil {
		opts = append(opts, swarm.WithResourceManager(rcmgr))
	}
	if cfg.MultiaddrResolver != nil {
		opts = append(opts, swarm.WithMultiaddrResolver(cfg.MultiaddrResolver))
	}
	if cfg.DialRanker != nil {
		opts = append(opts, swarm.WithDialRanker(cfg.DialRanker))
	}

	if !cfg.DisableMetrics {
		opts = append(opts,
			swarm.WithMetricsTracer(swarm.NewMetricsTracer(swarm.WithRegisterer(cfg.PrometheusRegisterer))))
	}
	// TODO: Make the swarm implementation configurable.
	return swarm.NewSwarm(pid, peerstore, eventBus, opts...)
}

func (cfg *Config) newAutoNATV2() (*autonatv2.AutoNAT, error) {
	ah, err := cfg.newAutoNATV2Host()
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
}

func (cfg *Config) newAutoNATV2Host() (host.Host, error) {
	autonatPrivKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}
	ps, err := pstoremem.NewPeerstore()
	if err != nil {
		return nil, err
	}

	autoNatCfg := Config{
		Transports:                  cfg.Transports,
		QUICReuse:                   privateQUICReuse,
		Muxers:                      cfg.Muxers,
		SecurityTransports:          cfg.SecurityTransports,
		Insecure:                    cfg.Insecure,
		PSK:                         cfg.PSK,
		ConnectionGater:             cfg.ConnectionGater,
		Reporter:                    cfg.Reporter,
		DialRanker:                  swarm.NoDelayDialRanker,
		UDPBlackHoleSuccessCounter:  cfg.UDPBlackHoleSuccessCounter,
		IPv6BlackHoleSuccessCounter: cfg.IPv6BlackHoleSuccessCounter,
		DisableMetrics:              true,
		ResourceManager:             cfg.ResourceManager,
		SwarmOpts: []swarm.Option{
			// Don't update black hole state for failed autonat dials
			swarm.WithReadOnlyBlackHoleDetector(),
		},
	}
	fxopts, err := autoNatCfg.addTransports(fxPrivate{})
	if err != nil {
		return nil, err
	}
	var dialerHost host.Host
	fxopts = append(fxopts,
		fx.Provide(eventbus.NewBus),
		fx.Supply(fx.Annotate(ps,
			fx.As(new(peerstore.Peerstore)),
			fx.OnStop(func() error {
				return ps.Close()
			}),
		)),
		fx.Provide(autoNatCfg.newSwarm),
		fx.Provide(func(sw *swarm.Swarm) *blankhost.BlankHost {
			return blankhost.NewBlankHost(sw)
		}),
		fx.Provide(func(bh *blankhost.BlankHost) host.Host {
			return bh
		}),
		fx.Provide(func() crypto.PrivKey { return autonatPrivKey }),
		fx.Provide(func(bh host.Host) peer.ID { return bh.ID() }),
		fx.Invoke(func(bh *blankhost.BlankHost) {
			dialerHost = bh
		}),
	)
	app := fx.New(fxopts...)
	if err := app.Err(); err != nil {
		return nil, err
	}
	err = app.Start(context.Background())
	if err != nil {
		return nil, err
	}
	go func() {
		<-dialerHost.Network().(*swarm.Swarm).Done()
		app.Stop(context.Background())
	}()
	return dialerHost, nil
}

func defaultQUICReuse(fxi fxInterface) TypedFxOption[*quicreuse.ConnManager] {
	return Must(NewTypedFxProvide[*quicreuse.ConnManager](func(key quic.StatelessResetKey, tokenGenerator quic.TokenGeneratorKey, rcmgr network.ResourceManager, cfg *Config, lifecycle fx.Lifecycle, userOpts ...quicreuse.Option) (*quicreuse.ConnManager, error) {
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
		opts = append(opts, userOpts...)

		if !cfg.DisableMetrics {
			opts = append(opts, quicreuse.EnableMetrics(cfg.PrometheusRegisterer))
		}
		cm, err := quicreuse.NewConnManager(key, tokenGenerator, opts...)
		if err != nil {
			return nil, err
		}
		lifecycle.Append(fx.StopHook(cm.Close))
		return cm, nil
	}))
}

var DefaultQUICReuse = defaultQUICReuse(fxPublic{})
var privateQUICReuse = defaultQUICReuse(fxPrivate{})

type fxInterface interface {
	Supply(values ...any) fx.Option
	Provide(values ...any) fx.Option
}

type fxPublic struct {
}

func (pf fxPublic) Supply(values ...any) fx.Option {
	return fx.Supply(values...)
}

func (pf fxPublic) Provide(values ...any) fx.Option {
	return fx.Provide(values...)
}

type fxPrivate struct{}

func (pf fxPrivate) Supply(values ...any) fx.Option {
	values = append(values, fx.Private)
	return fx.Supply(values...)
}

func (pf fxPrivate) Provide(values ...any) fx.Option {
	values = append(values, fx.Private)
	return fx.Provide(values...)
}

func (cfg *Config) addTransports(fxi fxInterface) ([]fx.Option, error) {
	fxopts := []fx.Option{
		fx.WithLogger(func() fxevent.Logger { return getFXLogger() }),
		fxi.Supply(cfg.Muxers),
		fxi.Provide(
			fx.Annotate(tptu.New, fx.ParamTags(`name:"security"`)),
			func() connmgr.ConnectionGater { return cfg.ConnectionGater },
			func() pnet.PSK { return cfg.PSK },
			func(upgrader transport.Upgrader) *tcpreuse.ConnMgr {
				if !cfg.ShareTCPListener {
					return nil
				}
				return tcpreuse.NewConnMgr(tcpreuse.EnvReuseportVal, upgrader)
			},
			func(cm *quicreuse.ConnManager, sw *swarm.Swarm) libp2pwebrtc.ListenUDPFn {
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
			fxi.Provide(
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
			fxopts = append(fxopts, fxi.Supply(fx.Annotate(s.ID, fx.ResultTags(fxName))))
			fxopts = append(fxopts,
				fxi.Provide(fx.Annotate(
					s.Constructor,
					fx.ParamTags(fxName),
					fx.As(new(sec.SecureTransport)),
					fx.ResultTags(`group:"security_unordered"`),
				)),
			)
		}
		// Then we consume the group security_unordered, and order them by the user's preference.
		fxopts = append(fxopts, fxi.Provide(
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

	fxopts = append(fxopts, fxi.Provide(PrivKeyToStatelessResetKey))
	fxopts = append(fxopts, fxi.Provide(PrivKeyToTokenGeneratorKey))
	if cfg.QUICReuse != nil {

		fxopts = append(fxopts, cfg.QUICReuse.option())
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

func (cfg *Config) newBasicHost(swrm *swarm.Swarm, cm connmgr.ConnManager, eventBus event.Bus, an *autonatv2.AutoNAT) (*bhost.BasicHost, error) {
	h, err := bhost.NewHost(swrm, &bhost.HostOpts{
		EventBus:                        eventBus,
		ConnManager:                     cm,
		AddrsFactory:                    cfg.AddrsFactory,
		NATManager:                      cfg.NATManager,
		EnablePing:                      !cfg.DisablePing,
		UserAgent:                       cfg.UserAgent,
		ProtocolVersion:                 cfg.ProtocolVersion,
		EnableHolePunching:              cfg.EnableHolePunching,
		HolePunchingOptions:             cfg.HolePunchingOptions,
		EnableRelayService:              cfg.EnableRelayService,
		RelayServiceOpts:                cfg.RelayServiceOpts,
		EnableMetrics:                   !cfg.DisableMetrics,
		PrometheusRegisterer:            cfg.PrometheusRegisterer,
		DisableIdentifyAddressDiscovery: cfg.DisableIdentifyAddressDiscovery,
		AutoNATv2:                       an,
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (cfg *Config) validate() error {
	if cfg.EnableAutoRelay && !cfg.Relay {
		return fmt.Errorf("cannot enable autorelay; relay is not enabled")
	}

	if len(cfg.PSK) > 0 && cfg.ShareTCPListener {
		return errors.New("cannot use shared TCP listener with PSK")
	}

	return nil
}

func (cfg *Config) orderedSecurity() fx.Option {
	var fxopts []fx.Option
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
	return fx.Options(fxopts...)
}

func (cfg *Config) Fx() fx.Option {

	// TODO remove this
	type swarmParams struct {
		fx.In
		Pk        crypto.PrivKey
		EventBus  event.Bus
		Rcmgr     network.ResourceManager
		Ps        peerstore.Peerstore
		Lifecycle fx.Lifecycle
		// Make sure the swarm constructor depends on the quicreuse.ConnManager.
		// That way, the ConnManager will be started before the swarm, and more importantly,
		// the swarm will be stopped before the ConnManager.
		QuicCM *quicreuse.ConnManager `optional:"true"`
	}

	var conditionalOpts []fx.Option
	if cfg.EnableAutoNATv2 {
		conditionalOpts = append(conditionalOpts, fx.Provide(cfg.newAutoNATV2))
	}
	if cfg.ShareTCPListener {
		conditionalOpts = append(conditionalOpts, fx.Provide(func(upgrader transport.Upgrader) *tcpreuse.ConnMgr {
			return tcpreuse.NewConnMgr(tcpreuse.EnvReuseportVal, upgrader)
		}))
	}
	if cfg.Relay {
		conditionalOpts = append(conditionalOpts, fx.Invoke(circuitv2.AddTransport))
	}
	if cfg.Routing != nil {
		conditionalOpts = append(conditionalOpts,
			fx.Provide(cfg.Routing),
			fx.Decorate(func(h host.Host, router routing.PeerRouting) host.Host {
				rh := routed.Wrap(h, router)
				return rh
			}))
	}
	if cfg.EnableAutoRelay {
		conditionalOpts = append(conditionalOpts, fx.Invoke(
			func(h *bhost.BasicHost, lifecycle fx.Lifecycle) error {
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
			}))
	}

	return fx.Options(
		fx.WithLogger(func() fxevent.Logger { return getFXLogger() }),
		fx.Options(conditionalOpts...),
		cfg.ResourceManager.option(),
		fx.Provide(func() event.Bus {
			return eventbus.NewBus(eventbus.WithMetricsTracer(eventbus.NewMetricsTracer(eventbus.WithRegisterer(cfg.PrometheusRegisterer))))
		}),
		cfg.PeerKey.option(),
		fx.Provide(func(params swarmParams) (*swarm.Swarm, error) {
			sw, err := cfg.newSwarm(params.Pk, params.EventBus, params.Rcmgr, params.Ps)
			if err != nil {
				return nil, err
			}
			params.Lifecycle.Append(fx.Hook{
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
		fx.Provide(fx.Annotate(cfg.newBasicHost, fx.OnStart(func(bh *bhost.BasicHost) {
			bh.Start()
		}))),
		fx.Provide(func(bh *bhost.BasicHost) identify.IDService {
			return bh.IDService()
		}),
		fx.Provide(func(bh *bhost.BasicHost) host.Host {
			return bh
		}),
		fx.Provide(func(h *swarm.Swarm) peer.ID { return h.LocalPeer() }),
		fx.Provide(cfg.orderedSecurity()),
		fx.Provide(fx.Annotate(tptu.New, fx.ParamTags(`name:"security"`))),
		fx.Supply(cfg.Muxers),
		fx.Provide(func() connmgr.ConnectionGater { return cfg.ConnectionGater }),
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
		fx.Provide(PrivKeyToStatelessResetKey),
		fx.Provide(PrivKeyToTokenGeneratorKey),
		fx.Options(cfg.Transports...),
		cfg.QUICReuse.option(),
		fx.Invoke(
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
		fx.Options(cfg.UserFxOptions...),

		// Sanity Check
		fx.Invoke(func(rcmgr network.ResourceManager, cmgr connmgr.ConnManager) {
			// If possible check that the resource manager conn limit is higher than the
			// limit set in the conn manager.
			if l, ok := rcmgr.(connmgr.GetConnLimiter); ok {
				err := cmgr.CheckLimit(l)
				if err != nil {
					log.Warn(fmt.Sprintf("rcmgr limit conflicts with connmgr limit: %v", err))
				}
			}
		}),

		// Host shut down
		fx.Invoke(func(h host.Host, l fx.Lifecycle) {
			l.Append(fx.StopHook(func() error {
				return h.Close()
			}))
		}),
	)

}

// NewNode constructs a new libp2p Host from the Config.
//
// This function consumes the config. Do not reuse it (really!).
func (cfg *Config) NewNode() (host.Host, error) {

	validateErr := cfg.validate()
	if validateErr != nil {
		return nil, validateErr
	}

	if !cfg.DisableMetrics {
		rcmgr.MustRegisterWith(cfg.PrometheusRegisterer)
	}

	fxopts := []fx.Option{
		cfg.ResourceManager.option(),
		cfg.ConnManager.option(),
		cfg.Peerstore.option(),
		cfg.PeerKey.option(),
		fx.Supply(cfg),
		fx.Invoke(func(rcmgr network.ResourceManager, cmgr connmgr.ConnManager) {
			// If possible check that the resource manager conn limit is higher than the
			// limit set in the conn manager.
			if l, ok := rcmgr.(connmgr.GetConnLimiter); ok {
				err := cmgr.CheckLimit(l)
				if err != nil {
					log.Warn(fmt.Sprintf("rcmgr limit conflicts with connmgr limit: %v", err))
				}
			}
		}),
		fx.Provide(func() event.Bus {
			return eventbus.NewBus(eventbus.WithMetricsTracer(eventbus.NewMetricsTracer(eventbus.WithRegisterer(cfg.PrometheusRegisterer))))
		}),
		// Make sure the swarm constructor depends on the quicreuse.ConnManager.
		// That way, the ConnManager will be started before the swarm, and more importantly,
		// the swarm will be stopped before the ConnManager.
		fx.Provide(func(k crypto.PrivKey, eventBus event.Bus, rcmgr network.ResourceManager, _ *quicreuse.ConnManager, ps peerstore.Peerstore, lifecycle fx.Lifecycle) (*swarm.Swarm, error) {
			sw, err := cfg.newSwarm(k, eventBus, rcmgr, ps)
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
			ah, err := cfg.newAutoNATV2Host()
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
	transportOpts, err := cfg.addTransports(fxPublic{})
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

	fxopts = append(fxopts,
		cfg.addAutoNAT(),
		fx.Invoke(func(bh *basichost.BasicHost, autonat autonat.AutoNAT) {
			bh.SetAutoNat(autonat)
		}))

	app := fx.New(fxopts...)
	if err := app.Start(context.Background()); err != nil {
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

func (cfg *Config) addAutoNAT() fx.Option {
	var fxOpts []fx.Option

	fxOpts = append(fxOpts, fx.Provide(fx.Annotate(func(bh *basichost.BasicHost) []autonat.Option {
		// Only use public addresses for autonat
		addrFunc := func() []ma.Multiaddr {
			return slices.DeleteFunc(bh.AllAddrs(), func(a ma.Multiaddr) bool { return !manet.IsPublicAddr(a) })
		}
		if cfg.AddrsFactory != nil {
			addrFunc = func() []ma.Multiaddr {
				return slices.DeleteFunc(
					slices.Clone(cfg.AddrsFactory(bh.AllAddrs())),
					func(a ma.Multiaddr) bool { return !manet.IsPublicAddr(a) })
			}
		}
		autonatOpts := []autonat.Option{autonat.UsingAddresses(addrFunc)}

		if !cfg.DisableMetrics {
			autonatOpts = append(autonatOpts, autonat.WithMetricsTracer(
				autonat.NewMetricsTracer(autonat.WithRegisterer(cfg.PrometheusRegisterer)),
			))
		}
		if cfg.AutoNATConfig.ThrottleInterval != 0 {
			autonatOpts = append(autonatOpts,
				autonat.WithThrottling(cfg.AutoNATConfig.ThrottleGlobalLimit, cfg.AutoNATConfig.ThrottleInterval),
				autonat.WithPeerThrottling(cfg.AutoNATConfig.ThrottlePeerLimit))
		}
		return autonatOpts
	}, fx.ResultTags(`group:"autonat_opt,flatten"`)), fx.Private))

	if cfg.AutoNATConfig.EnableService {
		// Pull out the pieces of the config that we _actually_ care about.
		// Specifically, don't set up things like listeners, identify, etc.
		autoNatCfg := Config{
			Transports: cfg.Transports,
			// QUICReuse:          privateQUICReuse,
			Muxers:             cfg.Muxers,
			SecurityTransports: cfg.SecurityTransports,
			Insecure:           cfg.Insecure,
			PSK:                cfg.PSK,
			ConnectionGater:    cfg.ConnectionGater,
			Reporter:           cfg.Reporter,
			DialRanker:         swarm.NoDelayDialRanker,
			ResourceManager:    cfg.ResourceManager,
			DisableMetrics:     true,
			SwarmOpts: []swarm.Option{
				swarm.WithUDPBlackHoleSuccessCounter(nil),
				swarm.WithIPv6BlackHoleSuccessCounter(nil),
			},
		}
		swarmOpts := []fx.Option{
			fx.Supply(&autoNatCfg, fx.Private),
			fx.Provide(func() (crypto.PrivKey, error) {
				autonatPrivKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
				return autonatPrivKey, err
			}, fx.Private),
			fx.Provide(func(l fx.Lifecycle) (peerstore.Peerstore, error) {
				ps, err := pstoremem.NewPeerstore()
				l.Append(fx.StopHook(func() error {
					return ps.Close()
				}))
				return ps, err
			}, fx.Private),
		}

		tptOpts, err := autoNatCfg.addTransports(fxPrivate{})
		if err != nil {
			return fx.Error(err)
		}
		swarmOpts = append(swarmOpts, tptOpts...)

		swarmOpts = append(swarmOpts,
			fx.Provide(eventbus.NewBus, fx.Private),
			fx.Provide(autoNatCfg.newSwarm, fx.Private),
			fx.Provide(func(s *swarm.Swarm) peer.ID { return s.LocalPeer() }, fx.Private),
		)

		fxOpts = append(fxOpts,
			fx.Options(swarmOpts...),
			fx.Provide(fx.Annotate(func(dialer *swarm.Swarm) autonat.Option {
				return autonat.EnableService(dialer)
			}, fx.ResultTags(`group:"autonat_opt"`)), fx.Private),
		)
	}

	if cfg.AutoNATConfig.ForceReachability != nil {
		fxOpts = append(fxOpts,
			fx.Provide(fx.Annotate(func() {
				autonat.WithReachability(*cfg.AutoNATConfig.ForceReachability)
			}, fx.ResultTags(`group:"autonat_opt`)), fx.Private),
		)
	}

	type autonatParams struct {
		fx.In
		Bh   *basichost.BasicHost
		L    fx.Lifecycle
		Opts []autonat.Option `group:"autonat_opt"`
	}
	fxOpts = append(fxOpts, fx.Provide(func(params autonatParams) (autonat.AutoNAT, error) {
		a, err := autonat.New(params.Bh, params.Opts...)
		if err != nil {
			return nil, err
		}
		params.L.Append(fx.StopHook(func() error {
			return a.Close()
		}))
		return a, nil
	}))
	return fx.Module("autonat", fx.Options(fxOpts...))
}

// Option is a libp2p config option that can be given to the libp2p constructor
// (`libp2p.New`).
type Option func(cfg *Config) error

// Apply applies the given options to the config, returning the first error
// encountered (if any).
func (cfg *Config) Apply(opts ...Option) error {
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(cfg); err != nil {
			return err
		}
	}
	return nil
}
