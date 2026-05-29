package basichost

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/x/simlibp2p"
	"github.com/marcopolo/simnet"

	"github.com/stretchr/testify/require"
)

// TestCloseWithBlockingConnectedNotifee verifies that swarm.Close
// eventually returns even when Notifiee.Connected handlers block for a
// long time. The handler blocks for blockDur after first being called and
// becomes a noop afterwards. After closeDelay we close the dialer; Close
// must return once the handlers unblock.
func TestCloseWithBlockingConnectedNotifee(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			numListeners = 100
			blockDur     = 5 * time.Second
			closeDelay   = 2 * time.Second
		)

		latency := 10 * time.Millisecond
		sn, meta, err := simlibp2p.SimpleLibp2pNetwork([]simlibp2p.NodeLinkSettingsAndCount{
			{LinkSettings: simnet.NodeBiDiLinkSettings{
				Downlink: simnet.LinkSettings{BitsPerSecond: 20 * simlibp2p.OneMbps}, // Divide by two since this is latency for each direction
				Uplink:   simnet.LinkSettings{BitsPerSecond: 20 * simlibp2p.OneMbps},
			}, Count: 100},
		}, simnet.StaticLatency(latency/2), simlibp2p.NetworkSettings{})
		require.NoError(t, err)
		defer sn.Close()
		defer func() {
			for _, h := range meta.Nodes {
				h.Close()
			}
		}()

		dialer := meta.Nodes[0]

		// Connected handler blocks on this channel; we close it after blockDur.
		unblock := make(chan struct{})
		time.AfterFunc(blockDur, func() { close(unblock) })
		dialer.Network().Notify(&network.NotifyBundle{
			ConnectedF: func(_ network.Network, _ network.Conn) {
				<-unblock
			},
		})
		var wg sync.WaitGroup
		for i := 1; i < len(meta.Nodes); i++ {
			wg.Go(func() {
				node := meta.Nodes[i]
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				dialer.Connect(ctx, node.Peerstore().PeerInfo(node.ID()))
				cancel()
				// we don't care about the error. swarm closing might return error here.
			})
		}

		time.Sleep(closeDelay)

		closed := make(chan struct{})
		wg.Go(func() {
			require.NoError(t, dialer.Close())
			close(closed)
		})
		select {
		case <-closed:
		case <-time.After(10 * time.Second):
			t.Fatalf("close timed out")
		}

		wg.Wait()
	})
}
