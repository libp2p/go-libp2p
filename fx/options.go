package libp2pfx

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"slices"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/transport"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	connmgrImpl "github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	tls "github.com/libp2p/go-libp2p/p2p/security/tls"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse"
	"github.com/multiformats/go-multiaddr"
	mstream "github.com/multiformats/go-multistream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/quic-go/quic-go"
	"go.uber.org/fx"
	"golang.org/x/crypto/hkdf"
)

type blankHostParams struct {
	fx.In
	Network   network.Network
	Mux       *mstream.MultistreamMuxer[protocol.ID]
	ConnMgr   connmgr.ConnManager
	EventBus  event.Bus
	Lifecycle fx.Lifecycle
}

func BlankHost() fx.Option {
	return fx.Provide(func(params blankHostParams) host.Host {
		h := blankhost.BlankHost{
			N:       params.Network,
			M:       params.Mux,
			ConnMgr: params.ConnMgr,
			E:       params.EventBus,
			// Users can do this manually, but can't opt out of it otherwise.
			SkipInitSignedRecord: true,
		}
		params.Lifecycle.Append(fx.Hook{
			OnStart: func(context.Context) error {
				return h.Start()
			},
			OnStop: func(context.Context) error {
				return h.Close()
			},
		})
		return &h
	})
}

type swarmParams struct {
	fx.In
	fx.Lifecycle
	Local       peer.ID
	Peerstore   peerstore.Peerstore
	EventBus    event.Bus
	ListenAddrs []ListenAddr `group:"listenAddr"`
}

func SwarmNetwork(opts ...swarm.Option) fx.Option {
	return fx.Module("swarm",
		fx.Provide(fx.Annotate(func(params swarmParams) (*swarm.Swarm, error) {
			s, err := swarm.NewSwarm(
				params.Local,
				params.Peerstore,
				params.EventBus,
				opts...,
			)
			if err != nil {
				return nil, err
			}
			params.Lifecycle.Append(fx.StartStopHook(
				func() error {
					addrs := make([]multiaddr.Multiaddr, len(params.ListenAddrs))
					for i, a := range params.ListenAddrs {
						addrs[i] = multiaddr.Multiaddr(a)
					}
					return s.Listen(addrs...)
				},
				s.Close,
			))
			return s, nil
		}, fx.As(new(network.Network)), fx.As(fx.Self()))),
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
	)
}

type peerIDRes struct {
	fx.Out
	Peer peer.ID
	Key  crypto.PrivKey
}

func RandomPeerID() fx.Option {
	return fx.Provide(func() (peerIDRes, error) {
		priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return peerIDRes{}, err
		}
		pid, err := peer.IDFromPrivateKey(priv)
		if err != nil {
			return peerIDRes{}, err
		}
		return peerIDRes{Peer: pid, Key: priv}, nil
	})
}

var QUICTransport = fx.Provide(
	fx.Annotate(
		func(params struct {
			fx.In
			PrivKey     crypto.PrivKey
			QUICConnMgr *quicreuse.ConnManager
			ConnGater   connmgr.ConnectionGater
			Rcmgr       network.ResourceManager
		}) (transport.Transport, error) {
			return libp2pquic.NewTransport(
				params.PrivKey,
				params.QUICConnMgr,
				nil,
				params.ConnGater,
				params.Rcmgr,
			)
		},
		fx.ResultTags(`group:"transport"`),
	),
)

var NullTCPReuseConnMgr = fx.Provide(func() *tcpreuse.ConnMgr {
	return nil
})

var TCPReuseConnMgr = fx.Provide(func(upgrader transport.Upgrader) *tcpreuse.ConnMgr {
	return tcpreuse.NewConnMgr(tcpreuse.EnvReuseportVal, upgrader)
})

func TCPTransport(opts ...tcp.Option) fx.Option {
	return fx.Provide(
		fx.Annotate(
			func(p struct {
				fx.In
				Upgrader     transport.Upgrader
				Rcmgr        network.ResourceManager
				ReuseConnMgr *tcpreuse.ConnMgr
			}) (transport.Transport, error) {
				return tcp.NewTCPTransport(p.Upgrader, p.Rcmgr, p.ReuseConnMgr, opts...)
			},
			fx.As(new(transport.Transport)),
			fx.ResultTags(`group:"transport"`),
		),
	)
}

func Upgrader(opts ...tptu.Option) fx.Option {
	return fx.Provide(
		func(p struct {
			fx.In
			Security  []sec.SecureTransport
			Muxers    []tptu.StreamMuxer
			Rcmgr     network.ResourceManager
			ConnGater connmgr.ConnectionGater
		}) (transport.Upgrader, error) {
			// Not supporting PSK here since it doesn't work on all transports,
			// and there are better ways of authenticating peers.  If you need
			// PSK, provide the upgrader manually.
			var psk pnet.PSK = nil
			return tptu.New(p.Security, p.Muxers, psk, p.Rcmgr, p.ConnGater, opts...)
		},
	)
}

