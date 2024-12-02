package libp2p

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
)

func TestGetPeerID(t *testing.T) {
	var id peer.ID
	host, err := New(
		WithFxOption(fx.Populate(&id)),
	)
	require.NoError(t, err)
	defer host.Close()

	require.Equal(t, host.ID(), id)

}

func TestGetEventBus(t *testing.T) {
	var eb event.Bus
	host, err := New(
		NoTransports,
		WithFxOption(fx.Populate(&eb)),
	)
	require.NoError(t, err)
	defer host.Close()

	require.NotNil(t, eb)
}

func TestGetHost(t *testing.T) {
	var h host.Host
	host, err := New(
		NoTransports,
		WithFxOption(fx.Populate(&h)),
	)
	require.NoError(t, err)
	defer host.Close()

	require.NotNil(t, h)
}

func TestGetIDService(t *testing.T) {
	var id identify.IDService
	host, err := New(
		NoTransports,
		WithFxOption(fx.Populate(&id)),
	)
	require.NoError(t, err)
	defer host.Close()

	require.NotNil(t, id)
}
