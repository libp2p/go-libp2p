package mdns

import (
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupMDNS(t *testing.T, notifee Notifee) peer.ID {
	t.Helper()
	host, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	require.NoError(t, err)
	s := NewMdnsService(host, "", notifee)
	require.NoError(t, s.Start())
	t.Cleanup(func() {
		host.Close()
		s.Close()
	})
	return host.ID()
}

type notif struct {
	mutex sync.Mutex
	infos []peer.AddrInfo
}

var _ Notifee = &notif{}

func (n *notif) HandlePeerFound(info peer.AddrInfo) {
	n.mutex.Lock()
	n.infos = append(n.infos, info)
	n.mutex.Unlock()
}

func (n *notif) GetPeers() []peer.AddrInfo {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	infos := make([]peer.AddrInfo, 0, len(n.infos))
	infos = append(infos, n.infos...)
	return infos
}

func TestOtherDiscovery(t *testing.T) {
	if runtime.GOOS != "linux" && os.Getenv("CI") != "" {
		t.Skip("this test is flaky on CI outside of linux")
	}

	const n = 4

	notifs := make([]*notif, n)
	hostIDs := make([]peer.ID, n)
	for i := 0; i < n; i++ {
		notif := &notif{}
		notifs[i] = notif
		hostIDs[i] = setupMDNS(t, notif)
	}

	containsAllHostIDs := func(ids []peer.ID, currentHostID peer.ID) bool {
		for _, id := range hostIDs {
			var found bool
			if currentHostID == id {
				continue
			}
			for _, i := range ids {
				if id == i {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}

	assert.Eventuallyf(
		t,
		func() bool {
			for i, notif := range notifs {
				infos := notif.GetPeers()
				ids := make([]peer.ID, 0, len(infos))
				for _, info := range infos {
					ids = append(ids, info.ID)
				}
				if !containsAllHostIDs(ids, hostIDs[i]) {
					return false
				}
			}
			return true
		},
		5*time.Second,
		100*time.Millisecond,
		"expected peers to find each other",
	)
}
