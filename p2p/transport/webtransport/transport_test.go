package libp2pwebtransport_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"testing/quick"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	mocknetwork "github.com/libp2p/go-libp2p/core/network/mocks"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	libp2pwebtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"

	"github.com/benbjohnson/clock"
	"github.com/golang/mock/gomock"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	quicproxy "github.com/quic-go/quic-go/integrationtests/tools/proxy"
	"github.com/stretchr/testify/require"
)

const clockSkewAllowance = time.Hour
const certValidity = 14 * 24 * time.Hour

func newIdentity(t *testing.T) (peer.ID, ic.PrivKey) {
	key, _, err := ic.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(key)
	require.NoError(t, err)
	return id, key
}

func randomMultihash(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	rand.Read(b)
	h, err := multihash.Encode(b, multihash.KECCAK_224)
	require.NoError(t, err)
	s, err := multibase.Encode(multibase.Base32hex, h)
	require.NoError(t, err)
	return s
}

func extractCertHashes(addr ma.Multiaddr) []string {
	var certHashesStr []string
	ma.ForEach(addr, func(c ma.Component) bool {
		if c.Protocol().Code == ma.P_CERTHASH {
			certHashesStr = append(certHashesStr, c.Value())
		}
		return true
	})
	return certHashesStr
}

func stripCertHashes(addr ma.Multiaddr) ma.Multiaddr {
	for {
		_, err := addr.ValueForProtocol(ma.P_CERTHASH)
		if err != nil {
			return addr
		}
		addr, _ = ma.SplitLast(addr)
	}
}

// create a /certhash multiaddr component using the SHA256 of foobar
func getCerthashComponent(t *testing.T, b []byte) ma.Multiaddr {
	t.Helper()
	h := sha256.Sum256(b)
	mh, err := multihash.Encode(h[:], multihash.SHA2_256)
	require.NoError(t, err)
	certStr, err := multibase.Encode(multibase.Base58BTC, mh)
	require.NoError(t, err)
	ha, err := ma.NewComponent(ma.ProtocolWithCode(ma.P_CERTHASH).Name, certStr)
	require.NoError(t, err)
	return ha
}

func newConnManager(t *testing.T, opts ...quicreuse.Option) *quicreuse.ConnManager {
	t.Helper()
	cm, err := quicreuse.NewConnManager([32]byte{}, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { cm.Close() })
	return cm
}

func TestTransport(t *testing.T) {
	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, nil, newConnManager(t), nil, nil)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)
	defer ln.Close()

	addrChan := make(chan ma.Multiaddr)
	go func() {
		_, clientKey := newIdentity(t)
		tr2, err := libp2pwebtransport.New(clientKey, nil, newConnManager(t), nil, nil)
		require.NoError(t, err)
		defer tr2.(io.Closer).Close()

		conn, err := tr2.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.NoError(t, err)
		str, err := conn.OpenStream(context.Background())
		require.NoError(t, err)
		_, err = str.Write([]byte("foobar"))
		require.NoError(t, err)
		require.NoError(t, str.Close())

		// check RemoteMultiaddr
		_, addr, err := manet.DialArgs(ln.Multiaddr())
		require.NoError(t, err)
		_, port, err := net.SplitHostPort(addr)
		require.NoError(t, err)
		require.Equal(t, ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/udp/%s/quic-v1/webtransport", port)), conn.RemoteMultiaddr())
		addrChan <- conn.RemoteMultiaddr()
	}()

	conn, err := ln.Accept()
	require.NoError(t, err)
	require.False(t, conn.IsClosed())
	str, err := conn.AcceptStream()
	require.NoError(t, err)
	data, err := io.ReadAll(str)
	require.NoError(t, err)
	require.Equal(t, "foobar", string(data))
	require.Equal(t, <-addrChan, conn.LocalMultiaddr())
	require.NoError(t, conn.Close())
	require.True(t, conn.IsClosed())
}

