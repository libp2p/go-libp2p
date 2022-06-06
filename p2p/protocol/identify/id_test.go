package identify_test

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	blhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	pb "github.com/libp2p/go-libp2p/p2p/protocol/identify/pb"

	ic "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/libp2p/go-libp2p-core/record"
	coretest "github.com/libp2p/go-libp2p-core/test"

	"github.com/libp2p/go-eventbus"
	"github.com/libp2p/go-libp2p-peerstore/pstoremem"

	mockClock "github.com/benbjohnson/clock"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-msgio/protoio"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	logging.SetLogLevel("net/identify", "debug")
}

func testKnowsAddrs(t *testing.T, h host.Host, p peer.ID, expected []ma.Multiaddr) {
	t.Helper()
	require.True(t, assert.ElementsMatchf(t, expected, h.Peerstore().Addrs(p), fmt.Sprintf("%s did not have addr for %s", h.ID(), p)))
}

func testHasCertifiedAddrs(t *testing.T, h host.Host, p peer.ID, expected []ma.Multiaddr) {
	t.Helper()
	cab, ok := peerstore.GetCertifiedAddrBook(h.Peerstore())
	if !ok {
		t.Error("expected peerstore to implement CertifiedAddrBook")
	}
	recordEnvelope := cab.GetPeerRecord(p)
	if recordEnvelope == nil {
		if len(expected) == 0 {
			return
		}
		t.Fatalf("peerstore has no signed record for peer %s", p)
	}
	r, err := recordEnvelope.Record()
	if err != nil {
		t.Error("Error unwrapping signed PeerRecord from envelope", err)
	}
	rec, ok := r.(*peer.PeerRecord)
	if !ok {
		t.Error("unexpected record type")
	}
	require.True(t, assert.ElementsMatchf(t, expected, rec.Addrs, fmt.Sprintf("%s did not have certified addr for %s", h.ID(), p)))
}

