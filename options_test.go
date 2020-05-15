package libp2p

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/test"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestDeprecatedFiltersOptions(t *testing.T) {
	require := require.New(t)

	f := ma.NewFilters()
	_, ipnet, _ := net.ParseCIDR("127.0.0.0/24")
	f.AddFilter(*ipnet, ma.ActionDeny)

	host, err := New(context.TODO(), Filters(f))
	require.NoError(err)
	require.NotNil(host)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	id, _ := test.RandPeerID()
	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/0/p2p/" + id.Pretty())
	ai, _ := peer.AddrInfoFromP2pAddr(addr)

	err = host.Connect(ctx, *ai)
	require.Error(err)
	require.Contains(err.Error(), "no good addresses")
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
