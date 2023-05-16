package swarm

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/sec/insecure"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/stretchr/testify/require"
)

func newPeer(t *testing.T) (crypto.PrivKey, peer.ID) {
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)
	return priv, id
}

func makeSwarm(t *testing.T) *Swarm {
	priv, id := newPeer(t)

	ps, err := pstoremem.NewPeerstore()
	require.NoError(t, err)
	ps.AddPubKey(id, priv.GetPublic())
	ps.AddPrivKey(id, priv)
	t.Cleanup(func() { ps.Close() })

	s, err := NewSwarm(id, ps, eventbus.NewBus(), WithDialTimeout(time.Second))
	require.NoError(t, err)

	upgrader := makeUpgrader(t, s)

	var tcpOpts []tcp.Option
	tcpOpts = append(tcpOpts, tcp.DisableReuseport())
	tcpTransport, err := tcp.NewTCPTransport(upgrader, nil, tcpOpts...)
	require.NoError(t, err)
	if err := s.AddTransport(tcpTransport); err != nil {
		t.Fatal(err)
	}
	if err := s.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0")); err != nil {
		t.Fatal(err)
	}

	reuse, err := quicreuse.NewConnManager([32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	quicTransport, err := quic.NewTransport(priv, reuse, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTransport(quicTransport); err != nil {
		t.Fatal(err)
	}
	if err := s.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic")); err != nil {
		t.Fatal(err)
	}

	return s
}

func makeUpgrader(t *testing.T, n *Swarm) transport.Upgrader {
	id := n.LocalPeer()
	pk := n.Peerstore().PrivKey(id)
	st := insecure.NewWithIdentity(insecure.ID, id, pk)

	u, err := tptu.New([]sec.SecureTransport{st}, []tptu.StreamMuxer{{ID: yamux.ID, Muxer: yamux.DefaultTransport}}, nil, nil, nil)
	require.NoError(t, err)
	return u
}

func TestDialWorkerLoopBasic(t *testing.T) {
	s1 := makeSwarm(t)
	s2 := makeSwarm(t)
	defer s1.Close()
	defer s2.Close()

	// Only pass in a single address here, otherwise we might end up with a TCP and QUIC connection dialed.
	s1.Peerstore().AddAddrs(s2.LocalPeer(), []ma.Multiaddr{s2.ListenAddresses()[0]}, peerstore.PermanentAddrTTL)

	reqch := make(chan dialRequest)
	resch := make(chan dialResponse)
	worker := newDialWorker(s1, s2.LocalPeer(), reqch)
	go worker.loop()

	var conn *Conn
	reqch <- dialRequest{ctx: context.Background(), resch: resch}
	select {
	case res := <-resch:
		require.NoError(t, res.err)
		conn = res.conn
	case <-time.After(10 * time.Second):
		t.Fatal("dial didn't complete")
	}

	s, err := conn.NewStream(context.Background())
	require.NoError(t, err)
	s.Close()

	var conn2 *Conn
	reqch <- dialRequest{ctx: context.Background(), resch: resch}
	select {
	case res := <-resch:
		require.NoError(t, res.err)
		conn2 = res.conn
	case <-time.After(10 * time.Second):
		t.Fatal("dial didn't complete")
	}

	// can't use require.Equal here, as this does a deep comparison
	if conn != conn2 {
		t.Fatalf("expecting the same connection from both dials. %s <-> %s vs. %s <-> %s", conn.LocalMultiaddr(), conn.RemoteMultiaddr(), conn2.LocalMultiaddr(), conn2.RemoteMultiaddr())
	}

	close(reqch)
	worker.wg.Wait()
}

func TestDialWorkerLoopConcurrent(t *testing.T) {
	s1 := makeSwarm(t)
	s2 := makeSwarm(t)
	defer s1.Close()
	defer s2.Close()

	s1.Peerstore().AddAddrs(s2.LocalPeer(), s2.ListenAddresses(), peerstore.PermanentAddrTTL)

	reqch := make(chan dialRequest)
	worker := newDialWorker(s1, s2.LocalPeer(), reqch)
	go worker.loop()

	const dials = 100
	var wg sync.WaitGroup
	resch := make(chan dialResponse, dials)
	for i := 0; i < dials; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reschgo := make(chan dialResponse, 1)
			reqch <- dialRequest{ctx: context.Background(), resch: reschgo}
			select {
			case res := <-reschgo:
				resch <- res
			case <-time.After(time.Minute):
				resch <- dialResponse{err: errors.New("timed out!")}
			}
		}()
	}
	wg.Wait()

	for i := 0; i < dials; i++ {
		res := <-resch
		require.NoError(t, res.err)
	}

	t.Log("all concurrent dials done")

	close(reqch)
	worker.wg.Wait()
}

