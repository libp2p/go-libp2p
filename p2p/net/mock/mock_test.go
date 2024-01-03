package mocknet

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/libp2p/go-libp2p-testing/ci"
	tetc "github.com/libp2p/go-libp2p-testing/etc"
	"github.com/libp2p/go-libp2p-testing/race"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

var lastPort = struct {
	port int
	sync.Mutex
}{}

// randLocalTCPAddress returns a random multiaddr. it suppresses errors
// for nice composability-- do check the address isn't nil.
//
// NOTE: for real network tests, use a :0 address, so the kernel
// assigns an unused TCP port. otherwise you may get clashes.
func randLocalTCPAddress() ma.Multiaddr {
	// chances are it will work out, but it **might** fail if the port is in use
	// most ports above 10000 aren't in use by long running processes, so yay.
	// (maybe there should be a range of "loopback" ports that are guaranteed
	// to be open for the process, but naturally can only talk to self.)

	lastPort.Lock()
	if lastPort.port == 0 {
		lastPort.port = 10000 + tetc.SeededRand.Intn(50000)
	}
	port := lastPort.port
	lastPort.port++
	lastPort.Unlock()

	addr := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port)
	maddr, _ := ma.NewMultiaddr(addr)
	return maddr
}

func TestNetworkSetup(t *testing.T) {
	ctx := context.Background()
	priv1, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	priv2, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	priv3, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	mn := New()
	defer mn.Close()

	// add peers to mock net

	a1 := randLocalTCPAddress()
	a2 := randLocalTCPAddress()
	a3 := randLocalTCPAddress()

	h1, err := mn.AddPeer(priv1, a1)
	if err != nil {
		t.Fatal(err)
	}
	p1 := h1.ID()

	h2, err := mn.AddPeer(priv2, a2)
	if err != nil {
		t.Fatal(err)
	}
	p2 := h2.ID()

	h3, err := mn.AddPeer(priv3, a3)
	if err != nil {
		t.Fatal(err)
	}
	p3 := h3.ID()

	// check peers and net
	if mn.Host(p1) != h1 {
		t.Error("host for p1.ID != h1")
	}
	if mn.Host(p2) != h2 {
		t.Error("host for p2.ID != h2")
	}
	if mn.Host(p3) != h3 {
		t.Error("host for p3.ID != h3")
	}

	n1 := h1.Network()
	if mn.Net(p1) != n1 {
		t.Error("net for p1.ID != n1")
	}
	n2 := h2.Network()
	if mn.Net(p2) != n2 {
		t.Error("net for p2.ID != n1")
	}
	n3 := h3.Network()
	if mn.Net(p3) != n3 {
		t.Error("net for p3.ID != n1")
	}

	// link p1<-->p2, p1<-->p1, p2<-->p3, p3<-->p2

	l12, err := mn.LinkPeers(p1, p2)
	if err != nil {
		t.Fatal(err)
	}
	if !(l12.Networks()[0] == n1 && l12.Networks()[1] == n2) &&
		!(l12.Networks()[0] == n2 && l12.Networks()[1] == n1) {
		t.Error("l12 networks incorrect")
	}

	l11, err := mn.LinkPeers(p1, p1)
	if err != nil {
		t.Fatal(err)
	}
	if !(l11.Networks()[0] == n1 && l11.Networks()[1] == n1) {
		t.Error("l11 networks incorrect")
	}

	l23, err := mn.LinkPeers(p2, p3)
	if err != nil {
		t.Fatal(err)
	}
	if !(l23.Networks()[0] == n2 && l23.Networks()[1] == n3) &&
		!(l23.Networks()[0] == n3 && l23.Networks()[1] == n2) {
		t.Error("l23 networks incorrect")
	}

	l32, err := mn.LinkPeers(p3, p2)
	if err != nil {
		t.Fatal(err)
	}
	if !(l32.Networks()[0] == n2 && l32.Networks()[1] == n3) &&
		!(l32.Networks()[0] == n3 && l32.Networks()[1] == n2) {
		t.Error("l32 networks incorrect")
	}

	// check things

	links12 := mn.LinksBetweenPeers(p1, p2)
	if len(links12) != 1 {
		t.Errorf("should be 1 link bt. p1 and p2 (found %d)", len(links12))
	}
	if links12[0] != l12 {
		t.Error("links 1-2 should be l12.")
	}

	links11 := mn.LinksBetweenPeers(p1, p1)
	if len(links11) != 1 {
		t.Errorf("should be 1 link bt. p1 and p1 (found %d)", len(links11))
	}
	if links11[0] != l11 {
		t.Error("links 1-1 should be l11.")
	}

	links23 := mn.LinksBetweenPeers(p2, p3)
	if len(links23) != 2 {
		t.Errorf("should be 2 link bt. p2 and p3 (found %d)", len(links23))
	}
	if !((links23[0] == l23 && links23[1] == l32) ||
		(links23[0] == l32 && links23[1] == l23)) {
		t.Error("links 2-3 should be l23 and l32.")
	}

	// unlinking

	if err := mn.UnlinkPeers(p2, p1); err != nil {
		t.Error(err)
	}

	// check only one link affected:

	links12 = mn.LinksBetweenPeers(p1, p2)
	if len(links12) != 0 {
		t.Error("should be 0 now...", len(links12))
	}

	links11 = mn.LinksBetweenPeers(p1, p1)
	if len(links11) != 1 {
		t.Errorf("should be 1 link bt. p1 and p1 (found %d)", len(links11))
	}
	if links11[0] != l11 {
		t.Error("links 1-1 should be l11.")
	}

	links23 = mn.LinksBetweenPeers(p2, p3)
	if len(links23) != 2 {
		t.Errorf("should be 2 link bt. p2 and p3 (found %d)", len(links23))
	}
	if !((links23[0] == l23 && links23[1] == l32) ||
		(links23[0] == l32 && links23[1] == l23)) {
		t.Error("links 2-3 should be l23 and l32.")
	}

	// check connecting

	// first, no conns
	if len(n2.Conns()) > 0 || len(n3.Conns()) > 0 {
		t.Errorf("should have 0 conn. Got: (%d, %d)", len(n2.Conns()), len(n3.Conns()))
	}

	// connect p2->p3
	if _, err := n2.DialPeer(ctx, p3); err != nil {
		t.Error(err)
	}

	// should immediately have a conn on peer 1
	if len(n2.Conns()) != 1 {
		t.Errorf("should have 1 conn on initiator. Got: %d)", len(n2.Conns()))
	}

	// wait for reciever to see the conn.
	for i := 0; i < 10 && len(n3.Conns()) == 0; i++ {
		time.Sleep(time.Duration(10*i) * time.Millisecond)
	}

	if len(n3.Conns()) != 1 {
		t.Errorf("should have 1 conn on reciever. Got: %d", len(n3.Conns()))
	}

	// p := PrinterTo(os.Stdout)
	// p.NetworkConns(n1)
	// p.NetworkConns(n2)
	// p.NetworkConns(n3)

	// can create a stream 2->3, 3->2,
	if _, err := n2.NewStream(ctx, p3); err != nil {
		t.Error(err)
	}
	if _, err := n3.NewStream(ctx, p2); err != nil {
		t.Error(err)
	}

	// but not 1->2 nor 2->2 (not linked), nor 1->1 (not connected)
	if _, err := n1.NewStream(ctx, p2); err == nil {
		t.Error("should not be able to connect")
	}
	if _, err := n2.NewStream(ctx, p2); err == nil {
		t.Error("should not be able to connect")
	}
	if _, err := n1.NewStream(ctx, p1); err == nil {
		t.Error("should not be able to connect")
	}

	// connect p1->p1 (should fail)
	if _, err := n1.DialPeer(ctx, p1); err == nil {
		t.Error("p1 shouldn't be able to dial self")
	}

	// and a stream too
	if _, err := n1.NewStream(ctx, p1); err == nil {
		t.Error("p1 shouldn't be able to dial self")
	}

	// connect p1->p2
	if _, err := n1.DialPeer(ctx, p2); err == nil {
		t.Error("p1 should not be able to dial p2, not connected...")
	}

	// connect p3->p1
	if _, err := n3.DialPeer(ctx, p1); err == nil {
		t.Error("p3 should not be able to dial p1, not connected...")
	}

	// relink p1->p2

	l12, err = mn.LinkPeers(p1, p2)
	if err != nil {
		t.Fatal(err)
	}
	if !(l12.Networks()[0] == n1 && l12.Networks()[1] == n2) &&
		!(l12.Networks()[0] == n2 && l12.Networks()[1] == n1) {
		t.Error("l12 networks incorrect")
	}

	// should now be able to connect

	// connect p1->p2
	if _, err := n1.DialPeer(ctx, p2); err != nil {
		t.Error(err)
	}

	// and a stream should work now too :)
	if _, err := n2.NewStream(ctx, p3); err != nil {
		t.Error(err)
	}

}

