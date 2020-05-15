package basichost

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/helpers"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/libp2p/go-libp2p-core/record"
	"github.com/libp2p/go-libp2p-core/test"

	"github.com/libp2p/go-eventbus"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"

	ma "github.com/multiformats/go-multiaddr"
	madns "github.com/multiformats/go-multiaddr-dns"
	"github.com/stretchr/testify/require"
)

func TestHostDoubleClose(t *testing.T) {
	ctx := context.Background()
	h1 := New(swarmt.GenSwarm(t, ctx))
	h1.Close()
	h1.Close()
}

func TestHostSimple(t *testing.T) {
	ctx := context.Background()
	h1 := New(swarmt.GenSwarm(t, ctx))
	h2 := New(swarmt.GenSwarm(t, ctx))
	defer h1.Close()
	defer h2.Close()

	h2pi := h2.Peerstore().PeerInfo(h2.ID())
	if err := h1.Connect(ctx, h2pi); err != nil {
		t.Fatal(err)
	}

	piper, pipew := io.Pipe()
	h2.SetStreamHandler(protocol.TestingID, func(s network.Stream) {
		defer s.Close()
		w := io.MultiWriter(s, pipew)
		io.Copy(w, s) // mirror everything
	})

	s, err := h1.NewStream(ctx, h2pi.ID, protocol.TestingID)
	if err != nil {
		t.Fatal(err)
	}

	// write to the stream
	buf1 := []byte("abcdefghijkl")
	if _, err := s.Write(buf1); err != nil {
		t.Fatal(err)
	}

	// get it from the stream (echoed)
	buf2 := make([]byte, len(buf1))
	if _, err := io.ReadFull(s, buf2); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf1, buf2) {
		t.Fatalf("buf1 != buf2 -- %x != %x", buf1, buf2)
	}

	// get it from the pipe (tee)
	buf3 := make([]byte, len(buf1))
	if _, err := io.ReadFull(piper, buf3); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf1, buf3) {
		t.Fatalf("buf1 != buf3 -- %x != %x", buf1, buf3)
	}
}

func TestMultipleClose(t *testing.T) {
	ctx := context.Background()
	h := New(swarmt.GenSwarm(t, ctx))

	require.NoError(t, h.Close())
	require.NoError(t, h.Close())
	require.NoError(t, h.Close())

}

func TestProtocolHandlerEvents(t *testing.T) {
	ctx := context.Background()
	h := New(swarmt.GenSwarm(t, ctx))
	defer h.Close()

	sub, err := h.EventBus().Subscribe(&event.EvtLocalProtocolsUpdated{}, eventbus.BufSize(16))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	// the identify service adds new protocol handlers shortly after the host
	// starts. this helps us filter those events out, since they're unrelated
	// to the test.
	isIdentify := func(evt event.EvtLocalProtocolsUpdated) bool {
		for _, p := range evt.Added {
			if p == identify.ID || p == identify.IDPush {
				return true
			}
		}
		return false
	}

	nextEvent := func() event.EvtLocalProtocolsUpdated {
		for {
			select {
			case evt := <-sub.Out():
				next := evt.(event.EvtLocalProtocolsUpdated)
				if isIdentify(next) {
					continue
				}
				return next
			case <-time.After(5 * time.Second):
				t.Fatal("event not received in 5 seconds")
			}
		}
	}

	assert := func(added, removed []protocol.ID) {
		next := nextEvent()
		if !reflect.DeepEqual(added, next.Added) {
			t.Errorf("expected added: %v; received: %v", added, next.Added)
		}
		if !reflect.DeepEqual(removed, next.Removed) {
			t.Errorf("expected removed: %v; received: %v", removed, next.Removed)
		}
	}

	h.SetStreamHandler(protocol.TestingID, func(s network.Stream) {})
	assert([]protocol.ID{protocol.TestingID}, nil)
	h.SetStreamHandler(protocol.ID("foo"), func(s network.Stream) {})
	assert([]protocol.ID{protocol.ID("foo")}, nil)
	h.RemoveStreamHandler(protocol.TestingID)
	assert(nil, []protocol.ID{protocol.TestingID})
}