func TestDialWorkerLoopFailure(t *testing.T) {
	s1 := makeSwarm(t)
	defer s1.Close()

	_, p2 := newPeer(t)

	s1.Peerstore().AddAddrs(p2, []ma.Multiaddr{ma.StringCast("/ip4/11.0.0.1/tcp/1234"), ma.StringCast("/ip4/11.0.0.1/udp/1234/quic")}, peerstore.PermanentAddrTTL)

	reqch := make(chan dialRequest)
	resch := make(chan dialResponse)
	worker := newDialWorker(s1, p2, reqch)
	go worker.loop()

	reqch <- dialRequest{ctx: context.Background(), resch: resch}
	select {
	case res := <-resch:
		require.Error(t, res.err)
	case <-time.After(time.Minute):
		t.Fatal("dial didn't complete")
	}

	close(reqch)
	worker.wg.Wait()
}

func TestDialWorkerLoopConcurrentFailure(t *testing.T) {
	s1 := makeSwarm(t)
	defer s1.Close()

	_, p2 := newPeer(t)

	s1.Peerstore().AddAddrs(p2, []ma.Multiaddr{ma.StringCast("/ip4/11.0.0.1/tcp/1234"), ma.StringCast("/ip4/11.0.0.1/udp/1234/quic")}, peerstore.PermanentAddrTTL)

	reqch := make(chan dialRequest)
	worker := newDialWorker(s1, p2, reqch)
	go worker.loop()

	const dials = 100
	var errTimeout = errors.New("timed out!")
	var wg sync.WaitGroup
	resch := make(chan dialResponse, dials)
	for i := 0; i < dials; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reschgo := make(chan dialResponse, 1)
			reqch <- dialRequest{ctx: context.Background(), resch: reschgo}

			select {
			case res := <-reschgo:
				resch <- res
			case <-time.After(time.Minute):
				resch <- dialResponse{err: errTimeout}
			}
		}()
	}
	wg.Wait()

	for i := 0; i < dials; i++ {
		res := <-resch
		require.Error(t, res.err)
		if res.err == errTimeout {
			t.Fatal("dial response timed out")
		}
	}

	t.Log("all concurrent dials done")

	close(reqch)
	worker.wg.Wait()
}

func TestDialWorkerLoopConcurrentMix(t *testing.T) {
	s1 := makeSwarm(t)
	s2 := makeSwarm(t)
	defer s1.Close()
	defer s2.Close()

	s1.Peerstore().AddAddrs(s2.LocalPeer(), s2.ListenAddresses(), peerstore.PermanentAddrTTL)
	s1.Peerstore().AddAddrs(s2.LocalPeer(), []ma.Multiaddr{ma.StringCast("/ip4/11.0.0.1/tcp/1234"), ma.StringCast("/ip4/11.0.0.1/udp/1234/quic")}, peerstore.PermanentAddrTTL)

	reqch := make(chan dialRequest)
	worker := newDialWorker(s1, s2.LocalPeer(), reqch)
	go worker.loop()

	const dials = 100
	var wg sync.WaitGroup
	resch := make(chan dialResponse, dials)
	for i := 0; i < dials; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reschgo := make(chan dialResponse, 1)
			reqch <- dialRequest{ctx: context.Background(), resch: reschgo}
			select {
			case res := <-reschgo:
				resch <- res
			case <-time.After(time.Minute):
				resch <- dialResponse{err: errors.New("timed out!")}
			}
		}()
	}
	wg.Wait()

	for i := 0; i < dials; i++ {
		res := <-resch
		require.NoError(t, res.err)
	}

	t.Log("all concurrent dials done")

	close(reqch)
	worker.wg.Wait()
}