func TestStreams(t *testing.T) {
	ctx := context.Background()

	mn, err := FullMeshConnected(3)
	if err != nil {
		t.Fatal(err)
	}
	defer mn.Close()

	handler := func(s network.Stream) {
		b := make([]byte, 4)
		if _, err := io.ReadFull(s, b); err != nil {
			panic(err)
		}
		if !bytes.Equal(b, []byte("beep")) {
			panic("bytes mismatch")
		}
		if _, err := s.Write([]byte("boop")); err != nil {
			panic(err)
		}
		s.Close()
	}

	hosts := mn.Hosts()
	for _, h := range mn.Hosts() {
		h.SetStreamHandler(protocol.TestingID, handler)
	}

	s, err := hosts[0].NewStream(ctx, hosts[1].ID(), protocol.TestingID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write([]byte("beep")); err != nil {
		panic(err)
	}
	b := make([]byte, 4)
	if _, err := io.ReadFull(s, b); err != nil {
		panic(err)
	}
	if !bytes.Equal(b, []byte("boop")) {
		panic("bytes mismatch 2")
	}

}

func TestAdding(t *testing.T) {
	mn := New()
	defer mn.Close()

	var peers []peer.ID
	for i := 0; i < 3; i++ {
		priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		a := randLocalTCPAddress()
		h, err := mn.AddPeer(priv, a)
		if err != nil {
			t.Fatal(err)
		}

		peers = append(peers, h.ID())
	}

	p1 := peers[0]
	p2 := peers[1]

	// link them
	for _, p1 := range peers {
		for _, p2 := range peers {
			if _, err := mn.LinkPeers(p1, p2); err != nil {
				t.Error(err)
			}
		}
	}

	// set the new stream handler on p2
	h2 := mn.Host(p2)
	if h2 == nil {
		t.Fatalf("no host for %s", p2)
	}
	h2.SetStreamHandler(protocol.TestingID, func(s network.Stream) {
		defer s.Close()

		b := make([]byte, 4)
		if _, err := io.ReadFull(s, b); err != nil {
			panic(err)
		}
		if string(b) != "beep" {
			panic("did not beep!")
		}

		if _, err := s.Write([]byte("boop")); err != nil {
			panic(err)
		}
	})

	// connect p1 to p2
	if _, err := mn.ConnectPeers(p1, p2); err != nil {
		t.Fatal(err)
	}

	// talk to p2
	h1 := mn.Host(p1)
	if h1 == nil {
		t.Fatalf("no network for %s", p1)
	}

	ctx := context.Background()
	s, err := h1.NewStream(ctx, p2, protocol.TestingID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write([]byte("beep")); err != nil {
		t.Error(err)
	}
	b := make([]byte, 4)
	if _, err := io.ReadFull(s, b); err != nil {
		t.Error(err)
	}
	if !bytes.Equal(b, []byte("boop")) {
		t.Error("bytes mismatch 2")
	}

}

func TestRateLimiting(t *testing.T) {
	if ci.IsRunning() {
		t.Skip("buggy in CI")
	}

	rl := NewRateLimiter(10)

	if !within(rl.Limit(10), time.Duration(float32(time.Second)), time.Millisecond) {
		t.Fatal()
	}
	if !within(rl.Limit(10), time.Duration(float32(time.Second*2)), time.Millisecond) {
		t.Fatal()
	}
	if !within(rl.Limit(10), time.Duration(float32(time.Second*3)), time.Millisecond) {
		t.Fatal()
	}

	if within(rl.Limit(10), time.Duration(float32(time.Second*3)), time.Millisecond) {
		t.Fatal()
	}

	rl.UpdateBandwidth(50)
	if !within(rl.Limit(75), time.Duration(float32(time.Second)*1.5), time.Millisecond) {
		t.Fatal()
	}

	if within(rl.Limit(75), time.Duration(float32(time.Second)*1.5), time.Millisecond) {
		t.Fatal()
	}

	rl.UpdateBandwidth(100)
	if !within(rl.Limit(1), time.Millisecond*10, time.Millisecond) {
		t.Fatal()
	}

	if within(rl.Limit(1), time.Millisecond*10, time.Millisecond) {
		t.Fatal()
	}
}

func within(t1 time.Duration, t2 time.Duration, tolerance time.Duration) bool {
	return math.Abs(float64(t1)-float64(t2)) < float64(tolerance)
}

func TestLimitedStreams(t *testing.T) {
	mn, err := FullMeshConnected(2)
	if err != nil {
		t.Fatal(err)
	}
	defer mn.Close()

	var wg sync.WaitGroup
	messages := 4
	messageSize := 500
	handler := func(s network.Stream) {
		b := make([]byte, messageSize)
		for i := 0; i < messages; i++ {
			if _, err := io.ReadFull(s, b); err != nil {
				log.Fatal(err)
			}
			if !bytes.Equal(b[:4], []byte("ping")) {
				log.Fatal("bytes mismatch")
			}
			wg.Done()
		}
		s.Close()
	}

	hosts := mn.Hosts()
	for _, h := range mn.Hosts() {
		h.SetStreamHandler(protocol.TestingID, handler)
	}

	peers := mn.Peers()
	links := mn.LinksBetweenPeers(peers[0], peers[1])
	//  1000 byte per second bandwidth
	bps := float64(1000)
	opts := links[0].Options()
	opts.Bandwidth = bps
	for _, link := range links {
		link.SetOptions(opts)
	}

	ctx := context.Background()
	s, err := hosts[0].NewStream(ctx, hosts[1].ID(), protocol.TestingID)
	if err != nil {
		t.Fatal(err)
	}

	filler := make([]byte, messageSize-4)
	data := append([]byte("ping"), filler...)
	before := time.Now()
	for i := 0; i < messages; i++ {
		wg.Add(1)
		if _, err := s.Write(data); err != nil {
			panic(err)
		}
	}

	wg.Wait()
	if !within(time.Since(before), time.Second*5/2, time.Second) {
		t.Fatal("Expected 2ish seconds but got ", time.Since(before))
	}
}
func TestFuzzManyPeers(t *testing.T) {
	peerCount := 500
	if race.WithRace() {
		peerCount = 100
	}
	for i := 0; i < peerCount; i++ {
		mn, err := FullMeshConnected(2)
		if err != nil {
			t.Fatal(err)
		}
		mn.Close()
	}
}

func TestStreamsWithLatency(t *testing.T) {
	latency := time.Millisecond * 500

	mn, err := WithNPeers(2)
	if err != nil {
		t.Fatal(err)
	}
	defer mn.Close()

	// configure the Mocknet with some latency and link/connect its peers
	mn.SetLinkDefaults(LinkOptions{Latency: latency})
	mn.LinkAll()
	mn.ConnectAllButSelf()

	msg := []byte("ping")
	mln := len(msg)

	var wg sync.WaitGroup

	// we'll write once to a single stream
	wg.Add(1)

	handler := func(s network.Stream) {
		b := make([]byte, mln)

		if _, err := io.ReadFull(s, b); err != nil {
			t.Fatal(err)
		}

		wg.Done()
		s.Close()
	}

	mn.Hosts()[0].SetStreamHandler(protocol.TestingID, handler)
	mn.Hosts()[1].SetStreamHandler(protocol.TestingID, handler)

	s, err := mn.Hosts()[0].NewStream(context.Background(), mn.Hosts()[1].ID(), protocol.TestingID)
	if err != nil {
		t.Fatal(err)
	}

	// writing to the stream will be subject to our configured latency
	checkpoint := time.Now()
	if _, err := s.Write(msg); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	delta := time.Since(checkpoint)
	tolerance := time.Second
	if !within(delta, latency, tolerance) {
		t.Fatalf("Expected write to take ~%s (+/- %s), but took %s", latency.String(), tolerance.String(), delta.String())
	}
}

func TestEventBus(t *testing.T) {
	const peers = 2

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	mn, err := FullMeshLinked(peers)
	if err != nil {
		t.Fatal(err)
	}
	defer mn.Close()

	sub0, err := mn.Hosts()[0].EventBus().Subscribe(new(event.EvtPeerConnectednessChanged))
	if err != nil {
		t.Fatal(err)
	}
	defer sub0.Close()
	sub1, err := mn.Hosts()[1].EventBus().Subscribe(new(event.EvtPeerConnectednessChanged))
	if err != nil {
		t.Fatal(err)
	}
	defer sub1.Close()

	id0, id1 := mn.Hosts()[0].ID(), mn.Hosts()[1].ID()

	_, err = mn.ConnectPeers(id0, id1)
	if err != nil {
		t.Fatal(err)
	}
	for range make([]int, peers) {
		select {
		case evt := <-sub0.Out():
			evnt := evt.(event.EvtPeerConnectednessChanged)
			if evnt.Peer != id1 {
				t.Fatal("wrong remote peer")
			}
			if evnt.Connectedness != network.Connected {
				t.Fatal("wrong connectedness type")
			}
		case evt := <-sub1.Out():
			evnt := evt.(event.EvtPeerConnectednessChanged)
			if evnt.Peer != id0 {
				t.Fatal("wrong remote peer")
			}
			if evnt.Connectedness != network.Connected {
				t.Fatal("wrong connectedness type")
			}
		case <-ctx.Done():
			t.Fatal("didn't get connectedness events in time")
		}
	}

	err = mn.DisconnectPeers(id0, id1)
	if err != nil {
		t.Fatal(err)
	}
	for range make([]int, peers) {
		select {
		case evt := <-sub0.Out():
			evnt := evt.(event.EvtPeerConnectednessChanged)
			if evnt.Peer != id1 {
				t.Fatal("wrong remote peer")
			}
			if evnt.Connectedness != network.NotConnected {
				t.Fatal("wrong connectedness type")
			}
		case evt := <-sub1.Out():
			evnt := evt.(event.EvtPeerConnectednessChanged)
			if evnt.Peer != id0 {
				t.Fatal("wrong remote peer")
			}
			if evnt.Connectedness != network.NotConnected {
				t.Fatal("wrong connectedness type")
			}
		case <-ctx.Done():
			t.Fatal("didn't get connectedness events in time")
		}
	}
}

func TestBlockByPeerID(t *testing.T) {
	m, gater1, host1, _, host2 := WithConnectionGaters(t)

	err := gater1.BlockPeer(host2.ID())
	if err != nil {
		t.Fatal(err)
	}

	_, err = m.ConnectPeers(host1.ID(), host2.ID())
	if err == nil {
		t.Fatal("Should have blocked connection to banned peer")
	}

	_, err = m.ConnectPeers(host2.ID(), host1.ID())
	if err == nil {
		t.Fatal("Should have blocked connection from banned peer")
	}
}

func TestBlockByIP(t *testing.T) {
	m, gater1, host1, _, host2 := WithConnectionGaters(t)

	ip, err := manet.ToIP(host2.Addrs()[0])
	if err != nil {
		t.Fatal(err)
	}
	err = gater1.BlockAddr(ip)
	if err != nil {
		t.Fatal(err)
	}

	_, err = m.ConnectPeers(host1.ID(), host2.ID())
	if err == nil {
		t.Fatal("Should have blocked connection to banned IP")
	}

	_, err = m.ConnectPeers(host2.ID(), host1.ID())
	if err == nil {
		t.Fatal("Should have blocked connection from banned IP")
	}
}

func WithConnectionGaters(t *testing.T) (Mocknet, *conngater.BasicConnectionGater, host.Host, *conngater.BasicConnectionGater, host.Host) {
	m := New()
	addPeer := func() (*conngater.BasicConnectionGater, host.Host) {
		gater, err := conngater.NewBasicConnectionGater(nil)
		if err != nil {
			t.Fatal(err)
		}
		h, err := m.GenPeerWithOptions(PeerOptions{gater: gater})
		if err != nil {
			t.Fatal(err)
		}
		return gater, h
	}
	gater1, host1 := addPeer()
	gater2, host2 := addPeer()

	err := m.LinkAll()
	if err != nil {
		t.Fatal(err)
	}
	return m, gater1, host1, gater2, host2
}
