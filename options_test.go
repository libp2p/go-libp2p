package libp2p

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/test"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestDeprecatedFiltersOptionsOutbound(t *testing.T) {
	require := require.New(t)

	f := ma.NewFilters()
	_, ipnet, _ := net.ParseCIDR("127.0.0.0/24")
	f.AddFilter(*ipnet, ma.ActionDeny)

	host0, err := New(context.TODO(), Filters(f))
	require.NoError(err)
	require.NotNil(host0)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	id, _ := test.RandPeerID()
	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/0/p2p/" + id.Pretty())
	ai, _ := peer.AddrInfoFromP2pAddr(addr)

	err = host0.Connect(ctx, *ai)
	require.Error(err)
	require.Contains(err.Error(), "no good addresses")
}

func TestDeprecatedFiltersOptionsInbound(t *testing.T) {
	require := require.New(t)

	host0, err := New(context.TODO())
	require.NoError(err)
	require.NotNil(host0)

	f := ma.NewFilters()
	var ipNet net.IPNet
	for _, addr := range host0.Addrs() {
		ipNet, err = ipNetFromMaddr(addr)
		if err == nil {
			require.NotNil(t, ipNet)
			f.AddFilter(ipNet, ma.ActionDeny)
		}
	}

	host1, err := New(context.TODO(), Filters(f))
	require.NoError(err)
	require.NotNil(host1)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	peerInfo := peer.AddrInfo{
		ID:    host1.ID(),
		Addrs: host1.Addrs(),
	}
	addrs, err := peer.AddrInfoToP2pAddrs(&peerInfo)
	ai, err := peer.AddrInfoFromP2pAddr(addrs[0])
	require.NoError(err)

	err = host0.Connect(ctx, *ai)
	require.Error(err)
}

func TestDeprecatedFiltersAndAddressesOptions(t *testing.T) {
	require := require.New(t)

	f := ma.NewFilters()
	_, ipnet1, _ := net.ParseCIDR("127.0.0.0/24")
	_, ipnet2, _ := net.ParseCIDR("128.0.0.0/24")
	f.AddFilter(*ipnet1, ma.ActionDeny)

	host, err := New(context.TODO(), Filters(f), FilterAddresses(ipnet2))
	require.NoError(err)
	require.NotNil(host)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	id, _ := test.RandPeerID()
	addr1, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/0/p2p/" + id.Pretty())
	addr2, _ := ma.NewMultiaddr("/ip4/128.0.0.1/tcp/0/p2p/" + id.Pretty())
	ai, _ := peer.AddrInfosFromP2pAddrs(addr1, addr2)

	err = host.Connect(ctx, ai[0])
	require.Error(err)
	require.Contains(err.Error(), "no good addresses")
}

func TestCannotSetFiltersAndConnGater(t *testing.T) {
	require := require.New(t)

	f := ma.NewFilters()

	_, err := New(context.TODO(), Filters(f), ConnectionGater(nil))
	require.Error(err)
	require.Contains(err.Error(), "cannot configure multiple connection gaters")
}

/// NOTE(jalextowle): This was taken directly from https://github.com/0x-mesh/p2p/banner/banner.go
func ipNetFromMaddr(maddr ma.Multiaddr) (ipNet net.IPNet, err error) {
	ip, err := ipFromMaddr(maddr)
	if err != nil {
		return net.IPNet{}, err
	}
	mask := getAllMaskForIP(ip)
	return net.IPNet{
		IP:   ip,
		Mask: mask,
	}, nil
}

/// NOTE(jalextowle): This was taken directly from https://github.com/0x-mesh/p2p/banner/banner.go
func ipFromMaddr(maddr ma.Multiaddr) (net.IP, error) {
	var (
		ip    net.IP
		found bool
	)

	ma.ForEach(maddr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_IP6ZONE:
			return true
		case ma.P_IP6, ma.P_IP4:
			found = true
			ip = net.IP(c.RawValue())
			return false
		default:
			return false
		}
	})

	if !found {
		return net.IP{}, fmt.Errorf("could not parse IP address from multiaddress: %s", maddr)
	}
	return ip, nil
}

/// NOTE(jalextowle): This was taken directly from https://github.com/0x-mesh/p2p/banner/banner.go
var (
	ipv4AllMask = net.IPMask{255, 255, 255, 255}
	ipv6AllMask = net.IPMask{255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255}
)

/// NOTE(jalextowle): This was taken directly from https://github.com/0x-mesh/p2p/banner/banner.go
// getAllMaskForIP returns an IPMask that will match all IP addresses. The size
// of the mask depends on whether the given IP address is an IPv4 or an IPv6
// address.
func getAllMaskForIP(ip net.IP) net.IPMask {
	if ip.To4() != nil {
		// This is an ipv4 address. Return 4 byte mask.
		return ipv4AllMask
	}
	// Assume ipv6 address. Return 16 byte mask.
	return ipv6AllMask
}
