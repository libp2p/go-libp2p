package nat

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/p2p/net/nat/internal/nat"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

//go:generate sh -c "go run go.uber.org/mock/mockgen -package nat -destination mock_nat_test.go github.com/libp2p/go-libp2p/p2p/net/nat/internal/nat NAT"

// Helper functions for test setup

// expectPortMappingFailure sets up mock expectations for a port mapping failure
func expectPortMappingFailure(mockNAT *MockNAT, protocol string, port int, err error) {
	mockNAT.EXPECT().AddPortMapping(gomock.Any(), protocol, port, gomock.Any(), MappingDuration).Return(0, err).Times(1)
	mockNAT.EXPECT().AddPortMapping(gomock.Any(), protocol, port, gomock.Any(), time.Duration(0)).Return(0, err).Times(1)
}

// expectPortMappingSuccess sets up mock expectations for a successful port mapping
func expectPortMappingSuccess(mockNAT *MockNAT, protocol string, internalPort, externalPort int) {
	mockNAT.EXPECT().AddPortMapping(gomock.Any(), protocol, internalPort, gomock.Any(), MappingDuration).Return(externalPort, nil).Times(1)
}

// setupMockNATWithAddress creates a mock NAT with the given external address
func setupMockNATWithAddress(ctrl *gomock.Controller, addr net.IP) *MockNAT {
	mockNAT := NewMockNAT(ctrl)
	mockNAT.EXPECT().GetDeviceAddress().Return(nil, errors.New("nope")).AnyTimes()
	mockNAT.EXPECT().GetExternalAddress().Return(addr, nil).AnyTimes()
	return mockNAT
}

func setupMockNAT(t *testing.T) (mockNAT *MockNAT, reset func()) {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockNAT = NewMockNAT(ctrl)
	mockNAT.EXPECT().GetDeviceAddress().Return(nil, errors.New("nope")) // is only used for logging
	origDiscoverGateway := discoverGateway
	discoverGateway = func(_ context.Context) (nat.NAT, error) { return mockNAT, nil }
	return mockNAT, func() {
		discoverGateway = origDiscoverGateway
		ctrl.Finish()
	}
}

// TestAddMapping tests basic port mapping creation and retrieval to ensure mappings are stored correctly.
func TestAddMapping(t *testing.T) {
	mockNAT, reset := setupMockNAT(t)
	defer reset()

	mockNAT.EXPECT().GetExternalAddress().Return(net.IPv4(1, 2, 3, 4), nil)
	nat, err := DiscoverNAT(context.Background())
	require.NoError(t, err)

	expectPortMappingSuccess(mockNAT, "tcp", 10000, 1234)
	require.NoError(t, nat.AddMapping(context.Background(), "tcp", 10000))

	_, found := nat.GetMapping("tcp", 9999)
	require.False(t, found, "didn't expect a port mapping for unmapped port")
	_, found = nat.GetMapping("udp", 10000)
	require.False(t, found, "didn't expect a port mapping for unmapped protocol")
	mapped, found := nat.GetMapping("tcp", 10000)
	require.True(t, found, "expected port mapping")
	addr, _ := netip.AddrFromSlice(net.IPv4(1, 2, 3, 4))
	require.Equal(t, netip.AddrPortFrom(addr, 1234), mapped)
}

// TestRemoveMapping tests deletion of port mappings to ensure cleanup works and unknown mappings error.
func TestRemoveMapping(t *testing.T) {
	mockNAT, reset := setupMockNAT(t)
	defer reset()

	mockNAT.EXPECT().GetExternalAddress().Return(net.IPv4(1, 2, 3, 4), nil)
	nat, err := DiscoverNAT(context.Background())
	require.NoError(t, err)
	expectPortMappingSuccess(mockNAT, "tcp", 10000, 1234)
	require.NoError(t, nat.AddMapping(context.Background(), "tcp", 10000))
	_, found := nat.GetMapping("tcp", 10000)
	require.True(t, found, "expected port mapping")

	require.Error(t, nat.RemoveMapping(context.Background(), "tcp", 9999), "expected error for unknown mapping")
	mockNAT.EXPECT().DeletePortMapping(gomock.Any(), "tcp", 10000)
	require.NoError(t, nat.RemoveMapping(context.Background(), "tcp", 10000))

	_, found = nat.GetMapping("tcp", 10000)
	require.False(t, found, "didn't expect port mapping for deleted mapping")
}