func TestDialWorkerLoopConcurrentFailureStress(t *testing.T) {
	s1 := makeSwarm(t)
	defer s1.Close()

	_, p2 := newPeer(t)

	var addrs []ma.Multiaddr
	for i := 0; i < 16; i++ {
		addrs = append(addrs, ma.StringCast(fmt.Sprintf("/ip4/11.0.0.%d/tcp/%d", i%256, 1234+i)))
	}
	s1.Peerstore().AddAddrs(p2, addrs, peerstore.PermanentAddrTTL)

	reqch := make(chan dialRequest)
	worker := newDialWorker(s1, p2, reqch)
	go worker.loop()

	const dials = 100
	var errTimeout = errors.New("timed out!")
	var wg sync.WaitGroup
	resch := make(chan dialResponse, dials)
	for i := 0; i < dials; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reschgo := make(chan dialResponse, 1)
			reqch <- dialRequest{ctx: context.Background(), resch: reschgo}
			select {
			case res := <-reschgo:
				t.Log("received result")
				resch <- res
			case <-time.After(15 * time.Second):
				resch <- dialResponse{err: errTimeout}
			}
		}()
	}
	wg.Wait()

	for i := 0; i < dials; i++ {
		res := <-resch
		require.Error(t, res.err)
		if res.err == errTimeout {
			t.Fatal("dial response timed out")
		}
	}

	t.Log("all concurrent dials done")

	close(reqch)
	worker.wg.Wait()
}

func TestDialWorkerLoopAddrDedup(t *testing.T) {
	s1 := makeSwarm(t)
	s2 := makeSwarm(t)
	defer s1.Close()
	defer s2.Close()
	t1 := ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", 10000))
	t2 := ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", 10000))

	// acceptAndClose accepts a connection and closes it
	acceptAndClose := func(a ma.Multiaddr, ch chan struct{}, closech chan struct{}) {
		list, err := manet.Listen(a)
		if err != nil {
			t.Error(err)
			return
		}
		go func() {
			ch <- struct{}{}
			for {
				conn, err := list.Accept()
				if err != nil {
					return
				}
				ch <- struct{}{}
				conn.Close()
			}
		}()
		<-closech
		list.Close()
	}
	ch := make(chan struct{}, 1)
	closeCh := make(chan struct{})
	go acceptAndClose(t1, ch, closeCh)
	defer close(closeCh)
	<-ch // the routine has started listening on addr

	s1.Peerstore().AddAddrs(s2.LocalPeer(), []ma.Multiaddr{t1}, peerstore.PermanentAddrTTL)

	reqch := make(chan dialRequest)
	resch := make(chan dialResponse, 2)

	worker := newDialWorker(s1, s2.LocalPeer(), reqch)
	go worker.loop()
	defer worker.wg.Wait()
	defer close(reqch)

	reqch <- dialRequest{ctx: context.Background(), resch: resch}
	<-ch
	<-resch
	// Need to clear backoff otherwise the dial attempt would not be made
	s1.Backoff().Clear(s2.LocalPeer())

	s1.Peerstore().ClearAddrs(s2.LocalPeer())
	s1.Peerstore().AddAddrs(s2.LocalPeer(), []ma.Multiaddr{t2}, peerstore.PermanentAddrTTL)

	reqch <- dialRequest{ctx: context.Background(), resch: resch}
	select {
	case r := <-resch:
		require.Error(t, r.err)
	case <-ch:
		t.Errorf("didn't expect a connection attempt")
	case <-time.After(5 * time.Second):
		t.Errorf("expected a fail response")
	}
}
