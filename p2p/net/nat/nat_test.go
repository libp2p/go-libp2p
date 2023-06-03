package nat

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/libp2p/go-nat"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

//go:generate sh -c "go run github.com/golang/mock/mockgen -package nat -destination mock_nat_test.go github.com/libp2p/go-nat NAT"

func setupMockNAT(t *testing.T) (mockNAT *MockNAT, reset func()) {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockNAT = NewMockNAT(ctrl)
	mockNAT.EXPECT().GetDeviceAddress().Return(nil, errors.New("nope")) // is only used for logging
	origDiscoverGateway := discoverGateway
	discoverGateway = func(ctx context.Context) (nat.NAT, error) { return mockNAT, nil }
	return mockNAT, func() {
		discoverGateway = origDiscoverGateway
		ctrl.Finish()
	}
}

func TestAddMapping(t *testing.T) {
	mockNAT, reset := setupMockNAT(t)
	defer reset()

	mockNAT.EXPECT().GetExternalAddress().Return(net.IPv4(1, 2, 3, 4), nil)
	nat, err := DiscoverNAT(context.Background())
	require.NoError(t, err)

	mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10000, gomock.Any(), MappingDuration).Return(1234, nil)
	require.NoError(t, nat.AddMapping(context.Background(), "tcp", 10000))

	_, found := nat.GetMapping("tcp", 9999)
	require.False(t, found, "didn't expect a port mapping for unmapped port")
	_, found = nat.GetMapping("udp", 10000)
	require.False(t, found, "didn't expect a port mapping for unmapped protocol")
	mapped, found := nat.GetMapping("tcp", 10000)
	require.True(t, found, "expected port mapping")
	require.Equal(t, netip.AddrPortFrom(netip.AddrFrom4([4]byte{1, 2, 3, 4}), 1234), mapped)
}

func TestRemoveMapping(t *testing.T) {
	mockNAT, reset := setupMockNAT(t)
	defer reset()

	mockNAT.EXPECT().GetExternalAddress().Return(net.IPv4(1, 2, 3, 4), nil)
	nat, err := DiscoverNAT(context.Background())
	require.NoError(t, err)
	mockNAT.EXPECT().AddPortMapping(gomock.Any(), "tcp", 10000, gomock.Any(), MappingDuration).Return(1234, nil)
	require.NoError(t, nat.AddMapping(context.Background(), "tcp", 10000))
	_, found := nat.GetMapping("tcp", 10000)
	require.True(t, found, "expected port mapping")

	require.Error(t, nat.RemoveMapping(context.Background(), "tcp", 9999), "expected error for unknown mapping")
	mockNAT.EXPECT().DeletePortMapping(gomock.Any(), "tcp", 10000)
	require.NoError(t, nat.RemoveMapping(context.Background(), "tcp", 10000))

	_, found = nat.GetMapping("tcp", 10000)
	require.False(t, found, "didn't expect port mapping for deleted mapping")
}
