package client

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	"github.com/stretchr/testify/require"
)

// connOverStream returns a Conn on a new stream between two hosts, and the
// other end of that stream.
func connOverStream(t *testing.T) (*Conn, network.Stream) {
	t.Helper()
	h1 := bhost.NewBlankHost(swarmt.GenSwarm(t))
	h2 := bhost.NewBlankHost(swarmt.GenSwarm(t))
	t.Cleanup(func() { h1.Close(); h2.Close() })

	remote := make(chan network.Stream, 1)
	h2.SetStreamHandler("/test", func(s network.Stream) { remote <- s })
	require.NoError(t, h1.Connect(context.Background(), peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}))
	s, err := h1.NewStream(context.Background(), h2.ID(), "/test")
	require.NoError(t, err)
	_, err = s.Write([]byte("hi")) // the stream is opened lazily
	require.NoError(t, err)

	c := &Conn{stream: s, client: &Client{host: h1, hopCount: make(map[peer.ID]int)}}
	c.tagHop()
	rs := <-remote
	_, err = io.ReadFull(rs, make([]byte, 2))
	require.NoError(t, err)
	return c, rs
}

// What was written before Close reaches the remote side, followed by EOF.
func TestConnCloseFlushes(t *testing.T) {
	c, rs := connOverStream(t)

	_, err := c.Write([]byte("last words"))
	require.NoError(t, err)
	require.NoError(t, c.Close())
	require.NoError(t, c.Close())

	b, err := io.ReadAll(rs)
	require.NoError(t, err, "stream was reset")
	require.Equal(t, "last words", string(b))

	_, err = c.Read(make([]byte, 1))
	require.ErrorIs(t, err, net.ErrClosed)
	_, err = c.Write([]byte("x"))
	require.Error(t, err)
}

// Close interrupts a Read, and resets the stream if the remote side doesn't
// close its end.
func TestConnCloseResetsAfterTimeout(t *testing.T) {
	old := closeTimeout
	closeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { closeTimeout = old })
	c, rs := connOverStream(t)

	readErr := make(chan error, 1)
	go func() {
		_, err := c.Read(make([]byte, 1))
		readErr <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the Read block
	require.NoError(t, c.Close())
	select {
	case err := <-readErr:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close didn't interrupt the Read")
	}

	// The remote side sees EOF and keeps its end open. Its writes fail once
	// the stream is reset.
	_, err := io.ReadAll(rs)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, err := rs.Write([]byte("x"))
		return err != nil
	}, 5*time.Second, 20*time.Millisecond, "stream wasn't reset after closeTimeout")
}
