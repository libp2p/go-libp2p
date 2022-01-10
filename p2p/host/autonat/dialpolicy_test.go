package autonat

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/libp2p/go-libp2p/p2p/host/blank"

	"github.com/stretchr/testify/require"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/transport"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	"github.com/multiformats/go-multiaddr"
)

func makeMA(a string) multiaddr.Multiaddr {
	addr, err := multiaddr.NewMultiaddr(a)
	if err != nil {
		panic(err)
	}
	return addr
}

type mockT struct {
	ctx  context.Context
	addr multiaddr.Multiaddr
}

func (m *mockT) Dial(ctx context.Context, a multiaddr.Multiaddr, p peer.ID) (transport.CapableConn, error) {
	return nil, nil
}
func (m *mockT) CanDial(_ multiaddr.Multiaddr) bool { return true }
func (m *mockT) Listen(a multiaddr.Multiaddr) (transport.Listener, error) {
	return &mockL{m.ctx, m.addr}, nil
}
func (m *mockT) Protocols() []int { return []int{multiaddr.P_IP4} }
func (m *mockT) Proxy() bool      { return false }
func (m *mockT) String() string   { return "mock-tcp-ipv4" }

type mockL struct {
	ctx  context.Context
	addr multiaddr.Multiaddr
}

func (l *mockL) Accept() (transport.CapableConn, error) {
	<-l.ctx.Done()
	return nil, errors.New("expected in mocked test")
}
func (l *mockL) Close() error                   { return nil }
func (l *mockL) Addr() net.Addr                 { return nil }
func (l *mockL) Multiaddr() multiaddr.Multiaddr { return l.addr }

func TestSkipDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := swarmt.GenSwarm(t)
	h, err := blank.NewHost(s)
	require.NoError(t, err)
	d := dialPolicy{host: h}
	if d.skipDial(makeMA("/ip4/8.8.8.8")) != false {
		t.Fatal("failed dialing a valid public addr")
	}

	if d.skipDial(makeMA("/ip6/2607:f8b0:400a::1")) != false {
		t.Fatal("failed dialing a valid public addr")
	}

	if d.skipDial(makeMA("/ip4/192.168.0.1")) != true {
		t.Fatal("didn't skip dialing an internal addr")
	}

	s.AddTransport(&mockT{ctx, makeMA("/ip4/8.8.8.8")})
	require.NoError(t, s.AddListenAddr(makeMA("/ip4/8.8.8.8")))
	if d.skipDial(makeMA("/ip4/8.8.8.8")) != true {
		t.Fatal("failed dialing a valid host address")
	}
}

func TestSkipPeer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := swarmt.GenSwarm(t)
	h, err := blank.NewHost(s)
	require.NoError(t, err)
	d := dialPolicy{host: h}
	if d.skipPeer([]multiaddr.Multiaddr{makeMA("/ip4/8.8.8.8")}) != false {
		t.Fatal("failed dialing a valid public addr")
	}
	if d.skipPeer([]multiaddr.Multiaddr{makeMA("/ip4/8.8.8.8"), makeMA("/ip4/192.168.0.1")}) != false {
		t.Fatal("failed dialing a valid public addr")
	}
	if d.skipPeer([]multiaddr.Multiaddr{makeMA("/ip4/192.168.0.1")}) != true {
		t.Fatal("succeeded with no public addr")
	}

	s.AddTransport(&mockT{ctx, makeMA("/ip4/8.8.8.8")})
	require.NoError(t, s.AddListenAddr(makeMA("/ip4/8.8.8.8")))

	if d.skipPeer([]multiaddr.Multiaddr{makeMA("/ip4/8.8.8.8"), makeMA("/ip4/192.168.0.1")}) != true {
		t.Fatal("succeeded dialing host address")
	}
	if d.skipPeer([]multiaddr.Multiaddr{makeMA("/ip4/8.8.8.8"), makeMA("/ip4/9.9.9.9")}) != true {
		t.Fatal("succeeded dialing host address when other public")
	}
	if d.skipPeer([]multiaddr.Multiaddr{makeMA("/ip4/9.9.9.9")}) != false {
		t.Fatal("succeeded dialing host address when other public")
	}
}

func TestSkipLocalPeer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := swarmt.GenSwarm(t)
	h, err := blank.NewHost(s)
	require.NoError(t, err)
	d := dialPolicy{host: h}
	s.AddTransport(&mockT{ctx, makeMA("/ip4/192.168.0.1")})
	require.NoError(t, s.AddListenAddr(makeMA("/ip4/192.168.0.1")))

	if d.skipPeer([]multiaddr.Multiaddr{makeMA("/ip4/8.8.8.8")}) != false {
		t.Fatal("failed dialing a valid public addr")
	}
	if d.skipPeer([]multiaddr.Multiaddr{makeMA("/ip4/8.8.8.8"), makeMA("/ip4/192.168.0.1")}) != false {
		t.Fatal("failed dialing a valid public addr")
	}
	if d.skipPeer([]multiaddr.Multiaddr{makeMA("/ip4/192.168.0.1")}) != true {
		t.Fatal("succeeded with no public addr")
	}
}
