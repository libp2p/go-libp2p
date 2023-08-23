package identify_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/record"
	coretest "github.com/libp2p/go-libp2p/core/test"
	basichost "github.com/libp2p/go-libp2p/p2p/host/basic"
	blhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify/pb"

	mockClock "github.com/benbjohnson/clock"
	"github.com/libp2p/go-libp2p-testing/race"
	"github.com/libp2p/go-msgio/pbio"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKnowsAddrs(t *testing.T, h host.Host, p peer.ID, expected []ma.Multiaddr) {
	t.Helper()
	require.True(t, assert.ElementsMatchf(t, expected, h.Peerstore().Addrs(p), fmt.Sprintf("%s did not have addr for %s", h.ID(), p)))
}

func testHasAgentVersion(t *testing.T, h host.Host, p peer.ID) {
	v, err := h.Peerstore().Get(p, "AgentVersion")
	if v.(string) != "github.com/libp2p/go-libp2p" { // this is the default user agent
		t.Error("agent version mismatch", err)
	}
}

func testHasPublicKey(t *testing.T, h host.Host, p peer.ID, shouldBe ic.PubKey) {
	k := h.Peerstore().PubKey(p)
	if k == nil {
		t.Error("no public key")
		return
	}
	if !k.Equals(shouldBe) {
		t.Error("key mismatch")
		return
	}

	p2, err := peer.IDFromPublicKey(k)
	if err != nil {
		t.Error("could not make key")
	} else if p != p2 {
		t.Error("key does not match peerid")
	}
}

// we're using BlankHost in our tests, which doesn't automatically generate peer records
// and emit address change events on the bus like BasicHost.
// This generates a record, puts it in the peerstore and emits an addr change event
// which will cause the identify service to push it to all peers it's connected to.
func emitAddrChangeEvt(t *testing.T, h host.Host) {
	t.Helper()

	key := h.Peerstore().PrivKey(h.ID())
	if key == nil {
		t.Fatal("no private key for host")
	}

	rec := peer.NewPeerRecord()
	rec.PeerID = h.ID()
	rec.Addrs = h.Addrs()
	signed, err := record.Seal(rec, key)
	if err != nil {
		t.Fatalf("error generating peer record: %s", err)
	}

	cab, ok := peerstore.GetCertifiedAddrBook(h.Peerstore())
	require.True(t, ok)
	_, err = cab.ConsumePeerRecord(signed, peerstore.PermanentAddrTTL)
	require.NoError(t, err)

	evt := event.EvtLocalAddressesUpdated{}
	emitter, err := h.EventBus().Emitter(new(event.EvtLocalAddressesUpdated), eventbus.Stateful)
	if err != nil {
		t.Fatal(err)
	}
	err = emitter.Emit(evt)
	if err != nil {
		t.Fatal(err)
	}
}

