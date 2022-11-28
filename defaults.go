package libp2p

// This file contains all the default configuration options.

import (
	"crypto/rand"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	tls "github.com/libp2p/go-libp2p/p2p/security/tls"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"

	"github.com/multiformats/go-multiaddr"
	madns "github.com/multiformats/go-multiaddr-dns"
)

// DefaultSecurity is the default security option.
//
// Useful when you want to extend, but not replace, the supported transport
// security protocols.
var DefaultSecurity = ChainOptions(
	Security(noise.ID, noise.New),
	Security(tls.ID, tls.New),
)

// DefaultMuxers configures libp2p to use the stream connection multiplexers.
//
// Use this option when you want to *extend* the set of multiplexers used by
// libp2p instead of replacing them.
var DefaultMuxers = Muxer("/yamux/1.0.0", yamux.DefaultTransport)

// DefaultTransports are the default libp2p transports.
//
// Use this option when you want to *extend* the set of transports used by
// libp2p instead of replacing them.
var DefaultTransports = ChainOptions(
	Transport(tcp.NewTCPTransport),
	Transport(quic.NewTransport),
	Transport(ws.New),
)

// DefaultPrivateTransports are the default libp2p transports when a PSK is supplied.
//
// Use this option when you want to *extend* the set of transports used by
// libp2p instead of replacing them.
var DefaultPrivateTransports = ChainOptions(
	Transport(tcp.NewTCPTransport),
	Transport(ws.New),
)

// DefaultPeerstore configures libp2p to use the default peerstore.
var DefaultPeerstore Option = func(cfg *Config) error {
	ps, err := pstoremem.NewPeerstore()
	if err != nil {
		return err
	}
	return cfg.Apply(Peerstore(ps))
}

// RandomIdentity generates a random identity. (default behaviour)
var RandomIdentity = func(cfg *Config) error {
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return err
	}
	return cfg.Apply(Identity(priv))
}

// DefaultListenAddrs configures libp2p to use default listen address.
var DefaultListenAddrs = func(cfg *Config) error {
	defaultIP4TCPListenAddr, err := multiaddr.NewMultiaddr("/ip4/0.0.0.0/tcp/0")
	if err != nil {
		return err
	}
	defaultIP4QUICListenAddr, err := multiaddr.NewMultiaddr("/ip4/0.0.0.0/udp/0/quic")
	if err != nil {
		return err
	}

	defaultIP6TCPListenAddr, err := multiaddr.NewMultiaddr("/ip6/::/tcp/0")
	if err != nil {
		return err
	}
	defaultIP6QUICListenAddr, err := multiaddr.NewMultiaddr("/ip6/::/udp/0/quic")
	if err != nil {
		return err
	}
	return cfg.Apply(ListenAddrs(
		defaultIP4TCPListenAddr,
		defaultIP4QUICListenAddr,
		defaultIP6TCPListenAddr,
		defaultIP6QUICListenAddr,
	))
}

// DefaultEnableRelay enables relay dialing and listening by default.
var DefaultEnableRelay = func(cfg *Config) error {
	return cfg.Apply(EnableRelay())
}

var DefaultResourceManager = func(cfg *Config) error {
	// Default memory limit: 1/8th of total memory, minimum 128MB, maximum 1GB
	limits := rcmgr.DefaultLimits
	SetDefaultServiceLimits(&limits)
	mgr, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(limits.AutoScale()))
	if err != nil {
		return err
	}

	return cfg.Apply(ResourceManager(mgr))
}

// DefaultConnectionManager creates a default connection manager
var DefaultConnectionManager = func(cfg *Config) error {
	mgr, err := connmgr.NewConnManager(160, 192)
	if err != nil {
		return err
	}

	return cfg.Apply(ConnectionManager(mgr))
}

// DefaultMultiaddrResolver creates a default connection manager
var DefaultMultiaddrResolver = func(cfg *Config) error {
	return cfg.Apply(MultiaddrResolver(madns.DefaultResolver))
}

// Complete list of default options and when to fallback on them.
//
// Please *DON'T* specify default options any other way. Putting this all here
// makes tracking defaults *much* easier.
var defaults = []struct {
	fallback func(cfg *Config) bool
	opt      Option
}{
	{
		fallback: func(cfg *Config) bool { return cfg.Transports == nil && cfg.ListenAddrs == nil },
		opt:      DefaultListenAddrs,
	},
	{
		fallback: func(cfg *Config) bool { return cfg.Transports == nil && cfg.PSK == nil },
		opt:      DefaultTransports,
	},
	{
		fallback: func(cfg *Config) bool { return cfg.Transports == nil && cfg.PSK != nil },
		opt:      DefaultPrivateTransports,
	},
	{
		fallback: func(cfg *Config) bool { return cfg.Muxers == nil },
		opt:      DefaultMuxers,
	},
	{
		fallback: func(cfg *Config) bool { return !cfg.Insecure && cfg.SecurityTransports == nil },
		opt:      DefaultSecurity,
	},
	{
		fallback: func(cfg *Config) bool { return cfg.PeerKey == nil },
		opt:      RandomIdentity,
	},
	{
		fallback: func(cfg *Config) bool { return cfg.Peerstore == nil },
		opt:      DefaultPeerstore,
	},
	{
		fallback: func(cfg *Config) bool { return !cfg.RelayCustom },
		opt:      DefaultEnableRelay,
	},
	{
		fallback: func(cfg *Config) bool { return cfg.ResourceManager == nil },
		opt:      DefaultResourceManager,
	},
	{
		fallback: func(cfg *Config) bool { return cfg.ConnManager == nil },
		opt:      DefaultConnectionManager,
	},
	{
		fallback: func(cfg *Config) bool { return cfg.MultiaddrResolver == nil },
		opt:      DefaultMultiaddrResolver,
	},
}

// Defaults configures libp2p to use the default options. Can be combined with
// other options to *extend* the default options.
var Defaults Option = func(cfg *Config) error {
	for _, def := range defaults {
		if err := cfg.Apply(def.opt); err != nil {
			return err
		}
	}
	return nil
}

// FallbackDefaults applies default options to the libp2p node if and only if no
// other relevant options have been applied. will be appended to the options
// passed into New.
var FallbackDefaults Option = func(cfg *Config) error {
	for _, def := range defaults {
		if !def.fallback(cfg) {
			continue
		}
		if err := cfg.Apply(def.opt); err != nil {
			return err
		}
	}
	return nil
}
