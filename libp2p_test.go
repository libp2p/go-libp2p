package libp2p

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	tls "github.com/libp2p/go-libp2p/p2p/security/tls"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	webtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestNewHost(t *testing.T) {
	h, err := makeRandomHost(t, 9000)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
}

func TestBadTransportConstructor(t *testing.T) {
	h, err := New(Transport(func() {}))
	if err == nil {
		h.Close()
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "libp2p_test.go") {
		t.Error("expected error to contain debugging info")
	}
}

func TestTransportConstructor(t *testing.T) {
	ctor := func(
		h host.Host,
		_ connmgr.ConnectionGater,
		upgrader transport.Upgrader,
	) transport.Transport {
		tpt, err := tcp.NewTCPTransport(upgrader, nil)
		require.NoError(t, err)
		return tpt
	}
	h, err := New(Transport(ctor))
	require.NoError(t, err)
	h.Close()
}

func TestNoListenAddrs(t *testing.T) {
	h, err := New(NoListenAddrs)
	require.NoError(t, err)
	defer h.Close()
	if len(h.Addrs()) != 0 {
		t.Fatal("expected no addresses")
	}
}

func TestNoTransports(t *testing.T) {
	ctx := context.Background()
	a, err := New(NoTransports)
	require.NoError(t, err)
	defer a.Close()

	b, err := New(ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	require.NoError(t, err)
	defer b.Close()

	err = a.Connect(ctx, peer.AddrInfo{
		ID:    b.ID(),
		Addrs: b.Addrs(),
	})
	if err == nil {
		t.Error("dial should have failed as no transports have been configured")
	}
}

func TestInsecure(t *testing.T) {
	h, err := New(NoSecurity)
	require.NoError(t, err)
	h.Close()
}

func TestAutoNATService(t *testing.T) {
	h, err := New(EnableNATService())
	require.NoError(t, err)
	h.Close()
}

func TestDefaultListenAddrs(t *testing.T) {
	reTCP := regexp.MustCompile("/(ip)[4|6]/((0.0.0.0)|(::))/tcp/")
	reQUIC := regexp.MustCompile("/(ip)[4|6]/((0.0.0.0)|(::))/udp/([0-9]*)/quic")
	reCircuit := regexp.MustCompile("/p2p-circuit")

	// Test 1: Setting the correct listen addresses if userDefined.Transport == nil && userDefined.ListenAddrs == nil
	h, err := New()
	require.NoError(t, err)
	for _, addr := range h.Network().ListenAddresses() {
		if reTCP.FindStringSubmatchIndex(addr.String()) == nil &&
			reQUIC.FindStringSubmatchIndex(addr.String()) == nil &&
			reCircuit.FindStringSubmatchIndex(addr.String()) == nil {
			t.Error("expected ip4 or ip6 or relay interface")
		}
	}

	h.Close()

	// Test 2: Listen addr only include relay if user defined transport is passed.
	h, err = New(Transport(tcp.NewTCPTransport))
	require.NoError(t, err)

	if len(h.Network().ListenAddresses()) != 1 {
		t.Error("expected one listen addr with user defined transport")
	}
	if reCircuit.FindStringSubmatchIndex(h.Network().ListenAddresses()[0].String()) == nil {
		t.Error("expected relay address")
	}
	h.Close()
}

func makeRandomHost(t *testing.T, port int) (host.Host, error) {
	priv, _, err := crypto.GenerateKeyPair(crypto.RSA, 2048)
	require.NoError(t, err)

	return New([]Option{
		ListenAddrStrings(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port)),
		Identity(priv),
		DefaultTransports,
		DefaultMuxers,
		DefaultSecurity,
		NATPortMap(),
	}...)
}

func TestChainOptions(t *testing.T) {
	var cfg Config
	var optsRun []int
	optcount := 0
	newOpt := func() Option {
		index := optcount
		optcount++
		return func(c *Config) error {
			optsRun = append(optsRun, index)
			return nil
		}
	}

	if err := cfg.Apply(newOpt(), nil, ChainOptions(newOpt(), newOpt(), ChainOptions(), ChainOptions(nil, newOpt()))); err != nil {
		t.Fatal(err)
	}

	// Make sure we ran all options.
	if optcount != 4 {
		t.Errorf("expected to have handled %d options, handled %d", optcount, len(optsRun))
	}

	// Make sure we ran the options in-order.
	for i, x := range optsRun {
		if i != x {
			t.Errorf("expected opt %d, got opt %d", i, x)
		}
	}
}

