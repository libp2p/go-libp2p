//go:build nocover

package swarm

import (
	"context"
	"crypto/rand"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	ma "github.com/multiformats/go-multiaddr"

	mrand "math/rand"

	"github.com/stretchr/testify/require"
)

func BenchmarkMetricsConnOpen(b *testing.B) {
	b.ReportAllocs()
	quicConnState := network.ConnectionState{Transport: "quic"}
	tcpConnState := network.ConnectionState{
		StreamMultiplexer: "yamux",
		Security:          "tls",
		Transport:         "tcp",
	}
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(b, err)
	quicAddr := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")
	tcpAddr := ma.StringCast("/ip4/1.2.3.4/tcp/1/")
	tr := NewMetricsTracer()
	for i := 0; i < b.N; i++ {
		switch i % 2 {
		case 0:
			tr.OpenedConnection(network.DirInbound, pub, quicConnState, quicAddr)
		case 1:
			tr.OpenedConnection(network.DirInbound, pub, tcpConnState, tcpAddr)
		}
	}
}

func randItem[T any](items []T) T {
	return items[mrand.Intn(len(items))]
}

func TestMetricsNoAllocNoCover(t *testing.T) {
	mt := NewMetricsTracer()

	connections := []network.ConnectionState{
		{StreamMultiplexer: "yamux", Security: "tls", Transport: "tcp", UsedEarlyMuxerNegotiation: true},
		{StreamMultiplexer: "yamux", Security: "noise", Transport: "tcp", UsedEarlyMuxerNegotiation: false},
		{StreamMultiplexer: "", Security: "", Transport: "quic"},
		{StreamMultiplexer: "another-yamux", Security: "noise", Transport: "tcp"},
	}

	directions := []network.Direction{network.DirInbound, network.DirOutbound}

	_, pub1, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	_, pub2, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	_, pub3, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	keys := []crypto.PubKey{pub1, pub2, pub3}

	errors := []error{
		context.Canceled,
		context.DeadlineExceeded,
		&net.OpError{Err: syscall.ETIMEDOUT},
	}

	addrs := []ma.Multiaddr{
		ma.StringCast("/ip4/1.2.3.4/tcp/1"),
		ma.StringCast("/ip4/1.2.3.4/tcp/2"),
		ma.StringCast("/ip4/1.2.3.4/udp/2345"),
	}

	bhfNames := []string{"udp", "ipv6", "tcp", "icmp"}
	bhfState := []blackHoleState{blackHoleStateAllowed, blackHoleStateBlocked}

	tests := map[string]func(){
		"OpenedConnection": func() {
			mt.OpenedConnection(randItem(directions), randItem(keys), randItem(connections), randItem(addrs))
		},
		"ClosedConnection": func() {
			mt.ClosedConnection(randItem(directions), time.Duration(mrand.Intn(100))*time.Second, randItem(connections), randItem(addrs))
		},
		"CompletedHandshake": func() {
			mt.CompletedHandshake(time.Duration(mrand.Intn(100))*time.Second, randItem(connections), randItem(addrs))
		},
		"FailedDialing":    func() { mt.FailedDialing(randItem(addrs), randItem(errors), randItem(errors)) },
		"DialCompleted":    func() { mt.DialCompleted(mrand.Intn(2) == 1, mrand.Intn(10)) },
		"DialRankingDelay": func() { mt.DialRankingDelay(time.Duration(mrand.Intn(1e10))) },
		"UpdatedBlackHoleFilterState": func() {
			mt.UpdatedBlackHoleFilterState(
				randItem(bhfNames),
				randItem(bhfState),
				mrand.Intn(100),
				mrand.Float64(),
			)
		},
	}

	for method, f := range tests {
		allocs := testing.AllocsPerRun(1000, f)
		if allocs > 0 {
			t.Fatalf("Alloc Test: %s, got: %0.2f, expected: 0 allocs", method, allocs)
		}
	}
}
