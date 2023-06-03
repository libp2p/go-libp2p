package transport_integration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gomock "github.com/golang/mock/gomock"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	mocknetwork "github.com/libp2p/go-libp2p/core/network/mocks"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/stretchr/testify/require"
)

func TestResourceManagerIsUsed(t *testing.T) {
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			for _, testDialer := range []bool{true, false} {
				t.Run(tc.Name+fmt.Sprintf(" test_dialer=%v", testDialer), func(t *testing.T) {

					var reservedMemory, releasedMemory atomic.Int32
					defer func() {
						require.Equal(t, reservedMemory.Load(), releasedMemory.Load())
						require.NotEqual(t, 0, reservedMemory.Load())
					}()

					ctrl := gomock.NewController(t)
					defer ctrl.Finish()
					rcmgr := mocknetwork.NewMockResourceManager(ctrl)
					rcmgr.EXPECT().Close()

					var listener, dialer host.Host
					var expectedPeer peer.ID
					var expectedDir network.Direction
					var expectedAddr interface{}
					if testDialer {
						listener = tc.HostGenerator(t, TransportTestCaseOpts{NoRcmgr: true})
						dialer = tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true, ResourceManager: rcmgr})
						expectedPeer = listener.ID()
						expectedDir = network.DirOutbound
						expectedAddr = listener.Addrs()[0]
					} else {
						listener = tc.HostGenerator(t, TransportTestCaseOpts{ResourceManager: rcmgr})
						dialer = tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true, NoRcmgr: true})
						expectedPeer = dialer.ID()
						expectedDir = network.DirInbound
						expectedAddr = gomock.Any()
					}

					expectFd := true
					if strings.Contains(tc.Name, "QUIC") || strings.Contains(tc.Name, "WebTransport") {
						expectFd = false
					}

					peerScope := mocknetwork.NewMockPeerScope(ctrl)
					peerScope.EXPECT().ReserveMemory(gomock.Any(), gomock.Any()).AnyTimes().Do(func(amount int, pri uint8) {
						reservedMemory.Add(int32(amount))
					})
					peerScope.EXPECT().ReleaseMemory(gomock.Any()).AnyTimes().Do(func(amount int) {
						releasedMemory.Add(int32(amount))
					})
					peerScope.EXPECT().BeginSpan().AnyTimes().DoAndReturn(func() (network.ResourceScopeSpan, error) {
						s := mocknetwork.NewMockResourceScopeSpan(ctrl)
						s.EXPECT().BeginSpan().AnyTimes().Return(mocknetwork.NewMockResourceScopeSpan(ctrl), nil)
						// No need to track these memory reservations since we assert that Done is called
						s.EXPECT().ReserveMemory(gomock.Any(), gomock.Any())
						s.EXPECT().Done()
						return s, nil
					})
					var calledSetPeer atomic.Bool

					connScope := mocknetwork.NewMockConnManagementScope(ctrl)
					connScope.EXPECT().SetPeer(expectedPeer).Do(func(peer.ID) {
						calledSetPeer.Store(true)
					})
					connScope.EXPECT().PeerScope().AnyTimes().DoAndReturn(func() network.PeerScope {
						if calledSetPeer.Load() {
							return peerScope
						}
						return nil
					})
					connScope.EXPECT().Done()

					var allStreamsDone sync.WaitGroup

					rcmgr.EXPECT().OpenConnection(expectedDir, expectFd, expectedAddr).Return(connScope, nil)
					rcmgr.EXPECT().OpenStream(expectedPeer, gomock.Any()).AnyTimes().DoAndReturn(func(id peer.ID, dir network.Direction) (network.StreamManagementScope, error) {
						allStreamsDone.Add(1)
						streamScope := mocknetwork.NewMockStreamManagementScope(ctrl)
						// No need to track these memory reservations since we assert that Done is called
						streamScope.EXPECT().ReserveMemory(gomock.Any(), gomock.Any()).AnyTimes()
						streamScope.EXPECT().ReleaseMemory(gomock.Any()).AnyTimes()
						streamScope.EXPECT().BeginSpan().AnyTimes().DoAndReturn(func() (network.ResourceScopeSpan, error) {
							s := mocknetwork.NewMockResourceScopeSpan(ctrl)
							s.EXPECT().BeginSpan().AnyTimes().Return(mocknetwork.NewMockResourceScopeSpan(ctrl), nil)
							s.EXPECT().Done()
							return s, nil
						})

						streamScope.EXPECT().SetService(gomock.Any()).MaxTimes(1)
						streamScope.EXPECT().SetProtocol(gomock.Any())

						streamScope.EXPECT().Done().Do(func() {
							allStreamsDone.Done()
						})
						return streamScope, nil
					})

					require.NoError(t, dialer.Connect(context.Background(), peer.AddrInfo{
						ID:    listener.ID(),
						Addrs: listener.Addrs(),
					}))
					// Wait for any in progress identifies to finish.
					// We shouldn't have to do this, but basic host currently
					// always does an identify.
					<-dialer.(interface{ IDService() identify.IDService }).IDService().IdentifyWait(dialer.Network().ConnsToPeer(listener.ID())[0])
					<-listener.(interface{ IDService() identify.IDService }).IDService().IdentifyWait(listener.Network().ConnsToPeer(dialer.ID())[0])
					<-ping.Ping(context.Background(), dialer, listener.ID())
					err := dialer.Network().ClosePeer(listener.ID())
					require.NoError(t, err)

					// Wait a bit for any pending .Adds before we call .Wait to avoid a data race.
					// This shouldn't be necessary since it should be impossible
					// for an OpenStream to happen *after* a ClosePeer, however
					// in practice it does and leads to test flakiness.
					time.Sleep(10 * time.Millisecond)
					allStreamsDone.Wait()
					dialer.Close()
					listener.Close()
				})
			}
		})
	}
}
