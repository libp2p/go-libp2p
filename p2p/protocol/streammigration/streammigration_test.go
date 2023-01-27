package streammigration_test

import (
	"context"
	"testing"
	"time"

	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"

	"github.com/libp2p/go-libp2p-core/peer"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/libp2p/go-libp2p/p2p/protocol/streammigration"
)

func filterMAAddrs(mas []multiaddr.Multiaddr, hasCode int) []multiaddr.Multiaddr {
	var addrs []multiaddr.Multiaddr
	for _, m := range mas {
		for _, p := range m.Protocols() {
			if p.Code == hasCode {
				addrs = append(addrs, m)
			}
		}
	}
	return addrs
}

func TestStreamMigrationOnBasicHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1, err := bhost.NewHost(swarmt.GenSwarm(t), &bhost.HostOpts{
		EnableStreamMigration: true,
	})
	require.NoError(t, err)

	h2, err := bhost.NewHost(swarmt.GenSwarm(t), &bhost.HostOpts{
		EnableStreamMigration: true,
	})
	require.NoError(t, err)

	err = h1.Connect(ctx, peer.AddrInfo{
		ID:    h2.ID(),
		Addrs: filterMAAddrs(h2.Addrs(), multiaddr.P_TCP),
	})
	require.NoError(t, err)

	ps1 := ping.NewPingService(h1)
	// ps2 := ping.NewPingService(h2)
	ping.NewPingService(h2)

	testPing(t, h1, ps1, peer.AddrInfo{
		ID:    h2.ID(),
		Addrs: h2.Addrs(),
	})
	// testPing(t, h2, ps2, h1.ID())

}

func testPing(t *testing.T, h *bhost.BasicHost, ps *ping.PingService, pi peer.AddrInfo) {
	pctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, ts := ping.PingWithStream(pctx, ps.Host, pi.ID)

	for i := 0; i < 20; i++ {
		if i%5 == 0 {
			newStream, err := h.NewStream(pctx, pi.ID, streammigration.ID)
			require.NoError(t, err)
			require.True(t, h.MigrateStream(s, newStream))
		}
		select {
		case res := <-ts:
			require.NoError(t, res.Error)
			t.Log("ping took: ", res.RTT)
			time.Sleep(100 * time.Millisecond)
		case <-time.After(time.Second * 4):
			t.Fatal("failed to receive ping")
		}
	}

}
