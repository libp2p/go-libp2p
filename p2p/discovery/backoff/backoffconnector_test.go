package backoff

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	"github.com/stretchr/testify/require"
)

type maxDialHost struct {
	host.Host

	mux            sync.Mutex
	timesDialed    map[peer.ID]int
	maxTimesToDial map[peer.ID]int
}

func (h *maxDialHost) Connect(ctx context.Context, ai peer.AddrInfo) error {
	pid := ai.ID

	h.mux.Lock()
	defer h.mux.Unlock()
	numDials := h.timesDialed[pid]
	numDials += 1
	h.timesDialed[pid] = numDials

	if maxDials, ok := h.maxTimesToDial[pid]; ok && numDials > maxDials {
		return fmt.Errorf("should not be dialing peer %s", pid.String())
	}

	return h.Host.Connect(ctx, ai)
}

func getNetHosts(t *testing.T, n int) []host.Host {
	var out []host.Host

	for i := 0; i < n; i++ {
		netw := swarmt.GenSwarm(t)
		h := bhost.NewBlankHost(netw)
		t.Cleanup(func() { h.Close() })
		out = append(out, h)
	}

	return out
}

func loadCh(peers []host.Host) <-chan peer.AddrInfo {
	ch := make(chan peer.AddrInfo, len(peers))
	for _, p := range peers {
		ch <- p.Peerstore().PeerInfo(p.ID())
	}
	close(ch)
	return ch
}

func TestBackoffConnector(t *testing.T) {
	hosts := getNetHosts(t, 5)
	primary := &maxDialHost{
		Host:        hosts[0],
		timesDialed: make(map[peer.ID]int),
		maxTimesToDial: map[peer.ID]int{
			hosts[1].ID(): 1,
			hosts[2].ID(): 2,
		},
	}

	bc, err := NewBackoffConnector(primary, 10, time.Minute, NewFixedBackoff(250*time.Millisecond))
	require.NoError(t, err)

	bc.Connect(context.Background(), loadCh(hosts))
	require.Eventually(t, func() bool { return len(primary.Network().Peers()) == len(hosts)-1 }, 3*time.Second, 10*time.Millisecond)

	time.Sleep(100 * time.Millisecond) // give connection attempts time to complete (relevant when using multiple transports)
	for _, c := range primary.Network().Conns() {
		c.Close()
	}
	require.Eventually(t, func() bool { return len(primary.Network().Peers()) == 0 }, 3*time.Second, 10*time.Millisecond)

	bc.Connect(context.Background(), loadCh(hosts))
	require.Empty(t, primary.Network().Peers(), "shouldn't be connected to any peers")

	time.Sleep(time.Millisecond * 500)
	bc.Connect(context.Background(), loadCh(hosts))
	require.Eventually(t, func() bool { return len(primary.Network().Peers()) == len(hosts)-2 }, 3*time.Second, 10*time.Millisecond)
	// make sure we actually don't connect to host 1 any more
	time.Sleep(100 * time.Millisecond)
	require.Len(t, primary.Network().Peers(), len(hosts)-2, "wrong number of connections")
}