// TestAddMappingInvalidPort tests NAT returning port 0 to ensure invalid mappings are not stored.
func TestAddMappingInvalidPort(t *testing.T) {
	mockNAT, reset := setupMockNAT(t)
	defer reset()

	mockNAT.EXPECT().GetExternalAddress().Return(net.IPv4(1, 2, 3, 4), nil)
	nat, err := DiscoverNAT(context.Background())
	require.NoError(t, err)

	expectPortMappingSuccess(mockNAT, "tcp", 10000, 0)
	require.NoError(t, nat.AddMapping(context.Background(), "tcp", 10000))

	_, found := nat.GetMapping("tcp", 10000)
	require.False(t, found, "didn't expect a port mapping for invalid nat-ed port")
}

// TestAddMappingDeduplication tests that duplicate AddMapping calls don't trigger duplicate NAT operations.
// This is a regression test for a bug where multiple libp2p listeners sharing the same port
// (e.g., TCP, QUIC, WebTransport, WebRTC-direct all on the same port) would cause duplicate NAT
// port mapping requests - resulting in 5+ duplicate mapping attempts for the same protocol/port combination.
func TestAddMappingDeduplication(t *testing.T) {
	mockNAT, reset := setupMockNAT(t)
	defer reset()

	mockNAT.EXPECT().GetExternalAddress().Return(net.IPv4(1, 2, 3, 4), nil)
	nat, err := DiscoverNAT(context.Background())
	require.NoError(t, err)

	// Expect only ONE call to AddPortMapping despite multiple AddMapping calls
	expectPortMappingSuccess(mockNAT, "tcp", 10000, 1234)

	// First call should trigger NAT operation
	require.NoError(t, nat.AddMapping(context.Background(), "tcp", 10000))

	// Verify mapping was created
	mapped, found := nat.GetMapping("tcp", 10000)
	require.True(t, found, "expected port mapping")
	addr, _ := netip.AddrFromSlice(net.IPv4(1, 2, 3, 4))
	require.Equal(t, netip.AddrPortFrom(addr, 1234), mapped)

	// Second and third calls should NOT trigger NAT operations (no additional expectations)
	// This simulates what happens when multiple transports use the same port
	require.NoError(t, nat.AddMapping(context.Background(), "tcp", 10000))
	require.NoError(t, nat.AddMapping(context.Background(), "tcp", 10000))

	// Mapping should still exist
	mapped, found = nat.GetMapping("tcp", 10000)
	require.True(t, found, "expected port mapping")
	require.Equal(t, netip.AddrPortFrom(addr, 1234), mapped)
}