func TestHostAddrsFactory(t *testing.T) {
	maddr := ma.StringCast("/ip4/1.2.3.4/tcp/1234")
	addrsFactory := func(addrs []ma.Multiaddr) []ma.Multiaddr {
		return []ma.Multiaddr{maddr}
	}

	ctx := context.Background()
	h := New(swarmt.GenSwarm(t, ctx), AddrsFactory(addrsFactory))
	defer h.Close()

	addrs := h.Addrs()
	if len(addrs) != 1 {
		t.Fatalf("expected 1 addr, got %d", len(addrs))
	}
	if !addrs[0].Equal(maddr) {
		t.Fatalf("expected %s, got %s", maddr.String(), addrs[0].String())
	}
}

func getHostPair(ctx context.Context, t *testing.T) (host.Host, host.Host) {
	t.Helper()

	h1 := New(swarmt.GenSwarm(t, ctx))
	h2 := New(swarmt.GenSwarm(t, ctx))

	h2pi := h2.Peerstore().PeerInfo(h2.ID())
	if err := h1.Connect(ctx, h2pi); err != nil {
		t.Fatal(err)
	}

	return h1, h2
}

func assertWait(t *testing.T, c chan protocol.ID, exp protocol.ID) {
	t.Helper()
	select {
	case proto := <-c:
		if proto != exp {
			t.Fatal("should have connected on ", exp)
		}
	case <-time.After(time.Second * 5):
		t.Fatal("timeout waiting for stream")
	}
}

func TestHostProtoPreference(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1, h2 := getHostPair(ctx, t)
	defer h1.Close()
	defer h2.Close()

	protoOld := protocol.ID("/testing")
	protoNew := protocol.ID("/testing/1.1.0")
	protoMinor := protocol.ID("/testing/1.2.0")

	connectedOn := make(chan protocol.ID)

	handler := func(s network.Stream) {
		connectedOn <- s.Protocol()
		s.Close()
	}

	h1.SetStreamHandler(protoOld, handler)

	s, err := h2.NewStream(ctx, h1.ID(), protoMinor, protoNew, protoOld)
	if err != nil {
		t.Fatal(err)
	}

	// force the lazy negotiation to complete
	_, err = s.Write(nil)
	if err != nil {
		t.Fatal(err)
	}

	assertWait(t, connectedOn, protoOld)
	s.Close()

	mfunc, err := helpers.MultistreamSemverMatcher(protoMinor)
	if err != nil {
		t.Fatal(err)
	}

	h1.SetStreamHandlerMatch(protoMinor, mfunc, handler)

	// remembered preference will be chosen first, even when the other side newly supports it
	s2, err := h2.NewStream(ctx, h1.ID(), protoMinor, protoNew, protoOld)
	if err != nil {
		t.Fatal(err)
	}

	// required to force 'lazy' handshake
	_, err = s2.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}

	// XXX: This is racy now that we push protocol updates. If this tests
	// fails, try allowing both protoOld and protoMinor.
	assertWait(t, connectedOn, protoOld)

	s2.Close()

	s3, err := h2.NewStream(ctx, h1.ID(), protoMinor)
	if err != nil {
		t.Fatal(err)
	}

	// Force a lazy handshake as we may have received a protocol update by this point.
	_, err = s3.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}

	assertWait(t, connectedOn, protoMinor)
	s3.Close()
}

func TestHostProtoMismatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1, h2 := getHostPair(ctx, t)
	defer h1.Close()
	defer h2.Close()

	h1.SetStreamHandler("/super", func(s network.Stream) {
		t.Error("shouldnt get here")
		s.Reset()
	})

	_, err := h2.NewStream(ctx, h1.ID(), "/foo", "/bar", "/baz/1.0.0")
	if err == nil {
		t.Fatal("expected new stream to fail")
	}
}

