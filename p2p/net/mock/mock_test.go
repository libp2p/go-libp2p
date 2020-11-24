package mocknet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"

	detectrace "github.com/ipfs/go-detect-race"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/libp2p/go-libp2p-core/test"
	"github.com/libp2p/go-libp2p-testing/ci"
	tnet "github.com/libp2p/go-libp2p-testing/net"
)

func randPeer(t *testing.T) peer.ID {
	p, err := test.RandPeerID()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNetworkSetup(t *testing.T) {
	ctx := context.Background()
	id1 := tnet.RandIdentityOrFatal(t)
	id2 := tnet.RandIdentityOrFatal(t)
	id3 := tnet.RandIdentityOrFatal(t)
	mn := New(ctx)
	// peers := []peer.ID{p1, p2, p3}

	// add peers to mock net

	a1 := tnet.RandLocalTCPAddress()
	a2 := tnet.RandLocalTCPAddress()
	a3 := tnet.RandLocalTCPAddress()

	h1, err := mn.AddPeer(id1.PrivateKey(), a1)
	if err != nil {
		t.Fatal(err)
	}
	p1 := h1.ID()

	h2, err := mn.AddPeer(id2.PrivateKey(), a2)
	if err != nil {
		t.Fatal(err)
	}
	p2 := h2.ID()

	h3, err := mn.AddPeer(id3.PrivateKey(), a3)
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

	mn, err := FullMeshConnected(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}

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

func performPing(t *testing.T, st string, n int, s network.Stream) error {
	t.Helper()

	defer s.Close()

	for i := 0; i < n; i++ {
		b := make([]byte, 4+len(st))
		if _, err := s.Write([]byte("ping" + st)); err != nil {
			return err
		}
		if _, err := io.ReadFull(s, b); err != nil {
			return err
		}
		if !bytes.Equal(b, []byte("pong"+st)) {
			return errors.New("bytes mismatch")
		}
	}
	return nil
}

func makePonger(t *testing.T, st string, errs chan<- error) func(network.Stream) {
	t.Helper()

	return func(s network.Stream) {
		go func() {
			defer s.Close()

			for {
				b := make([]byte, 4+len(st))
				if _, err := io.ReadFull(s, b); err != nil {
					if err == io.EOF {
						return
					}
					errs <- err
				}
				if !bytes.Equal(b, []byte("ping"+st)) {
					errs <- errors.New("bytes mismatch")
				}
				if _, err := s.Write([]byte("pong" + st)); err != nil {
					errs <- err
				}
			}
		}()
	}
}

func TestStreamsStress(t *testing.T) {
	ctx := context.Background()
	nnodes := 100
	if detectrace.WithRace() {
		nnodes = 30
	}

	mn, err := FullMeshConnected(context.Background(), nnodes)
	if err != nil {
		t.Fatal(err)
	}

	errs := make(chan error)

	hosts := mn.Hosts()
	for _, h := range hosts {
		ponger := makePonger(t, "pingpong", errs)
		h.SetStreamHandler(protocol.TestingID, ponger)
	}

	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var from, to int
			for from == to {
				from = rand.Intn(len(hosts))
				to = rand.Intn(len(hosts))
			}
			s, err := hosts[from].NewStream(ctx, hosts[to].ID(), protocol.TestingID)
			if err != nil {
				log.Debugf("%d (%s) %d (%s)", from, hosts[from], to, hosts[to])
				panic(err)
			}

			log.Infof("%d start pinging", i)
			errs <- performPing(t, "pingpong", rand.Intn(100), s)
			log.Infof("%d done pinging", i)
		}(i)
	}

	go func() {
		wg.Wait()
		close(errs)
	}()

	for err := range errs {
		if err == nil {
			continue
		}
		t.Fatal(err)
	}
}

func TestAdding(t *testing.T) {
	mn := New(context.Background())

	var peers []peer.ID
	for i := 0; i < 3; i++ {
		id := tnet.RandIdentityOrFatal(t)

		a := tnet.RandLocalTCPAddress()
		h, err := mn.AddPeer(id.PrivateKey(), a)
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
	if !within(rl.Limit(1), time.Duration(time.Millisecond*10), time.Millisecond) {
		t.Fatal()
	}

	if within(rl.Limit(1), time.Duration(time.Millisecond*10), time.Millisecond) {
		t.Fatal()
	}
}

func within(t1 time.Duration, t2 time.Duration, tolerance time.Duration) bool {
	return math.Abs(float64(t1)-float64(t2)) < float64(tolerance)
}

func TestLimitedStreams(t *testing.T) {
	mn, err := FullMeshConnected(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}

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
	if !within(time.Since(before), time.Second*2, time.Second) {
		t.Fatal("Expected 2ish seconds but got ", time.Since(before))
	}
}
func TestFuzzManyPeers(t *testing.T) {
	peerCount := 50000
	if detectrace.WithRace() {
		peerCount = 100
	}
	for i := 0; i < peerCount; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		_, err := FullMeshConnected(ctx, 2)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestStreamsWithLatency(t *testing.T) {
	latency := time.Millisecond * 500

	mn, err := WithNPeers(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}

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