// TestIDServiceWait gives the ID service 1s to finish after dialing
// this is because it used to be concurrent. Now, Dial wait till the
// id service is done.
func TestIDService(t *testing.T) {
	// This test is highly timing dependent, waiting on timeouts/expiration.
	oldTTL := peerstore.RecentlyConnectedAddrTTL
	peerstore.RecentlyConnectedAddrTTL = 500 * time.Millisecond
	t.Cleanup(func() { peerstore.RecentlyConnectedAddrTTL = oldTTL })

	clk := mockClock.NewMock()
	swarm1 := swarmt.GenSwarm(t, swarmt.WithClock(clk))
	swarm2 := swarmt.GenSwarm(t, swarmt.WithClock(clk))
	h1 := blhost.NewBlankHost(swarm1)
	h2 := blhost.NewBlankHost(swarm2)

	h1p := h1.ID()
	h2p := h2.ID()

	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)
	defer ids1.Close()
	ids1.Start()

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)
	defer ids2.Close()
	ids2.Start()

	sub, err := ids1.Host.EventBus().Subscribe(new(event.EvtPeerIdentificationCompleted))
	if err != nil {
		t.Fatal(err)
	}

	testKnowsAddrs(t, h1, h2p, []ma.Multiaddr{}) // nothing
	testKnowsAddrs(t, h2, h1p, []ma.Multiaddr{}) // nothing

	// the forgetMe addr represents an address for h1 that h2 has learned out of band
	// (not via identify protocol). During the identify exchange, it will be
	// forgotten and replaced by the addrs h1 sends.
	forgetMe, _ := ma.NewMultiaddr("/ip4/1.2.3.4/tcp/1234")

	h2.Peerstore().AddAddr(h1p, forgetMe, peerstore.RecentlyConnectedAddrTTL)
	h2pi := h2.Peerstore().PeerInfo(h2p)
	require.NoError(t, h1.Connect(context.Background(), h2pi))

	h1t2c := h1.Network().ConnsToPeer(h2p)
	require.NotEmpty(t, h1t2c, "should have a conn here")

	ids1.IdentifyConn(h1t2c[0])

	// the idService should be opened automatically, by the network.
	// what we should see now is that both peers know about each others listen addresses.
	t.Log("test peer1 has peer2 addrs correctly")
	testKnowsAddrs(t, h1, h2p, h2.Addrs()) // has them
	testHasAgentVersion(t, h1, h2p)
	testHasPublicKey(t, h1, h2p, h2.Peerstore().PubKey(h2p)) // h1 should have h2's public key

	// now, this wait we do have to do. it's the wait for the Listening side
	// to be done identifying the connection.
	c := h2.Network().ConnsToPeer(h1.ID())
	require.NotEmpty(t, c, "should have connection by now at least.")
	ids2.IdentifyConn(c[0])

	// and the protocol versions.
	t.Log("test peer2 has peer1 addrs correctly")
	testKnowsAddrs(t, h2, h1p, h1.Addrs()) // has them
	testHasAgentVersion(t, h2, h1p)
	testHasPublicKey(t, h2, h1p, h1.Peerstore().PubKey(h1p)) // h1 should have h2's public key

	// Need both sides to actually notice that the connection has been closed.
	sentDisconnect1 := waitForDisconnectNotification(swarm1)
	sentDisconnect2 := waitForDisconnectNotification(swarm2)
	h1.Network().ClosePeer(h2p)
	h2.Network().ClosePeer(h1p)
	if len(h2.Network().ConnsToPeer(h1.ID())) != 0 || len(h1.Network().ConnsToPeer(h2.ID())) != 0 {
		t.Fatal("should have no connections")
	}

	t.Log("testing addrs just after disconnect")
	// addresses don't immediately expire on disconnect, so we should still have them
	testKnowsAddrs(t, h2, h1p, h1.Addrs())
	testKnowsAddrs(t, h1, h2p, h2.Addrs())

	<-sentDisconnect1
	<-sentDisconnect2

	// the addrs had their TTLs reduced on disconnect, and
	// will be forgotten soon after
	t.Log("testing addrs after TTL expiration")
	clk.Add(time.Second)
	testKnowsAddrs(t, h1, h2p, []ma.Multiaddr{})
	testKnowsAddrs(t, h2, h1p, []ma.Multiaddr{})

	// test that we received the "identify completed" event.
	select {
	case <-sub.Out():
	case <-time.After(3 * time.Second):
		t.Fatalf("expected EvtPeerIdentificationCompleted event within 10 seconds; none received")
	}
}

func TestProtoMatching(t *testing.T) {
	tcp1, _ := ma.NewMultiaddr("/ip4/1.2.3.4/tcp/1234")
	tcp2, _ := ma.NewMultiaddr("/ip4/1.2.3.4/tcp/2345")
	tcp3, _ := ma.NewMultiaddr("/ip4/1.2.3.4/tcp/4567")
	utp, _ := ma.NewMultiaddr("/ip4/1.2.3.4/udp/1234/utp")

	if !identify.HasConsistentTransport(tcp1, []ma.Multiaddr{tcp2, tcp3, utp}) {
		t.Fatal("expected match")
	}

	if identify.HasConsistentTransport(utp, []ma.Multiaddr{tcp2, tcp3}) {
		t.Fatal("expected mismatch")
	}
}

