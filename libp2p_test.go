package libp2p

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	"github.com/libp2p/go-libp2p-core/connmgr"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/transport"

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
	re := regexp.MustCompile("/(ip)[4|6]/((0.0.0.0)|(::))/tcp/")
	re2 := regexp.MustCompile("/p2p-circuit")

	// Test 1: Setting the correct listen addresses if userDefined.Transport == nil && userDefined.ListenAddrs == nil
	h, err := New()
	require.NoError(t, err)
	for _, addr := range h.Network().ListenAddresses() {
		if re.FindStringSubmatchIndex(addr.String()) == nil &&
			re2.FindStringSubmatchIndex(addr.String()) == nil {
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
	if re2.FindStringSubmatchIndex(h.Network().ListenAddresses()[0].String()) == nil {
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
