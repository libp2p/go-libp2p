package nat

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/huin/goupnp"
	"github.com/stretchr/testify/require"
)

type upnpMapping struct {
	protocol     string
	externalPort uint16
}

// fakeUPnPClient records the port mappings that exist on the "router".
type fakeUPnPClient struct {
	mappings map[upnpMapping]uint16 // -> internal port
}

func (c *fakeUPnPClient) GetExternalIPAddress() (string, error) { return "1.2.3.4", nil }

func (c *fakeUPnPClient) AddPortMappingCtx(_ context.Context, _ string, externalPort uint16, protocol string, internalPort uint16, _ string, _ bool, _ string, _ uint32) error {
	c.mappings[upnpMapping{protocol, externalPort}] = internalPort
	return nil
}

func (c *fakeUPnPClient) DeletePortMappingCtx(_ context.Context, _ string, externalPort uint16, protocol string) error {
	delete(c.mappings, upnpMapping{protocol, externalPort})
	return nil
}

// A TCP and a UDP mapping for the same internal port (the default libp2p
// setup: TCP and QUIC on the same port) are two separate mappings on the
// router. Deleting both must remove both.
func TestUPnPDeleteMappingSamePortBothProtocols(t *testing.T) {
	client := &fakeUPnPClient{mappings: make(map[upnpMapping]uint16)}
	n := &upnp_NAT{
		c:          client,
		ports:      make(map[portMapping]int),
		typ:        "UPNP (test)",
		rootDevice: &goupnp.RootDevice{URLBase: url.URL{Host: "127.0.0.1:1900"}},
	}
	ctx := context.Background()

	_, err := n.AddPortMapping(ctx, "tcp", 4001, "libp2p", time.Minute)
	require.NoError(t, err)
	_, err = n.AddPortMapping(ctx, "udp", 4001, "libp2p", time.Minute)
	require.NoError(t, err)
	require.Len(t, client.mappings, 2)

	require.NoError(t, n.DeletePortMapping(ctx, "tcp", 4001))
	require.NoError(t, n.DeletePortMapping(ctx, "udp", 4001))
	require.Empty(t, client.mappings, "mappings left behind on the router")
}