func TestLocalhostAddrFiltering(t *testing.T) {
	t.Skip("need to fix this test")
	mn := mocknet.New()
	defer mn.Close()
	id1 := coretest.RandPeerIDFatal(t)
	ps1, err := pstoremem.NewPeerstore()
	if err != nil {
		t.Fatal(err)
	}
	p1addr1, _ := ma.NewMultiaddr("/ip4/1.2.3.4/tcp/1234")
	p1addr2, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/2345")
	ps1.AddAddrs(id1, []ma.Multiaddr{p1addr1, p1addr2}, peerstore.PermanentAddrTTL)
	p1, err := mn.AddPeerWithPeerstore(id1, ps1)
	if err != nil {
		t.Fatal(err)
	}

	id2 := coretest.RandPeerIDFatal(t)
	ps2, err := pstoremem.NewPeerstore()
	if err != nil {
		t.Fatal(err)
	}
	p2addr1, _ := ma.NewMultiaddr("/ip4/1.2.3.5/tcp/1234")
	p2addr2, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/3456")
	p2addrs := []ma.Multiaddr{p2addr1, p2addr2}
	ps2.AddAddrs(id2, p2addrs, peerstore.PermanentAddrTTL)
	p2, err := mn.AddPeerWithPeerstore(id2, ps2)
	if err != nil {
		t.Fatal(err)
	}

	id3 := coretest.RandPeerIDFatal(t)
	ps3, err := pstoremem.NewPeerstore()
	if err != nil {
		t.Fatal(err)
	}
	p3addr1, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/4567")
	ps3.AddAddrs(id3, []ma.Multiaddr{p3addr1}, peerstore.PermanentAddrTTL)
	p3, err := mn.AddPeerWithPeerstore(id3, ps3)
	if err != nil {
		t.Fatal(err)
	}

	err = mn.LinkAll()
	if err != nil {
		t.Fatal(err)
	}
	p1.Connect(context.Background(), peer.AddrInfo{
		ID:    id2,
		Addrs: p2addrs[0:1],
	})
	p3.Connect(context.Background(), peer.AddrInfo{
		ID:    id2,
		Addrs: p2addrs[1:],
	})

	ids1, err := identify.NewIDService(p1)
	require.NoError(t, err)
	ids1.Start()

	ids2, err := identify.NewIDService(p2)
	require.NoError(t, err)
	ids2.Start()

	ids3, err := identify.NewIDService(p3)
	require.NoError(t, err)
	ids3.Start()

	defer func() {
		ids1.Close()
		ids2.Close()
		ids3.Close()
	}()

	conns := p2.Network().ConnsToPeer(id1)
	if len(conns) == 0 {
		t.Fatal("no conns")
	}
	conn := conns[0]
	ids2.IdentifyConn(conn)
	addrs := p2.Peerstore().Addrs(id1)
	if len(addrs) != 1 {
		t.Fatalf("expected one addr, found %s", addrs)
	}

	conns = p3.Network().ConnsToPeer(id2)
	if len(conns) == 0 {
		t.Fatal("no conns")
	}
	conn = conns[0]
	ids3.IdentifyConn(conn)
	addrs = p3.Peerstore().Addrs(id2)
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addrs for %s, found %d: %s", id2, len(addrs), addrs)
	}
}

