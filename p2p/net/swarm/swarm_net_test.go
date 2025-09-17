package swarm_test

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	. "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	"github.com/stretchr/testify/require"
)

// TestConnectednessCorrect starts a few networks, connects a few
// and tests Connectedness value is correct.
func TestConnectednessCorrect(t *testing.T) {
	nets := make([]network.Network, 4)
	for i := 0; i < 4; i++ {
		nets[i] = GenSwarm(t)
	}

	// connect 0-1, 0-2, 0-3, 1-2, 2-3

	dial := func(a, b network.Network) {
		DivulgeAddresses(b, a)
		if _, err := a.DialPeer(context.Background(), b.LocalPeer()); err != nil {
			t.Fatalf("Failed to dial: %s", err)
		}
	}

	dial(nets[0], nets[1])
	dial(nets[0], nets[3])
	dial(nets[1], nets[2])
	dial(nets[3], nets[2])

	// The notifications for new connections get sent out asynchronously.
	// There is the potential for a race condition here, so we sleep to ensure
	// that they have been received.
	time.Sleep(time.Millisecond * 100)

	// test those connected show up correctly

	// test connected
	expectConnectedness(t, nets[0], nets[1], network.Connected)
	expectConnectedness(t, nets[0], nets[3], network.Connected)
	expectConnectedness(t, nets[1], nets[2], network.Connected)
	expectConnectedness(t, nets[3], nets[2], network.Connected)

	// test not connected
	expectConnectedness(t, nets[0], nets[2], network.NotConnected)
	expectConnectedness(t, nets[1], nets[3], network.NotConnected)

	require.Len(t, nets[0].Peers(), 2, "expected net 0 to have two peers")
	require.Len(t, nets[2].Peers(), 2, "expected net 2 to have two peers")
	require.NotZerof(t, nets[1].ConnsToPeer(nets[3].LocalPeer()), "net 1 should have no connections to net 3")
	require.NoError(t, nets[2].ClosePeer(nets[1].LocalPeer()))

	time.Sleep(time.Millisecond * 50)
	expectConnectedness(t, nets[2], nets[1], network.NotConnected)

	for _, n := range nets {
		n.Close()
	}
}

func expectConnectedness(t *testing.T, a, b network.Network, expected network.Connectedness) {
	es := "%s is connected to %s, but Connectedness incorrect. %s %s %s"
	atob := a.Connectedness(b.LocalPeer())
	btoa := b.Connectedness(a.LocalPeer())
	if atob != expected {
		t.Errorf(es, a, b, printConns(a), printConns(b), atob)
	}

	// test symmetric case
	if btoa != expected {
		t.Errorf(es, b, a, printConns(b), printConns(a), btoa)
	}
}

func printConns(n network.Network) string {
	s := fmt.Sprintf("Connections in %s:\n", n)
	for _, c := range n.Conns() {
		s = s + fmt.Sprintf("- %s\n", c)
	}
	return s
}

func TestNetworkOpenStream(t *testing.T) {
	ctx := t.Context()

	testString := "hello ipfs"

	nets := make([]network.Network, 4)
	for i := 0; i < 4; i++ {
		nets[i] = GenSwarm(t)
	}

	dial := func(a, b network.Network) {
		DivulgeAddresses(b, a)
		if _, err := a.DialPeer(ctx, b.LocalPeer()); err != nil {
			t.Fatalf("Failed to dial: %s", err)
		}
	}

	dial(nets[0], nets[1])
	dial(nets[0], nets[3])
	dial(nets[1], nets[2])

	done := make(chan bool)
	nets[1].SetStreamHandler(func(s network.Stream) {
		defer close(done)
		defer s.Close()

		buf, err := io.ReadAll(s)
		if err != nil {
			t.Error(err)
			return
		}
		if string(buf) != testString {
			t.Error("got wrong message")
		}
	})

	s, err := nets[0].NewStream(ctx, nets[1].LocalPeer())
	if err != nil {
		t.Fatal(err)
	}

	var numStreams int
	for _, conn := range nets[0].ConnsToPeer(nets[1].LocalPeer()) {
		numStreams += conn.Stat().NumStreams
	}

	if numStreams != 1 {
		t.Fatal("should only have one stream there")
	}

	n, err := s.Write([]byte(testString))
	if err != nil {
		t.Fatal(err)
	} else if n != len(testString) {
		t.Errorf("expected to write %d bytes, wrote %d", len(testString), n)
	}

	err = s.Close()
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(time.Millisecond * 100):
		t.Fatal("timed out waiting on stream")
	}

	_, err = nets[1].NewStream(ctx, nets[3].LocalPeer())
	if err == nil {
		t.Fatal("expected stream open 1->3 to fail")
	}
}
