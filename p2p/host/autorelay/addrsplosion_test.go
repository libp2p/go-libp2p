package autorelay

import (
	"testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestCleanupAddrs(t *testing.T) {
	t.Run("with no addrplosion", func(t *testing.T) {
		addrs := makeAddrList(
			"/ip4/127.0.0.1/tcp/4001",
			"/ip4/127.0.0.1/udp/4002/quic-v1",
			"/ip4/1.2.3.4/tcp/4001",
			"/ip4/1.2.3.4/udp/4002/quic-v1",
			"/dnsaddr/somedomain.com/tcp/4002/ws",
		)
		clean := makeAddrList(
			"/ip4/1.2.3.4/tcp/4001",
			"/ip4/1.2.3.4/udp/4002/quic-v1",
			"/dnsaddr/somedomain.com/tcp/4002/ws",
		)
		require.ElementsMatch(t, clean, cleanupAddressSet(addrs), "cleaned up set doesn't match expected")
	})

	t.Run("with default port", func(t *testing.T) {
		// test with default port addrspolosion
		addrs := makeAddrList(
			"/ip4/127.0.0.1/tcp/4001",
			"/ip4/1.2.3.4/tcp/4001",
			"/ip4/1.2.3.4/tcp/33333",
			"/ip4/1.2.3.4/tcp/33334",
			"/ip4/1.2.3.4/tcp/33335",
			"/ip4/1.2.3.4/udp/4002/quic-v1",
		)
		clean := makeAddrList(
			"/ip4/1.2.3.4/tcp/4001",
			"/ip4/1.2.3.4/udp/4002/quic-v1",
		)
		require.ElementsMatch(t, clean, cleanupAddressSet(addrs), "cleaned up set doesn't match expected")
	})

	t.Run("with default port, but no private addrs", func(t *testing.T) {
		// test with default port addrsplosion but no private addrs
		addrs := makeAddrList(
			"/ip4/1.2.3.4/tcp/4001",
			"/ip4/1.2.3.4/tcp/33333",
			"/ip4/1.2.3.4/tcp/33334",
			"/ip4/1.2.3.4/tcp/33335",
			"/ip4/1.2.3.4/udp/4002/quic-v1",
		)
		clean := makeAddrList(
			"/ip4/1.2.3.4/tcp/4001",
			"/ip4/1.2.3.4/udp/4002/quic-v1",
		)
		require.ElementsMatch(t, clean, cleanupAddressSet(addrs), "cleaned up set doesn't match expected")
	})

	t.Run("with non-standard port", func(t *testing.T) {
		addrs := makeAddrList(
			"/ip4/127.0.0.1/tcp/12345",
			"/ip4/1.2.3.4/tcp/12345",
			"/ip4/1.2.3.4/tcp/33333",
			"/ip4/1.2.3.4/tcp/33334",
			"/ip4/1.2.3.4/tcp/33335",
		)
		clean := makeAddrList(
			"/ip4/1.2.3.4/tcp/12345",
		)
		require.ElementsMatch(t, clean, cleanupAddressSet(addrs), "cleaned up set doesn't match expected")
	})

	t.Run("with a clean address set", func(t *testing.T) {
		// test with a squeaky clean address set
		addrs := makeAddrList(
			"/ip4/1.2.3.4/tcp/4001",
			"/ip4/1.2.3.4/udp/4001/quic-v1",
		)
		require.ElementsMatch(t, addrs, cleanupAddressSet(addrs), "cleaned up set doesn't match expected")
	})
}

func makeAddrList(strs ...string) []ma.Multiaddr {
	result := make([]ma.Multiaddr, 0, len(strs))
	for _, s := range strs {
		result = append(result, ma.StringCast(s))
	}
	return result
}