// TestIdentifyPushWhileIdentifyingConn tests that the host waits to push updates if an identify is ongoing.
func TestIdentifyPushWhileIdentifyingConn(t *testing.T) {
	t.Skip()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h2.Close()
	defer h1.Close()
	t.Log("h1:", h1.ID())
	t.Log("h2:", h2.ID())

	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)
	ids1.Start()

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)
	ids2.Start()

	defer ids1.Close()
	defer ids2.Close()

	// replace the original identify handler by one that blocks until we close the block channel.
	// this allows us to control how long identify runs.
	block := make(chan struct{})
	handler := func(s network.Stream) {
		<-block
		w := pbio.NewDelimitedWriter(s)
		w.WriteMsg(&pb.Identify{Protocols: protocol.ConvertToStrings(h1.Mux().Protocols())})
		s.Close()
	}
	h1.RemoveStreamHandler(identify.ID)
	h1.SetStreamHandler(identify.ID, handler)

	// from h2 connect to h1.
	if err := h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}); err != nil {
		t.Fatal(err)
	}

	// from h2, identify h1.
	conn := h2.Network().ConnsToPeer(h1.ID())[0]
	go ids2.IdentifyConn(conn)

	<-time.After(500 * time.Millisecond)

	// subscribe to events in h1; after identify h1 should receive the update from h2 and publish an event in the bus.
	sub, err := h1.EventBus().Subscribe(&event.EvtPeerProtocolsUpdated{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	// add a handler in h2; the update to h1 will queue until we're done identifying h1.
	h2.SetStreamHandler(protocol.TestingID, func(_ network.Stream) {})
	<-time.After(500 * time.Millisecond)

	// make sure we haven't received any events yet.
	if q := len(sub.Out()); q > 0 {
		t.Fatalf("expected no events yet; queued: %d", q)
	}

	close(block)
	select {
	case evt := <-sub.Out():
		e := evt.(event.EvtPeerProtocolsUpdated)
		if e.Peer != h2.ID() || len(e.Added) != 1 || e.Added[0] != protocol.TestingID {
			t.Fatalf("expected an event for protocol changes in h2, with the testing protocol added; instead got: %v", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out while waiting for an event for the protocol changes in h2")
	}
}

func TestIdentifyPushOnAddrChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))

	h1p := h1.ID()
	h2p := h2.ID()

	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)
	defer ids1.Close()
	ids1.Start()

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)
	defer ids2.Close()
	ids2.Start()

	testKnowsAddrs(t, h1, h2p, []ma.Multiaddr{}) // nothing
	testKnowsAddrs(t, h2, h1p, []ma.Multiaddr{}) // nothing

	require.NoError(t, h1.Connect(ctx, h2.Peerstore().PeerInfo(h2p)))
	// h1 should immediately see a connection from h2
	require.NotEmpty(t, h1.Network().ConnsToPeer(h2p))
	// wait for h2 to Identify itself so we are sure h2 has seen the connection.
	ids1.IdentifyConn(h1.Network().ConnsToPeer(h2p)[0])

	// h2 should now see the connection and we should wait for h1 to Identify itself to h2.
	require.NotEmpty(t, h2.Network().ConnsToPeer(h1p))
	ids2.IdentifyConn(h2.Network().ConnsToPeer(h1p)[0])

	testKnowsAddrs(t, h1, h2p, h2.Peerstore().Addrs(h2p))
	testKnowsAddrs(t, h2, h1p, h1.Peerstore().Addrs(h1p))

	// change addr on host 1 and ensure host2 gets a push
	lad := ma.StringCast("/ip4/127.0.0.1/tcp/1234")
	require.NoError(t, h1.Network().Listen(lad))
	require.Contains(t, h1.Addrs(), lad)

	h2AddrStream := h2.Peerstore().AddrStream(ctx, h1p)

	emitAddrChangeEvt(t, h1)

	// Wait for h2 to process the new addr
	waitForAddrInStream(t, h2AddrStream, lad, 10*time.Second, "h2 did not receive addr change")

	require.True(t, ma.Contains(h2.Peerstore().Addrs(h1p), lad))

	// change addr on host2 and ensure host 1 gets a pus
	lad = ma.StringCast("/ip4/127.0.0.1/tcp/1235")
	require.NoError(t, h2.Network().Listen(lad))
	require.Contains(t, h2.Addrs(), lad)
	h1AddrStream := h1.Peerstore().AddrStream(ctx, h2p)
	emitAddrChangeEvt(t, h2)

	// Wait for h1 to process the new addr
	waitForAddrInStream(t, h1AddrStream, lad, 10*time.Second, "h1 did not receive addr change")

	require.True(t, ma.Contains(h1.Peerstore().Addrs(h2p), lad))

	// change addr on host2 again
	lad2 := ma.StringCast("/ip4/127.0.0.1/tcp/1236")
	require.NoError(t, h2.Network().Listen(lad2))
	require.Contains(t, h2.Addrs(), lad2)
	emitAddrChangeEvt(t, h2)

	// Wait for h1 to process the new addr
	waitForAddrInStream(t, h1AddrStream, lad2, 10*time.Second, "h1 did not receive addr change")

	require.True(t, ma.Contains(h1.Peerstore().Addrs(h2p), lad2))
}

func TestUserAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1, err := libp2p.New(libp2p.UserAgent("foo"), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Close()

	h2, err := libp2p.New(libp2p.UserAgent("bar"), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()

	err = h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
	if err != nil {
		t.Fatal(err)
	}
	av, err := h1.Peerstore().Get(h2.ID(), "AgentVersion")
	if err != nil {
		t.Fatal(err)
	}
	if ver, ok := av.(string); !ok || ver != "bar" {
		t.Errorf("expected agent version %q, got %q", "bar", av)
	}
}

func TestNotListening(t *testing.T) {
	// Make sure we don't panic if we're not listening on any addresses.
	//
	// https://github.com/libp2p/go-libp2p/issues/939
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Close()

	h2, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()

	err = h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSendPush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h2.Close()
	defer h1.Close()

	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)
	defer ids1.Close()
	ids1.Start()

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)
	defer ids2.Close()
	ids2.Start()

	err = h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
	require.NoError(t, err)

	// wait for them to Identify each other
	ids1.IdentifyConn(h1.Network().ConnsToPeer(h2.ID())[0])
	ids2.IdentifyConn(h2.Network().ConnsToPeer(h1.ID())[0])

	// h1 starts listening on a new protocol and h2 finds out about that through a push
	h1.SetStreamHandler("rand", func(network.Stream) {})
	require.Eventually(t, func() bool {
		sup, err := h2.Peerstore().SupportsProtocols(h1.ID(), []protocol.ID{"rand"}...)
		return err == nil && len(sup) == 1 && sup[0] == "rand"
	}, time.Second, 10*time.Millisecond)

	// h1 stops listening on a protocol and h2 finds out about it via a push
	h1.RemoveStreamHandler("rand")
	require.Eventually(t, func() bool {
		sup, err := h2.Peerstore().SupportsProtocols(h1.ID(), []protocol.ID{"rand"}...)
		return err == nil && len(sup) == 0
	}, time.Second, 10*time.Millisecond)
}

func TestLargeIdentifyMessage(t *testing.T) {
	if race.WithRace() {
		t.Skip("setting peerstore.RecentlyConnectedAddrTTL is racy")
	}
	oldTTL := peerstore.RecentlyConnectedAddrTTL
	peerstore.RecentlyConnectedAddrTTL = 500 * time.Millisecond
	t.Cleanup(func() { peerstore.RecentlyConnectedAddrTTL = oldTTL })

	clk := mockClock.NewMock()
	swarm1 := swarmt.GenSwarm(t, swarmt.WithClock(clk))
	swarm2 := swarmt.GenSwarm(t, swarmt.WithClock(clk))
	h1 := blhost.NewBlankHost(swarm1)
	h2 := blhost.NewBlankHost(swarm2)

	// add protocol strings to make the message larger
	// about 2K of protocol strings
	for i := 0; i < 500; i++ {
		r := protocol.ID(fmt.Sprintf("rand%d", i))
		h1.SetStreamHandler(r, func(network.Stream) {})
		h2.SetStreamHandler(r, func(network.Stream) {})
	}

	h1p := h1.ID()
	h2p := h2.ID()

	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)
	defer ids1.Close()
	ids1.Start()

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)
	defer ids2.Close()
	ids2.Start()

	sub, err := ids1.Host.EventBus().Subscribe(new(event.EvtPeerIdentificationCompleted))
	require.NoError(t, err)

	testKnowsAddrs(t, h1, h2p, []ma.Multiaddr{}) // nothing
	testKnowsAddrs(t, h2, h1p, []ma.Multiaddr{}) // nothing

	// the forgetMe addr represents an address for h1 that h2 has learned out of band
	// (not via identify protocol). During the identify exchange, it will be
	// forgotten and replaced by the addrs h1 sends.
	forgetMe, _ := ma.NewMultiaddr("/ip4/1.2.3.4/tcp/1234")
	h2.Peerstore().AddAddr(h1p, forgetMe, peerstore.RecentlyConnectedAddrTTL)

	h2pi := h2.Peerstore().PeerInfo(h2p)
	h2pi.Addrs = h2pi.Addrs[:1]
	require.NoError(t, h1.Connect(context.Background(), h2pi))

	h1t2c := h1.Network().ConnsToPeer(h2p)
	require.Equal(t, 1, len(h1t2c), "should have a conn here")

	ids1.IdentifyConn(h1t2c[0])

	// the idService should be opened automatically, by the network.
	// what we should see now is that both peers know about each others listen addresses.
	t.Log("test peer1 has peer2 addrs correctly")
	testKnowsAddrs(t, h1, h2p, h2.Addrs()) // has them
	testHasAgentVersion(t, h1, h2p)
	testHasPublicKey(t, h1, h2p, h2.Peerstore().PubKey(h2p)) // h1 should have h2's public key

	// now, this wait we do have to do. it's the wait for the Listening side
	// to be done identifying the connection.
	c := h2.Network().ConnsToPeer(h1.ID())
	if len(c) != 1 {
		t.Fatal("should have connection by now at least.")
	}
	ids2.IdentifyConn(c[0])

	// and the protocol versions.
	t.Log("test peer2 has peer1 addrs correctly")
	testKnowsAddrs(t, h2, h1p, h1.Addrs()) // has them
	testHasAgentVersion(t, h2, h1p)
	testHasPublicKey(t, h2, h1p, h1.Peerstore().PubKey(h1p)) // h1 should have h2's public key

	// Need both sides to actually notice that the connection has been closed.
	sentDisconnect1 := waitForDisconnectNotification(swarm1)
	sentDisconnect2 := waitForDisconnectNotification(swarm2)
	h1.Network().ClosePeer(h2p)
	h2.Network().ClosePeer(h1p)
	if len(h2.Network().ConnsToPeer(h1.ID())) != 0 || len(h1.Network().ConnsToPeer(h2.ID())) != 0 {
		t.Fatal("should have no connections")
	}

	t.Log("testing addrs just after disconnect")
	// addresses don't immediately expire on disconnect, so we should still have them
	testKnowsAddrs(t, h2, h1p, h1.Addrs())
	testKnowsAddrs(t, h1, h2p, h2.Addrs())

	<-sentDisconnect1
	<-sentDisconnect2

	// the addrs had their TTLs reduced on disconnect, and
	// will be forgotten soon after
	t.Log("testing addrs after TTL expiration")
	clk.Add(time.Second)
	testKnowsAddrs(t, h1, h2p, []ma.Multiaddr{})
	testKnowsAddrs(t, h2, h1p, []ma.Multiaddr{})

	// test that we received the "identify completed" event.
	select {
	case <-sub.Out():
	case <-time.After(3 * time.Second):
		t.Fatalf("expected EvtPeerIdentificationCompleted event within 3 seconds; none received")
	}
}

func TestLargePushMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))

	// add protocol strings to make the message larger
	// about 2K of protocol strings
	for i := 0; i < 500; i++ {
		r := protocol.ID(fmt.Sprintf("rand%d", i))
		h1.SetStreamHandler(r, func(network.Stream) {})
		h2.SetStreamHandler(r, func(network.Stream) {})
	}

	h1p := h1.ID()
	h2p := h2.ID()

	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)
	defer ids1.Close()
	ids1.Start()

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)
	defer ids2.Close()
	ids2.Start()

	testKnowsAddrs(t, h1, h2p, []ma.Multiaddr{}) // nothing
	testKnowsAddrs(t, h2, h1p, []ma.Multiaddr{}) // nothing

	h2pi := h2.Peerstore().PeerInfo(h2p)
	require.NoError(t, h1.Connect(ctx, h2pi))
	// h1 should immediately see a connection from h2
	require.NotEmpty(t, h1.Network().ConnsToPeer(h2p))
	// wait for h2 to Identify itself so we are sure h2 has seen the connection.
	ids1.IdentifyConn(h1.Network().ConnsToPeer(h2p)[0])

	// h2 should now see the connection and we should wait for h1 to Identify itself to h2.
	require.NotEmpty(t, h2.Network().ConnsToPeer(h1p))
	ids2.IdentifyConn(h2.Network().ConnsToPeer(h1p)[0])

	testKnowsAddrs(t, h1, h2p, h2.Peerstore().Addrs(h2p))
	testKnowsAddrs(t, h2, h1p, h1.Peerstore().Addrs(h1p))

	// change addr on host 1 and ensure host2 gets a push
	lad := ma.StringCast("/ip4/127.0.0.1/tcp/1234")
	require.NoError(t, h1.Network().Listen(lad))
	require.Contains(t, h1.Addrs(), lad)
	emitAddrChangeEvt(t, h1)

	require.Eventually(t, func() bool {
		return ma.Contains(h2.Peerstore().Addrs(h1p), lad)
	}, time.Second, 10*time.Millisecond)

	// change addr on host2 and ensure host 1 gets a pus
	lad = ma.StringCast("/ip4/127.0.0.1/tcp/1235")
	require.NoError(t, h2.Network().Listen(lad))
	require.Contains(t, h2.Addrs(), lad)
	emitAddrChangeEvt(t, h2)

	require.Eventually(t, func() bool {
		return ma.Contains(h1.Peerstore().Addrs(h2p), lad)
	}, time.Second, 10*time.Millisecond)

	// change addr on host2 again
	lad2 := ma.StringCast("/ip4/127.0.0.1/tcp/1236")
	require.NoError(t, h2.Network().Listen(lad2))
	require.Contains(t, h2.Addrs(), lad2)
	emitAddrChangeEvt(t, h2)

	require.Eventually(t, func() bool {
		return ma.Contains(h1.Peerstore().Addrs(h2p), lad2)
	}, time.Second, 10*time.Millisecond)
}

