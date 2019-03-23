package config

import (
	"context"
	"fmt"

	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	relay "github.com/libp2p/go-libp2p/p2p/host/relay"
	routed "github.com/libp2p/go-libp2p/p2p/host/routed"

	logging "github.com/ipfs/go-log"
	circuit "github.com/libp2p/go-libp2p-circuit"
	crypto "github.com/libp2p/go-libp2p-crypto"
	discovery "github.com/libp2p/go-libp2p-discovery"
	host "github.com/libp2p/go-libp2p-host"
	ifconnmgr "github.com/libp2p/go-libp2p-interface-connmgr"
	pnet "github.com/libp2p/go-libp2p-interface-pnet"
	metrics "github.com/libp2p/go-libp2p-metrics"
	inet "github.com/libp2p/go-libp2p-net"
	peer "github.com/libp2p/go-libp2p-peer"
	pstore "github.com/libp2p/go-libp2p-peerstore"
	routing "github.com/libp2p/go-libp2p-routing"
	swarm "github.com/libp2p/go-libp2p-swarm"
	tptu "github.com/libp2p/go-libp2p-transport-upgrader"
	filter "github.com/libp2p/go-maddr-filter"
	ma "github.com/multiformats/go-multiaddr"
)

var log = logging.Logger("p2p-config")

// AddrsFactory is a function that takes a set of multiaddrs we're listening on and
// returns the set of multiaddrs we should advertise to the network.
type AddrsFactory = bhost.AddrsFactory

// NATManagerC is a NATManager constructor.
type NATManagerC func(inet.Network) bhost.NATManager

type RoutingC func(host.Host) (routing.PeerRouting, error)

// Config describes a set of settings for a libp2p node
//
// This is *not* a stable interface. Use the options defined in the root
// package.
type Config struct {
	PeerKey crypto.PrivKey

	Transports         []TptC
	Muxers             []MsMuxC
	SecurityTransports []MsSecC
	Insecure           bool
	Protector          pnet.Protector

	RelayCustom bool
	Relay       bool
	RelayOpts   []circuit.RelayOpt

	ListenAddrs  []ma.Multiaddr
	AddrsFactory bhost.AddrsFactory
	Filters      *filter.Filters

	ConnManager ifconnmgr.ConnManager
	NATManager  NATManagerC
	Peerstore   pstore.Peerstore
	Reporter    metrics.Reporter

	DisablePing bool

	Routing RoutingC

	EnableAutoRelay bool
}

// NewNode constructs a new libp2p Host from the Config.
//
// This function consumes the config. Do not reuse it (really!).
func (cfg *Config) NewNode(ctx context.Context) (host.Host, error) {
	// Check this early. Prevents us from even *starting* without verifying this.
	if pnet.ForcePrivateNetwork && cfg.Protector == nil {
		log.Error("tried to create a libp2p node with no Private" +
			" Network Protector but usage of Private Networks" +
			" is forced by the enviroment")
		// Note: This is *also* checked the upgrader itself so it'll be
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

	if cfg.Peerstore == nil {
		return nil, fmt.Errorf("no peerstore specified")
	}

	if !cfg.Insecure {
		cfg.Peerstore.AddPrivKey(pid, cfg.PeerKey)
		cfg.Peerstore.AddPubKey(pid, cfg.PeerKey.GetPublic())
	}

	// TODO: Make the swarm implementation configurable.
	swrm := swarm.NewSwarm(ctx, pid, cfg.Peerstore, cfg.Reporter)
	if cfg.Filters != nil {
		swrm.Filters = cfg.Filters
	}

	var h host.Host
	h, err = bhost.NewHost(ctx, swrm, &bhost.HostOpts{
		ConnManager:  cfg.ConnManager,
		AddrsFactory: cfg.AddrsFactory,
		NATManager:   cfg.NATManager,
		EnablePing:   !cfg.DisablePing,
	})
	if err != nil {
		swrm.Close()
		return nil, err
	}

	upgrader := new(tptu.Upgrader)
	upgrader.Protector = cfg.Protector
	upgrader.Filters = swrm.Filters
	if cfg.Insecure {
		upgrader.Secure = makeInsecureTransport(pid)
	} else {
		upgrader.Secure, err = makeSecurityTransport(h, cfg.SecurityTransports)
		if err != nil {
			h.Close()
			return nil, err
		}
	}

	upgrader.Muxer, err = makeMuxer(h, cfg.Muxers)
	if err != nil {
		h.Close()
		return nil, err
	}

	tpts, err := makeTransports(h, upgrader, cfg.Transports)
	if err != nil {
		h.Close()
		return nil, err
	}
	for _, t := range tpts {
		err = swrm.AddTransport(t)
		if err != nil {
			h.Close()
			return nil, err
		}
	}

	if cfg.Relay {
		err := circuit.AddRelayTransport(swrm.Context(), h, upgrader, cfg.RelayOpts...)
		if err != nil {
			h.Close()
			return nil, err
		}
	}

	// TODO: This method succeeds if listening on one address succeeds. We
	// should probably fail if listening on *any* addr fails.
	if err := h.Network().Listen(cfg.ListenAddrs...); err != nil {
		h.Close()
		return nil, err
	}

	// Configure routing and autorelay
	var router routing.PeerRouting
	if cfg.Routing != nil {
		router, err = cfg.Routing(h)
		if err != nil {
			h.Close()
			return nil, err
		}
	}

	if cfg.EnableAutoRelay {
		if !cfg.Relay {
			h.Close()
			return nil, fmt.Errorf("cannot enable autorelay; relay is not enabled")
		}

		if router == nil {
			h.Close()
			return nil, fmt.Errorf("cannot enable autorelay; no routing for discovery")
		}

		crouter, ok := router.(routing.ContentRouting)
		if !ok {
			h.Close()
			return nil, fmt.Errorf("cannot enable autorelay; no suitable routing for discovery")
		}

		discovery := discovery.NewRoutingDiscovery(crouter)

		hop := false
		for _, opt := range cfg.RelayOpts {
			if opt == circuit.OptHop {
				hop = true
				break
			}
		}

		if hop {
			h = relay.NewRelayHost(swrm.Context(), h.(*bhost.BasicHost), discovery)
		} else {
			h = relay.NewAutoRelayHost(swrm.Context(), h.(*bhost.BasicHost), discovery, router)
		}
	}

	if router != nil {
		h = routed.Wrap(h, router)
	}

	// TODO: Bootstrapping.

	return h, nil
}

// Option is a libp2p config option that can be given to the libp2p constructor
// (`libp2p.New`).
type Option func(cfg *Config) error

// Apply applies the given options to the config, returning the first error
// encountered (if any).
func (cfg *Config) Apply(opts ...Option) error {
	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return err
		}
	}
	return nil
}