// TestNATRediscoveryOnConnectionError tests automatic NAT rediscovery after router restart
// to ensure mappings are restored when router's NAT service (e.g. miniupnpd) changes its listening port
// (a regression test for https://github.com/libp2p/go-libp2p/issues/3224#issuecomment-2866844723).
func TestNATRediscoveryOnConnectionError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Setup initial mock NAT
	mockNAT := NewMockNAT(ctrl)
	mockNAT.EXPECT().GetDeviceAddress().Return(nil, errors.New("nope")).AnyTimes()
	mockNAT.EXPECT().GetExternalAddress().Return(net.IPv4(1, 2, 3, 4), nil).Times(1)

	// Setup new mock NAT for rediscovery
	newMockNAT := setupMockNATWithAddress(ctrl, net.IPv4(5, 6, 7, 8))

	// Track discovery calls with atomic counter
	var discoveryCalls atomic.Int32
	origDiscoverGateway := discoverGateway
	discoverGateway = func(_ context.Context) (nat.NAT, error) {
		count := discoveryCalls.Add(1)
		if count == 1 {
			return mockNAT, nil
		}
		return newMockNAT, nil
	}
	defer func() {
		discoverGateway = origDiscoverGateway
	}()

	// Create NAT instance
	n, err := DiscoverNAT(context.Background())
	require.NoError(t, err)

	// Expect cleanup on close
	defer func() {
		// The final NAT instance is newMockNAT, which will have these mappings
		newMockNAT.EXPECT().DeletePortMapping(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		n.Close()
	}()

	// Add some existing mappings that should be restored after rediscovery
	expectPortMappingSuccess(mockNAT, "tcp", 4001, 4001)
	require.NoError(t, n.AddMapping(context.Background(), "tcp", 4001))
	expectPortMappingSuccess(mockNAT, "udp", 4002, 4002)
	require.NoError(t, n.AddMapping(context.Background(), "udp", 4002))

	// Simulate connection refused error that happens when router's UPnP port changes
	errConnectionRefused := errors.New("goupnp: error performing SOAP HTTP request: Post \"http://192.168.1.1:1234/ctl/IPConn\": dial tcp 192.168.1.1:1234: connect: connection refused")

	// Set up expectations for the failures that will trigger rediscovery
	for i := 0; i < 3; i++ {
		expectPortMappingFailure(mockNAT, "tcp", 10000+i, errConnectionRefused)
	}

	// Expect the existing mappings to be restored on the new NAT instance
	expectPortMappingSuccess(newMockNAT, "tcp", 4001, 4001)
	expectPortMappingSuccess(newMockNAT, "udp", 4002, 4002)

	// Now trigger the failures
	for i := 0; i < 3; i++ {
		externalPort := n.establishMapping(context.Background(), "tcp", 10000+i)
		require.Equal(t, 0, externalPort)
	}

	// Give time for async rediscovery and mapping restoration
	time.Sleep(100 * time.Millisecond)

	// Verify mappings were restored
	mapped, found := n.GetMapping("tcp", 4001)
	require.True(t, found, "expected tcp/4001 mapping to be restored")
	addr, _ := netip.AddrFromSlice(net.IPv4(5, 6, 7, 8)) // new NAT's external IP
	require.Equal(t, netip.AddrPortFrom(addr, 4001), mapped)

	mapped, found = n.GetMapping("udp", 4002)
	require.True(t, found, "expected udp/4002 mapping to be restored")
	require.Equal(t, netip.AddrPortFrom(addr, 4002), mapped)

	// Next mapping should use the new NAT
	expectPortMappingSuccess(newMockNAT, "tcp", 10003, 12345)

	externalPort := n.establishMapping(context.Background(), "tcp", 10003)
	require.Equal(t, 12345, externalPort)
	require.Equal(t, int32(2), discoveryCalls.Load()) // Initial + rediscovery
}

