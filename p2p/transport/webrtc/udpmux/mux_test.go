package udpmux

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getSTUNBindingRequest(ufrag string) *stun.Message {
	msg := stun.New()
	msg.SetType(stun.BindingRequest)
	uattr := stun.RawAttribute{
		Type:  stun.AttrUsername,
		Value: fmt.Appendf(nil, "%s:%s", ufrag, ufrag), // This is the format we expect in our connections
	}
	uattr.AddTo(msg)
	msg.Encode()
	return msg
}

func setupMapping(t *testing.T, ufrag string, from net.PacketConn, m *UDPMux) {
	t.Helper()
	msg := getSTUNBindingRequest(ufrag)
	_, err := from.WriteTo(msg.Raw, m.GetListenAddresses()[0])
	require.NoError(t, err)
}

func getSTUNBindingRequestWithUsername(username string) *stun.Message {
	msg := stun.New()
	msg.SetType(stun.BindingRequest)
	uattr := stun.RawAttribute{
		Type:  stun.AttrUsername,
		Value: []byte(username),
	}
	uattr.AddTo(msg)
	msg.Encode()
	return msg
}

// v1Ufrag builds a valid WebRTC Direct v1 ICE ufrag from a short seed. Tests
// that drive the mux through the real inbound STUN path (via setupMapping) need
// credentials that credentialsFromSTUNMessage accepts; bare seeds like "a" have
// no version prefix and would be dropped.
func v1Ufrag(seed string) string {
	return UfragPrefixV1 + seed
}

// In WebRTC Direct v2 the STUN USERNAME carries distinct server and client
// ufrags ("server_ufrag:client_ufrag"). The mux must key on the server (local)
// ufrag, since that is the ufrag pion uses to retrieve the muxed connection,
// while still surfacing the client ufrag on the Candidate.
func TestAcceptV2DistinctUfrags(t *testing.T) {
	c := newPacketConn(t)
	defer c.Close()
	m := NewUDPMux(c)
	m.Start()
	defer m.Close()

	clientPwd := "browserClientPassword1234"
	serverUfrag := UfragPrefixV2 + clientPwd
	clientUfrag := "browserClientUfrag"

	from := newPacketConn(t)
	msg := getSTUNBindingRequestWithUsername(serverUfrag + ":" + clientUfrag)
	_, err := from.WriteTo(msg.Raw, m.GetListenAddresses()[0])
	require.NoError(t, err)

	cand, err := m.Accept(context.Background())
	require.NoError(t, err)
	require.Equal(t, serverUfrag, cand.LocalUfrag)
	require.Equal(t, clientUfrag, cand.RemoteUfrag)
	// v2 recovers the client's ICE password from the server ufrag.
	require.Equal(t, clientPwd, cand.RemotePwd)
	require.Equal(t, from.LocalAddr(), cand.Addr)
}