func TestHashVerification(t *testing.T) {
	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, nil, newConnManager(t), nil, &network.NullResourceManager{})
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := ln.Accept()
		require.Error(t, err)
	}()

	_, clientKey := newIdentity(t)
	tr2, err := libp2pwebtransport.New(clientKey, nil, newConnManager(t), nil, &network.NullResourceManager{})
	require.NoError(t, err)
	defer tr2.(io.Closer).Close()

	foobarHash := getCerthashComponent(t, []byte("foobar"))

	t.Run("fails using only a wrong hash", func(t *testing.T) {
		// replace the certificate hash in the multiaddr with a fake hash
		addr := stripCertHashes(ln.Multiaddr()).Encapsulate(foobarHash)
		_, err := tr2.Dial(context.Background(), addr, serverID)
		require.Error(t, err)
		var trErr *quic.TransportError
		require.ErrorAs(t, err, &trErr)
		require.Equal(t, quic.TransportErrorCode(0x12a), trErr.ErrorCode)
		require.Contains(t, trErr.ErrorMessage, "cert hash not found")
	})

	t.Run("fails when adding a wrong hash", func(t *testing.T) {
		_, err := tr2.Dial(context.Background(), ln.Multiaddr().Encapsulate(foobarHash), serverID)
		require.Error(t, err)
	})

	require.NoError(t, ln.Close())
	<-done
}

func TestCanDial(t *testing.T) {
	valid := []ma.Multiaddr{
		ma.StringCast("/ip4/127.0.0.1/udp/1234/quic-v1/webtransport/certhash/" + randomMultihash(t)),
		ma.StringCast("/ip6/b16b:8255:efc6:9cd5:1a54:ee86:2d7a:c2e6/udp/1234/quic-v1/webtransport/certhash/" + randomMultihash(t)),
		ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/udp/1234/quic-v1/webtransport/certhash/%s/certhash/%s/certhash/%s", randomMultihash(t), randomMultihash(t), randomMultihash(t))),
		ma.StringCast("/ip4/127.0.0.1/udp/1234/quic-v1/webtransport"), // no certificate hash
	}

	invalid := []ma.Multiaddr{
		ma.StringCast("/ip4/127.0.0.1/udp/1234"),              // missing webtransport
		ma.StringCast("/ip4/127.0.0.1/udp/1234/webtransport"), // missing quic
		ma.StringCast("/ip4/127.0.0.1/tcp/1234/webtransport"), // WebTransport over TCP? Is this a joke?
	}

	_, key := newIdentity(t)
	tr, err := libp2pwebtransport.New(key, nil, newConnManager(t), nil, &network.NullResourceManager{})
	require.NoError(t, err)
	defer tr.(io.Closer).Close()

	for _, addr := range valid {
		require.Truef(t, tr.CanDial(addr), "expected to be able to dial %s", addr)
	}
	for _, addr := range invalid {
		require.Falsef(t, tr.CanDial(addr), "expected to not be able to dial %s", addr)
	}
}

func TestListenAddrValidity(t *testing.T) {
	valid := []ma.Multiaddr{
		ma.StringCast("/ip6/::/udp/0/quic-v1/webtransport/"),
	}

	invalid := []ma.Multiaddr{
		ma.StringCast("/ip4/127.0.0.1/udp/0"),                                                     // missing webtransport
		ma.StringCast("/ip4/127.0.0.1/udp/0/webtransport"),                                        // missing quic
		ma.StringCast("/ip4/127.0.0.1/tcp/0/webtransport"),                                        // WebTransport over TCP? Is this a joke?
		ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport/certhash/" + randomMultihash(t)), // We can't listen on a specific certhash
	}

	_, key := newIdentity(t)
	tr, err := libp2pwebtransport.New(key, nil, newConnManager(t), nil, &network.NullResourceManager{})
	require.NoError(t, err)
	defer tr.(io.Closer).Close()

	for _, addr := range valid {
		ln, err := tr.Listen(addr)
		require.NoErrorf(t, err, "expected to be able to listen on %s", addr)
		ln.Close()
	}
	for _, addr := range invalid {
		_, err := tr.Listen(addr)
		require.Errorf(t, err, "expected to not be able to listen on %s", addr)
	}
}

func TestListenerAddrs(t *testing.T) {
	_, key := newIdentity(t)
	tr, err := libp2pwebtransport.New(key, nil, newConnManager(t), nil, &network.NullResourceManager{})
	require.NoError(t, err)
	defer tr.(io.Closer).Close()

	ln1, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)
	ln2, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)
	hashes1 := extractCertHashes(ln1.Multiaddr())
	require.Len(t, hashes1, 2)
	hashes2 := extractCertHashes(ln2.Multiaddr())
	require.Equal(t, hashes1, hashes2)
}

