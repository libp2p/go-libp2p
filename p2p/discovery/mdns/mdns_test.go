package mdns

import (
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
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

func TestIsSuitableForMDNS(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		expected bool
	}{
		// IP addresses with native transports - suitable for mDNS
		{"tcp", "/ip4/192.168.1.1/tcp/4001", true},
		{"quic-v1", "/ip4/192.168.1.2/udp/4001/quic-v1", true},
		{"tcp-ipv6", "/ip6/fe80::1/tcp/4001", true},
		{"quic-v1-ipv6", "/ip6/fe80::2/udp/4001/quic-v1", true},

		// Browser transports - NOT suitable for mDNS
		// (browsers don't use mDNS for peer discovery)
		{"webtransport", "/ip4/192.168.1.1/udp/4001/quic-v1/webtransport", false},
		{"webrtc", "/ip4/192.168.1.1/udp/4001/webrtc/certhash/uEiAkH5a4DPGKUuOBjYw0CgwjLa2R_RF71v86aVxlqdKNOQ", false},
		{"webrtc-direct", "/ip4/192.168.1.1/udp/4001/webrtc-direct", false},
		{"ws", "/ip4/192.168.1.1/tcp/4001/ws", false},
		{"wss", "/ip4/192.168.1.1/tcp/443/wss", false},

		// .local DNS names - suitable for mDNS
		// (.local TLD is resolved via mDNS per RFC 6762)
		{"dns-local", "/dns/myhost.local/tcp/4001", true},
		{"dns4-local", "/dns4/myhost.local/tcp/4001", true},
		{"dns6-local", "/dns6/myhost.local/tcp/4001", true},
		{"dnsaddr-local", "/dnsaddr/myhost.local/tcp/4001", true},
		{"dns-local-mixed-case", "/dns4/MyHost.LOCAL/tcp/4001", true},

		// Non-.local DNS names - NOT suitable for mDNS
		// (require unicast DNS resolution, not mDNS)
		{"dns4-public", "/dns4/example.com/tcp/4001", false},
		{"dns6-public", "/dns6/example.com/tcp/4001", false},
		{"dnsaddr-public", "/dnsaddr/example.com/tcp/4001", false},
		{"dns-local-suffix-not-tld", "/dns4/notlocal.com/tcp/4001", false},
		{"dns-fake-local", "/dns4/local.example.com/tcp/4001", false},
		{"libp2p-direct", "/dns4/192-0-2-1.k51qzi5uqu5dgutdk6i1ynyzgkqngpha5xpgia3a5qqp4jsh0u4csozksxel3r.libp2p.direct/tcp/30895/tls/ws", false},

		// Circuit relay addresses - NOT suitable for mDNS
		// (require relay node, not direct LAN connectivity)
		{"circuit-relay", "/ip4/198.51.100.1/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN/p2p-circuit/p2p/12D3KooWGzBXWNvHpLALvz3jhwdCF6kfv9MfhMn9CuS2MBD2GpSy", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, err := ma.NewMultiaddr(tc.addr)
			require.NoError(t, err)
			got := isSuitableForMDNS(addr)
			assert.Equal(t, tc.expected, got, "isSuitableForMDNS(%s)", tc.addr)
		})
	}
}