func testHasProtocolVersions(t *testing.T, h host.Host, p peer.ID) {
	v, err := h.Peerstore().Get(p, "ProtocolVersion")
	if v == nil {
		t.Error("no protocol version")
		return
	}
	if v.(string) != identify.LibP2PVersion {
		t.Error("protocol mismatch", err)
	}
	v, err = h.Peerstore().Get(p, "AgentVersion")
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

func getSignedRecord(t *testing.T, h host.Host, p peer.ID) *record.Envelope {
	cab, ok := peerstore.GetCertifiedAddrBook(h.Peerstore())
	require.True(t, ok)
	rec := cab.GetPeerRecord(p)
	return rec
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

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)
	defer ids2.Close()

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
	testKnowsAddrs(t, h1, h2p, h2.Addrs())                       // has them
	testHasCertifiedAddrs(t, h1, h2p, h2.Peerstore().Addrs(h2p)) // should have signed addrs also
	testHasProtocolVersions(t, h1, h2p)
	testHasPublicKey(t, h1, h2p, h2.Peerstore().PubKey(h2p)) // h1 should have h2's public key

	// now, this wait we do have to do. it's the wait for the Listening side
	// to be done identifying the connection.
	c := h2.Network().ConnsToPeer(h1.ID())
	require.NotEmpty(t, c, "should have connection by now at least.")
	ids2.IdentifyConn(c[0])

	// and the protocol versions.
	t.Log("test peer2 has peer1 addrs correctly")
	testKnowsAddrs(t, h2, h1p, h1.Addrs()) // has them
	testHasCertifiedAddrs(t, h2, h1p, h1.Peerstore().Addrs(h1p))
	testHasProtocolVersions(t, h2, h1p)
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
	testHasCertifiedAddrs(t, h1, h2p, h2.Peerstore().Addrs(h2p))
	testHasCertifiedAddrs(t, h2, h1p, h1.Peerstore().Addrs(h1p))

	<-sentDisconnect1
	<-sentDisconnect2

	// the addrs had their TTLs reduced on disconnect, and
	// will be forgotten soon after
	t.Log("testing addrs after TTL expiration")
	clk.Add(time.Second)
	testKnowsAddrs(t, h1, h2p, []ma.Multiaddr{})
	testKnowsAddrs(t, h2, h1p, []ma.Multiaddr{})
	testHasCertifiedAddrs(t, h1, h2p, []ma.Multiaddr{})
	testHasCertifiedAddrs(t, h2, h1p, []ma.Multiaddr{})

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

	ids2, err := identify.NewIDService(p2)
	require.NoError(t, err)

	ids3, err := identify.NewIDService(p3)
	require.NoError(t, err)

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

func TestIdentifyDeltaOnProtocolChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h2.Close()
	defer h1.Close()

	h2.SetStreamHandler(protocol.TestingID, func(_ network.Stream) {})

	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)

	defer func() {
		ids1.Close()
		ids2.Close()
	}()

	idComplete, err := h1.EventBus().Subscribe(&event.EvtPeerIdentificationCompleted{})
	require.NoError(t, err)
	defer idComplete.Close()
	idFailed, err := h1.EventBus().Subscribe(&event.EvtPeerIdentificationFailed{})
	require.NoError(t, err)
	defer idFailed.Close()

	if err := h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}); err != nil {
		t.Fatal(err)
	}

	conn := h1.Network().ConnsToPeer(h2.ID())[0]
	select {
	case <-ids1.IdentifyWait(conn):
	case <-time.After(5 * time.Second):
		t.Fatal("took over 5 seconds to identify")
	}

	select {
	case <-idComplete.Out():
	case evt := <-idFailed.Out():
		t.Fatalf("Failed to identify: %v", evt.(event.EvtPeerIdentificationFailed).Reason)
	case <-time.After(5 * time.Second):
		t.Fatal("Missing id event")
	}

	protos, err := h1.Peerstore().GetProtocols(h2.ID())
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(protos)
	if sort.SearchStrings(protos, string(protocol.TestingID)) == len(protos) {
		t.Fatalf("expected peer 1 to know that peer 2 speaks the Test protocol amongst others")
	}

	// set up a subscriber to listen to peer protocol updated events in h1. We expect to receive events from h2
	// as protocols are added and removed.
	sub, err := h1.EventBus().Subscribe(&event.EvtPeerProtocolsUpdated{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	h1ProtocolsUpdates, err := h1.EventBus().Subscribe(&event.EvtPeerProtocolsUpdated{})
	require.NoError(t, err)
	defer h1ProtocolsUpdates.Close()

	waitForDelta := make(chan struct{})
	go func() {
		expectedCount := 2
		for expectedCount > 0 {
			evt := <-h1ProtocolsUpdates.Out()
			expectedCount -= len(evt.(event.EvtPeerProtocolsUpdated).Added)
		}
		close(waitForDelta)
	}()

	// add two new protocols in h2 and wait for identify to send deltas.
	h2.SetStreamHandler(protocol.ID("foo"), func(_ network.Stream) {})
	h2.SetStreamHandler(protocol.ID("bar"), func(_ network.Stream) {})

	recvWithTimeout(t, waitForDelta, 10*time.Second, "Timed out waiting to read protocol ids from the wire")

	protos, err = h1.Peerstore().GetProtocols(h2.ID())
	require.NoError(t, err)

	have := make(map[string]bool, len(protos))
	for _, p := range protos {
		have[p] = true
	}
	require.True(t, have["foo"])
	require.True(t, have["bar"])

	// remove one of the newly added protocols from h2, and wait for identify to send the delta.
	h2.RemoveStreamHandler(protocol.ID("bar"))

	waitForDelta = make(chan struct{})
	go func() {
		expectedCount := 1
		for expectedCount > 0 {
			evt := <-h1ProtocolsUpdates.Out()
			expectedCount -= len(evt.(event.EvtPeerProtocolsUpdated).Removed)
		}
		close(waitForDelta)
	}()

	// check that h1 now has forgotten about h2's bar protocol.
	recvWithTimeout(t, waitForDelta, 10*time.Second, "timed out waiting for protocol to be removed")
	protos, err = h1.Peerstore().GetProtocols(h2.ID())
	require.NoError(t, err)
	have = make(map[string]bool, len(protos))
	for _, p := range protos {
		have[p] = true
	}
	require.True(t, have["foo"])
	require.False(t, have["bar"])

	// make sure that h1 emitted events in the eventbus for h2's protocol updates.
	done := make(chan struct{})

	var lk sync.Mutex
	var added []string
	var removed []string
	var success bool

	go func() {
		defer close(done)
		for {
			select {
			case <-time.After(5 * time.Second):
				return
			case e, ok := <-sub.Out():
				if !ok {
					return
				}
				evt := e.(event.EvtPeerProtocolsUpdated)
				lk.Lock()
				added = append(added, protocol.ConvertToStrings(evt.Added)...)
				removed = append(removed, protocol.ConvertToStrings(evt.Removed)...)
				sort.Strings(added)
				sort.Strings(removed)
				if reflect.DeepEqual(added, []string{"bar", "foo"}) &&
					reflect.DeepEqual(removed, []string{"bar"}) {
					success = true
					lk.Unlock()
					return
				}
				lk.Unlock()
			}
		}
	}()

	<-done

	lk.Lock()
	defer lk.Unlock()
	require.True(t, success, "did not get correct peer protocol updated events")
}

// TestIdentifyDeltaWhileIdentifyingConn tests that the host waits to push delta updates if an identify is ongoing.
func TestIdentifyDeltaWhileIdentifyingConn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h2.Close()
	defer h1.Close()

	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)

	defer ids1.Close()
	defer ids2.Close()

	// replace the original identify handler by one that blocks until we close the block channel.
	// this allows us to control how long identify runs.
	block := make(chan struct{})
	handler := func(s network.Stream) {
		<-block
		w := protoio.NewDelimitedWriter(s)
		w.WriteMsg(&pb.Identify{Protocols: h1.Mux().Protocols()})
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
	go func() {
		ids2.IdentifyConn(conn)
	}()

	<-time.After(500 * time.Millisecond)

	// subscribe to events in h1; after identify h1 should receive the delta from h2 and publish an event in the bus.
	sub, err := h1.EventBus().Subscribe(&event.EvtPeerProtocolsUpdated{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	// add a handler in h2; the delta to h1 will queue until we're done identifying h1.
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
	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)

	defer ids1.Close()
	defer ids2.Close()

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

	found := false
	addrs := h2.Peerstore().Addrs(h1p)
	for _, ad := range addrs {
		if ad.Equal(lad) {
			found = true
		}
	}
	require.True(t, found)
	require.NotNil(t, getSignedRecord(t, h2, h1p))

	// change addr on host2 and ensure host 1 gets a pus
	lad = ma.StringCast("/ip4/127.0.0.1/tcp/1235")
	require.NoError(t, h2.Network().Listen(lad))
	require.Contains(t, h2.Addrs(), lad)
	h1AddrStream := h1.Peerstore().AddrStream(ctx, h2p)
	emitAddrChangeEvt(t, h2)

	// Wait for h1 to process the new addr
	waitForAddrInStream(t, h1AddrStream, lad, 10*time.Second, "h1 did not receive addr change")

	found = false
	addrs = h1.Peerstore().Addrs(h2p)
	for _, ad := range addrs {
		if ad.Equal(lad) {
			found = true
		}
	}
	require.True(t, found)
	require.NotNil(t, getSignedRecord(t, h1, h2p))

	// change addr on host2 again
	lad2 := ma.StringCast("/ip4/127.0.0.1/tcp/1236")
	require.NoError(t, h2.Network().Listen(lad2))
	require.Contains(t, h2.Addrs(), lad2)
	emitAddrChangeEvt(t, h2)

	// Wait for h1 to process the new addr
	waitForAddrInStream(t, h1AddrStream, lad2, 10*time.Second, "h1 did not receive addr change")

	found = false
	addrs = h1.Peerstore().Addrs(h2p)
	for _, ad := range addrs {
		if ad.Equal(lad2) {
			found = true
		}
	}
	require.True(t, found)
	require.NotNil(t, getSignedRecord(t, h1, h2p))
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

func TestSendPushIfDeltaNotSupported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h2.Close()
	defer h1.Close()

	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)

	defer func() {
		ids1.Close()
		ids2.Close()
	}()

	err = h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
	require.NoError(t, err)

	// wait for them to Identify each other
	ids1.IdentifyConn(h1.Network().ConnsToPeer(h2.ID())[0])
	ids2.IdentifyConn(h2.Network().ConnsToPeer(h1.ID())[0])

	// h1 knows h2 speaks Delta
	sup, err := h1.Peerstore().SupportsProtocols(h2.ID(), []string{identify.IDDelta}...)
	require.NoError(t, err)
	require.Equal(t, []string{identify.IDDelta}, sup)

	// h2 stops supporting Delta and that information flows to h1
	h2.RemoveStreamHandler(identify.IDDelta)

	require.Eventually(t, func() bool {
		sup, err := h1.Peerstore().SupportsProtocols(h2.ID(), []string{identify.IDDelta}...)
		return err == nil && len(sup) == 0
	}, time.Second, 10*time.Millisecond)

	// h1 starts listening on a new protocol and h2 finds out about that through a push
	h1.SetStreamHandler("rand", func(network.Stream) {})
	require.Eventually(t, func() bool {
		sup, err := h2.Peerstore().SupportsProtocols(h1.ID(), []string{"rand"}...)
		return err == nil && len(sup) == 1 && sup[0] == "rand"
	}, time.Second, 10*time.Millisecond)

	// h1 stops listening on a protocol and h2 finds out about it via a push
	h1.RemoveStreamHandler("rand")
	require.Eventually(t, func() bool {
		sup, err := h2.Peerstore().SupportsProtocols(h1.ID(), []string{"rand"}...)
		return err == nil && len(sup) == 0
	}, time.Second, 10*time.Millisecond)
}