func TestResourceManagerDialing(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	rcmgr := mocknetwork.NewMockResourceManager(ctrl)

	addr := ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport")
	p := peer.ID("foobar")

	_, key := newIdentity(t)
	tr, err := libp2pwebtransport.New(key, nil, newConnManager(t), nil, rcmgr)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	l, err := tr.Listen(addr)
	require.NoError(t, err)

	addr = l.Multiaddr()

	scope := mocknetwork.NewMockConnManagementScope(ctrl)
	rcmgr.EXPECT().OpenConnection(network.DirOutbound, false, addr).Return(scope, nil)
	scope.EXPECT().SetPeer(p).Return(errors.New("denied"))
	scope.EXPECT().Done()

	_, err = tr.Dial(context.Background(), addr, p)
	require.EqualError(t, err, "denied")
}

func TestResourceManagerListening(t *testing.T) {
	clientID, key := newIdentity(t)
	cl, err := libp2pwebtransport.New(key, nil, newConnManager(t), nil, &network.NullResourceManager{})
	require.NoError(t, err)
	defer cl.(io.Closer).Close()

	t.Run("blocking the connection", func(t *testing.T) {
		serverID, key := newIdentity(t)
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		rcmgr := mocknetwork.NewMockResourceManager(ctrl)
		tr, err := libp2pwebtransport.New(key, nil, newConnManager(t), nil, rcmgr)
		require.NoError(t, err)
		ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
		require.NoError(t, err)
		defer ln.Close()

		rcmgr.EXPECT().OpenConnection(network.DirInbound, false, gomock.Any()).DoAndReturn(func(_ network.Direction, _ bool, addr ma.Multiaddr) (network.ConnManagementScope, error) {
			_, err := addr.ValueForProtocol(ma.P_WEBTRANSPORT)
			require.NoError(t, err, "expected a WebTransport multiaddr")
			_, addrStr, err := manet.DialArgs(addr)
			require.NoError(t, err)
			host, _, err := net.SplitHostPort(addrStr)
			require.NoError(t, err)
			require.Equal(t, "127.0.0.1", host)
			return nil, errors.New("denied")
		})

		_, err = cl.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.EqualError(t, err, "received status 503")
	})

	t.Run("blocking the peer", func(t *testing.T) {
		serverID, key := newIdentity(t)
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		rcmgr := mocknetwork.NewMockResourceManager(ctrl)
		tr, err := libp2pwebtransport.New(key, nil, newConnManager(t), nil, rcmgr)
		require.NoError(t, err)
		ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
		require.NoError(t, err)
		defer ln.Close()

		serverDone := make(chan struct{})
		scope := mocknetwork.NewMockConnManagementScope(ctrl)
		rcmgr.EXPECT().OpenConnection(network.DirInbound, false, gomock.Any()).Return(scope, nil)
		scope.EXPECT().SetPeer(clientID).Return(errors.New("denied"))
		scope.EXPECT().Done().Do(func() { close(serverDone) })

		// The handshake will complete, but the server will immediately close the connection.
		conn, err := cl.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.NoError(t, err)
		defer conn.Close()
		clientDone := make(chan struct{})
		go func() {
			defer close(clientDone)
			_, err = conn.AcceptStream()
			require.Error(t, err)
		}()
		select {
		case <-clientDone:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout")
		}
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout")
		}
	})
}

// TODO: unify somehow. We do the same in libp2pquic.
//go:generate sh -c "go run github.com/golang/mock/mockgen -package libp2pwebtransport_test -destination mock_connection_gater_test.go github.com/libp2p/go-libp2p/core/connmgr ConnectionGater && go run golang.org/x/tools/cmd/goimports -w mock_connection_gater_test.go"

func TestConnectionGaterDialing(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	connGater := NewMockConnectionGater(ctrl)

	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, nil, newConnManager(t), nil, &network.NullResourceManager{})
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)
	defer ln.Close()

	connGater.EXPECT().InterceptSecured(network.DirOutbound, serverID, gomock.Any()).Do(func(_ network.Direction, _ peer.ID, addrs network.ConnMultiaddrs) {
		require.Equal(t, stripCertHashes(ln.Multiaddr()), addrs.RemoteMultiaddr())
	})
	_, key := newIdentity(t)
	cl, err := libp2pwebtransport.New(key, nil, newConnManager(t), connGater, &network.NullResourceManager{})
	require.NoError(t, err)
	defer cl.(io.Closer).Close()
	_, err = cl.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.EqualError(t, err, "secured connection gated")
}