func TestHostProtoPreknowledge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := New(swarmt.GenSwarm(t, ctx))
	h2 := New(swarmt.GenSwarm(t, ctx))

	conn := make(chan protocol.ID)
	handler := func(s network.Stream) {
		conn <- s.Protocol()
		s.Close()
	}

	h1.SetStreamHandler("/super", handler)

	h2pi := h2.Peerstore().PeerInfo(h2.ID())
	if err := h1.Connect(ctx, h2pi); err != nil {
		t.Fatal(err)
	}
	defer h1.Close()
	defer h2.Close()

	// wait for identify handshake to finish completely
	select {
	case <-h1.ids.IdentifyWait(h1.Network().ConnsToPeer(h2.ID())[0]):
	case <-time.After(time.Second * 5):
		t.Fatal("timed out waiting for identify")
	}

	select {
	case <-h2.ids.IdentifyWait(h2.Network().ConnsToPeer(h1.ID())[0]):
	case <-time.After(time.Second * 5):
		t.Fatal("timed out waiting for identify")
	}

	h1.SetStreamHandler("/foo", handler)

	s, err := h2.NewStream(ctx, h1.ID(), "/foo", "/bar", "/super")
	if err != nil {
		t.Fatal(err)
	}

	select {
	case p := <-conn:
		t.Fatal("shouldnt have gotten connection yet, we should have a lazy stream: ", p)
	case <-time.After(time.Millisecond * 50):
	}

	_, err = s.Read(nil)
	if err != nil {
		t.Fatal(err)
	}

	assertWait(t, conn, "/super")

	s.Close()
}

func TestNewDialOld(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1, h2 := getHostPair(ctx, t)
	defer h1.Close()
	defer h2.Close()

	connectedOn := make(chan protocol.ID)
	h1.SetStreamHandler("/testing", func(s network.Stream) {
		connectedOn <- s.Protocol()
		s.Close()
	})

	s, err := h2.NewStream(ctx, h1.ID(), "/testing/1.0.0", "/testing")
	if err != nil {
		t.Fatal(err)
	}

	// force the lazy negotiation to complete
	_, err = s.Write(nil)
	if err != nil {
		t.Fatal(err)
	}

	assertWait(t, connectedOn, "/testing")

	if s.Protocol() != "/testing" {
		t.Fatal("shoould have gotten /testing")
	}

	s.Close()
}

func TestProtoDowngrade(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1, h2 := getHostPair(ctx, t)
	defer h1.Close()
	defer h2.Close()

	connectedOn := make(chan protocol.ID)
	h1.SetStreamHandler("/testing/1.0.0", func(s network.Stream) {
		connectedOn <- s.Protocol()
		s.Close()
	})

	s, err := h2.NewStream(ctx, h1.ID(), "/testing/1.0.0", "/testing")
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Write(nil)
	if err != nil {
		t.Fatal(err)
	}

	assertWait(t, connectedOn, "/testing/1.0.0")

	if s.Protocol() != "/testing/1.0.0" {
		t.Fatal("shoould have gotten /testing")
	}
	s.Close()

	h1.Network().ConnsToPeer(h2.ID())[0].Close()

	time.Sleep(time.Millisecond * 50) // allow notifications to propagate
	h1.RemoveStreamHandler("/testing/1.0.0")
	h1.SetStreamHandler("/testing", func(s network.Stream) {
		connectedOn <- s.Protocol()
		s.Close()
	})

	h2pi := h2.Peerstore().PeerInfo(h2.ID())
	if err := h1.Connect(ctx, h2pi); err != nil {
		t.Fatal(err)
	}

	s2, err := h2.NewStream(ctx, h1.ID(), "/testing/1.0.0", "/testing")
	if err != nil {
		t.Fatal(err)
	}

	_, err = s2.Write(nil)
	if err != nil {
		t.Fatal(err)
	}

	assertWait(t, connectedOn, "/testing")

	if s2.Protocol() != "/testing" {
		t.Fatal("shoould have gotten /testing")
	}
	s2.Close()

}

