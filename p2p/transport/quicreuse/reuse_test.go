package quicreuse

import (
	"bytes"
	"net"
	"os"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-netroute"
	"github.com/stretchr/testify/require"
)

func (c *reuseConn) GetCount() int {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.refCount
}

func closeAllConns(reuse *reuse) {
	reuse.mutex.Lock()
	for _, conn := range reuse.globalListeners {
		for conn.GetCount() > 0 {
			conn.DecreaseCount()
		}
	}
	for _, conn := range reuse.globalDialers {
		for conn.GetCount() > 0 {
			conn.DecreaseCount()
		}
	}
	for _, conns := range reuse.unicast {
		for _, conn := range conns {
			for conn.GetCount() > 0 {
				conn.DecreaseCount()
			}
		}
	}
	reuse.mutex.Unlock()
}

func platformHasRoutingTables() bool {
	_, err := netroute.New()
	return err == nil
}

func isGarbageCollectorRunning() bool {
	var b bytes.Buffer
	pprof.Lookup("goroutine").WriteTo(&b, 1)
	return strings.Contains(b.String(), "quicreuse.(*reuse).gc")
}

func cleanup(t *testing.T, reuse *reuse) {
	t.Cleanup(func() {
		closeAllConns(reuse)
		reuse.Close()
		require.False(t, isGarbageCollectorRunning(), "reuse gc still running")
	})
}

func TestReuseListenOnAllIPv4(t *testing.T) {
	reuse := newReuse()
	require.Eventually(t, isGarbageCollectorRunning, 500*time.Millisecond, 50*time.Millisecond, "expected garbage collector to be running")
	cleanup(t, reuse)

	addr, err := net.ResolveUDPAddr("udp4", "0.0.0.0:0")
	require.NoError(t, err)
	conn, err := reuse.Listen("udp4", addr)
	require.NoError(t, err)
	require.Equal(t, conn.GetCount(), 1)
}

func TestReuseListenOnAllIPv6(t *testing.T) {
	reuse := newReuse()
	require.Eventually(t, isGarbageCollectorRunning, 500*time.Millisecond, 50*time.Millisecond, "expected garbage collector to be running")
	cleanup(t, reuse)

	addr, err := net.ResolveUDPAddr("udp6", "[::]:1234")
	require.NoError(t, err)
	conn, err := reuse.Listen("udp6", addr)
	require.NoError(t, err)
	require.Equal(t, conn.GetCount(), 1)
}

func TestReuseCreateNewGlobalConnOnDial(t *testing.T) {
	reuse := newReuse()
	cleanup(t, reuse)

	addr, err := net.ResolveUDPAddr("udp4", "1.1.1.1:1234")
	require.NoError(t, err)
	conn, err := reuse.Dial("udp4", addr)
	require.NoError(t, err)
	require.Equal(t, conn.GetCount(), 1)
	laddr := conn.LocalAddr().(*net.UDPAddr)
	require.Equal(t, laddr.IP.String(), "0.0.0.0")
	require.NotEqual(t, laddr.Port, 0)
}

func TestReuseConnectionWhenDialing(t *testing.T) {
	reuse := newReuse()
	cleanup(t, reuse)

	addr, err := net.ResolveUDPAddr("udp4", "0.0.0.0:0")
	require.NoError(t, err)
	lconn, err := reuse.Listen("udp4", addr)
	require.NoError(t, err)
	require.Equal(t, lconn.GetCount(), 1)
	// dial
	raddr, err := net.ResolveUDPAddr("udp4", "1.1.1.1:1234")
	require.NoError(t, err)
	conn, err := reuse.Dial("udp4", raddr)
	require.NoError(t, err)
	require.Equal(t, conn.GetCount(), 2)
}

func TestReuseConnectionWhenListening(t *testing.T) {
	reuse := newReuse()
	cleanup(t, reuse)

	raddr, err := net.ResolveUDPAddr("udp4", "1.1.1.1:1234")
	require.NoError(t, err)
	conn, err := reuse.Dial("udp4", raddr)
	require.NoError(t, err)
	laddr := &net.UDPAddr{IP: net.IPv4zero, Port: conn.UDPConn.LocalAddr().(*net.UDPAddr).Port}
	lconn, err := reuse.Listen("udp4", laddr)
	require.NoError(t, err)
	require.Equal(t, lconn.GetCount(), 2)
	require.Equal(t, conn.GetCount(), 2)
}