func TestTransportConstructorTCP(t *testing.T) {
	h, err := New(
		Transport(tcp.NewTCPTransport, tcp.DisableReuseport()),
		DisableRelay(),
	)
	require.NoError(t, err)
	defer h.Close()
	require.NoError(t, h.Network().Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0")))
	err = h.Network().Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic"))
	require.Error(t, err)
	require.Contains(t, err.Error(), swarm.ErrNoTransport.Error())
}

func TestTransportConstructorQUIC(t *testing.T) {
	h, err := New(
		Transport(quic.NewTransport),
		DisableRelay(),
	)
	require.NoError(t, err)
	defer h.Close()
	require.NoError(t, h.Network().Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic")))
	err = h.Network().Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0"))
	require.Error(t, err)
	require.Contains(t, err.Error(), swarm.ErrNoTransport.Error())
}

type mockTransport struct{}

func (m mockTransport) Dial(context.Context, ma.Multiaddr, peer.ID) (transport.CapableConn, error) {
	panic("implement me")
}

func (m mockTransport) CanDial(ma.Multiaddr) bool                       { panic("implement me") }
func (m mockTransport) Listen(ma.Multiaddr) (transport.Listener, error) { panic("implement me") }
func (m mockTransport) Protocols() []int                                { return []int{1337} }
func (m mockTransport) Proxy() bool                                     { panic("implement me") }

var _ transport.Transport = &mockTransport{}

func TestTransportConstructorWithoutOpts(t *testing.T) {
	t.Run("successful", func(t *testing.T) {
		var called bool
		constructor := func() transport.Transport {
			called = true
			return &mockTransport{}
		}

		h, err := New(
			Transport(constructor),
			DisableRelay(),
		)
		require.NoError(t, err)
		require.True(t, called, "expected constructor to be called")
		defer h.Close()
	})

	t.Run("with options", func(t *testing.T) {
		var called bool
		constructor := func() transport.Transport {
			called = true
			return &mockTransport{}
		}

		_, err := New(
			Transport(constructor, tcp.DisableReuseport()),
			DisableRelay(),
		)
		require.EqualError(t, err, "transport constructor doesn't take any options")
		require.False(t, called, "didn't expected constructor to be called")
	})
}

func TestTransportConstructorWithWrongOpts(t *testing.T) {
	_, err := New(
		Transport(quic.NewTransport, tcp.DisableReuseport()),
		DisableRelay(),
	)
	require.EqualError(t, err, "transport constructor doesn't take any options")
}

func TestSecurityConstructor(t *testing.T) {
	h, err := New(
		Transport(tcp.NewTCPTransport),
		Security("/noisy", noise.New),
		Security("/tls", tls.New),
		DefaultListenAddrs,
		DisableRelay(),
	)
	require.NoError(t, err)
	defer h.Close()

	h1, err := New(
		NoListenAddrs,
		Transport(tcp.NewTCPTransport),
		Security("/noise", noise.New), // different name
		DisableRelay(),
	)
	require.NoError(t, err)
	defer h1.Close()

	h2, err := New(
		NoListenAddrs,
		Transport(tcp.NewTCPTransport),
		Security("/noisy", noise.New),
		DisableRelay(),
	)
	require.NoError(t, err)
	defer h2.Close()

	ai := peer.AddrInfo{
		ID:    h.ID(),
		Addrs: h.Addrs(),
	}
	err = h1.Connect(context.Background(), ai)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to negotiate security protocol")
	require.NoError(t, h2.Connect(context.Background(), ai))
}

func TestTransportConstructorWebTransport(t *testing.T) {
	h, err := New(
		Transport(webtransport.New),
		DisableRelay(),
	)
	require.NoError(t, err)
	defer h.Close()
	require.NoError(t, h.Network().Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport")))
	err = h.Network().Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/"))
	require.Error(t, err)
	require.Contains(t, err.Error(), swarm.ErrNoTransport.Error())
}

func TestTransportCustomAddressWebTransport(t *testing.T) {
	customAddr, err := ma.NewMultiaddr("/ip4/127.0.0.1/udp/0/quic-v1/webtransport")
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(
		Transport(webtransport.New),
		ListenAddrs(customAddr),
		DisableRelay(),
		AddrsFactory(func(multiaddrs []ma.Multiaddr) []ma.Multiaddr {
			return []ma.Multiaddr{customAddr}
		}),
	)
	require.NoError(t, err)
	defer h.Close()
	require.NoError(t, h.Network().Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport")))
	addrs := h.Addrs()
	require.Len(t, addrs, 1)
	require.NotEqual(t, addrs[0], customAddr)
	restOfAddr, lastComp := ma.SplitLast(addrs[0])
	restOfAddr, secondToLastComp := ma.SplitLast(restOfAddr)
	require.Equal(t, lastComp.Protocol().Code, ma.P_CERTHASH)
	require.Equal(t, secondToLastComp.Protocol().Code, ma.P_CERTHASH)
	require.True(t, restOfAddr.Equal(customAddr))
}

// TestTransportCustomAddressWebTransportDoesNotStall tests that if the user
// manually returns a webtransport address from AddrsFactory, but we aren't
// listening on a webtranport address, we don't stall.
func TestTransportCustomAddressWebTransportDoesNotStall(t *testing.T) {
	customAddr, err := ma.NewMultiaddr("/ip4/127.0.0.1/udp/0/quic-v1/webtransport")
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(
		Transport(webtransport.New),
		// Purposely not listening on the custom address so that we make sure the node doesn't stall if it fails to add a certhash to the multiaddr
		// ListenAddrs(customAddr),
		DisableRelay(),
		AddrsFactory(func(multiaddrs []ma.Multiaddr) []ma.Multiaddr {
			return []ma.Multiaddr{customAddr}
		}),
	)
	require.NoError(t, err)
	defer h.Close()
	addrs := h.Addrs()
	require.Len(t, addrs, 1)
	_, lastComp := ma.SplitLast(addrs[0])
	require.NotEqual(t, lastComp.Protocol().Code, ma.P_CERTHASH)
	// We did not add the certhash to the multiaddr
	require.Equal(t, addrs[0], customAddr)
}