func TestAddrResolution(t *testing.T) {
	ctx := context.Background()

	p1, err := test.RandPeerID()
	if err != nil {
		t.Error(err)
	}
	p2, err := test.RandPeerID()
	if err != nil {
		t.Error(err)
	}
	addr1 := ma.StringCast("/dnsaddr/example.com")
	addr2 := ma.StringCast("/ip4/192.0.2.1/tcp/123")
	p2paddr1 := ma.StringCast("/dnsaddr/example.com/p2p/" + p1.Pretty())
	p2paddr2 := ma.StringCast("/ip4/192.0.2.1/tcp/123/p2p/" + p1.Pretty())
	p2paddr3 := ma.StringCast("/ip4/192.0.2.1/tcp/123/p2p/" + p2.Pretty())

	backend := &madns.MockBackend{
		TXT: map[string][]string{"_dnsaddr.example.com": []string{
			"dnsaddr=" + p2paddr2.String(), "dnsaddr=" + p2paddr3.String(),
		}},
	}
	resolver := &madns.Resolver{Backend: backend}

	h := New(swarmt.GenSwarm(t, ctx), resolver)
	defer h.Close()

	pi, err := peer.AddrInfoFromP2pAddr(p2paddr1)
	if err != nil {
		t.Error(err)
	}

	tctx, cancel := context.WithTimeout(ctx, time.Millisecond*100)
	defer cancel()
	_ = h.Connect(tctx, *pi)

	addrs := h.Peerstore().Addrs(pi.ID)

	require.Len(t, addrs, 2)
	require.Contains(t, addrs, addr1)
	require.Contains(t, addrs, addr2)
}

func TestAddrResolutionRecursive(t *testing.T) {
	ctx := context.Background()

	p1, err := test.RandPeerID()
	if err != nil {
		t.Error(err)
	}
	p2, err := test.RandPeerID()
	if err != nil {
		t.Error(err)
	}
	addr1 := ma.StringCast("/dnsaddr/example.com")
	addr2 := ma.StringCast("/ip4/192.0.2.1/tcp/123")
	p2paddr1 := ma.StringCast("/dnsaddr/example.com/p2p/" + p1.Pretty())
	p2paddr2 := ma.StringCast("/dnsaddr/example.com/p2p/" + p2.Pretty())
	p2paddr1i := ma.StringCast("/dnsaddr/foo.example.com/p2p/" + p1.Pretty())
	p2paddr2i := ma.StringCast("/dnsaddr/bar.example.com/p2p/" + p2.Pretty())
	p2paddr1f := ma.StringCast("/ip4/192.0.2.1/tcp/123/p2p/" + p1.Pretty())

	backend := &madns.MockBackend{
		TXT: map[string][]string{
			"_dnsaddr.example.com": []string{
				"dnsaddr=" + p2paddr1i.String(),
				"dnsaddr=" + p2paddr2i.String(),
			},
			"_dnsaddr.foo.example.com": []string{
				"dnsaddr=" + p2paddr1f.String(),
			},
			"_dnsaddr.bar.example.com": []string{
				"dnsaddr=" + p2paddr2i.String(),
			},
		},
	}
	resolver := &madns.Resolver{Backend: backend}

	h := New(swarmt.GenSwarm(t, ctx), resolver)
	defer h.Close()

	pi1, err := peer.AddrInfoFromP2pAddr(p2paddr1)
	if err != nil {
		t.Error(err)
	}

	tctx, cancel := context.WithTimeout(ctx, time.Millisecond*100)
	defer cancel()
	_ = h.Connect(tctx, *pi1)

	addrs1 := h.Peerstore().Addrs(pi1.ID)
	require.Len(t, addrs1, 2)
	require.Contains(t, addrs1, addr1)
	require.Contains(t, addrs1, addr2)

	pi2, err := peer.AddrInfoFromP2pAddr(p2paddr2)
	if err != nil {
		t.Error(err)
	}

	_ = h.Connect(tctx, *pi2)

	addrs2 := h.Peerstore().Addrs(pi2.ID)
	require.Len(t, addrs2, 1)
	require.Contains(t, addrs2, addr1)
}