func TestLargeIdentifyMessage(t *testing.T) {
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
		r := fmt.Sprintf("rand%d", i)
		h1.SetStreamHandler(protocol.ID(r), func(network.Stream) {})
		h2.SetStreamHandler(protocol.ID(r), func(network.Stream) {})
	}

	h1p := h1.ID()
	h2p := h2.ID()

	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)
	defer ids1.Close()

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)
	defer ids2.Close()

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
	testKnowsAddrs(t, h1, h2p, h2.Addrs())                       // has them
	testHasCertifiedAddrs(t, h1, h2p, h2.Peerstore().Addrs(h2p)) // should have signed addrs also
	testHasProtocolVersions(t, h1, h2p)
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
	testHasCertifiedAddrs(t, h2, h1p, h1.Peerstore().Addrs(h1p))
	testHasProtocolVersions(t, h2, h1p)
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
	testHasCertifiedAddrs(t, h1, h2p, h2.Peerstore().Addrs(h2p))
	testHasCertifiedAddrs(t, h2, h1p, h1.Peerstore().Addrs(h1p))

	<-sentDisconnect1
	<-sentDisconnect2

	// the addrs had their TTLs reduced on disconnect, and
	// will be forgotten soon after
	t.Log("testing addrs after TTL expiration")
	clk.Add(time.Second)
	testKnowsAddrs(t, h1, h2p, []ma.Multiaddr{})
	testKnowsAddrs(t, h2, h1p, []ma.Multiaddr{})
	testHasCertifiedAddrs(t, h1, h2p, []ma.Multiaddr{})
	testHasCertifiedAddrs(t, h2, h1p, []ma.Multiaddr{})

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
		r := fmt.Sprintf("rand%d", i)
		h1.SetStreamHandler(protocol.ID(r), func(network.Stream) {})
		h2.SetStreamHandler(protocol.ID(r), func(network.Stream) {})
	}

	h1p := h1.ID()
	h2p := h2.ID()

	ids1, err := identify.NewIDService(h1)
	require.NoError(t, err)

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)

	defer ids1.Close()
	defer ids2.Close()

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
		addrs := h2.Peerstore().Addrs(h1p)
		for _, ad := range addrs {
			if ad.Equal(lad) {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)
	require.NotNil(t, getSignedRecord(t, h2, h1p))

	// change addr on host2 and ensure host 1 gets a pus
	lad = ma.StringCast("/ip4/127.0.0.1/tcp/1235")
	require.NoError(t, h2.Network().Listen(lad))
	require.Contains(t, h2.Addrs(), lad)
	emitAddrChangeEvt(t, h2)

	require.Eventually(t, func() bool {
		addrs := h1.Peerstore().Addrs(h2p)
		for _, ad := range addrs {
			if ad.Equal(lad) {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)
	testHasCertifiedAddrs(t, h1, h2p, h2.Addrs())

	// change addr on host2 again
	lad2 := ma.StringCast("/ip4/127.0.0.1/tcp/1236")
	require.NoError(t, h2.Network().Listen(lad2))
	require.Contains(t, h2.Addrs(), lad2)
	emitAddrChangeEvt(t, h2)

	require.Eventually(t, func() bool {
		addrs := h1.Peerstore().Addrs(h2p)
		for _, ad := range addrs {
			if ad.Equal(lad2) {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)
	testHasCertifiedAddrs(t, h2, h1p, h1.Addrs())
}

func TestIdentifyResponseReadTimeout(t *testing.T) {
	timeout := identify.StreamReadTimeout
	identify.StreamReadTimeout = 100 * time.Millisecond
	defer func() {
		identify.StreamReadTimeout = timeout
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

	ids2, err := identify.NewIDService(h2)
	require.NoError(t, err)

	defer ids1.Close()
	defer ids2.Close()
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
	timeout := identify.StreamReadTimeout
	identify.StreamReadTimeout = 100 * time.Millisecond
	defer func() {
		identify.StreamReadTimeout = timeout
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	protocols := []protocol.ID{identify.IDPush, identify.IDDelta}

	for _, p := range protocols {
		h1 := blhost.NewBlankHost(swarmt.GenSwarm(t))
		h2 := blhost.NewBlankHost(swarmt.GenSwarm(t))
		defer h1.Close()
		defer h2.Close()

		ids1, err := identify.NewIDService(h1)
		require.NoError(t, err)

		ids2, err := identify.NewIDService(h2)
		require.NoError(t, err)

		defer ids1.Close()
		defer ids2.Close()

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
		}, 1*time.Second, 200*time.Millisecond)
	}
}

func recvWithTimeout(t *testing.T, s <-chan struct{}, timeout time.Duration, failMsg string) {
	t.Helper()
	select {
	case <-s:
		return
	case <-time.After(timeout):
		t.Fatalf("Hit time while waiting to recv from channel: %s", failMsg)
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
