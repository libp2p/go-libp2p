package rcmgr

import (
	"crypto/rand"
	"fmt"
	"net"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	"github.com/multiformats/go-multiaddr"
)

func ExampleWithAllowlistedMultiaddrs() {
	somePeer, err := test.RandPeerID()
	if err != nil {
		panic("Failed to generate somePeer")
	}

	limits := DefaultLimits.AutoScale()
	rcmgr, err := NewResourceManager(NewFixedLimiter(limits), WithAllowlistedMultiaddrs([]multiaddr.Multiaddr{
		// Any peer connecting from this IP address
		multiaddr.StringCast("/ip4/1.2.3.4"),
		// Only the specified peer from this address
		multiaddr.StringCast("/ip4/2.2.3.4/p2p/" + somePeer.String()),
		// Only peers from this 1.2.3.0/24 IP address range
		multiaddr.StringCast("/ip4/1.2.3.0/ipcidr/24"),
	}))
	if err != nil {
		panic("Failed to start resource manager")
	}

	// Use rcmgr as before
	_ = rcmgr
}

func TestAllowedSimple(t *testing.T) {
	allowlist := newAllowlist()
	ma := multiaddr.StringCast("/ip4/1.2.3.4/tcp/1234")
	err := allowlist.Add(ma)
	if err != nil {
		t.Fatalf("failed to add ip4: %s", err)
	}

	if !allowlist.Allowed(ma) {
		t.Fatalf("addr should be allowed")
	}
}

func TestAllowedWithPeer(t *testing.T) {
	type testcase struct {
		name      string
		allowlist []string
		endpoint  multiaddr.Multiaddr
		peer      peer.ID
		// Is this endpoint allowed? (We don't have peer info yet)
		isConnAllowed bool
		// Is this peer + endpoint allowed?
		isAllowedWithPeer bool
	}

	peerA := test.RandPeerIDFatal(t)
	peerB := test.RandPeerIDFatal(t)
	multiaddrA := multiaddr.StringCast("/ip4/1.2.3.4/tcp/1234")
	multiaddrB := multiaddr.StringCast("/ip4/2.2.3.4/tcp/1234")

	testcases := []testcase{
		{
			name:              "Blocked",
			isConnAllowed:     false,
			isAllowedWithPeer: false,
			allowlist:         []string{"/ip4/1.2.3.1"},
			endpoint:          multiaddrA,
			peer:              peerA,
		},
		{
			name:              "Blocked wrong peer",
			isConnAllowed:     true,
			isAllowedWithPeer: false,
			allowlist:         []string{"/ip4/1.2.3.4" + "/p2p/" + peerB.String()},
			endpoint:          multiaddrA,
			peer:              peerA,
		},
		{
			name:              "allowed on network",
			isConnAllowed:     true,
			isAllowedWithPeer: true,
			allowlist:         []string{"/ip4/1.2.3.0/ipcidr/24"},
			endpoint:          multiaddrA,
			peer:              peerA,
		},
		{
			name:              "Blocked peer not on network",
			isConnAllowed:     true,
			isAllowedWithPeer: true,
			allowlist:         []string{"/ip4/1.2.3.0/ipcidr/24"},
			endpoint:          multiaddrA,
			peer:              peerA,
		}, {
			name:              "allowed. right network, right peer",
			isConnAllowed:     true,
			isAllowedWithPeer: true,
			allowlist:         []string{"/ip4/1.2.3.0/ipcidr/24" + "/p2p/" + peerA.String()},
			endpoint:          multiaddrA,
			peer:              peerA,
		}, {
			name:              "allowed. right network, no peer",
			isConnAllowed:     true,
			isAllowedWithPeer: true,
			allowlist:         []string{"/ip4/1.2.3.0/ipcidr/24"},
			endpoint:          multiaddrA,
			peer:              peerA,
		},
		{
			name:              "Blocked. right network, wrong peer",
			isConnAllowed:     true,
			isAllowedWithPeer: false,
			allowlist:         []string{"/ip4/1.2.3.0/ipcidr/24" + "/p2p/" + peerB.String()},
			endpoint:          multiaddrA,
			peer:              peerA,
		},
		{
			name:              "allowed peer any ip",
			isConnAllowed:     true,
			isAllowedWithPeer: true,
			allowlist:         []string{"/ip4/0.0.0.0/ipcidr/0"},
			endpoint:          multiaddrA,
			peer:              peerA,
		},
		{
			name:              "allowed peer multiple ips in allowlist",
			isConnAllowed:     true,
			isAllowedWithPeer: true,
			allowlist:         []string{"/ip4/1.2.3.4/p2p/" + peerA.String(), "/ip4/2.2.3.4/p2p/" + peerA.String()},
			endpoint:          multiaddrA,
			peer:              peerA,
		},
		{
			name:              "allowed peer multiple ips in allowlist",
			isConnAllowed:     true,
			isAllowedWithPeer: true,
			allowlist:         []string{"/ip4/1.2.3.4/p2p/" + peerA.String(), "/ip4/1.2.3.4/p2p/" + peerA.String()},
			endpoint:          multiaddrA,
			peer:              peerA,
		},
		{
			name:              "allowed peer multiple ips in allowlist",
			isConnAllowed:     true,
			isAllowedWithPeer: true,
			allowlist:         []string{"/ip4/1.2.3.4/p2p/" + peerA.String(), "/ip4/2.2.3.4/p2p/" + peerA.String()},
			endpoint:          multiaddrB,
			peer:              peerA,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			allowlist := newAllowlist()
			for _, maStr := range tc.allowlist {
				ma, err := multiaddr.NewMultiaddr(maStr)
				if err != nil {
					fmt.Printf("failed to parse multiaddr: %s", err)
				}
				allowlist.Add(ma)
			}

			if allowlist.Allowed(tc.endpoint) != tc.isConnAllowed {
				t.Fatalf("%v: expected %v", !tc.isConnAllowed, tc.isConnAllowed)
			}

			if allowlist.AllowedPeerAndMultiaddr(tc.peer, tc.endpoint) != tc.isAllowedWithPeer {
				t.Fatalf("%v: expected %v", !tc.isAllowedWithPeer, tc.isAllowedWithPeer)
			}
		})
	}

}

