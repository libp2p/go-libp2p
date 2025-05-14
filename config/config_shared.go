package config

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/host/autonat"
	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"go.uber.org/fx"
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

func (cfg *Config) makeSwarm(eventBus event.Bus, enableMetrics bool) (*swarm.Swarm, error) {
	if cfg.Peerstore == nil {
		return nil, fmt.Errorf("no peerstore specified")
	}

	// Check this early. Prevents us from even *starting* without verifying this.
	if pnet.ForcePrivateNetwork && len(cfg.PSK) == 0 {
		log.Error("tried to create a libp2p node with no Private" +
			" Network Protector but usage of Private Networks" +
			" is forced by the environment")
		// Note: This is *also* checked the upgrader itself, so it'll be
		// enforced even *if* you don't use the libp2p constructor.
		return nil, pnet.ErrNotInPrivateNetwork
	}

	if cfg.PeerKey == nil {
		return nil, fmt.Errorf("no peer key specified")
	}

	// Obtain Peer ID from public key
	pid, err := peer.IDFromPublicKey(cfg.PeerKey.GetPublic())
	if err != nil {
		return nil, err
	}

	if err := cfg.Peerstore.AddPrivKey(pid, cfg.PeerKey); err != nil {
		return nil, err
	}
	if err := cfg.Peerstore.AddPubKey(pid, cfg.PeerKey.GetPublic()); err != nil {
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
		opts = append(opts, swarm.WithResourceManager(cfg.ResourceManager))
	}
	if cfg.MultiaddrResolver != nil {
		opts = append(opts, swarm.WithMultiaddrResolver(cfg.MultiaddrResolver))
	}
	if cfg.DialRanker != nil {
		opts = append(opts, swarm.WithDialRanker(cfg.DialRanker))
	}

	if enableMetrics {
		opts = append(opts,
			swarm.WithMetricsTracer(swarm.NewMetricsTracer(swarm.WithRegisterer(cfg.PrometheusRegisterer))))
	}
	// TODO: Make the swarm implementation configurable.
	return swarm.NewSwarm(pid, cfg.Peerstore, eventBus, opts...)
}

