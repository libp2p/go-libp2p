//lint:file-ignore U1000 Ignore all unused code, we're not running any tests.
package relay_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	. "github.com/libp2p/go-libp2p/p2p/protocol/internal/circuitv1-deprecated"
	pb "github.com/libp2p/go-libp2p/p2p/protocol/internal/circuitv1-deprecated/pb"

	"github.com/libp2p/go-libp2p-core/host"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

/* TODO: add tests
- simple A -[R]-> B
- A tries to relay through R, R doesnt support relay
- A tries to relay through R to B, B doesnt support relay
- A sends too long multiaddr
- R drops stream mid-message
- A relays through R, R has no connection to B
*/

func getNetHosts(t *testing.T, n int) []host.Host {
	var out []host.Host

	for i := 0; i < n; i++ {
		netw := swarmt.GenSwarm(t)
		h := bhost.NewBlankHost(netw)
		out = append(out, h)
	}

	return out
}

func newTestRelay(t *testing.T, host host.Host, opts ...RelayOpt) *Relay {
	r, err := NewRelay(host, swarmt.GenUpgrader(t, host.Network().(*swarm.Swarm)), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func connect(t *testing.T, a, b host.Host) {
	pinfo := a.Peerstore().PeerInfo(a.ID())
	err := b.Connect(context.Background(), pinfo)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBasicRelay(t *testing.T) {
	t.Skip("This package is legacy code we only keep around for testing purposes.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts := getNetHosts(t, 3)

	connect(t, hosts[0], hosts[1])
	connect(t, hosts[1], hosts[2])

	time.Sleep(10 * time.Millisecond)

	r1 := newTestRelay(t, hosts[0])

	newTestRelay(t, hosts[1], OptHop)

	r3 := newTestRelay(t, hosts[2])

	var (
		conn1, conn2 net.Conn
		done         = make(chan struct{})
	)

	defer func() {
		<-done
		if conn1 != nil {
			conn1.Close()
		}
		if conn2 != nil {
			conn2.Close()
		}
	}()

	msg := []byte("relay works!")
	go func() {
		defer close(done)
		list := r3.Listener()

		var err error
		conn1, err = list.Accept()
		if err != nil {
			t.Error(err)
			return
		}

		_, err = conn1.Write(msg)
		if err != nil {
			t.Error(err)
			return
		}
	}()

	rinfo := hosts[1].Peerstore().PeerInfo(hosts[1].ID())
	dinfo := hosts[2].Peerstore().PeerInfo(hosts[2].ID())

	rctx, rcancel := context.WithTimeout(ctx, time.Second)
	defer rcancel()

	var err error
	conn2, err = r1.DialPeer(rctx, rinfo, dinfo)
	if err != nil {
		t.Fatal(err)
	}

	result := make([]byte, len(msg))
	_, err = io.ReadFull(conn2, result)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(result, msg) {
		t.Fatal("message was incorrect:", string(result))
	}
}

func TestRelayReset(t *testing.T) {
	t.Skip("This package is legacy code we only keep around for testing purposes.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts := getNetHosts(t, 3)

	connect(t, hosts[0], hosts[1])
	connect(t, hosts[1], hosts[2])

	time.Sleep(10 * time.Millisecond)

	r1 := newTestRelay(t, hosts[0])

	newTestRelay(t, hosts[1], OptHop)

	r3 := newTestRelay(t, hosts[2])

	ready := make(chan struct{})

	msg := []byte("relay works!")
	go func() {
		list := r3.Listener()

		con, err := list.Accept()
		if err != nil {
			t.Error(err)
			return
		}

		<-ready

		_, err = con.Write(msg)
		if err != nil {
			t.Error(err)
			return
		}

		hosts[2].Network().ClosePeer(hosts[1].ID())
	}()

	rinfo := hosts[1].Peerstore().PeerInfo(hosts[1].ID())
	dinfo := hosts[2].Peerstore().PeerInfo(hosts[2].ID())

	rctx, rcancel := context.WithTimeout(ctx, time.Second)
	defer rcancel()

	con, err := r1.DialPeer(rctx, rinfo, dinfo)
	if err != nil {
		t.Fatal(err)
	}

	close(ready)

	_, err = io.ReadAll(con)
	if err == nil {
		t.Fatal("expected error for reset relayed connection")
	}
}

func TestBasicRelayDial(t *testing.T) {
	t.Skip("This package is legacy code we only keep around for testing purposes.")

	ctx, cancel := context.WithCancel(context.Background())

	hosts := getNetHosts(t, 3)

	connect(t, hosts[0], hosts[1])
	connect(t, hosts[1], hosts[2])

	time.Sleep(10 * time.Millisecond)

	r1 := newTestRelay(t, hosts[0])

	_ = newTestRelay(t, hosts[1], OptHop)
	r3 := newTestRelay(t, hosts[2])

	var (
		conn1, conn2 net.Conn
		done         = make(chan struct{})
	)

	defer func() {
		cancel()
		<-done
		if conn1 != nil {
			conn1.Close()
		}
		if conn2 != nil {
			conn2.Close()
		}
	}()

	msg := []byte("relay works!")
	go func() {
		defer close(done)
		list := r3.Listener()

		var err error
		conn1, err = list.Accept()
		if err != nil {
			t.Error(err)
			return
		}

		_, err = conn1.Write(msg)
		if err != nil {
			t.Error(err)
			return
		}
	}()

	addr := ma.StringCast(fmt.Sprintf("/ipfs/%s/p2p-circuit", hosts[1].ID().Pretty()))

	rctx, rcancel := context.WithTimeout(ctx, time.Second)
	defer rcancel()

	var err error
	conn2, err = r1.Dial(rctx, addr, hosts[2].ID())
	if err != nil {
		t.Fatal(err)
	}

	data := make([]byte, len(msg))
	_, err = io.ReadFull(conn2, data)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(data, msg) {
		t.Fatal("message was incorrect:", string(data))
	}
}

func TestUnspecificRelayDialFails(t *testing.T) {
	t.Skip("This package is legacy code we only keep around for testing purposes.")

	hosts := getNetHosts(t, 3)

	r1 := newTestRelay(t, hosts[0])
	newTestRelay(t, hosts[1], OptHop)
	r3 := newTestRelay(t, hosts[2])

	connect(t, hosts[0], hosts[1])
	connect(t, hosts[1], hosts[2])

	time.Sleep(100 * time.Millisecond)

	go func() {
		if _, err := r3.Listener().Accept(); err == nil {
			t.Error("should not have received relay connection")
		}
	}()

	addr := ma.StringCast("/p2p-circuit")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r1.Dial(ctx, addr, hosts[2].ID()); err == nil {
		t.Fatal("expected dial with unspecified relay address to fail, even if we're connected to a relay")
	}
}

func TestRelayThroughNonHop(t *testing.T) {
	t.Skip("This package is legacy code we only keep around for testing purposes.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts := getNetHosts(t, 3)

	connect(t, hosts[0], hosts[1])
	connect(t, hosts[1], hosts[2])

	time.Sleep(10 * time.Millisecond)

	r1 := newTestRelay(t, hosts[0])

	newTestRelay(t, hosts[1])

	newTestRelay(t, hosts[2])

	rinfo := hosts[1].Peerstore().PeerInfo(hosts[1].ID())
	dinfo := hosts[2].Peerstore().PeerInfo(hosts[2].ID())

	rctx, rcancel := context.WithTimeout(ctx, time.Second)
	defer rcancel()

	_, err := r1.DialPeer(rctx, rinfo, dinfo)
	if err == nil {
		t.Fatal("expected error")
	}

	rerr, ok := err.(RelayError)
	if !ok {
		t.Fatalf("expected RelayError: %#v", err)
	}

	if rerr.Code != pb.CircuitRelay_HOP_CANT_SPEAK_RELAY {
		t.Fatal("expected 'HOP_CANT_SPEAK_RELAY' error")
	}
}

func TestRelayNoDestConnection(t *testing.T) {
	t.Skip("This package is legacy code we only keep around for testing purposes.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts := getNetHosts(t, 3)

	connect(t, hosts[0], hosts[1])

	time.Sleep(10 * time.Millisecond)

	r1 := newTestRelay(t, hosts[0])

	newTestRelay(t, hosts[1], OptHop)

	rinfo := hosts[1].Peerstore().PeerInfo(hosts[1].ID())
	dinfo := hosts[2].Peerstore().PeerInfo(hosts[2].ID())

	rctx, rcancel := context.WithTimeout(ctx, time.Second)
	defer rcancel()

	_, err := r1.DialPeer(rctx, rinfo, dinfo)
	if err == nil {
		t.Fatal("expected error")
	}

	rerr, ok := err.(RelayError)
	if !ok {
		t.Fatalf("expected RelayError: %#v", err)
	}

	if rerr.Code != pb.CircuitRelay_HOP_NO_CONN_TO_DST {
		t.Fatal("expected 'HOP_NO_CONN_TO_DST' error")
	}
}

func TestActiveRelay(t *testing.T) {
	t.Skip("This package is legacy code we only keep around for testing purposes.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts := getNetHosts(t, 3)

	connect(t, hosts[0], hosts[1])

	time.Sleep(10 * time.Millisecond)

	r1 := newTestRelay(t, hosts[0])
	newTestRelay(t, hosts[1], OptHop, OptActive)
	r3 := newTestRelay(t, hosts[2])

	connChan := make(chan manet.Conn)

	msg := []byte("relay works!")
	go func() {
		defer close(connChan)
		list := r3.Listener()

		conn1, err := list.Accept()
		if err != nil {
			t.Error(err)
			return
		}

		if _, err := conn1.Write(msg); err != nil {
			t.Error(err)
			return
		}
		connChan <- conn1
	}()

	rinfo := hosts[1].Peerstore().PeerInfo(hosts[1].ID())
	dinfo := hosts[2].Peerstore().PeerInfo(hosts[2].ID())

	rctx, rcancel := context.WithTimeout(ctx, time.Second)
	defer rcancel()

	conn2, err := r1.DialPeer(rctx, rinfo, dinfo)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()

	data := make([]byte, len(msg))
	_, err = io.ReadFull(conn2, data)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(data, msg) {
		t.Fatal("message was incorrect:", string(data))
	}
	conn1, ok := <-connChan
	if !ok {
		t.Fatal("listener didn't accept a connection")
	}
	conn1.Close()
}

func TestRelayCanHop(t *testing.T) {
	t.Skip("This package is legacy code we only keep around for testing purposes.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts := getNetHosts(t, 2)

	connect(t, hosts[0], hosts[1])

	time.Sleep(10 * time.Millisecond)

	r1 := newTestRelay(t, hosts[0])

	newTestRelay(t, hosts[1], OptHop)

	canhop, err := r1.CanHop(ctx, hosts[1].ID())
	if err != nil {
		t.Fatal(err)
	}

	if !canhop {
		t.Fatal("Relay can't hop")
	}
}
