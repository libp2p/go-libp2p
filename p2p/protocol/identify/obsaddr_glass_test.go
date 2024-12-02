package identify

// This test lives in the identify package, not the identify_test package, so it
// can access internal types.

import (
	"fmt"
	"sync/atomic"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

type mockConn struct {
	local, remote ma.Multiaddr
	isClosed      atomic.Bool
}

// LocalMultiaddr implements connMultiaddrProvider
func (c *mockConn) LocalMultiaddr() ma.Multiaddr {
	return c.local
}

// RemoteMultiaddr implements connMultiaddrProvider
func (c *mockConn) RemoteMultiaddr() ma.Multiaddr {
	return c.remote
}

func (c *mockConn) Close() {
	c.isClosed.Store(true)
}

func (c *mockConn) IsClosed() bool {
	return c.isClosed.Load()
}

func TestShouldRecordObservationWithWebTransport(t *testing.T) {
	listenAddr := ma.StringCast("/ip4/0.0.0.0/udp/0/quic-v1/webtransport/certhash/uEgNmb28")
	ifaceAddr := ma.StringCast("/ip4/10.0.0.2/udp/9999/quic-v1/webtransport/certhash/uEgNmb28")
	listenAddrs := func() []ma.Multiaddr { return []ma.Multiaddr{listenAddr} }
	ifaceListenAddrs := func() ([]ma.Multiaddr, error) { return []ma.Multiaddr{ifaceAddr}, nil }
	addrs := func() []ma.Multiaddr { return []ma.Multiaddr{listenAddr} }

	c := &mockConn{
		local:  listenAddr,
		remote: ma.StringCast("/ip4/1.2.3.6/udp/1236/quic-v1/webtransport"),
	}
	observedAddr := ma.StringCast("/ip4/1.2.3.4/udp/1231/quic-v1/webtransport")
	o, err := NewObservedAddrManager(listenAddrs, addrs, ifaceListenAddrs, normalize)
	require.NoError(t, err)
	shouldRecord, _, _ := o.shouldRecordObservation(c, observedAddr)
	require.True(t, shouldRecord)
}

func TestShouldNotRecordObservationWithRelayedAddr(t *testing.T) {
	listenAddr := ma.StringCast("/ip4/1.2.3.4/udp/8888/quic-v1/p2p-circuit")
	ifaceAddr := ma.StringCast("/ip4/10.0.0.2/udp/9999/quic-v1")
	listenAddrs := func() []ma.Multiaddr { return []ma.Multiaddr{listenAddr} }
	ifaceListenAddrs := func() ([]ma.Multiaddr, error) { return []ma.Multiaddr{ifaceAddr}, nil }
	addrs := func() []ma.Multiaddr { return []ma.Multiaddr{listenAddr} }

	c := &mockConn{
		local:  listenAddr,
		remote: ma.StringCast("/ip4/1.2.3.6/udp/1236/quic-v1/p2p-circuit"),
	}
	observedAddr := ma.StringCast("/ip4/1.2.3.4/udp/1231/quic-v1/p2p-circuit")
	o, err := NewObservedAddrManager(listenAddrs, addrs, ifaceListenAddrs, normalize)
	require.NoError(t, err)
	shouldRecord, _, _ := o.shouldRecordObservation(c, observedAddr)
	require.False(t, shouldRecord)
}

func TestShouldRecordObservationWithNAT64Addr(t *testing.T) {
	listenAddr1 := ma.StringCast("/ip4/0.0.0.0/tcp/1234")
	ifaceAddr1 := ma.StringCast("/ip4/10.0.0.2/tcp/4321")
	listenAddr2 := ma.StringCast("/ip6/::/tcp/1234")
	ifaceAddr2 := ma.StringCast("/ip6/1::1/tcp/4321")

	var (
		listenAddrs      = func() []ma.Multiaddr { return []ma.Multiaddr{listenAddr1, listenAddr2} }
		ifaceListenAddrs = func() ([]ma.Multiaddr, error) { return []ma.Multiaddr{ifaceAddr1, ifaceAddr2}, nil }
		addrs            = func() []ma.Multiaddr { return []ma.Multiaddr{listenAddr1, listenAddr2} }
	)
	c := &mockConn{
		local:  listenAddr1,
		remote: ma.StringCast("/ip4/1.2.3.6/tcp/4321"),
	}

	cases := []struct {
		addr          ma.Multiaddr
		want          bool
		failureReason string
	}{
		{
			addr:          ma.StringCast("/ip4/1.2.3.4/tcp/1234"),
			want:          true,
			failureReason: "IPv4 should be observed",
		},
		{
			addr:          ma.StringCast("/ip6/1::4/tcp/1234"),
			want:          true,
			failureReason: "public IPv6 address should be observed",
		},
		{
			addr:          ma.StringCast("/ip6/64:ff9b::192.0.1.2/tcp/1234"),
			want:          false,
			failureReason: "NAT64 IPv6 address shouldn't be observed",
		},
	}

	o, err := NewObservedAddrManager(listenAddrs, addrs, ifaceListenAddrs, normalize)
	require.NoError(t, err)
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {

			if shouldRecord, _, _ := o.shouldRecordObservation(c, tc.addr); shouldRecord != tc.want {
				t.Fatalf("%s %s", tc.addr, tc.failureReason)
			}
		})
	}
}

func TestThinWaistForm(t *testing.T) {
	tc := []struct {
		input string
		tw    string
		rest  string
		err   bool
	}{{
		input: "/ip4/1.2.3.4/tcp/1",
		tw:    "/ip4/1.2.3.4/tcp/1",
		rest:  "",
	}, {
		input: "/ip4/1.2.3.4/tcp/1/ws",
		tw:    "/ip4/1.2.3.4/tcp/1",
		rest:  "/ws",
	}, {
		input: "/ip4/127.0.0.1/udp/1/quic-v1",
		tw:    "/ip4/127.0.0.1/udp/1",
		rest:  "/quic-v1",
	}, {
		input: "/ip4/1.2.3.4/udp/1/quic-v1/webtransport",
		tw:    "/ip4/1.2.3.4/udp/1",
		rest:  "/quic-v1/webtransport",
	}, {
		input: "/ip4/1.2.3.4/",
		err:   true,
	}, {
		input: "/tcp/1",
		err:   true,
	}, {
		input: "/ip6/::1/tcp/1",
		tw:    "/ip6/::1/tcp/1",
		rest:  "",
	}}
	for i, tt := range tc {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			inputAddr := ma.StringCast(tt.input)
			tw, err := thinWaistForm(inputAddr)
			if tt.err {
				require.Equal(t, tw, thinWaist{})
				require.Error(t, err)
				return
			}
			wantTW := ma.StringCast(tt.tw)
			var restTW ma.Multiaddr
			if tt.rest != "" {
				restTW = ma.StringCast(tt.rest)
			}
			require.Equal(t, tw.Addr, inputAddr, "%s %s", tw.Addr, inputAddr)
			require.Equal(t, wantTW, tw.TW, "%s %s", tw.TW, wantTW)
			require.Equal(t, restTW, tw.Rest, "%s %s", restTW, tw.Rest)
		})
	}

}