func (cfg *Config) makeAutoNATV2Host() (host.Host, error) {
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
		Muxers:                      cfg.Muxers,
		SecurityTransports:          cfg.SecurityTransports,
		Insecure:                    cfg.Insecure,
		PSK:                         cfg.PSK,
		ConnectionGater:             cfg.ConnectionGater,
		Reporter:                    cfg.Reporter,
		PeerKey:                     autonatPrivKey,
		Peerstore:                   ps,
		DialRanker:                  swarm.NoDelayDialRanker,
		UDPBlackHoleSuccessCounter:  cfg.UDPBlackHoleSuccessCounter,
		IPv6BlackHoleSuccessCounter: cfg.IPv6BlackHoleSuccessCounter,
		ResourceManager:             cfg.ResourceManager,
		SwarmOpts: []swarm.Option{
			// Don't update black hole state for failed autonat dials
			swarm.WithReadOnlyBlackHoleDetector(),
		},
	}
	fxopts, err := autoNatCfg.addTransports()
	if err != nil {
		return nil, err
	}
	var dialerHost host.Host
	fxopts = append(fxopts,
		fx.Provide(eventbus.NewBus),
		fx.Provide(func(lifecycle fx.Lifecycle, b event.Bus) (*swarm.Swarm, error) {
			lifecycle.Append(fx.Hook{
				OnStop: func(context.Context) error {
					return ps.Close()
				}})
			sw, err := autoNatCfg.makeSwarm(b, false)
			return sw, err
		}),
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

func (cfg *Config) newBasicHost(swrm *swarm.Swarm, eventBus event.Bus) (*bhost.BasicHost, error) {
	var autonatv2Dialer host.Host
	if cfg.EnableAutoNATv2 {
		ah, err := cfg.makeAutoNATV2Host()
		if err != nil {
			return nil, err
		}
		autonatv2Dialer = ah
	}
	h, err := bhost.NewHost(swrm, &bhost.HostOpts{
		EventBus:                        eventBus,
		ConnManager:                     cfg.ConnManager,
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
		EnableAutoNATv2:                 cfg.EnableAutoNATv2,
		AutoNATv2Dialer:                 autonatv2Dialer,
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
	// If possible check that the resource manager conn limit is higher than the
	// limit set in the conn manager.
	if l, ok := cfg.ResourceManager.(connmgr.GetConnLimiter); ok {
		err := cfg.ConnManager.CheckLimit(l)
		if err != nil {
			log.Warn(fmt.Sprintf("rcmgr limit conflicts with connmgr limit: %v", err))
		}
	}

	if len(cfg.PSK) > 0 && cfg.ShareTCPListener {
		return errors.New("cannot use shared TCP listener with PSK")
	}

	return nil
}

func (cfg *Config) addAutoNAT(h *bhost.BasicHost) error {
	// Only use public addresses for autonat
	addrFunc := func() []ma.Multiaddr {
		return slices.DeleteFunc(h.AllAddrs(), func(a ma.Multiaddr) bool { return !manet.IsPublicAddr(a) })
	}
	if cfg.AddrsFactory != nil {
		addrFunc = func() []ma.Multiaddr {
			return slices.DeleteFunc(
				slices.Clone(cfg.AddrsFactory(h.AllAddrs())),
				func(a ma.Multiaddr) bool { return !manet.IsPublicAddr(a) })
		}
	}
	autonatOpts := []autonat.Option{
		autonat.UsingAddresses(addrFunc),
	}
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
	if cfg.AutoNATConfig.EnableService {
		autonatPrivKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return err
		}
		ps, err := pstoremem.NewPeerstore()
		if err != nil {
			return err
		}

		// Pull out the pieces of the config that we _actually_ care about.
		// Specifically, don't set up things like listeners, identify, etc.
		autoNatCfg := Config{
			Transports:         cfg.Transports,
			Muxers:             cfg.Muxers,
			SecurityTransports: cfg.SecurityTransports,
			Insecure:           cfg.Insecure,
			PSK:                cfg.PSK,
			ConnectionGater:    cfg.ConnectionGater,
			Reporter:           cfg.Reporter,
			PeerKey:            autonatPrivKey,
			Peerstore:          ps,
			DialRanker:         swarm.NoDelayDialRanker,
			ResourceManager:    cfg.ResourceManager,
			SwarmOpts: []swarm.Option{
				swarm.WithUDPBlackHoleSuccessCounter(nil),
				swarm.WithIPv6BlackHoleSuccessCounter(nil),
			},
		}

		fxopts, err := autoNatCfg.addTransports()
		if err != nil {
			return err
		}
		var dialer *swarm.Swarm

		fxopts = append(fxopts,
			fx.Provide(eventbus.NewBus),
			fx.Provide(func(lifecycle fx.Lifecycle, b event.Bus) (*swarm.Swarm, error) {
				lifecycle.Append(fx.Hook{
					OnStop: func(context.Context) error {
						return ps.Close()
					}})
				var err error
				dialer, err = autoNatCfg.makeSwarm(b, false)
				return dialer, err

			}),
			fx.Provide(func(s *swarm.Swarm) peer.ID { return s.LocalPeer() }),
			fx.Provide(func() crypto.PrivKey { return autonatPrivKey }),
		)
		app := fx.New(fxopts...)
		if err := app.Err(); err != nil {
			return err
		}
		err = app.Start(context.Background())
		if err != nil {
			return err
		}
		go func() {
			<-dialer.Done() // The swarm used for autonat has closed, we can cleanup now
			app.Stop(context.Background())
		}()
		autonatOpts = append(autonatOpts, autonat.EnableService(dialer))
	}
	if cfg.AutoNATConfig.ForceReachability != nil {
		autonatOpts = append(autonatOpts, autonat.WithReachability(*cfg.AutoNATConfig.ForceReachability))
	}

	autonat, err := autonat.New(h, autonatOpts...)
	if err != nil {
		return fmt.Errorf("autonat init failed: %w", err)
	}
	h.SetAutoNat(autonat)
	return nil
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
