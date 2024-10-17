package libp2p

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
)

func TestGetPeerID(t *testing.T) {
	var id peer.ID
	host, err := New(
		WithFxOption(fx.Invoke(
			func(myid peer.ID) {
				id = myid
			})))
	require.NoError(t, err)
	defer host.Close()

	require.Equal(t, host.ID(), id)

}

func TestGetEventBus(t *testing.T) {
	var eb event.Bus
	host, err := New(
		NoTransports,
		WithFxOption(
			fx.Invoke(
				func(e event.Bus) {
					eb = e
				},
			),
		),
	)
	require.NoError(t, err)
	defer host.Close()

	require.NotNil(t, eb)
}

func TestGetHost(t *testing.T) {
	var h host.Host
	host, err := New(
		NoTransports,
		WithFxOption(
			fx.Invoke(
				func(hostFromInvoke host.Host) {
					h = hostFromInvoke
				},
			),
		),
	)
	require.NoError(t, err)
	defer host.Close()

	require.NotNil(t, h)
}