func TestRemoved(t *testing.T) {
	type testCase struct {
		name      string
		allowedMA string
	}
	peerA := test.RandPeerIDFatal(t)
	maA := multiaddr.StringCast("/ip4/1.2.3.4")

	testCases := []testCase{
		{name: "ip4", allowedMA: "/ip4/1.2.3.4"},
		{name: "ip4 with peer", allowedMA: "/ip4/1.2.3.4/p2p/" + peerA.String()},
		{name: "ip4 network", allowedMA: "/ip4/0.0.0.0/ipcidr/0"},
		{name: "ip4 network with peer", allowedMA: "/ip4/0.0.0.0/ipcidr/0/p2p/" + peerA.String()},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			allowlist := newAllowlist()
			ma := multiaddr.StringCast(tc.allowedMA)

			err := allowlist.Add(ma)
			if err != nil {
				t.Fatalf("failed to add ip4: %s", err)
			}

			if !allowlist.AllowedPeerAndMultiaddr(peerA, maA) {
				t.Fatalf("addr should be allowed")
			}

			allowlist.Remove((ma))

			if allowlist.AllowedPeerAndMultiaddr(peerA, maA) {
				t.Fatalf("addr should not be allowed")
			}
		})
	}
}

// BenchmarkAllowlistCheck benchmarks the allowlist with plausible conditions.
func BenchmarkAllowlistCheck(b *testing.B) {
	allowlist := newAllowlist()

	// How often do we expect a peer to be specified? 1 in N
	ratioOfSpecifiedPeers := 10

	// How often do we expect an allowlist hit? 1 in N
	ratioOfAllowlistHit := 100

	// How many multiaddrs in our allowlist?
	howManyMultiaddrsInAllowList := 1_000

	// How often is the IP addr an IPV6? 1 in N
	ratioOfIPV6 := 20

	countOfTotalPeersForTest := 100_000

	mas := make([]multiaddr.Multiaddr, countOfTotalPeersForTest)
	for i := range countOfTotalPeersForTest {

		ip := make([]byte, 16)
		n, err := rand.Reader.Read(ip)
		if err != nil || n != 16 {
			b.Fatalf("Failed to generate IP address")
		}

		var ipString string

		if i%ratioOfIPV6 == 0 {
			// IPv6
			ip6 := net.IP(ip)
			ipString = "/ip6/" + ip6.String()
		} else {
			// IPv4
			ip4 := net.IPv4(ip[0], ip[1], ip[2], ip[3])
			ipString = "/ip4/" + ip4.String()
		}

		var ma multiaddr.Multiaddr
		if i%ratioOfSpecifiedPeers == 0 {
			ma = multiaddr.StringCast(ipString + "/p2p/" + test.RandPeerIDFatal(b).String())
		} else {
			ma = multiaddr.StringCast(ipString)
		}
		if err != nil {
			b.Fatalf("Failed to generate multiaddr: %v", ipString)
		}

		mas[i] = ma
	}

	for _, ma := range mas[:howManyMultiaddrsInAllowList] {
		err := allowlist.Add(ma)
		if err != nil {
			b.Fatalf("Failed to add multiaddr")
		}
	}

	masInAllowList := mas[:howManyMultiaddrsInAllowList]
	masNotInAllowList := mas[howManyMultiaddrsInAllowList:]

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if n%ratioOfAllowlistHit == 0 {
			allowlist.Allowed(masInAllowList[n%len(masInAllowList)])
		} else {
			allowlist.Allowed(masNotInAllowList[n%len(masNotInAllowList)])
		}
	}
}
