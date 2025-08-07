package main

import (
	"context"
	"log"

	"github.com/libp2p/go-libp2p/core/host"

	"go.uber.org/fx"

	libp2pfx "github.com/libp2p/go-libp2p/fx"
)

func main() {
	factory := func() host.Host {
		var h host.Host
		app := fx.New(
			fx.NopLogger,
			libp2pfx.BlankHost(),
			libp2pfx.SwarmNetwork(),
			libp2pfx.RandomPeerID(),
			libp2pfx.EventBus(),
			libp2pfx.InMemoryPeerstore(),
			libp2pfx.MultistreamMuxer,
			libp2pfx.NullConnectionGater,
			libp2pfx.NullResourceManager,
			libp2pfx.NullConnManager,
			fx.Supply(libp2pfx.MetricsConfig{Disable: true}),
			fx.Populate(&h),
			libp2pfx.ListenAddrs(),
		)

		app.Start(context.Background())
		return h
	}

	h := factory()

	defer h.Close()

	log.Printf("Hello World, my second hosts ID is %s\n", h.ID())
}
