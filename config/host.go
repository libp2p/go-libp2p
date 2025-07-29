package config

import (
	"context"

	basichost "github.com/libp2p/go-libp2p/p2p/host/basic"
	routed "github.com/libp2p/go-libp2p/p2p/host/routed"

	"go.uber.org/fx"
)

type closableBasicHost struct {
	*fx.App
	*basichost.BasicHost
}

func (h *closableBasicHost) Close() error {
	_ = h.App.Stop(context.Background())
	return h.BasicHost.Close()
}

type closableRoutedHost struct {
	// closableBasicHost is embedded here so that interface assertions on
	// BasicHost exported methods work correctly.
	closableBasicHost
	*routed.RoutedHost
}

func (h *closableRoutedHost) Close() error {
	_ = h.App.Stop(context.Background())
	// The routed host will close the basic host
	return h.RoutedHost.Close()
}