// TestNATRediscoveryOldRouterReturns tests rediscovery when router comes back on same IP/port
// to ensure we handle transient failures without losing the NAT instance.
func TestNATRediscoveryOldRouterReturns(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Setup mock NAT
	mockNAT := NewMockNAT(ctrl)
	mockNAT.EXPECT().GetDeviceAddress().Return(nil, errors.New("nope")).AnyTimes()
	mockNAT.EXPECT().GetExternalAddress().Return(net.IPv4(1, 2, 3, 4), nil).AnyTimes()

	// Track discovery calls with atomic counter
	var discoveryCalls atomic.Int32
	origDiscoverGateway := discoverGateway
	discoverGateway = func(_ context.Context) (nat.NAT, error) {
		count := discoveryCalls.Add(1)
		if count == 2 {
			// During rediscovery, return the same NAT (router came back)
			return mockNAT, nil
		}
		return mockNAT, nil
	}
	defer func() {
		discoverGateway = origDiscoverGateway
	}()

	n, err := DiscoverNAT(context.Background())
	require.NoError(t, err)
	defer func() {
		mockNAT.EXPECT().DeletePortMapping(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		n.Close()
	}()

	// Add existing mapping
	mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 4001, gomock.Any(), MappingDuration).Return(4001, nil).Times(1)
	require.NoError(t, n.AddMapping(context.Background(), "tcp", 4001))

	errConnectionRefused := errors.New("goupnp: error performing SOAP HTTP request: dial tcp 192.168.1.1:1234: connect: connection refused")

	// Set up expectations for the first two failures
	for i := 0; i < 2; i++ {
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10000+i, gomock.Any(), MappingDuration).Return(0, errConnectionRefused).Times(1)
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10000+i, gomock.Any(), time.Duration(0)).Return(0, errConnectionRefused).Times(1)
	}

	// Third failure triggers rediscovery, but router is back now
	mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10002, gomock.Any(), MappingDuration).Return(0, errConnectionRefused).Times(1)
	mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10002, gomock.Any(), time.Duration(0)).Return(0, errConnectionRefused).Times(1)

	// Expect mapping restoration on the same NAT
	mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 4001, gomock.Any(), MappingDuration).Return(4001, nil).Times(1)

	// Trigger the failures
	for i := 0; i < 2; i++ {
		n.establishMapping(context.Background(), "tcp", 10000+i)
	}
	n.establishMapping(context.Background(), "tcp", 10002)

	time.Sleep(100 * time.Millisecond)

	// Verify we still have our mapping
	mapped, found := n.GetMapping("tcp", 4001)
	require.True(t, found)
	addr, _ := netip.AddrFromSlice(net.IPv4(1, 2, 3, 4))
	require.Equal(t, netip.AddrPortFrom(addr, 4001), mapped)
	require.Equal(t, int32(2), discoveryCalls.Load()) // Initial + rediscovery
}

// TestNATRediscoveryFailureThreshold tests the 3-failure threshold and counter reset behavior
// to ensure we don't trigger rediscovery on transient errors.
func TestNATRediscoveryFailureThreshold(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockNAT := NewMockNAT(ctrl)
	mockNAT.EXPECT().GetDeviceAddress().Return(nil, errors.New("nope")).AnyTimes()
	mockNAT.EXPECT().GetExternalAddress().Return(net.IPv4(1, 2, 3, 4), nil).AnyTimes()

	// Track discovery calls with atomic counter
	var discoveryCalls atomic.Int32
	origDiscoverGateway := discoverGateway
	discoverGateway = func(_ context.Context) (nat.NAT, error) {
		discoveryCalls.Add(1)
		return mockNAT, nil
	}
	defer func() {
		discoverGateway = origDiscoverGateway
	}()

	n, err := DiscoverNAT(context.Background())
	require.NoError(t, err)
	defer func() {
		mockNAT.EXPECT().DeletePortMapping(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		n.Close()
	}()

	errConnectionRefused := errors.New("connection refused")
	errOther := errors.New("some other error")

	// Test 1: Only 2 failures - should NOT trigger rediscovery
	for i := 0; i < 2; i++ {
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10000+i, gomock.Any(), MappingDuration).Return(0, errConnectionRefused).Times(1)
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10000+i, gomock.Any(), time.Duration(0)).Return(0, errConnectionRefused).Times(1)
		n.establishMapping(context.Background(), "tcp", 10000+i)
	}

	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), discoveryCalls.Load(), "should not trigger rediscovery with only 2 failures")

	// Test 2: Non-connection error resets counter
	mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10002, gomock.Any(), MappingDuration).Return(0, errOther).Times(1)
	mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10002, gomock.Any(), time.Duration(0)).Return(0, errOther).Times(1)
	n.establishMapping(context.Background(), "tcp", 10002)

	// Now even 2 more connection failures shouldn't trigger (counter was reset)
	for i := 0; i < 2; i++ {
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10003+i, gomock.Any(), MappingDuration).Return(0, errConnectionRefused).Times(1)
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10003+i, gomock.Any(), time.Duration(0)).Return(0, errConnectionRefused).Times(1)
		n.establishMapping(context.Background(), "tcp", 10003+i)
	}

	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), discoveryCalls.Load(), "counter should reset on non-connection error")

	// Test 3: Success resets counter
	mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10005, gomock.Any(), MappingDuration).Return(10005, nil).Times(1)
	n.establishMapping(context.Background(), "tcp", 10005)

	// Again, 2 failures shouldn't trigger
	for i := 0; i < 2; i++ {
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10006+i, gomock.Any(), MappingDuration).Return(0, errConnectionRefused).Times(1)
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10006+i, gomock.Any(), time.Duration(0)).Return(0, errConnectionRefused).Times(1)
		n.establishMapping(context.Background(), "tcp", 10006+i)
	}

	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), discoveryCalls.Load(), "counter should reset on success")
}