func TestConnectionGaterInterceptAccept(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	connGater := NewMockConnectionGater(ctrl)

	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, nil, newConnManager(t), connGater, &network.NullResourceManager{})
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)
	defer ln.Close()

	connGater.EXPECT().InterceptAccept(gomock.Any()).Do(func(addrs network.ConnMultiaddrs) {
		require.Equal(t, stripCertHashes(ln.Multiaddr()), addrs.LocalMultiaddr())
		require.NotEqual(t, stripCertHashes(ln.Multiaddr()), addrs.RemoteMultiaddr())
	})

	_, key := newIdentity(t)
	cl, err := libp2pwebtransport.New(key, nil, newConnManager(t), nil, &network.NullResourceManager{})
	require.NoError(t, err)
	defer cl.(io.Closer).Close()
	_, err = cl.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.EqualError(t, err, "received status 403")
}

func TestConnectionGaterInterceptSecured(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	connGater := NewMockConnectionGater(ctrl)

	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, nil, newConnManager(t), connGater, &network.NullResourceManager{})
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)
	defer ln.Close()

	clientID, key := newIdentity(t)
	cl, err := libp2pwebtransport.New(key, nil, newConnManager(t), nil, &network.NullResourceManager{})
	require.NoError(t, err)
	defer cl.(io.Closer).Close()

	connGater.EXPECT().InterceptAccept(gomock.Any()).Return(true)
	connGater.EXPECT().InterceptSecured(network.DirInbound, clientID, gomock.Any()).Do(func(_ network.Direction, _ peer.ID, addrs network.ConnMultiaddrs) {
		require.Equal(t, stripCertHashes(ln.Multiaddr()), addrs.LocalMultiaddr())
		require.NotEqual(t, stripCertHashes(ln.Multiaddr()), addrs.RemoteMultiaddr())
	})
	// The handshake will complete, but the server will immediately close the connection.
	conn, err := cl.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.NoError(t, err)
	defer conn.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err = conn.AcceptStream()
		require.Error(t, err)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
}

func TestAcceptQueueFilledUp(t *testing.T) {
	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, nil, newConnManager(t), nil, &network.NullResourceManager{})
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)
	defer ln.Close()

	newConn := func() (tpt.CapableConn, error) {
		t.Helper()
		_, key := newIdentity(t)
		cl, err := libp2pwebtransport.New(key, nil, newConnManager(t), nil, &network.NullResourceManager{})
		require.NoError(t, err)
		defer cl.(io.Closer).Close()
		return cl.Dial(context.Background(), ln.Multiaddr(), serverID)
	}

	const num = 16 + 1 // one more than the accept queue capacity
	// Dial one more connection than the accept queue can hold.
	errChan := make(chan error, num)
	for i := 0; i < num; i++ {
		go func() {
			conn, err := newConn()
			if err != nil {
				errChan <- err
				return
			}
			_, err = conn.AcceptStream()
			errChan <- err
		}()
	}

	// Since the handshakes complete asynchronously, we won't know _which_ one is rejected,
	// so the only thing we can test for is that exactly one connection attempt is rejected.
	select {
	case <-errChan:
	case <-time.After(time.Second):
		t.Fatal("expected one connection to be rejected")
	}
	select {
	case <-errChan:
		t.Fatal("only expected one connection to be rejected")
	case <-time.After(100 * time.Millisecond):
	}

	// test shutdown
	require.NoError(t, ln.Close())
	var count int
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for i := 0; i < 16; i++ {
		select {
		case <-errChan:
			count++
			if count == 16 {
				return
			}
		case <-timer.C:
			t.Fatal("shutdown failed")
		}
	}
}

type reportingRcmgr struct {
	network.NullResourceManager
	report chan<- int
}

func (m *reportingRcmgr) OpenConnection(dir network.Direction, usefd bool, endpoint ma.Multiaddr) (network.ConnManagementScope, error) {
	return &reportingScope{report: m.report}, nil
}

type reportingScope struct {
	network.NullScope
	report chan<- int
}

func (s *reportingScope) ReserveMemory(size int, _ uint8) error {
	s.report <- size
	return nil
}

