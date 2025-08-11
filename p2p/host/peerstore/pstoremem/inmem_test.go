package pstoremem

import (
	"strconv"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	pstore "github.com/libp2p/go-libp2p/core/peerstore"
	pt "github.com/libp2p/go-libp2p/p2p/host/peerstore/test"
	"github.com/multiformats/go-multiaddr"

	mockClock "github.com/benbjohnson/clock"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestInvalidOption(t *testing.T) {
	_, err := NewPeerstore(1337)
	require.EqualError(t, err, "unexpected peer store option: 1337")
}

func TestFuzzInMemoryPeerstore(t *testing.T) {
	// Just create and close a bunch of peerstores. If this leaks, we'll
	// catch it in the leak check below.
	for i := 0; i < 100; i++ {
		ps, err := NewPeerstore()
		require.NoError(t, err)
		ps.Close()
	}
}

func TestInMemoryPeerstore(t *testing.T) {
	pt.TestPeerstore(t, func() (pstore.Peerstore, func()) {
		ps, err := NewPeerstore()
		require.NoError(t, err)
		return ps, func() { ps.Close() }
	})
}

func TestPeerstoreProtoStoreLimits(t *testing.T) {
	const limit = 10
	ps, err := NewPeerstore(WithMaxProtocols(limit))
	require.NoError(t, err)
	defer ps.Close()
	pt.TestPeerstoreProtoStoreLimits(t, ps, limit)
}

func TestInMemoryAddrBook(t *testing.T) {
	clk := mockClock.NewMock()
	pt.TestAddrBook(t, func() (pstore.AddrBook, func()) {
		ps, err := NewPeerstore(WithClock(clk))
		require.NoError(t, err)
		return ps, func() { ps.Close() }
	}, clk)
}

func TestInMemoryKeyBook(t *testing.T) {
	pt.TestKeyBook(t, func() (pstore.KeyBook, func()) {
		ps, err := NewPeerstore()
		require.NoError(t, err)
		return ps, func() { ps.Close() }
	})
}

func BenchmarkInMemoryPeerstore(b *testing.B) {
	pt.BenchmarkPeerstore(b, func() (pstore.Peerstore, func()) {
		ps, err := NewPeerstore()
		require.NoError(b, err)
		return ps, func() { ps.Close() }
	}, "InMem")
}

func BenchmarkInMemoryKeyBook(b *testing.B) {
	pt.BenchmarkKeyBook(b, func() (pstore.KeyBook, func()) {
		ps, err := NewPeerstore()
		require.NoError(b, err)
		return ps, func() { ps.Close() }
	})
}

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(
		m,
		goleak.IgnoreTopFunction("github.com/libp2p/go-libp2p/gologshim/writer.(*MirrorWriter).logRoutine"),
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
	)
}

func BenchmarkGC(b *testing.B) {
	clock := mockClock.NewMock()
	ps, err := NewPeerstore(WithClock(clock))
	require.NoError(b, err)
	defer ps.Close()

	peerCount := 100_000
	addrsPerPeer := 32

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		for i := 0; i < peerCount; i++ {
			id := peer.ID(strconv.Itoa(i))
			addrs := make([]multiaddr.Multiaddr, addrsPerPeer)
			for j := 0; j < addrsPerPeer; j++ {
				addrs[j] = multiaddr.StringCast("/ip4/1.2.3.4/tcp/" + strconv.Itoa(j))
			}
			ps.AddAddrs(id, addrs, 24*time.Hour)
		}
		clock.Add(25 * time.Hour)
		b.StartTimer()
		ps.gc()
	}
}
