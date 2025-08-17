package simconnlibp2p

import (
	"crypto/rand"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/config"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/marcopolo/simnet"
	"github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
)

func MustNewHost(t *testing.T, opts ...libp2p.Option) host.Host {
	t.Helper()
	h, err := libp2p.New(opts...)
	require.NoError(t, err)
	return h
}

type MockSourceIPSelector struct {
	ip atomic.Pointer[net.IP]
}

func (m *MockSourceIPSelector) PreferredSourceIPForDestination(_ *net.UDPAddr) (net.IP, error) {
	return *m.ip.Load(), nil
}

const OneMbps = 1_000_000

func QUICSimnet(simnet *simnet.Simnet, linkSettings simnet.NodeBiDiLinkSettings, quicReuseOpts ...quicreuse.Option) libp2p.Option {
	m := &MockSourceIPSelector{}
	quicReuseOpts = append(quicReuseOpts,
		quicreuse.OverrideSourceIPSelector(func() (quicreuse.SourceIPSelector, error) {
			return m, nil
		}),
		quicreuse.OverrideListenUDP(func(_ string, address *net.UDPAddr) (net.PacketConn, error) {
			m.ip.Store(&address.IP)
			c := simnet.NewEndpoint(address, linkSettings)
			return c, nil
		}))
	return libp2p.QUICReuse(
		func(l fx.Lifecycle, statelessResetKey quic.StatelessResetKey, tokenKey quic.TokenGeneratorKey, opts ...quicreuse.Option) (*quicreuse.ConnManager, error) {
			cm, err := quicreuse.NewConnManager(statelessResetKey, tokenKey, opts...)
			if err != nil {
				return nil, err
			}
			l.Append(fx.StopHook(func() error {
				// When we pass in our own conn manager, we need to close it manually (??)
				// TODO: this seems like a bug
				return cm.Close()
			}))
			return cm, nil
		}, quicReuseOpts...)
}

type wrappedHost struct {
	blankhost.BlankHost
	ps        peerstore.Peerstore
	quicCM    *quicreuse.ConnManager
	idService identify.IDService
	connMgr   *connmgr.BasicConnMgr
}

func (h *wrappedHost) Close() error {
	h.BlankHost.Close()
	h.ps.Close()
	h.quicCM.Close()
	h.idService.Close()
	h.connMgr.Close()
	return nil
}

type BlankHostOpts struct {
	ConnMgr         *connmgr.BasicConnMgr
	listenMultiaddr multiaddr.Multiaddr
	simnet          *simnet.Simnet
	linkSettings    simnet.NodeBiDiLinkSettings
	quicReuseOpts   []quicreuse.Option
}

func newBlankHost(opts BlankHostOpts) (*wrappedHost, error) {
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	ps, err := pstoremem.NewPeerstore()
	if err != nil {
		return nil, err
	}
	ps.AddPrivKey(id, priv)

	eb := eventbus.NewBus()

	swarm, err := swarm.NewSwarm(id, ps, eb)
	if err != nil {
		return nil, err
	}

	statelessResetKey, err := config.PrivKeyToStatelessResetKey(priv)
	if err != nil {
		return nil, err
	}
	tokenGeneratorKey, err := config.PrivKeyToTokenGeneratorKey(priv)
	if err != nil {
		return nil, err
	}
	m := &MockSourceIPSelector{}
	quicReuseOpts := append(opts.quicReuseOpts,
		quicreuse.OverrideSourceIPSelector(func() (quicreuse.SourceIPSelector, error) {
			return m, nil
		}),
		quicreuse.OverrideListenUDP(func(_ string, address *net.UDPAddr) (net.PacketConn, error) {
			m.ip.Store(&address.IP)
			c := opts.simnet.NewEndpoint(address, opts.linkSettings)
			return c, nil
		}),
	)

	quicCM, err := quicreuse.NewConnManager(statelessResetKey, tokenGeneratorKey, quicReuseOpts...)
	if err != nil {
		return nil, err
	}
	quicTr, err := libp2pquic.NewTransport(priv, quicCM, nil, nil, &network.NullResourceManager{})
	if err != nil {
		return nil, err
	}

	err = swarm.AddTransport(quicTr)
	if err != nil {
		return nil, err
	}
	err = swarm.Listen(opts.listenMultiaddr)
	if err != nil {
		return nil, err
	}

	var cm *connmgr.BasicConnMgr
	if opts.ConnMgr == nil {
		cm, err = connmgr.NewConnManager(100, 200, connmgr.WithGracePeriod(time.Second*10))
		if err != nil {
			return nil, err
		}
	} else {
		cm = opts.ConnMgr
	}

	host := blankhost.NewBlankHost(swarm, blankhost.WithEventBus(eb), blankhost.WithConnectionManager(cm))

	idService, err := identify.NewIDService(host)
	if err != nil {
		return nil, err
	}
	idService.Start()

	return &wrappedHost{
		BlankHost: *host,
		ps:        ps,
		quicCM:    quicCM,
		idService: idService,
		connMgr:   cm,
	}, nil
}

type NodeLinkSettingsAndCount struct {
	LinkSettings simnet.NodeBiDiLinkSettings
	Count        int
}

type HostAndIdx struct {
	Host host.Host
	Idx  int
}

type SimpleLibp2pNetworkMeta struct {
	Nodes      []host.Host
	AddrToNode map[string]HostAndIdx
}

type NetworkSettings struct {
	UseBlankHost            bool
	QUICReuseOptsForHostIdx func(idx int) []quicreuse.Option
	BlankHostOptsForHostIdx func(idx int) BlankHostOpts
}

func SimpleLibp2pNetwork(linkSettings []NodeLinkSettingsAndCount, networkSettings NetworkSettings) (*simnet.Simnet, *SimpleLibp2pNetworkMeta, error) {
	nw := &simnet.Simnet{}
	meta := &SimpleLibp2pNetworkMeta{
		AddrToNode: make(map[string]HostAndIdx),
	}

	for _, l := range linkSettings {
		for i := 0; i < l.Count; i++ {
			idx := len(meta.Nodes)
			ip := simnet.IntToPublicIPv4(idx)
			addr := fmt.Sprintf("/ip4/%s/udp/8000/quic-v1", ip)
			var h host.Host
			var err error
			var quicReuseOpts []quicreuse.Option
			if networkSettings.QUICReuseOptsForHostIdx != nil {
				quicReuseOpts = networkSettings.QUICReuseOptsForHostIdx(idx)
			}
			if networkSettings.UseBlankHost {
				var opts BlankHostOpts
				if networkSettings.BlankHostOptsForHostIdx != nil {
					opts = networkSettings.BlankHostOptsForHostIdx(idx)
				}

				h, err = newBlankHost(BlankHostOpts{
					listenMultiaddr: multiaddr.StringCast(addr),
					simnet:          nw,
					linkSettings:    l.LinkSettings,
					quicReuseOpts:   quicReuseOpts,
					ConnMgr:         opts.ConnMgr,
				})
			} else {
				h, err = libp2p.New(
					libp2p.ListenAddrStrings(addr),
					QUICSimnet(nw, l.LinkSettings, quicReuseOpts...),
					// TODO: Currently using identify address discovery stalls
					// synctest
					libp2p.DisableIdentifyAddressDiscovery(),
					libp2p.ResourceManager(&network.NullResourceManager{}),
				)
			}
			if err != nil {
				return nil, nil, err
			}
			meta.Nodes = append(meta.Nodes, h)
			meta.AddrToNode[addr] = HostAndIdx{Host: h, Idx: idx}
		}
	}

	return nw, meta, nil
}