func TestAddrChangeImmediatelyIfAddressNonEmpty(t *testing.T) {
	ctx := context.Background()
	taddrs := []ma.Multiaddr{ma.StringCast("/ip4/1.2.3.4/tcp/1234")}

	h := New(swarmt.GenSwarm(t, ctx), AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
		return taddrs
	}))
	defer h.Close()

	sub, err := h.EventBus().Subscribe(&event.EvtLocalAddressesUpdated{})
	if err != nil {
		t.Error(err)
	}
	defer sub.Close()
	// wait for the host background thread to start
	time.Sleep(1 * time.Second)

	expected := event.EvtLocalAddressesUpdated{
		Diffs: true,
		Current: []event.UpdatedAddress{
			{Action: event.Added, Address: ma.StringCast("/ip4/1.2.3.4/tcp/1234")},
		},
		Removed: []event.UpdatedAddress{}}

	// assert we get expected event
	evt := waitForAddrChangeEvent(ctx, sub, t)
	if !updatedAddrEventsEqual(expected, evt) {
		t.Errorf("change events not equal: \n\texpected: %v \n\tactual: %v", expected, evt)
	}

	// assert it's on the signed record
	rc := peerRecordFromEnvelope(t, evt.SignedPeerRecord)
	require.Equal(t, taddrs, rc.Addrs)

	// assert it's in the peerstore
	ev := h.Peerstore().(peerstore.CertifiedAddrBook).GetPeerRecord(h.ID())
	require.NotNil(t, ev)
	rc = peerRecordFromEnvelope(t, ev)
	require.Equal(t, taddrs, rc.Addrs)
}

func TestHostAddrChangeDetection(t *testing.T) {
	// This test uses the address factory to provide several
	// sets of listen addresses for the host. It advances through
	// the sets by changing the currentAddrSet index var below.
	addrSets := [][]ma.Multiaddr{
		{},
		{ma.StringCast("/ip4/1.2.3.4/tcp/1234")},
		{ma.StringCast("/ip4/1.2.3.4/tcp/1234"), ma.StringCast("/ip4/2.3.4.5/tcp/1234")},
		{ma.StringCast("/ip4/2.3.4.5/tcp/1234"), ma.StringCast("/ip4/3.4.5.6/tcp/4321")},
	}

	// The events we expect the host to emit when SignalAddressChange is called
	// and the changes between addr sets are detected
	expectedEvents := []event.EvtLocalAddressesUpdated{
		{
			Diffs: true,
			Current: []event.UpdatedAddress{
				{Action: event.Added, Address: ma.StringCast("/ip4/1.2.3.4/tcp/1234")},
			},
			Removed: []event.UpdatedAddress{},
		},
		{
			Diffs: true,
			Current: []event.UpdatedAddress{
				{Action: event.Maintained, Address: ma.StringCast("/ip4/1.2.3.4/tcp/1234")},
				{Action: event.Added, Address: ma.StringCast("/ip4/2.3.4.5/tcp/1234")},
			},
			Removed: []event.UpdatedAddress{},
		},
		{
			Diffs: true,
			Current: []event.UpdatedAddress{
				{Action: event.Added, Address: ma.StringCast("/ip4/3.4.5.6/tcp/4321")},
				{Action: event.Maintained, Address: ma.StringCast("/ip4/2.3.4.5/tcp/1234")},
			},
			Removed: []event.UpdatedAddress{
				{Action: event.Removed, Address: ma.StringCast("/ip4/1.2.3.4/tcp/1234")},
			},
		},
	}

	var lk sync.Mutex
	currentAddrSet := 0
	addrsFactory := func(addrs []ma.Multiaddr) []ma.Multiaddr {
		lk.Lock()
		defer lk.Unlock()
		return addrSets[currentAddrSet]
	}

	ctx := context.Background()
	h := New(swarmt.GenSwarm(t, ctx), AddrsFactory(addrsFactory))
	defer h.Close()

	sub, err := h.EventBus().Subscribe(&event.EvtLocalAddressesUpdated{}, eventbus.BufSize(10))
	if err != nil {
		t.Error(err)
	}
	defer sub.Close()

	// wait for the host background thread to start
	time.Sleep(1 * time.Second)
	// host should start with no addrs (addrSet 0)
	addrs := h.Addrs()
	if len(addrs) != 0 {
		t.Fatalf("expected 0 addrs, got %d", len(addrs))
	}

	// change addr, signal and assert event
	for i := 1; i < len(addrSets); i++ {
		lk.Lock()
		currentAddrSet = i
		lk.Unlock()
		h.SignalAddressChange()
		evt := waitForAddrChangeEvent(ctx, sub, t)
		if !updatedAddrEventsEqual(expectedEvents[i-1], evt) {
			t.Errorf("change events not equal: \n\texpected: %v \n\tactual: %v", expectedEvents[i-1], evt)
		}

		// assert it's on the signed record
		rc := peerRecordFromEnvelope(t, evt.SignedPeerRecord)
		require.Equal(t, addrSets[i], rc.Addrs)

		// assert it's in the peerstore
		ev := h.Peerstore().(peerstore.CertifiedAddrBook).GetPeerRecord(h.ID())
		require.NotNil(t, ev)
		rc = peerRecordFromEnvelope(t, ev)
		require.Equal(t, addrSets[i], rc.Addrs)
	}
}

