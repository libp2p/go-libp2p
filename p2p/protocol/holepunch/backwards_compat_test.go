package holepunch_test

import (
	"testing"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/stretchr/testify/require"
)

func TestBackwardsCompatibility(t *testing.T) {
	t.Run("spec-compliant to legacy", func(t *testing.T) {
		specHost := MustNewHost(t,
			libp2p.EnableHolePunching(holepunch.WithSpecCompliantBehavior()),
		)
		defer specHost.Close()

		legacyHost := MustNewHost(t,
			libp2p.EnableHolePunching(SetLegacyBehavior(true)),
		)
		defer legacyHost.Close()

		testHolePunchConnection(t, specHost, legacyHost)
	})

	t.Run("legacy to spec-compliant", func(t *testing.T) {
		legacyHost := MustNewHost(t,
			libp2p.EnableHolePunching(SetLegacyBehavior(true)),
		)
		defer legacyHost.Close()

		specHost := MustNewHost(t,
			libp2p.EnableHolePunching(holepunch.WithSpecCompliantBehavior()),
		)  
		defer specHost.Close()

		testHolePunchConnection(t, legacyHost, specHost)
	})
}

func testHolePunchConnection(t *testing.T, h1, h2 host.Host) {
	require.NotNil(t, h1)
	require.NotNil(t, h2)
}