// TestNATRediscoveryConcurrency tests concurrent connection failures to ensure only one
// rediscovery happens even with multiple goroutines hitting errors.
func TestNATRediscoveryConcurrency(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockNAT := NewMockNAT(ctrl)
	mockNAT.EXPECT().GetDeviceAddress().Return(nil, errors.New("nope")).AnyTimes()
	mockNAT.EXPECT().GetExternalAddress().Return(net.IPv4(1, 2, 3, 4), nil).AnyTimes()

	newMockNAT := NewMockNAT(ctrl)
	newMockNAT.EXPECT().GetDeviceAddress().Return(nil, errors.New("nope")).AnyTimes()
	newMockNAT.EXPECT().GetExternalAddress().Return(net.IPv4(5, 6, 7, 8), nil).AnyTimes()

	// Track discovery calls with atomic counter
	var discoveryCalls atomic.Int32
	origDiscoverGateway := discoverGateway
	discoverGateway = func(_ context.Context) (nat.NAT, error) {
		count := discoveryCalls.Add(1)
		if count == 1 {
			return mockNAT, nil
		}
		// Simulate slow discovery to test concurrent calls
		time.Sleep(200 * time.Millisecond)
		return newMockNAT, nil
	}
	defer func() {
		discoverGateway = origDiscoverGateway
	}()

	n, err := DiscoverNAT(context.Background())
	require.NoError(t, err)
	defer func() {
		newMockNAT.EXPECT().DeletePortMapping(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		n.Close()
	}()

	errConnectionRefused := errors.New("connection refused")

	// Simulate multiple goroutines hitting failures after threshold
	// First get to threshold
	for i := 0; i < 3; i++ {
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10000+i, gomock.Any(), MappingDuration).Return(0, errConnectionRefused).Times(1)
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10000+i, gomock.Any(), time.Duration(0)).Return(0, errConnectionRefused).Times(1)
		n.establishMapping(context.Background(), "tcp", 10000+i)
	}

	// Set up expectations for concurrent failure attempts
	for i := 0; i < 5; i++ {
		port := 10003 + i
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", port, gomock.Any(), MappingDuration).Return(0, errConnectionRefused).AnyTimes()
		mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", port, gomock.Any(), time.Duration(0)).Return(0, errConnectionRefused).AnyTimes()
	}

	// Now launch multiple goroutines that would all try to trigger rediscovery
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			// These would all try to trigger rediscovery if not protected
			n.establishMapping(context.Background(), "tcp", port)
		}(10003 + i)
	}

	wg.Wait()
	time.Sleep(300 * time.Millisecond) // Wait for rediscovery to complete

	// Should only have triggered one rediscovery despite multiple concurrent failures
	require.Equal(t, int32(2), discoveryCalls.Load(), "should only trigger one rediscovery")
}
