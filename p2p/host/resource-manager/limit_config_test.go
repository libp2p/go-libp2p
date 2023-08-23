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

	require.Equal(t, int64(65536), cfg.system.Memory)
	require.Equal(t, defaults.system.Streams, cfg.system.Streams)
	require.Equal(t, defaults.system.StreamsInbound, cfg.system.StreamsInbound)
	require.Equal(t, defaults.system.StreamsOutbound, cfg.system.StreamsOutbound)
	require.Equal(t, 16, cfg.system.Conns)
	require.Equal(t, 8, cfg.system.ConnsInbound)
	require.Equal(t, 16, cfg.system.ConnsOutbound)
	require.Equal(t, 16, cfg.system.FD)

	require.Equal(t, defaults.transient, cfg.transient)
	require.Equal(t, int64(8765), cfg.serviceDefault.Memory)

	require.Contains(t, cfg.service, "A")
	require.Equal(t, withMemoryLimit(cfg.serviceDefault, 8192), cfg.service["A"])
	require.Contains(t, cfg.service, "B")
	require.Equal(t, cfg.serviceDefault, cfg.service["B"])
	require.Contains(t, cfg.service, "C")
	require.Equal(t, defaults.service["C"], cfg.service["C"])

	require.Equal(t, int64(4096), cfg.peerDefault.Memory)
	peerID, err := peer.Decode("12D3KooWPFH2Bx2tPfw6RLxN8k2wh47GRXgkt9yrAHU37zFwHWzS")
	require.NoError(t, err)
	require.Contains(t, cfg.peer, peerID)
	require.Equal(t, int64(4097), cfg.peer[peerID].Memory)
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

	require.Equal(t, int64(65536), cfg.system.Memory)
	require.Equal(t, defaults.system.Streams, cfg.system.Streams)
	require.Equal(t, defaults.system.StreamsInbound, cfg.system.StreamsInbound)
	require.Equal(t, defaults.system.StreamsOutbound, cfg.system.StreamsOutbound)
	require.Equal(t, 16, cfg.system.Conns)
	require.Equal(t, 8, cfg.system.ConnsInbound)
	require.Equal(t, 16, cfg.system.ConnsOutbound)
	require.Equal(t, 16, cfg.system.FD)

	require.Equal(t, defaults.transient, cfg.transient)
	require.Equal(t, int64(8765), cfg.serviceDefault.Memory)

	require.Contains(t, cfg.service, "A")
	require.Equal(t, withMemoryLimit(cfg.serviceDefault, 8192), cfg.service["A"])
	require.Contains(t, cfg.service, "B")
	require.Equal(t, cfg.serviceDefault, cfg.service["B"])
	require.Contains(t, cfg.service, "C")
	require.Equal(t, defaults.service["C"], cfg.service["C"])

	require.Equal(t, int64(4096), cfg.peerDefault.Memory)
	peerID, err := peer.Decode("12D3KooWPFH2Bx2tPfw6RLxN8k2wh47GRXgkt9yrAHU37zFwHWzS")
	require.NoError(t, err)
	require.Contains(t, cfg.peer, peerID)
	require.Equal(t, int64(4097), cfg.peer[peerID].Memory)

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

func TestDefaultsDontChange(t *testing.T) {
	concrete := DefaultLimits.Scale(8<<30, 16<<10) // 8GB, 16k fds
	jsonBytes, err := json.MarshalIndent(concrete.ToPartialLimitConfig(), "", "  ")
	require.NoError(t, err)

	// Uncomment to update the defaults file
	// err = os.WriteFile("limit_config_test_default.json", jsonBytes, 0644)
	// require.NoError(t, err)

	defaultsFromFile, err := os.ReadFile("limit_config_test_default.json")
	require.NoError(t, err)

	// replace crlf with lf because of windows
	defaultsFromFile = bytes.ReplaceAll(defaultsFromFile, []byte("\r\n"), []byte("\n"))
	jsonBytes = bytes.ReplaceAll(jsonBytes, []byte("\r\n"), []byte("\n"))

	require.Equal(t, string(defaultsFromFile), string(jsonBytes))
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