func waitForAddrChangeEvent(ctx context.Context, sub event.Subscription, t *testing.T) event.EvtLocalAddressesUpdated {
	for {
		select {
		case evt, more := <-sub.Out():
			if !more {
				t.Fatal("channel should not be closed")
			}
			return evt.(event.EvtLocalAddressesUpdated)
		case <-ctx.Done():
			t.Fatal("context should not have cancelled")
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for address change event")
		}
	}
}

// updatedAddrsEqual is a helper to check whether two lists of
// event.UpdatedAddress have the same contents, ignoring ordering.
func updatedAddrsEqual(a, b []event.UpdatedAddress) bool {
	if len(a) != len(b) {
		return false
	}

	// We can't use an UpdatedAddress directly as a map key, since
	// Multiaddr is an interface, and go won't know how to compare
	// for equality. So we convert to this little struct, which
	// stores the multiaddr as a string.
	type ua struct {
		action  event.AddrAction
		addrStr string
	}
	aSet := make(map[ua]struct{})
	for _, addr := range a {
		k := ua{action: addr.Action, addrStr: string(addr.Address.Bytes())}
		aSet[k] = struct{}{}
	}
	for _, addr := range b {
		k := ua{action: addr.Action, addrStr: string(addr.Address.Bytes())}
		_, ok := aSet[k]
		if !ok {
			return false
		}
	}
	return true
}

// updatedAddrEventsEqual is a helper to check whether two
// event.EvtLocalAddressesUpdated are equal, ignoring the ordering of
// addresses in the inner lists.
func updatedAddrEventsEqual(a, b event.EvtLocalAddressesUpdated) bool {
	return a.Diffs == b.Diffs &&
		updatedAddrsEqual(a.Current, b.Current) &&
		updatedAddrsEqual(a.Removed, b.Removed)
}

func peerRecordFromEnvelope(t *testing.T, ev *record.Envelope) *peer.PeerRecord {
	t.Helper()
	rec, err := ev.Record()
	if err != nil {
		t.Fatalf("error getting PeerRecord from event: %v", err)
		return nil
	}
	peerRec, ok := rec.(*peer.PeerRecord)
	if !ok {
		t.Fatalf("wrong type for peer record")
		return nil
	}
	return peerRec
}
