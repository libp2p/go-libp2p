//go:build js

package websocket

import (
	"bufio"
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestInBrowser(t *testing.T) {
	id, mux := newSecureMuxer(t)
	u, err := tptu.New(mux, []tptu.StreamMuxer{{ID: "/yamux", Muxer: yamux.DefaultTransport}}, nil, nil, nil)
	require.NoError(t, err)
	tpt, err := New(u, nil)
	require.NoError(t, err)
	addr, err := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/5555/ws")
	if err != nil {
		t.Fatal("could not parse multiaddress:" + err.Error())
	}
	conn, err := tpt.Dial(context.Background(), addr, id)
	if err != nil {
		t.Fatal("could not dial server:" + err.Error())
	}
	defer conn.Close()

	stream, err := conn.AcceptStream()
	if err != nil {
		t.Fatal("could not accept stream:" + err.Error())
	}
	defer stream.Close()

	buf := bufio.NewReader(stream)
	msg, err := buf.ReadString('\n')
	if err != nil {
		t.Fatal("could not read ping message:" + err.Error())
	}
	expected := "ping\n"
	if msg != expected {
		t.Fatalf("Received wrong message. Expected %q but got %q", expected, msg)
	}

	_, err = stream.Write([]byte("pong\n"))
	if err != nil {
		t.Fatal("could not write pong message:" + err.Error())
	}

	// TODO(albrow): This hack is necessary in order to give the reader time to
	// finish. As soon as this test function returns, the browser window is
	// closed, which means there is no time for the other end of the connection to
	// read the "pong" message. We should find some way to remove this hack if
	// possible.
	time.Sleep(1 * time.Second)
}