// A STUN binding request whose USERNAME fails validation must be dropped before
// the mux allocates a connection or queues a candidate, so nothing reaches the
// listener via Accept.
func TestAcceptRejectsInvalidCredentials(t *testing.T) {
	for _, username := range []string{
		"nocolon",                               // no ':' separator
		":" + v1Ufrag("a"),                      // empty server ufrag
		"ab:" + v1Ufrag("a"),                    // server ufrag too short
		"noprefix:noprefix",                     // valid ice-chars but unknown version
		UfragPrefixV2 + "short:" + v1Ufrag("a"), // v2 recovered password too short
	} {
		t.Run(username, func(t *testing.T) {
			c := newPacketConn(t)
			defer c.Close()
			m := NewUDPMux(c)
			m.Start()
			defer m.Close()

			from := newPacketConn(t)
			msg := getSTUNBindingRequestWithUsername(username)
			_, err := from.WriteTo(msg.Raw, m.GetListenAddresses()[0])
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err = m.Accept(ctx)
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func newPacketConn(t *testing.T) net.PacketConn {
	t.Helper()
	udpPort0 := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
	c, err := net.ListenUDP("udp", udpPort0)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

func TestAccept(t *testing.T) {
	c := newPacketConn(t)
	defer c.Close()
	m := NewUDPMux(c)
	m.Start()
	defer m.Close()

	ufrags := []string{v1Ufrag("a"), v1Ufrag("b"), v1Ufrag("c"), v1Ufrag("d")}
	conns := make([]net.PacketConn, len(ufrags))
	for i, ufrag := range ufrags {
		conns[i] = newPacketConn(t)
		setupMapping(t, ufrag, conns[i], m)
	}
	for i, ufrag := range ufrags {
		c, err := m.Accept(context.Background())
		require.NoError(t, err)
		require.Equal(t, c.LocalUfrag, ufrag)
		require.Equal(t, c.Addr, conns[i].LocalAddr())
	}

	for i, ufrag := range ufrags {
		// should not be accepted
		setupMapping(t, ufrag, conns[i], m)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := m.Accept(ctx)
		require.Error(t, err)

		// should not be accepted
		cc := newPacketConn(t)
		setupMapping(t, ufrag, cc, m)
		ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err = m.Accept(ctx)
		require.Error(t, err)
	}
}

func TestGetConn(t *testing.T) {
	c := newPacketConn(t)
	m := NewUDPMux(c)
	m.Start()
	defer m.Close()

	ufrags := []string{v1Ufrag("a"), v1Ufrag("b"), v1Ufrag("c"), v1Ufrag("d")}
	conns := make([]net.PacketConn, len(ufrags))
	for i, ufrag := range ufrags {
		conns[i] = newPacketConn(t)
		setupMapping(t, ufrag, conns[i], m)
	}
	for i, ufrag := range ufrags {
		c, err := m.Accept(context.Background())
		require.NoError(t, err)
		require.Equal(t, c.LocalUfrag, ufrag)
		require.Equal(t, c.Addr, conns[i].LocalAddr())
	}

	for i, ufrag := range ufrags {
		c, err := m.GetConn(ufrag, conns[i].LocalAddr())
		require.NoError(t, err)
		msg := make([]byte, 100)
		_, _, err = c.ReadFrom(msg)
		require.NoError(t, err)
	}

	for i, ufrag := range ufrags {
		cc := newPacketConn(t)
		// setupMapping of cc to ufrags[0] and remove the stun binding request from the queue
		setupMapping(t, ufrag, cc, m)
		mc, err := m.GetConn(ufrag, cc.LocalAddr())
		require.NoError(t, err)
		msg := make([]byte, 100)
		_, _, err = mc.ReadFrom(msg)
		require.NoError(t, err)

		// Write from new connection should provide the new address on ReadFrom
		_, err = cc.WriteTo([]byte("test1"), c.LocalAddr())
		require.NoError(t, err)
		n, addr, err := mc.ReadFrom(msg)
		require.NoError(t, err)
		require.Equal(t, addr, cc.LocalAddr())
		require.Equal(t, "test1", string(msg[:n]))

		// Write from original connection should provide the original address
		_, err = conns[i].WriteTo([]byte("test2"), c.LocalAddr())
		require.NoError(t, err)
		n, addr, err = mc.ReadFrom(msg)
		require.NoError(t, err)
		require.Equal(t, addr, conns[i].LocalAddr())
		require.Equal(t, "test2", string(msg[:n]))
	}
}

func TestRemoveConnByUfrag(t *testing.T) {
	c := newPacketConn(t)
	m := NewUDPMux(c)
	m.Start()
	defer m.Close()

	// Map each ufrag to two addresses
	ufrag := v1Ufrag("a")
	count := 10
	conns := make([]net.PacketConn, count)
	for i := range 10 {
		conns[i] = newPacketConn(t)
		setupMapping(t, ufrag, conns[i], m)
	}
	mc, err := m.GetConn(ufrag, conns[0].LocalAddr())
	require.NoError(t, err)
	for i := range 10 {
		mc1, err := m.GetConn(ufrag, conns[i].LocalAddr())
		require.NoError(t, err)
		if mc1 != mc {
			t.Fatalf("expected the two muxed connections to be same")
		}
	}

	// Now remove the ufrag
	m.RemoveConnByUfrag(ufrag)

	// All connections should now be associated with b
	ufrag = v1Ufrag("b")
	for i := range 10 {
		setupMapping(t, ufrag, conns[i], m)
	}
	mc, err = m.GetConn(ufrag, conns[0].LocalAddr())
	require.NoError(t, err)
	for i := range 10 {
		mc1, err := m.GetConn(ufrag, conns[i].LocalAddr())
		require.NoError(t, err)
		if mc1 != mc {
			t.Fatalf("expected the two muxed connections to be same")
		}
	}

	// Should be different even if the address is the same
	mc1, err := m.GetConn(v1Ufrag("a"), conns[0].LocalAddr())
	require.NoError(t, err)
	if mc1 == mc {
		t.Fatalf("expected the two connections to be different")
	}
}

func TestMuxedConnection(t *testing.T) {
	c := newPacketConn(t)
	m := NewUDPMux(c)
	m.Start()
	defer m.Close()

	msgCount := 3
	connCount := 3

	ufrags := []string{v1Ufrag("a"), v1Ufrag("b"), v1Ufrag("c")}
	addrUfragMap := make(map[string]string)
	ufragConnsMap := make(map[string][]net.PacketConn)
	for _, ufrag := range ufrags {
		for range connCount {
			cc := newPacketConn(t)
			addrUfragMap[cc.LocalAddr().String()] = ufrag
			ufragConnsMap[ufrag] = append(ufragConnsMap[ufrag], cc)
		}
	}

	done := make(chan bool, len(ufrags))
	for _, ufrag := range ufrags {
		go func(ufrag string) {
			for _, cc := range ufragConnsMap[ufrag] {
				setupMapping(t, ufrag, cc, m)
				for range msgCount {
					cc.WriteTo([]byte(ufrag), c.LocalAddr())
				}
			}
			done <- true
		}(ufrag)
	}
	for range ufrags {
		<-done
	}

	for _, ufrag := range ufrags {
		mc, err := m.GetConn(ufrag, c.LocalAddr()) // the address is irrelevant
		require.NoError(t, err)
		msgs := 0
		stunRequests := 0
		msg := make([]byte, 1500)
		addrPacketCount := make(map[string]int)
		for range connCount {
			for j := 0; j < msgCount+1; j++ {
				n, addr1, err := mc.ReadFrom(msg)
				require.NoError(t, err)
				require.Equal(t, addrUfragMap[addr1.String()], ufrag)
				addrPacketCount[addr1.String()]++
				if stun.IsMessage(msg[:n]) {
					stunRequests++
				} else {
					msgs++
				}
			}
		}
		for addr, v := range addrPacketCount {
			require.Equal(t, v, msgCount+1) // msgCount msgs + 1 STUN binding request
			delete(addrUfragMap, addr)
		}
		require.Len(t, addrPacketCount, connCount)
	}
	require.Empty(t, addrUfragMap)
}

func TestAddrsPerUfragCap(t *testing.T) {
	c := newPacketConn(t)
	m := NewUDPMux(c)
	m.Start()
	defer m.Close()

	const ufrag = "a"

	// First call creates the connection. Subsequent calls below the cap each
	// add the new address to the per-ufrag tracking.
	base := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}
	_, err := m.GetConn(ufrag, base)
	require.NoError(t, err)

	for i := 2; i <= maxAddrsPerUfrag; i++ {
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: i}
		_, err := m.GetConn(ufrag, addr)
		require.NoError(t, err)
	}

	key := ufragConnKey{localUfrag: ufrag, isIPv6: false}
	m.mx.Lock()
	require.Len(t, m.ufragAddrMap[key], maxAddrsPerUfrag)
	require.Len(t, m.addrMap, maxAddrsPerUfrag)
	m.mx.Unlock()

	// Past the cap, additional addresses still resolve to the same connection
	// but do not extend the tracking maps.
	for i := maxAddrsPerUfrag + 1; i <= maxAddrsPerUfrag+10; i++ {
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: i}
		_, err := m.GetConn(ufrag, addr)
		require.NoError(t, err)
	}

	m.mx.Lock()
	require.Len(t, m.ufragAddrMap[key], maxAddrsPerUfrag)
	require.Len(t, m.addrMap, maxAddrsPerUfrag)
	m.mx.Unlock()

	// Cleanup releases the cap, so a second ufrag can populate freely.
	m.RemoveConnByUfrag(ufrag)

	m.mx.Lock()
	require.Empty(t, m.ufragAddrMap[key])
	require.Empty(t, m.addrMap)
	m.mx.Unlock()
}

func TestRemovingUfragClosesConn(t *testing.T) {
	c := newPacketConn(t)
	m := NewUDPMux(c)
	m.Start()
	defer m.Close()
	remoteAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
	conn, err := m.GetConn("a", remoteAddr)
	require.NoError(t, err)
	defer conn.Close()

	connClosed := make(chan bool)
	go func() {
		_, _, err := conn.ReadFrom(make([]byte, 100))
		assert.ErrorIs(t, err, context.Canceled)
		close(connClosed)
	}()
	require.NoError(t, err)
	m.RemoveConnByUfrag("a")
	select {
	case <-connClosed:
	case <-time.After(1 * time.Second):
		t.Fatalf("expected the connection to be closed")
	}
}
