package rcmgr

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/stretchr/testify/require"
)

func withMemoryLimit(l BaseLimit, m int64) BaseLimit {
	l2 := l
	l2.Memory = m
	return l2
}

func TestLimitConfigParserBackwardsCompat(t *testing.T) {
	// Tests that we can parse the old limit config format.
	in, err := os.Open("limit_config_test.backwards-compat.json")
	require.NoError(t, err)
	defer in.Close()

	defaultScaledLimits := DefaultLimits
	defaultScaledLimits.AddServiceLimit("C", DefaultLimits.ServiceBaseLimit, BaseLimitIncrease{})
	defaultScaledLimits.AddProtocolPeerLimit("C", DefaultLimits.ServiceBaseLimit, BaseLimitIncrease{})
	defaults := defaultScaledLimits.AutoScale()
	cfg, err := readLimiterConfigFromJSON(in, defaults)
	require.NoError(t, err)

	require.Equal(t, int64(65536), cfg.System.Memory)
	require.Equal(t, defaults.System.Streams, cfg.System.Streams)
	require.Equal(t, defaults.System.StreamsInbound, cfg.System.StreamsInbound)
	require.Equal(t, defaults.System.StreamsOutbound, cfg.System.StreamsOutbound)
	require.Equal(t, 16, cfg.System.Conns)
	require.Equal(t, 8, cfg.System.ConnsInbound)
	require.Equal(t, 16, cfg.System.ConnsOutbound)
	require.Equal(t, 16, cfg.System.FD)

	require.Equal(t, defaults.Transient, cfg.Transient)
	require.Equal(t, int64(8765), cfg.ServiceDefault.Memory)

	require.Contains(t, cfg.Service, "A")
	require.Equal(t, withMemoryLimit(cfg.ServiceDefault, 8192), cfg.Service["A"])
	require.Contains(t, cfg.Service, "B")
	require.Equal(t, cfg.ServiceDefault, cfg.Service["B"])
	require.Contains(t, cfg.Service, "C")
	require.Equal(t, defaults.Service["C"], cfg.Service["C"])

	require.Equal(t, int64(4096), cfg.PeerDefault.Memory)
	peerID, err := peer.Decode("12D3KooWPFH2Bx2tPfw6RLxN8k2wh47GRXgkt9yrAHU37zFwHWzS")
	require.NoError(t, err)
	require.Contains(t, cfg.Peer, peerID)
	require.Equal(t, int64(4097), cfg.Peer[peerID].Memory)
}

func TestLimitConfigParser(t *testing.T) {
	in, err := os.Open("limit_config_test.json")
	require.NoError(t, err)
	defer in.Close()

	defaultScaledLimits := DefaultLimits
	defaultScaledLimits.AddServiceLimit("C", DefaultLimits.ServiceBaseLimit, BaseLimitIncrease{})
	defaultScaledLimits.AddProtocolPeerLimit("C", DefaultLimits.ServiceBaseLimit, BaseLimitIncrease{})
	defaults := defaultScaledLimits.AutoScale()
	cfg, err := readLimiterConfigFromJSON(in, defaults)
	require.NoError(t, err)

	require.Equal(t, int64(65536), cfg.System.Memory)
	require.Equal(t, defaults.System.Streams, cfg.System.Streams)
	require.Equal(t, defaults.System.StreamsInbound, cfg.System.StreamsInbound)
	require.Equal(t, defaults.System.StreamsOutbound, cfg.System.StreamsOutbound)
	require.Equal(t, 16, cfg.System.Conns)
	require.Equal(t, 8, cfg.System.ConnsInbound)
	require.Equal(t, 16, cfg.System.ConnsOutbound)
	require.Equal(t, 16, cfg.System.FD)

	require.Equal(t, defaults.Transient, cfg.Transient)
	require.Equal(t, int64(8765), cfg.ServiceDefault.Memory)

	require.Contains(t, cfg.Service, "A")
	require.Equal(t, withMemoryLimit(cfg.ServiceDefault, 8192), cfg.Service["A"])
	require.Contains(t, cfg.Service, "B")
	require.Equal(t, cfg.ServiceDefault, cfg.Service["B"])
	require.Contains(t, cfg.Service, "C")
	require.Equal(t, defaults.Service["C"], cfg.Service["C"])

	require.Equal(t, int64(4096), cfg.PeerDefault.Memory)
	peerID, err := peer.Decode("12D3KooWPFH2Bx2tPfw6RLxN8k2wh47GRXgkt9yrAHU37zFwHWzS")
	require.NoError(t, err)
	require.Contains(t, cfg.Peer, peerID)
	require.Equal(t, int64(4097), cfg.Peer[peerID].Memory)

	// Roundtrip
	limitConfig := cfg.ToPartialLimitConfig()
	jsonBytes, err := json.Marshal(&limitConfig)
	require.NoError(t, err)
	cfgAfterRoundTrip, err := readLimiterConfigFromJSON(bytes.NewReader(jsonBytes), defaults)
	require.NoError(t, err)
	require.Equal(t, limitConfig, cfgAfterRoundTrip.ToPartialLimitConfig())
}

func TestLimitConfigRoundTrip(t *testing.T) {
	// Tests that we can roundtrip a PartialLimitConfig to a ConcreteLimitConfig and back.
	in, err := os.Open("limit_config_test.json")
	require.NoError(t, err)
	defer in.Close()

	defaults := DefaultLimits
	defaults.AddServiceLimit("C", DefaultLimits.ServiceBaseLimit, BaseLimitIncrease{})
	defaults.AddProtocolPeerLimit("C", DefaultLimits.ServiceBaseLimit, BaseLimitIncrease{})
	concreteCfg, err := readLimiterConfigFromJSON(in, defaults.AutoScale())
	require.NoError(t, err)

	// Roundtrip
	limitConfig := concreteCfg.ToPartialLimitConfig()
	// Using InfiniteLimits because it's different then the defaults used above.
	// If anything was marked "default" in the round trip, it would show up as a
	// difference here.
	concreteCfgRT := limitConfig.Build(InfiniteLimits)
	require.Equal(t, concreteCfg, concreteCfgRT)
}

func TestReadmeLimitConfigSerialization(t *testing.T) {
	noisyNeighbor, _ := peer.Decode("QmVvtzcZgCkMnSFf2dnrBPXrWuNFWNM9J3MpZQCvWPuVZf")
	cfg := PartialLimitConfig{
		System: ResourceLimits{
			// Allow unlimited outbound streams
			StreamsOutbound: Unlimited,
		},
		Peer: map[peer.ID]ResourceLimits{
			noisyNeighbor: {
				// No inbound connections from this peer
				ConnsInbound: BlockAllLimit,
				// But let me open connections to them
				Conns:         DefaultLimit,
				ConnsOutbound: DefaultLimit,
				// No inbound streams from this peer
				StreamsInbound: BlockAllLimit,
				// And let me open unlimited (by me) outbound streams (the peer may have their own limits on me)
				StreamsOutbound: Unlimited,
			},
		},
	}
	jsonBytes, err := json.Marshal(&cfg)
	require.NoError(t, err)
	require.Equal(t, `{"Peer":{"QmVvtzcZgCkMnSFf2dnrBPXrWuNFWNM9J3MpZQCvWPuVZf":{"StreamsInbound":"blockAll","StreamsOutbound":"unlimited","ConnsInbound":"blockAll"}},"System":{"StreamsOutbound":"unlimited"}}`, string(jsonBytes))
}
