//go:build !js

package websocket

import (
	"testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestListeningOnDNSAddr(t *testing.T) {
	ln, err := newListener(ma.StringCast("/dns/localhost/tcp/0/ws"), nil)
	require.NoError(t, err)
	addr := ln.Multiaddr()
	first, rest := ma.SplitFirst(addr)
	require.Equal(t, first.Protocol().Code, ma.P_DNS)
	require.Equal(t, first.Value(), "localhost")
	next, _ := ma.SplitFirst(rest)
	require.Equal(t, next.Protocol().Code, ma.P_TCP)
	require.NotEqual(t, next.Value(), "0")
}
