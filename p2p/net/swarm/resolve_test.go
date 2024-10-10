package swarm

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/multiformats/go-multiaddr"
	madns "github.com/multiformats/go-multiaddr-dns"
	"github.com/stretchr/testify/require"
)

func TestSwarmResolver(t *testing.T) {
	mockResolver := madns.MockResolver{IP: make(map[string][]net.IPAddr)}
	ipaddr, err := net.ResolveIPAddr("ip4", "127.0.0.1")
	require.NoError(t, err)
	mockResolver.IP["example.com"] = []net.IPAddr{*ipaddr}
	mockResolver.TXT = map[string][]string{
		"_dnsaddr.example.com": {"dnsaddr=/ip4/127.0.0.1"},
	}
	madnsResolver, err := madns.NewResolver(madns.WithDomainResolver("example.com", &mockResolver))
	require.NoError(t, err)
	swarmResolver := ResolverFromMaDNS{madnsResolver}

	ctx := context.Background()
	res, err := swarmResolver.ResolveDNSComponent(ctx, multiaddr.StringCast("/dns/example.com"), 10)
	require.NoError(t, err)
	require.Equal(t, 1, len(res))
	require.Equal(t, "/ip4/127.0.0.1", res[0].String())

	res, err = swarmResolver.ResolveDNSAddr(ctx, "", multiaddr.StringCast("/dnsaddr/example.com"), 1, 10)
	require.NoError(t, err)
	require.Equal(t, 1, len(res))
	require.Equal(t, "/ip4/127.0.0.1", res[0].String())

	t.Run("Test Limits", func(t *testing.T) {
		var ipaddrs []net.IPAddr
		var manyDNSAddrs []string
		for i := 0; i < 255; i++ {
			ip := "1.2.3." + strconv.Itoa(i)
			ipaddrs = append(ipaddrs, net.IPAddr{IP: net.ParseIP(ip)})
			manyDNSAddrs = append(manyDNSAddrs, "dnsaddr=/ip4/"+ip)
		}

		mockResolver.IP = map[string][]net.IPAddr{
			"example.com": ipaddrs,
		}
		mockResolver.TXT = map[string][]string{
			"_dnsaddr.example.com": manyDNSAddrs,
		}

		res, err := swarmResolver.ResolveDNSComponent(ctx, multiaddr.StringCast("/dns/example.com"), 10)
		require.NoError(t, err)
		require.Equal(t, 10, len(res))
		for i := 0; i < 10; i++ {
			require.Equal(t, "/ip4/1.2.3."+strconv.Itoa(i), res[i].String())
		}

		res, err = swarmResolver.ResolveDNSAddr(ctx, "", multiaddr.StringCast("/dnsaddr/example.com"), 1, 10)
		require.NoError(t, err)
		require.Equal(t, 10, len(res))
		for i := 0; i < 10; i++ {
			require.Equal(t, "/ip4/1.2.3."+strconv.Itoa(i), res[i].String())
		}
	})

	t.Run("Test Recursive Limits", func(t *testing.T) {
		recursiveDNSAddr := make(map[string][]string)
		for i := 0; i < 255; i++ {
			recursiveDNSAddr["_dnsaddr."+strconv.Itoa(i)+".example.com"] = []string{"dnsaddr=/dnsaddr/" + strconv.Itoa(i+1) + ".example.com"}
		}
		recursiveDNSAddr["_dnsaddr.255.example.com"] = []string{"dnsaddr=/ip4/127.0.0.1"}
		mockResolver.TXT = recursiveDNSAddr

		res, err = swarmResolver.ResolveDNSAddr(ctx, "", multiaddr.StringCast("/dnsaddr/0.example.com"), 256, 10)
		require.NoError(t, err)
		require.Equal(t, 1, len(res))
		require.Equal(t, "/ip4/127.0.0.1", res[0].String())

		res, err = swarmResolver.ResolveDNSAddr(ctx, "", multiaddr.StringCast("/dnsaddr/0.example.com"), 255, 10)
		require.NoError(t, err)
		require.Equal(t, 1, len(res))
		require.Equal(t, "/dnsaddr/255.example.com", res[0].String())
	})

	t.Run("Test Resolve at output limit", func(t *testing.T) {
		recursiveDNSAddr := make(map[string][]string)
		recursiveDNSAddr["_dnsaddr.example.com"] = []string{
			"dnsaddr=/dnsaddr/0.example.com",
			"dnsaddr=/dnsaddr/1.example.com",
			"dnsaddr=/dnsaddr/2.example.com",
			"dnsaddr=/dnsaddr/3.example.com",
			"dnsaddr=/dnsaddr/4.example.com",
			"dnsaddr=/dnsaddr/5.example.com",
			"dnsaddr=/dnsaddr/6.example.com",
			"dnsaddr=/dnsaddr/7.example.com",
			"dnsaddr=/dnsaddr/8.example.com",
			"dnsaddr=/dnsaddr/9.example.com",
		}
		recursiveDNSAddr["_dnsaddr.0.example.com"] = []string{"dnsaddr=/ip4/127.0.0.1"}
		recursiveDNSAddr["_dnsaddr.1.example.com"] = []string{"dnsaddr=/ip4/127.0.0.1"}
		recursiveDNSAddr["_dnsaddr.2.example.com"] = []string{"dnsaddr=/ip4/127.0.0.1"}
		recursiveDNSAddr["_dnsaddr.3.example.com"] = []string{"dnsaddr=/ip4/127.0.0.1"}
		recursiveDNSAddr["_dnsaddr.4.example.com"] = []string{"dnsaddr=/ip4/127.0.0.1"}
		recursiveDNSAddr["_dnsaddr.5.example.com"] = []string{"dnsaddr=/ip4/127.0.0.1"}
		recursiveDNSAddr["_dnsaddr.6.example.com"] = []string{"dnsaddr=/ip4/127.0.0.1"}
		recursiveDNSAddr["_dnsaddr.7.example.com"] = []string{"dnsaddr=/ip4/127.0.0.1"}
		recursiveDNSAddr["_dnsaddr.8.example.com"] = []string{"dnsaddr=/ip4/127.0.0.1"}
		recursiveDNSAddr["_dnsaddr.9.example.com"] = []string{"dnsaddr=/ip4/127.0.0.1"}
		mockResolver.TXT = recursiveDNSAddr

		res, err = swarmResolver.ResolveDNSAddr(ctx, "", multiaddr.StringCast("/dnsaddr/example.com"), 256, 10)
		require.NoError(t, err)
		require.Equal(t, 10, len(res))
		for _, r := range res {
			require.Equal(t, "/ip4/127.0.0.1", r.String())
		}
	})
}