func TestIdentifyResponseReadTimeout(t *testing.T) {
	timeout := identify.Timeout
	identify.Timeout = 100 * time.Millisecond
	defer func() {
		identify.Timeout = timeout
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h1.Close()
	defer h2.Close()

	h2p := h2.ID()
	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)
	defer ids1.Close()
	ids1.Start()

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)
	defer ids2.Close()
	ids2.Start()

	// remote stream handler will just hang and not send back an identify response
	h2.SetStreamHandler(identify.ID, func(s network.Stream) {
		time.Sleep(100 * time.Second)
	})

	sub, err := ids1.Host.EventBus().Subscribe(new(event.EvtPeerIdentificationFailed))
	require.NoError(t, err)

	h2pi := h2.Peerstore().PeerInfo(h2p)
	require.NoError(t, h1.Connect(ctx, h2pi))

	select {
	case ev := <-sub.Out():
		fev := ev.(event.EvtPeerIdentificationFailed)
		require.Contains(t, fev.Reason.Error(), "deadline")
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive identify failure event")
	}
}

func TestIncomingIDStreamsTimeout(t *testing.T) {
	timeout := identify.Timeout
	identify.Timeout = 100 * time.Millisecond
	defer func() {
		identify.Timeout = timeout
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	protocols := []protocol.ID{identify.IDPush}

	for _, p := range protocols {
		h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
		h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))
		defer h1.Close()
		defer h2.Close()

		ids1, err := identify.NewIDService(h1)
		require.NoError(t, err)
		defer ids1.Close()
		ids1.Start()

		ids2, err := identify.NewIDService(h2)
		require.NoError(t, err)
		defer ids2.Close()
		ids2.Start()

		h2p := h2.ID()
		h2pi := h2.Peerstore().PeerInfo(h2p)
		require.NoError(t, h1.Connect(ctx, h2pi))

		_, err = h1.NewStream(ctx, h2p, p)
		require.NoError(t, err)

		// remote peer should eventually reset stream
		require.Eventually(t, func() bool {
			for _, c := range h2.Network().ConnsToPeer(h1.ID()) {
				if len(c.GetStreams()) > 0 {
					return false
				}
			}
			return true
		}, 5*time.Second, 200*time.Millisecond)
	}
}

func TestOutOfOrderConnectedNotifs(t *testing.T) {
	h1, err := libp2p.New(libp2p.NoListenAddrs)
	require.NoError(t, err)
	h2, err := libp2p.New(libp2p.ListenAddrs(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")))
	require.NoError(t, err)

	doneCh := make(chan struct{})
	errCh := make(chan error)

	// This callback may be called before identify's Connnected callback completes. If it does, the IdentifyWait should still finish successfully.
	h1.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(n network.Network, c network.Conn) {
			bh1 := h1.(*basichost.BasicHost)
			idChan := bh1.IDService().IdentifyWait(c)
			go func() {
				<-idChan
				protos, err := bh1.Peerstore().GetProtocols(h2.ID())
				if err != nil {
					errCh <- err
				}
				if len(protos) == 0 {
					errCh <- errors.New("no protocols found. Identify did not complete")
				}

				close(doneCh)
			}()
		},
	})

	h1.Connect(context.Background(), peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})

	select {
	case <-doneCh:
	case err := <-errCh:
		t.Fatalf("err: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatalf("identify wait never completed")
	}
}

func waitForAddrInStream(t *testing.T, s <-chan ma.Multiaddr, expected ma.Multiaddr, timeout time.Duration, failMsg string) {
	t.Helper()
	for {
		select {
		case addr := <-s:
			if addr.Equal(expected) {
				return
			}
			continue
		case <-time.After(timeout):
			t.Fatalf(failMsg)
		}
	}
}

func waitForDisconnectNotification(swarm *swarm.Swarm) <-chan struct{} {
	done := make(chan struct{})
	var once sync.Once
	var nb *network.NotifyBundle
	nb = &network.NotifyBundle{
		DisconnectedF: func(n network.Network, c network.Conn) {
			once.Do(func() {
				go func() {
					swarm.StopNotify(nb)
					close(done)
				}()
			})
		},
	}
	swarm.Notify(nb)

	return done
}
