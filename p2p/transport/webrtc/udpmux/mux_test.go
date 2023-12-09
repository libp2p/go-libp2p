package udpmux

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/stun"
	"github.com/stretchr/testify/require"
)

func getSTUNBindingRequest(ufrag string) *stun.Message {
	msg := stun.New()
	msg.SetType(stun.BindingRequest)
	uattr := stun.RawAttribute{
		Type:  stun.AttrUsername,
		Value: []byte(fmt.Sprintf("%s:%s", ufrag, ufrag)), // This is the format we expect in our connections
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

	ufrags := []string{"a", "b", "c", "d"}
	conns := make([]net.PacketConn, len(ufrags))
	for i, ufrag := range ufrags {
		conns[i] = newPacketConn(t)
		setupMapping(t, ufrag, conns[i], m)
	}
	for i, ufrag := range ufrags {
		c, err := m.Accept(context.Background())
		require.NoError(t, err)
		require.Equal(t, c.Ufrag, ufrag)
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

	ufrags := []string{"a", "b", "c", "d"}
	conns := make([]net.PacketConn, len(ufrags))
	for i, ufrag := range ufrags {
		conns[i] = newPacketConn(t)
		setupMapping(t, ufrag, conns[i], m)
	}
	for i, ufrag := range ufrags {
		c, err := m.Accept(context.Background())
		require.NoError(t, err)
		require.Equal(t, c.Ufrag, ufrag)
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
	ufrag := "a"
	count := 10
	conns := make([]net.PacketConn, count)
	for i := 0; i < 10; i++ {
		conns[i] = newPacketConn(t)
		setupMapping(t, ufrag, conns[i], m)
	}
	mc, err := m.GetConn(ufrag, conns[0].LocalAddr())
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		mc1, err := m.GetConn(ufrag, conns[i].LocalAddr())
		require.NoError(t, err)
		if mc1 != mc {
			t.Fatalf("expected the two muxed connections to be same")
		}
	}

	// Now remove the ufrag
	m.RemoveConnByUfrag(ufrag)

	// All connections should now be associated with b
	ufrag = "b"
	for i := 0; i < 10; i++ {
		setupMapping(t, ufrag, conns[i], m)
	}
	mc, err = m.GetConn(ufrag, conns[0].LocalAddr())
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		mc1, err := m.GetConn(ufrag, conns[i].LocalAddr())
		require.NoError(t, err)
		if mc1 != mc {
			t.Fatalf("expected the two muxed connections to be same")
		}
	}

	// Should be different even if the address is the same
	mc1, err := m.GetConn("a", conns[0].LocalAddr())
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

	ufrags := []string{"a", "b", "c"}
	addrUfragMap := make(map[string]string)
	ufragConnsMap := make(map[string][]net.PacketConn)
	for _, ufrag := range ufrags {
		for i := 0; i < connCount; i++ {
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
				for j := 0; j < msgCount; j++ {
					cc.WriteTo([]byte(ufrag), c.LocalAddr())
				}
			}
			done <- true
		}(ufrag)
	}
	for i := 0; i < len(ufrags); i++ {
		<-done
	}

	for _, ufrag := range ufrags {
		mc, err := m.GetConn(ufrag, c.LocalAddr()) // the address is irrelevant
		require.NoError(t, err)
		msgs := 0
		stunRequests := 0
		msg := make([]byte, 1500)
		addrPacketCount := make(map[string]int)
		for i := 0; i < connCount; i++ {
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
