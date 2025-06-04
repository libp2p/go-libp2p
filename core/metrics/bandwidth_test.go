package metrics

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/libp2p/go-flow-metrics"

	"github.com/benbjohnson/clock"
	"github.com/stretchr/testify/require"
)

var cl = clock.NewMock()

func init() {
	flow.SetClock(cl)
}

func BenchmarkBandwidthCounter(b *testing.B) {
	b.StopTimer()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		bwc := NewBandwidthCounter()
		round(bwc, b)
	}
}

func round(bwc *BandwidthCounter, b *testing.B) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(10000)
	for i := 0; i < 1000; i++ {
		p := peer.ID(fmt.Sprintf("peer-%d", i))
		for j := 0; j < 10; j++ {
			proto := protocol.ID(fmt.Sprintf("bitswap-%d", j))
			go func() {
				defer wg.Done()
				<-start

				for i := 0; i < 1000; i++ {
					bwc.LogSentMessage(100)
					bwc.LogSentMessageStream(100, proto, p)
					time.Sleep(1 * time.Millisecond)
				}
			}()
		}
	}

	b.StartTimer()
	close(start)
	wg.Wait()
	b.StopTimer()
}

func TestBandwidthCounter(t *testing.T) {
	bwc := NewBandwidthCounter()
	for i := 0; i < 40; i++ {
		for i := 0; i < 100; i++ {
			p := peer.ID(fmt.Sprintf("peer-%d", i))
			for j := 0; j < 2; j++ {
				proto := protocol.ID(fmt.Sprintf("proto-%d", j))

				// make sure the bandwidth counters are active
				bwc.LogSentMessage(100)
				bwc.LogRecvMessage(50)
				bwc.LogSentMessageStream(100, proto, p)
				bwc.LogRecvMessageStream(50, proto, p)

				// <-start
			}
		}
		cl.Add(100 * time.Millisecond)
	}

	assertProtocols := func(check func(Stats)) {
		byProtocol := bwc.GetBandwidthByProtocol()
		require.Len(t, byProtocol, 2, "expected 2 protocols")
		for i := 0; i < 2; i++ {
			p := protocol.ID(fmt.Sprintf("proto-%d", i))
			for _, stats := range [...]Stats{bwc.GetBandwidthForProtocol(p), byProtocol[p]} {
				check(stats)
			}
		}
	}

	assertPeers := func(check func(Stats)) {
		byPeer := bwc.GetBandwidthByPeer()
		require.Len(t, byPeer, 100, "expected 100 peers")
		for i := 0; i < 100; i++ {
			p := peer.ID(fmt.Sprintf("peer-%d", i))
			for _, stats := range [...]Stats{bwc.GetBandwidthForPeer(p), byPeer[p]} {
				check(stats)
			}
		}
	}

	assertPeers(func(stats Stats) {
		require.Equal(t, int64(8000), stats.TotalOut)
		require.Equal(t, int64(4000), stats.TotalIn)
	})

	assertProtocols(func(stats Stats) {
		require.Equal(t, int64(400000), stats.TotalOut)
		require.Equal(t, int64(200000), stats.TotalIn)
	})

	stats := bwc.GetBandwidthTotals()
	require.Equal(t, int64(800000), stats.TotalOut)
	require.Equal(t, int64(400000), stats.TotalIn)
}

func TestResetBandwidthCounter(t *testing.T) {
	bwc := NewBandwidthCounter()

	p := peer.ID("peer-0")
	proto := protocol.ID("proto-0")

	// We don't calculate bandwidth till we've been active for a second.
	bwc.LogSentMessage(42)
	bwc.LogRecvMessage(24)
	bwc.LogSentMessageStream(100, proto, p)
	bwc.LogRecvMessageStream(50, proto, p)

	time.Sleep(200 * time.Millisecond) // make sure the meters are registered with the sweeper
	cl.Add(time.Second)

	bwc.LogSentMessage(42)
	bwc.LogRecvMessage(24)
	bwc.LogSentMessageStream(100, proto, p)
	bwc.LogRecvMessageStream(50, proto, p)

	cl.Add(time.Second)

	{
		stats := bwc.GetBandwidthTotals()
		require.Equal(t, int64(84), stats.TotalOut)
		require.Equal(t, int64(48), stats.TotalIn)
	}

	{
		stats := bwc.GetBandwidthByProtocol()
		require.Len(t, stats, 1)
		stat := stats[proto]
		require.Equal(t, float64(100), stat.RateOut)
		require.Equal(t, float64(50), stat.RateIn)
	}

	{
		stats := bwc.GetBandwidthByPeer()
		require.Len(t, stats, 1)
		stat := stats[p]
		require.Equal(t, float64(100), stat.RateOut)
		require.Equal(t, float64(50), stat.RateIn)
	}

	bwc.Reset()
	{
		stats := bwc.GetBandwidthTotals()
		require.Zero(t, stats.TotalOut)
		require.Zero(t, stats.TotalIn)
		require.Empty(t, bwc.GetBandwidthByProtocol(), "expected 0 protocols")
		require.Empty(t, bwc.GetBandwidthByPeer(), "expected 0 peers")
	}
}