// Security is a helper to provide a list of security transports in a specific order.
func Security(ss ...func() (protocol.ID, fx.Option)) fx.Option {
	order := make(map[protocol.ID]int)
	var opts []fx.Option
	for i, s := range ss {
		id, opt := s()
		order[id] = i
		opts = append(opts, opt)
	}
	opts = append(opts,
		fx.Provide(fx.Annotate(func(unorderedSecurity []sec.SecureTransport) []sec.SecureTransport {
			slices.SortFunc(unorderedSecurity, func(a, b sec.SecureTransport) int {
				return order[a.ID()] - order[b.ID()]
			})
			return unorderedSecurity
		}, fx.ParamTags(`group:"unorderedSecurity"`))),
	)
	return fx.Options(opts...)
}

var Noise = func() (protocol.ID, fx.Option) {
	return noise.ID, fx.Provide(
		fx.Annotate(
			func(
				p struct {
					fx.In
					Privkey crypto.PrivKey
					Muxers  []tptu.StreamMuxer
				}) (sec.SecureTransport, error) {
				return noise.New(noise.ID, p.Privkey, p.Muxers)
			},
			fx.ResultTags(`group:"unorderedSecurity"`),
		),
	)
}

var TLS = func() (protocol.ID, fx.Option) {
	return tls.ID, fx.Provide(
		fx.Annotate(
			func(
				p struct {
					fx.In
					Privkey crypto.PrivKey
					Muxers  []tptu.StreamMuxer
				}) (sec.SecureTransport, error) {
				return tls.New(tls.ID, p.Privkey, p.Muxers)
			},
			fx.ResultTags(`group:"unorderedSecurity"`),
		),
	)
}

var Yamux = fx.Supply(
	[]tptu.StreamMuxer{{
		ID:    yamux.ID,
		Muxer: yamux.DefaultTransport,
	}},
)

type MetricsConfig struct {
	Disable            bool
	PrometheusRegister prometheus.Registerer
}

var DisableMetrics = fx.Decorate(func(params struct {
	fx.In
	cfg *MetricsConfig `optional:"true"`
}) *MetricsConfig {
	if params.cfg == nil {
		params.cfg = new(MetricsConfig)
	}
	params.cfg.Disable = true
	return params.cfg
})

var QUICReuseConnManager = fx.Provide(
	func(metricsCfg MetricsConfig, key quic.StatelessResetKey, tokenGenerator quic.TokenGeneratorKey, lifecycle fx.Lifecycle) (*quicreuse.ConnManager, error) {
		var opts []quicreuse.Option
		if !metricsCfg.Disable {
			opts = append(opts, quicreuse.EnableMetrics(metricsCfg.PrometheusRegister))
		}
		cm, err := quicreuse.NewConnManager(key, tokenGenerator, opts...)
		if err != nil {
			return nil, err
		}
		lifecycle.Append(fx.StopHook(cm.Close))
		return cm, nil
	},
	func(key crypto.PrivKey) (quic.StatelessResetKey, error) {
		var statelessResetKey quic.StatelessResetKey
		keyBytes, err := key.Raw()
		if err != nil {
			return statelessResetKey, err
		}

		const statelessResetKeyInfo = "libp2p quic stateless reset key"
		keyReader := hkdf.New(sha256.New, keyBytes, nil, []byte(statelessResetKeyInfo))
		if _, err := io.ReadFull(keyReader, statelessResetKey[:]); err != nil {
			return statelessResetKey, err
		}
		return statelessResetKey, nil
	},
	func(key crypto.PrivKey) (quic.TokenGeneratorKey, error) {
		var tokenKey quic.TokenGeneratorKey
		keyBytes, err := key.Raw()
		if err != nil {
			return tokenKey, err
		}

		const tokenGeneratorKeyInfo = "libp2p quic token generator key"
		keyReader := hkdf.New(sha256.New, keyBytes, nil, []byte(tokenGeneratorKeyInfo))
		if _, err := io.ReadFull(keyReader, tokenKey[:]); err != nil {
			return tokenKey, err
		}
		return tokenKey, nil
	},
)

func EventBus(opts ...eventbus.Option) fx.Option {
	return fx.Supply(fx.Annotate(eventbus.NewBus(opts...), fx.As(new(event.Bus))))
}

func InMemoryPeerstore() fx.Option {
	return fx.Provide(func() (peerstore.Peerstore, error) {
		return pstoremem.NewPeerstore()
	})
}

func ConnManager(low, hi int) fx.Option {
	return fx.Provide(func() (connmgr.ConnManager, error) {
		return connmgrImpl.NewConnManager(low, hi)
	})
}

var DefaultConnManager = ConnManager(160, 192)

var NullConnManager = fx.Provide(func() connmgr.ConnManager {
	return connmgr.NullConnMgr{}
})

var NullResourceManager = fx.Provide(func() network.ResourceManager {
	return &network.NullResourceManager{}
})

var NullConnectionGater = fx.Provide(func() connmgr.ConnectionGater {
	return nil
})

var MultistreamMuxer = fx.Provide(func() *mstream.MultistreamMuxer[protocol.ID] {
	return mstream.NewMultistreamMuxer[protocol.ID]()
})

// New type to specify that these are used for listening.
type ListenAddr multiaddr.Multiaddr

func ListenAddrs(addrs ...multiaddr.Multiaddr) fx.Option {
	return fx.Provide(
		fx.Annotate(
			func() []ListenAddr {
				out := make([]ListenAddr, len(addrs))
				for i, a := range addrs {
					out[i] = ListenAddr(a)
				}
				return out
			},
			fx.ResultTags(`group:"listenAddr,flatten"`),
		))
}
