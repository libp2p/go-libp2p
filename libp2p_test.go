package libp2p

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/libp2p/go-libp2p-core/connmgr"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/transport"
	noise "github.com/libp2p/go-libp2p-noise"
	"github.com/libp2p/go-tcp-transport"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"

	tptu "github.com/libp2p/go-libp2p-transport-upgrader"
)

func TestNewHost(t *testing.T) {
	h, err := makeRandomHost(t, 9000)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
}

func TestBadTransportConstructor(t *testing.T) {
	ctx := context.Background()
	h, err := New(ctx, Transport(func() {}))
	if err == nil {
		h.Close()
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "libp2p_test.go") {
		t.Error("expected error to contain debugging info")
	}
}

func TestTransportConstructor(t *testing.T) {
	ctx := context.Background()
	ctor := func(
		h host.Host,
		_ connmgr.ConnectionGater,
		upgrader *tptu.Upgrader,
	) transport.Transport {
		return tcp.NewTCPTransport(upgrader)
	}
	h, err := New(ctx, Transport(ctor))
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
}

func TestNoListenAddrs(t *testing.T) {
	ctx := context.Background()
	h, err := New(ctx, NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if len(h.Addrs()) != 0 {
		t.Fatal("expected no addresses")
	}
}

func TestNoTransports(t *testing.T) {
	ctx := context.Background()
	a, err := New(ctx, NoTransports)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	b, err := New(ctx, ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
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
	ctx := context.Background()
	h, err := New(ctx, NoSecurity)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
}

func TestAutoNATService(t *testing.T) {
	ctx := context.Background()
	h, err := New(ctx, EnableNATService())
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
}

func TestDefaultListenAddrs(t *testing.T) {
	ctx := context.Background()

	re := regexp.MustCompile("/(ip)[4|6]/((0.0.0.0)|(::))/tcp/")
	re2 := regexp.MustCompile("/p2p-circuit")

	// Test 1: Setting the correct listen addresses if userDefined.Transport == nil && userDefined.ListenAddrs == nil
	h, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range h.Network().ListenAddresses() {
		if re.FindStringSubmatchIndex(addr.String()) == nil &&
			re2.FindStringSubmatchIndex(addr.String()) == nil {
			t.Error("expected ip4 or ip6 or relay interface")
		}
	}

	h.Close()

	// Test 2: Listen addr only include relay if user defined transport is passed.
	h, err = New(
		ctx,
		Transport(tcp.NewTCPTransport),
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(h.Network().ListenAddresses()) != 1 {
		t.Error("expected one listen addr with user defined transport")
	}
	if re2.FindStringSubmatchIndex(h.Network().ListenAddresses()[0].String()) == nil {
		t.Error("expected relay address")
	}
	h.Close()
}

func makeRandomHost(t *testing.T, port int) (host.Host, error) {
	ctx := context.Background()
	priv, _, err := crypto.GenerateKeyPair(crypto.RSA, 2048)
	if err != nil {
		t.Fatal(err)
	}

	opts := []Option{
		ListenAddrStrings(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port)),
		Identity(priv),
		DefaultTransports,
		DefaultMuxers,
		DefaultSecurity,
		NATPortMap(),
	}

	return New(ctx, opts...)
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

func TestTcpSimultaneousConnect(t *testing.T) {
	ctx := context.Background()

	// Host1
	h1, err := New(ctx, Transport(tcp.NewTCPTransport), Security(noise.ID, noise.New),
		ListenAddrs(ma.StringCast("/ip4/0.0.0.0/tcp/0")))
	require.NoError(t, err)
	defer h1.Close()

	// Host2
	h2, err := New(ctx, Transport(tcp.NewTCPTransport), Security(noise.ID, noise.New),
		ListenAddrs(ma.StringCast("/ip4/0.0.0.0/tcp/0")))
	require.NoError(t, err)
	defer h2.Close()

	h1Info := peer.AddrInfo{
		ID:    h1.ID(),
		Addrs: h1.Addrs(),
	}

	h2Info := peer.AddrInfo{
		ID:    h2.ID(),
		Addrs: h2.Addrs(),
	}

	wg := new(sync.WaitGroup)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			require.NoError(t, h1.Connect(context.Background(), h2Info))
			require.NoError(t, h1.Network().ClosePeer(h2.ID()))
		}
	}()

	// use another peer to constantly connect/disconnect with first peer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			require.NoError(t, h2.Connect(context.Background(), h1Info))
			require.NoError(t, h2.Network().ClosePeer(h1.ID()))
		}
	}()

	wg.Wait()
}