func TestReuseConnectionWhenDialBeforeListen(t *testing.T) {
	reuse := newReuse()
	cleanup(t, reuse)

	// dial any address
	raddr, err := net.ResolveUDPAddr("udp4", "1.1.1.1:1234")
	require.NoError(t, err)
	rconn, err := reuse.Dial("udp4", raddr)
	require.NoError(t, err)

	// open a listener
	laddr := &net.UDPAddr{IP: net.IPv4zero, Port: 1234}
	lconn, err := reuse.Listen("udp4", laddr)
	require.NoError(t, err)

	// new dials should go via the listener connection
	raddr, err = net.ResolveUDPAddr("udp4", "1.1.1.1:1235")
	require.NoError(t, err)
	conn, err := reuse.Dial("udp4", raddr)
	require.NoError(t, err)
	require.Equal(t, conn, lconn)
	require.Equal(t, conn.GetCount(), 2)

	// a listener on an unspecified port should reuse the dialer
	laddr2 := &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	lconn2, err := reuse.Listen("udp4", laddr2)
	require.NoError(t, err)
	require.Equal(t, lconn2, rconn)
	require.Equal(t, lconn2.GetCount(), 2)
}

func TestReuseListenOnSpecificInterface(t *testing.T) {
	if platformHasRoutingTables() {
		t.Skip("this test only works on platforms that support routing tables")
	}
	reuse := newReuse()
	cleanup(t, reuse)

	router, err := netroute.New()
	require.NoError(t, err)

	raddr, err := net.ResolveUDPAddr("udp4", "1.1.1.1:1234")
	require.NoError(t, err)
	_, _, ip, err := router.Route(raddr.IP)
	require.NoError(t, err)
	// listen
	addr, err := net.ResolveUDPAddr("udp4", ip.String()+":0")
	require.NoError(t, err)
	lconn, err := reuse.Listen("udp4", addr)
	require.NoError(t, err)
	require.Equal(t, lconn.GetCount(), 1)
	// dial
	conn, err := reuse.Dial("udp4", raddr)
	require.NoError(t, err)
	require.Equal(t, conn.GetCount(), 1)
}

func TestReuseGarbageCollect(t *testing.T) {
	maxUnusedDurationOrig := maxUnusedDuration
	garbageCollectIntervalOrig := garbageCollectInterval
	t.Cleanup(func() {
		maxUnusedDuration = maxUnusedDurationOrig
		garbageCollectInterval = garbageCollectIntervalOrig
	})
	garbageCollectInterval = 50 * time.Millisecond
	maxUnusedDuration = 100 * time.Millisecond
	if os.Getenv("CI") != "" {
		// Increase these timeouts if in CI
		garbageCollectInterval = 10 * garbageCollectInterval
		maxUnusedDuration = 10 * maxUnusedDuration
	}

	reuse := newReuse()
	cleanup(t, reuse)

	numGlobals := func() int {
		reuse.mutex.Lock()
		defer reuse.mutex.Unlock()
		return len(reuse.globalListeners) + len(reuse.globalDialers)
	}

	raddr, err := net.ResolveUDPAddr("udp4", "1.2.3.4:1234")
	require.NoError(t, err)
	dconn, err := reuse.Dial("udp4", raddr)
	require.NoError(t, err)
	require.Equal(t, dconn.GetCount(), 1)

	addr, err := net.ResolveUDPAddr("udp4", "0.0.0.0:1234")
	require.NoError(t, err)
	lconn, err := reuse.Listen("udp4", addr)
	require.NoError(t, err)
	require.Equal(t, lconn.GetCount(), 1)

	closeTime := time.Now()
	lconn.DecreaseCount()
	dconn.DecreaseCount()

	for {
		num := numGlobals()
		if closeTime.Add(maxUnusedDuration).Before(time.Now()) {
			break
		}
		require.Equal(t, num, 2)
		time.Sleep(2 * time.Millisecond)
	}
	require.Eventually(t, func() bool { return numGlobals() == 0 }, 4*garbageCollectInterval, 10*time.Millisecond)
}