func TestFlowControlWindowIncrease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test is flaky on Windows")
	}

	rtt := 10 * time.Millisecond
	timeout := 5 * time.Second

	if os.Getenv("CI") != "" {
		rtt = 40 * time.Millisecond
		timeout = 15 * time.Second
	}

	serverID, serverKey := newIdentity(t)
	serverWindowIncreases := make(chan int, 100)
	serverRcmgr := &reportingRcmgr{report: serverWindowIncreases}
	tr, err := libp2pwebtransport.New(serverKey, nil, newConnManager(t), nil, serverRcmgr)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		require.NoError(t, err)
		str, err := conn.AcceptStream()
		require.NoError(t, err)
		_, err = io.CopyBuffer(str, str, make([]byte, 2<<10))
		require.NoError(t, err)
		str.CloseWrite()
	}()

	proxy, err := quicproxy.NewQuicProxy("localhost:0", &quicproxy.Opts{
		RemoteAddr:  ln.Addr().String(),
		DelayPacket: func(quicproxy.Direction, []byte) time.Duration { return rtt / 2 },
	})
	require.NoError(t, err)
	defer proxy.Close()

	_, clientKey := newIdentity(t)
	clientWindowIncreases := make(chan int, 100)
	clientRcmgr := &reportingRcmgr{report: clientWindowIncreases}
	tr2, err := libp2pwebtransport.New(clientKey, nil, newConnManager(t), nil, clientRcmgr)
	require.NoError(t, err)
	defer tr2.(io.Closer).Close()

	var addr ma.Multiaddr
	for _, comp := range ma.Split(ln.Multiaddr()) {
		if _, err := comp.ValueForProtocol(ma.P_UDP); err == nil {
			addr = addr.Encapsulate(ma.StringCast(fmt.Sprintf("/udp/%d", proxy.LocalPort())))
			continue
		}
		if addr == nil {
			addr = comp
			continue
		}
		addr = addr.Encapsulate(comp)
	}

	conn, err := tr2.Dial(context.Background(), addr, serverID)
	require.NoError(t, err)
	str, err := conn.OpenStream(context.Background())
	require.NoError(t, err)
	var increasesDone atomic.Bool
	go func() {
		for {
			_, err := str.Write(bytes.Repeat([]byte{0x42}, 1<<10))
			require.NoError(t, err)
			if increasesDone.Load() {
				str.CloseWrite()
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := io.ReadAll(str)
		require.NoError(t, err)
	}()

	var numServerIncreases, numClientIncreases int
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-serverWindowIncreases:
			numServerIncreases++
		case <-clientWindowIncreases:
			numClientIncreases++
		case <-timer.C:
			t.Fatalf("didn't receive enough window increases (client: %d, server: %d)", numClientIncreases, numServerIncreases)
		}
		if numClientIncreases >= 1 && numServerIncreases >= 1 {
			increasesDone.Store(true)
			break
		}
	}

	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("timeout")
	}
}

var errTimeout = errors.New("timeout")

func serverSendsBackValidCert(t *testing.T, timeSinceUnixEpoch time.Duration, keySeed int64, randomClientSkew time.Duration) error {
	if timeSinceUnixEpoch < 0 {
		timeSinceUnixEpoch = -timeSinceUnixEpoch
	}

	// Bound this to 100 years
	timeSinceUnixEpoch = time.Duration(timeSinceUnixEpoch % (time.Hour * 24 * 365 * 100))
	// Start a bit further in the future to avoid edge cases around epoch
	timeSinceUnixEpoch += time.Hour * 24 * 365
	start := time.UnixMilli(timeSinceUnixEpoch.Milliseconds())

	randomClientSkew = randomClientSkew % clockSkewAllowance

	cl := clock.NewMock()
	cl.Set(start)

	priv, _, err := test.SeededTestKeyPair(ic.Ed25519, 256, keySeed)
	require.NoError(t, err)
	tr, err := libp2pwebtransport.New(priv, nil, newConnManager(t), nil, &network.NullResourceManager{}, libp2pwebtransport.WithClock(cl))
	require.NoError(t, err)
	l, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)
	defer l.Close()

	conn, err := quic.DialAddr(context.Background(), l.Addr().String(), &tls.Config{
		NextProtos:         []string{http3.NextProtoH3},
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			for _, c := range rawCerts {
				cert, err := x509.ParseCertificate(c)
				if err != nil {
					return err
				}

				for _, clientSkew := range []time.Duration{randomClientSkew, -clockSkewAllowance, clockSkewAllowance} {
					clientTime := cl.Now().Add(clientSkew)
					if clientTime.After(cert.NotAfter) || clientTime.Before(cert.NotBefore) {
						return fmt.Errorf("Times are not valid: server_now=%v client_now=%v certstart=%v certend=%v", cl.Now().UTC(), clientTime.UTC(), cert.NotBefore.UTC(), cert.NotAfter.UTC())
					}
				}

			}
			return nil
		},
	}, &quic.Config{MaxIdleTimeout: time.Second})

	if err != nil {
		if _, ok := err.(*quic.IdleTimeoutError); ok {
			return errTimeout
		}
		return err
	}
	defer conn.CloseWithError(0, "")

	return nil
}

