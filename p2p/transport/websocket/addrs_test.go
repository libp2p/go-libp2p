package websocket

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	ma "github.com/multiformats/go-multiaddr"
)

func TestMultiaddrParsing(t *testing.T) {
	addr, err := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/5555/ws")
	require.NoError(t, err)

	wsaddr, err := parseMultiaddr(addr)
	require.NoError(t, err)
	require.Equal(t, "ws://127.0.0.1:5555", wsaddr.String())
}

type httpAddr struct {
	*url.URL
}

func (addr *httpAddr) Network() string {
	return "http"
}

func TestParseWebsocketNetAddr(t *testing.T) {
	notWs := &httpAddr{&url.URL{Host: "http://127.0.0.1:1234"}}
	_, err := ParseWebsocketNetAddr(notWs)
	require.ErrorIs(t, err, errNotWebSocketAddress)

	wsAddr := NewAddrWithScheme("127.0.0.1:5555", false)
	parsed, err := ParseWebsocketNetAddr(wsAddr)
	require.NoError(t, err)
	require.Equal(t, "/ip4/127.0.0.1/tcp/5555/ws", parsed.String())
}

func TestConvertWebsocketMultiaddrToNetAddr(t *testing.T) {
	addr, err := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/5555/ws")
	require.NoError(t, err)

	wsaddr, err := ConvertWebsocketMultiaddrToNetAddr(addr)
	require.NoError(t, err)
	require.Equal(t, "ws://127.0.0.1:5555", wsaddr.String())
	require.Equal(t, "websocket", wsaddr.Network())
}

func TestListeningOnDNSAddr(t *testing.T) {
	ln, err := newListener(ma.StringCast("/dns/localhost/tcp/0/ws"), nil)
	require.NoError(t, err)
	addr := ln.Multiaddr()
	first, rest := ma.SplitFirst(addr)
	require.Equal(t, ma.P_DNS, first.Protocol().Code)
	require.Equal(t, "localhost", first.Value())
	next, _ := ma.SplitFirst(rest)
	require.Equal(t, ma.P_TCP, next.Protocol().Code)
	require.NotEqual(t, 0, next.Value())
}