func TestServerSendsBackValidCert(t *testing.T) {
	var maxTimeoutErrors = 10
	require.NoError(t, quick.Check(func(timeSinceUnixEpoch time.Duration, keySeed int64, randomClientSkew time.Duration) bool {
		err := serverSendsBackValidCert(t, timeSinceUnixEpoch, keySeed, randomClientSkew)
		if err == errTimeout {
			maxTimeoutErrors -= 1
			if maxTimeoutErrors <= 0 {
				fmt.Println("Too many timeout errors")
				return false
			}
			// Sporadic timeout errors on macOS
			return true
		} else if err != nil {
			fmt.Println("Err:", err)
			return false
		}

		return true
	}, nil))
}

func TestServerRotatesCertCorrectly(t *testing.T) {
	require.NoError(t, quick.Check(func(timeSinceUnixEpoch time.Duration, keySeed int64) bool {
		if timeSinceUnixEpoch < 0 {
			timeSinceUnixEpoch = -timeSinceUnixEpoch
		}

		// Bound this to 100 years
		timeSinceUnixEpoch = time.Duration(timeSinceUnixEpoch % (time.Hour * 24 * 365 * 100))
		// Start a bit further in the future to avoid edge cases around epoch
		timeSinceUnixEpoch += time.Hour * 24 * 365
		start := time.UnixMilli(timeSinceUnixEpoch.Milliseconds())

		cl := clock.NewMock()
		cl.Set(start)

		priv, _, err := test.SeededTestKeyPair(ic.Ed25519, 256, keySeed)
		if err != nil {
			return false
		}
		tr, err := libp2pwebtransport.New(priv, nil, newConnManager(t), nil, &network.NullResourceManager{}, libp2pwebtransport.WithClock(cl))
		if err != nil {
			return false
		}

		l, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
		if err != nil {
			return false
		}
		certhashes := extractCertHashes(l.Multiaddr())
		l.Close()

		// These two certificates together are valid for at most certValidity - (4*clockSkewAllowance)
		cl.Add(certValidity - (4 * clockSkewAllowance) - time.Second)
		tr, err = libp2pwebtransport.New(priv, nil, newConnManager(t), nil, &network.NullResourceManager{}, libp2pwebtransport.WithClock(cl))
		if err != nil {
			return false
		}

		l, err = tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
		if err != nil {
			return false
		}
		defer l.Close()

		var found bool
		ma.ForEach(l.Multiaddr(), func(c ma.Component) bool {
			if c.Protocol().Code == ma.P_CERTHASH {
				for _, prevCerthash := range certhashes {
					if c.Value() == prevCerthash {
						found = true
						return false
					}
				}
			}
			return true
		})

		return found

	}, nil))
}

func TestServerRotatesCertCorrectlyAfterSteps(t *testing.T) {
	cl := clock.NewMock()
	// Move one year ahead to avoid edge cases around epoch
	cl.Add(time.Hour * 24 * 365)

	priv, _, err := test.RandTestKeyPair(ic.Ed25519, 256)
	require.NoError(t, err)
	tr, err := libp2pwebtransport.New(priv, nil, newConnManager(t), nil, &network.NullResourceManager{}, libp2pwebtransport.WithClock(cl))
	require.NoError(t, err)

	l, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
	require.NoError(t, err)

	certhashes := extractCertHashes(l.Multiaddr())
	l.Close()

	// Traverse various time boundaries and make sure we always keep a common certhash.
	// e.g. certhash/A/certhash/B ... -> ... certhash/B/certhash/C ... -> ... certhash/C/certhash/D
	for i := 0; i < 200; i++ {
		cl.Add(24 * time.Hour)
		tr, err := libp2pwebtransport.New(priv, nil, newConnManager(t), nil, &network.NullResourceManager{}, libp2pwebtransport.WithClock(cl))
		require.NoError(t, err)
		l, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
		require.NoError(t, err)

		var found bool
		ma.ForEach(l.Multiaddr(), func(c ma.Component) bool {
			if c.Protocol().Code == ma.P_CERTHASH {
				for _, prevCerthash := range certhashes {
					if prevCerthash == c.Value() {
						found = true
						return false
					}
				}
			}
			return true
		})
		certhashes = extractCertHashes(l.Multiaddr())
		l.Close()

		require.True(t, found, "Failed after hour: %v", i)
	}
}
