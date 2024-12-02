package swarm

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	mrand "math/rand"
	"reflect"
	"sort"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/sec/insecure"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

type mockClock struct {
	*test.MockClock
}

func (m *mockClock) InstantTimer(when time.Time) InstantTimer {
	return m.MockClock.InstantTimer(when)
}

func newMockClock() *mockClock {
	return &mockClock{test.NewMockClock()}
}

func newPeer(t *testing.T) (crypto.PrivKey, peer.ID) {
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)
	return priv, id
}

func makeSwarm(t *testing.T) *Swarm {
	s := makeSwarmWithNoListenAddrs(t, WithDialTimeout(1*time.Second))
	if err := s.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0")); err != nil {
		t.Fatal(err)
	}

	if err := s.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")); err != nil {
		t.Fatal(err)
	}

	return s
}

func makeSwarmWithNoListenAddrs(t *testing.T, opts ...Option) *Swarm {
	priv, id := newPeer(t)

	ps, err := pstoremem.NewPeerstore()
	require.NoError(t, err)
	ps.AddPubKey(id, priv.GetPublic())
	ps.AddPrivKey(id, priv)
	t.Cleanup(func() { ps.Close() })

	s, err := NewSwarm(id, ps, eventbus.NewBus(), opts...)
	require.NoError(t, err)

	upgrader := makeUpgrader(t, s)
	var tcpOpts []tcp.Option
	tcpOpts = append(tcpOpts, tcp.DisableReuseport())
	tcpTransport, err := tcp.NewTCPTransport(upgrader, nil, nil, tcpOpts...)
	require.NoError(t, err)
	if err := s.AddTransport(tcpTransport); err != nil {
		t.Fatal(err)
	}
	reuse, err := quicreuse.NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{})
	if err != nil {
		t.Fatal(err)
	}
	quicTransport, err := libp2pquic.NewTransport(priv, reuse, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTransport(quicTransport); err != nil {
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

// makeTCPListener listens on tcp address a. On accepting a connection it notifies recvCh. Sending a message to
// channel ch will close an accepted connection
func makeTCPListener(t *testing.T, a ma.Multiaddr, recvCh chan struct{}) (list manet.Listener, ch chan struct{}) {
	t.Helper()
	list, err := manet.Listen(a)
	if err != nil {
		t.Fatal(err)
	}
	ch = make(chan struct{})
	go func() {
		for {
			c, err := list.Accept()
			if err != nil {
				break
			}
			recvCh <- struct{}{}
			<-ch
			err = c.Close()
			if err != nil {
				t.Error(err)
			}

		}
	}()
	return list, ch
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
	worker := newDialWorker(s1, s2.LocalPeer(), reqch, nil)
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
	worker := newDialWorker(s1, s2.LocalPeer(), reqch, nil)
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

	s1.Peerstore().AddAddrs(p2, []ma.Multiaddr{ma.StringCast("/ip4/11.0.0.1/tcp/1234"), ma.StringCast("/ip4/11.0.0.1/udp/1234/quic-v1")}, peerstore.PermanentAddrTTL)

	reqch := make(chan dialRequest)
	resch := make(chan dialResponse)
	worker := newDialWorker(s1, p2, reqch, nil)
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

	s1.Peerstore().AddAddrs(p2, []ma.Multiaddr{ma.StringCast("/ip4/11.0.0.1/tcp/1234"), ma.StringCast("/ip4/11.0.0.1/udp/1234/quic-v1")}, peerstore.PermanentAddrTTL)

	reqch := make(chan dialRequest)
	worker := newDialWorker(s1, p2, reqch, nil)
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
	s1.Peerstore().AddAddrs(s2.LocalPeer(), []ma.Multiaddr{ma.StringCast("/ip4/11.0.0.1/tcp/1234"), ma.StringCast("/ip4/11.0.0.1/udp/1234/quic-v1")}, peerstore.PermanentAddrTTL)

	reqch := make(chan dialRequest)
	worker := newDialWorker(s1, s2.LocalPeer(), reqch, nil)
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
	worker := newDialWorker(s1, p2, reqch, nil)
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

func TestDialQueueNextBatch(t *testing.T) {
	addrs := make([]ma.Multiaddr, 0)
	for i := 0; i < 10; i++ {
		addrs = append(addrs, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.4/tcp/%d", i)))
	}
	testcase := []struct {
		name       string
		input      []network.AddrDelay
		output     [][]ma.Multiaddr
		hasUpdates bool
	}{
		{
			name: "next batch",
			input: []network.AddrDelay{
				{Addr: addrs[0], Delay: 3},
				{Addr: addrs[1], Delay: 2},
				{Addr: addrs[2], Delay: 1},
				{Addr: addrs[3], Delay: 1},
			},
			output: [][]ma.Multiaddr{
				{addrs[2], addrs[3]},
				{addrs[1]},
				{addrs[0]},
			},
		},
		{
			name: "priority queue property 2",
			input: []network.AddrDelay{
				{Addr: addrs[0], Delay: 5},
				{Addr: addrs[1], Delay: 3},
				{Addr: addrs[2], Delay: 2},
				{Addr: addrs[3], Delay: 1},
				{Addr: addrs[4], Delay: 1},
			},

			output: [][]ma.Multiaddr{
				{addrs[3], addrs[4]},
				{addrs[2]},
				{addrs[1]},
				{addrs[0]},
			},
		},
		{
			name: "updates",
			input: []network.AddrDelay{
				{Addr: addrs[0], Delay: 3}, // decreasing order
				{Addr: addrs[1], Delay: 3},
				{Addr: addrs[2], Delay: 2},
				{Addr: addrs[3], Delay: 2},
				{Addr: addrs[4], Delay: 1},
				{Addr: addrs[0], Delay: 1}, // increasing order
				{Addr: addrs[1], Delay: 1},
				{Addr: addrs[2], Delay: 2},
				{Addr: addrs[3], Delay: 2},
				{Addr: addrs[4], Delay: 3},
			},
			output: [][]ma.Multiaddr{
				{addrs[0], addrs[1]},
				{addrs[2], addrs[3]},
				{addrs[4]},
				{},
			},
			hasUpdates: true,
		},
		{
			name:  "null input",
			input: []network.AddrDelay{},
			output: [][]ma.Multiaddr{
				{},
				{},
			},
		},
	}
	for _, tc := range testcase {
		t.Run(tc.name, func(t *testing.T) {
			q := newDialQueue()
			for i := 0; i < len(tc.input); i++ {
				if tc.hasUpdates {
					q.UpdateOrAdd(tc.input[i])
				} else {
					q.Add(tc.input[i])
				}
			}
			for _, batch := range tc.output {
				b := q.NextBatch()
				if len(batch) != len(b) {
					t.Errorf("expected %d elements got %d", len(batch), len(b))
				}
				sort.Slice(b, func(i, j int) bool { return b[i].Addr.String() < b[j].Addr.String() })
				sort.Slice(batch, func(i, j int) bool { return batch[i].String() < batch[j].String() })
				for i := 0; i < len(b); i++ {
					if !b[i].Addr.Equal(batch[i]) {
						log.Errorf("expected %s got %s", batch[i], b[i].Addr)
					}
				}
			}
			if q.Len() != 0 {
				t.Errorf("expected queue to be empty at end. got: %d", q.Len())
			}
		})
	}
}

// timedDial is a dial to a single address of the peer
type timedDial struct {
	// addr is the address to dial
	addr ma.Multiaddr
	// delay is the delay after which this address should be dialed
	delay time.Duration
	// success indicates whether the dial should succeed
	success bool
	// failAfter is how long this dial should take to fail after it is dialed
	failAfter time.Duration
}

// schedulingTestCase is used to test dialWorker loop scheduler logic
// a ranker is made according to `input` which provides the addresses to
// dial worker loop with the specified delays
// checkDialWorkerLoopScheduling then verifies that the different dial calls are
// made at the right moments
type schedulingTestCase struct {
	name        string
	input       []timedDial
	maxDuration time.Duration
}

// schedulingTestCase generates a random test case
func (s schedulingTestCase) Generate(rand *mrand.Rand, size int) reflect.Value {
	if size > 20 {
		size = 20
	}
	input := make([]timedDial, size)
	delays := make(map[time.Duration]struct{})
	for i := 0; i < size; i++ {
		input[i] = timedDial{
			addr:      ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", i+10550)),
			delay:     time.Duration(mrand.Intn(100)) * 10 * time.Millisecond, // max 1 second
			success:   false,
			failAfter: time.Duration(mrand.Intn(100)) * 10 * time.Millisecond, // max 1 second
		}
		delays[input[i].delay] = struct{}{}
	}
	successIdx := rand.Intn(size)
	for {
		// set a unique delay for success. This is required to test the property that
		// no extra dials are made after success
		d := time.Duration(rand.Intn(100)) * 10 * time.Millisecond
		if _, ok := delays[d]; !ok {
			input[successIdx].delay = d
			input[successIdx].success = true
			break
		}
	}
	return reflect.ValueOf(schedulingTestCase{
		name:        "",
		input:       input,
		maxDuration: 10 * time.Second, // not tested here
	})
}

// dialState is used to track the dials for testing dialWorker ranking logic
type dialState struct {
	// ch is the chan used to trigger dial failure.
	ch chan struct{}
	// addr is the address of the dial
	addr ma.Multiaddr
	// delay is the delay after which this address should be dialed
	delay time.Duration
	// success indicates whether the dial should succeed
	success bool
	// failAfter is how long this dial should take to fail after it is dialed
	failAfter time.Duration
	// failAt is the instant at which this dial should fail if success is false
	failAt time.Time
}

// checkDialWorkerLoopScheduling verifies whether s1 dials s2 according to the
// schedule specified by the test case tc
func checkDialWorkerLoopScheduling(t *testing.T, s1, s2 *Swarm, tc schedulingTestCase) error {
	t.Helper()
	// failDials is used to track dials which should fail in the future
	// at appropriate moment a message is sent to dialState.ch to trigger
	// failure
	failDials := make(map[ma.Multiaddr]dialState)
	// recvCh is used to receive dial notifications for dials that will fail
	recvCh := make(chan struct{}, 100)
	// allDials tracks all pending dials
	allDials := make(map[ma.Multiaddr]dialState)
	// addrs are the peer addresses the swarm will use for dialing
	addrs := make([]ma.Multiaddr, 0)
	// create pending dials
	// we add success cases as a listen address on swarm
	// failed cases are created using makeTCPListener
	for _, inp := range tc.input {
		var failCh chan struct{}
		if inp.success {
			// add the address as a listen address if this dial should succeed
			err := s2.AddListenAddr(inp.addr)
			if err != nil {
				return fmt.Errorf("failed to listen on addr: %s: err: %w", inp.addr, err)
			}
		} else {
			// make a listener which will fail on sending a message to ch
			l, ch := makeTCPListener(t, inp.addr, recvCh)
			failCh = ch
			f := func() {
				err := l.Close()
				if err != nil {
					t.Error(err)
				}
			}
			defer f()
		}
		addrs = append(addrs, inp.addr)
		// add to pending dials
		allDials[inp.addr] = dialState{
			ch:        failCh,
			addr:      inp.addr,
			delay:     inp.delay,
			success:   inp.success,
			failAfter: inp.failAfter,
		}
	}
	// setup the peers addresses
	s1.Peerstore().AddAddrs(s2.LocalPeer(), addrs, peerstore.PermanentAddrTTL)

	// create worker
	reqch := make(chan dialRequest)
	resch := make(chan dialResponse)
	cl := newMockClock()
	st := cl.Now()
	worker1 := newDialWorker(s1, s2.LocalPeer(), reqch, cl)
	go worker1.loop()
	defer worker1.wg.Wait()
	defer close(reqch)

	// trigger the request
	reqch <- dialRequest{ctx: context.Background(), resch: resch}

	connected := false

	// Advance the clock by 10 ms every iteration
	// At every iteration:
	//   Check if any dial should fail. if it should, trigger the failure by sending a message on the
	//   listener failCh
	//   If there are no dials in flight check the most urgent dials have been triggered
	//   If there are dials in flight check that the relevant dials have been triggered
	//   Before next iteration ensure that no unexpected dials are received
loop:
	for {
		// fail any dials that should fail at this instant
		for a, p := range failDials {
			if p.failAt.Before(cl.Now()) || p.failAt == cl.Now() {
				p.ch <- struct{}{}
				delete(failDials, a)
			}
		}
		// if there are no pending dials, next dial should have been triggered
		trigger := len(failDials) == 0

		// mi is the minimum delay of pending dials
		// if trigger is true, all dials with miDelay should have been triggered
		mi := time.Duration(math.MaxInt64)
		for _, ds := range allDials {
			if ds.delay < mi {
				mi = ds.delay
			}
		}
		for a, ds := range allDials {
			if (trigger && mi == ds.delay) ||
				cl.Now().After(st.Add(ds.delay)) ||
				cl.Now() == st.Add(ds.delay) {
				if ds.success {
					// check for success and exit
					select {
					case r := <-resch:
						if r.conn == nil {
							return errors.New("expected connection to succeed")
						}
					// High timeout here is okay. We will exit whenever the other branch
					// is triggered
					case <-time.After(10 * time.Second):
						return errors.New("expected to receive a response")
					}
					connected = true
					break loop
				} else {
					// ensure that a failing dial attempt happened but didn't succeed
					select {
					case <-recvCh:
					case <-resch:
						return errors.New("didn't expect a response")
					// High timeout here is okay. We will exit whenever the other branch
					// is triggered
					case <-time.After(10 * time.Second):
						return errors.New("didn't receive a dial attempt notification")
					}
					failDials[a] = dialState{
						ch:     ds.ch,
						failAt: cl.Now().Add(ds.failAfter),
						addr:   a,
						delay:  ds.delay,
					}
				}
				delete(allDials, a)
			}
		}
		// check for unexpected dials
		select {
		case <-recvCh:
			return errors.New("no dial should have succeeded at this instant")
		default:
		}

		// advance the clock
		cl.AdvanceBy(10 * time.Millisecond)
		// nothing more to do. exit
		if len(failDials) == 0 && len(allDials) == 0 {
			break
		}
	}

	if connected {
		// ensure we don't receive any extra connections
		select {
		case <-recvCh:
			return errors.New("didn't expect a dial attempt")
		case <-time.After(100 * time.Millisecond):
		}
	} else {
		// ensure that we do receive the final error response
		select {
		case r := <-resch:
			require.Error(t, r.err)
		case <-time.After(100 * time.Millisecond):
			return errors.New("expected to receive response")
		}
	}
	// check if this test didn't take too much time
	if cl.Now().Sub(st) > tc.maxDuration {
		return fmt.Errorf("expected test to finish early: expected %d, took: %d", tc.maxDuration, cl.Now().Sub(st))
	}
	return nil
}

// makeRanker takes a slice of timedDial objects and returns a DialRanker
// which will trigger dials to addresses at the specified delays in the timedDials
func makeRanker(tc []timedDial) network.DialRanker {
	return func(addrs []ma.Multiaddr) []network.AddrDelay {
		res := make([]network.AddrDelay, len(tc))
		for i := 0; i < len(tc); i++ {
			res[i] = network.AddrDelay{Addr: tc[i].addr, Delay: tc[i].delay}
		}
		return res
	}
}

// TestCheckDialWorkerLoopScheduling will check the checker
func TestCheckDialWorkerLoopScheduling(t *testing.T) {
	addrs := make([]ma.Multiaddr, 0)
	for i := 0; i < 10; i++ {
		for {
			p := 20000 + i
			addrs = append(addrs, ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", p)))
			break
		}
	}

	tc := schedulingTestCase{
		input: []timedDial{
			{
				addr:    addrs[1],
				delay:   0,
				success: true,
			},
			{
				addr:      addrs[0],
				delay:     100 * time.Millisecond,
				success:   false,
				failAfter: 50 * time.Millisecond,
			},
		},
		maxDuration: 20 * time.Millisecond,
	}
	s1 := makeSwarmWithNoListenAddrs(t)
	s2 := makeSwarmWithNoListenAddrs(t)
	// valid ranking logic, so it shouldn't error
	s1.dialRanker = makeRanker(tc.input)
	err := checkDialWorkerLoopScheduling(t, s1, s2, tc)
	require.NoError(t, err)
	// close swarms to remove address binding
	s1.Close()
	s2.Close()

	s3 := makeSwarmWithNoListenAddrs(t)
	defer s3.Close()
	s4 := makeSwarmWithNoListenAddrs(t)
	defer s4.Close()
	// invalid ranking logic to trigger an error
	s3.dialRanker = NoDelayDialRanker
	err = checkDialWorkerLoopScheduling(t, s3, s4, tc)
	require.Error(t, err)
}

func TestDialWorkerLoopRanking(t *testing.T) {
	addrs := make([]ma.Multiaddr, 0)
	for i := 0; i < 10; i++ {
		for {
			p := 20000 + i
			addrs = append(addrs, ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", p)))
			break
		}
	}

	testcases := []schedulingTestCase{
		{
			name: "first success",
			input: []timedDial{
				{
					addr:    addrs[1],
					delay:   0,
					success: true,
				},
				{
					addr:      addrs[0],
					delay:     100 * time.Millisecond,
					success:   false,
					failAfter: 50 * time.Millisecond,
				},
			},
			maxDuration: 20 * time.Millisecond,
		},
		{
			name: "delayed dials",
			input: []timedDial{
				{
					addr:      addrs[0],
					delay:     0,
					success:   false,
					failAfter: 200 * time.Millisecond,
				},
				{
					addr:      addrs[1],
					delay:     100 * time.Millisecond,
					success:   false,
					failAfter: 100 * time.Millisecond,
				},
				{
					addr:      addrs[2],
					delay:     300 * time.Millisecond,
					success:   false,
					failAfter: 100 * time.Millisecond,
				},
				{
					addr:    addrs[3],
					delay:   2 * time.Second,
					success: true,
				},
				{
					addr:      addrs[4],
					delay:     2*time.Second + 1*time.Millisecond,
					success:   false, // this call will never happened
					failAfter: 100 * time.Millisecond,
				},
			},
			maxDuration: 310 * time.Millisecond,
		},
		{
			name: "failed dials",
			input: []timedDial{
				{
					addr:      addrs[0],
					delay:     0,
					success:   false,
					failAfter: 105 * time.Millisecond,
				},
				{
					addr:      addrs[1],
					delay:     100 * time.Millisecond,
					success:   false,
					failAfter: 20 * time.Millisecond,
				},
			},
			maxDuration: 200 * time.Millisecond,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			s1 := makeSwarmWithNoListenAddrs(t)
			defer s1.Close()
			s2 := makeSwarmWithNoListenAddrs(t)
			defer s2.Close()
			// setup the ranker to trigger dials according to the test case
			s1.dialRanker = makeRanker(tc.input)
			err := checkDialWorkerLoopScheduling(t, s1, s2, tc)
			if err != nil {
				t.Error(err)
			}
		})
	}
}

func TestDialWorkerLoopSchedulingProperty(t *testing.T) {
	f := func(tc schedulingTestCase) bool {
		s1 := makeSwarmWithNoListenAddrs(t)
		defer s1.Close()
		// ignore limiter delays just check scheduling
		s1.limiter.perPeerLimit = 10000
		s2 := makeSwarmWithNoListenAddrs(t)
		defer s2.Close()
		// setup the ranker to trigger dials according to the test case
		s1.dialRanker = makeRanker(tc.input)
		err := checkDialWorkerLoopScheduling(t, s1, s2, tc)
		if err != nil {
			log.Error(err)
		}
		return err == nil
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 50}); err != nil {
		t.Error(err)
	}
}

func TestDialWorkerLoopQuicOverTCP(t *testing.T) {
	tc := schedulingTestCase{
		input: []timedDial{
			{
				addr:    ma.StringCast("/ip4/127.0.0.1/udp/20000/quic-v1"),
				delay:   0,
				success: true,
			},
			{
				addr:    ma.StringCast("/ip4/127.0.0.1/tcp/20000"),
				delay:   30 * time.Millisecond,
				success: true,
			},
		},
		maxDuration: 20 * time.Millisecond,
	}
	s1 := makeSwarmWithNoListenAddrs(t)
	defer s1.Close()

	s2 := makeSwarmWithNoListenAddrs(t)
	defer s2.Close()

	// we use the default ranker here

	err := checkDialWorkerLoopScheduling(t, s1, s2, tc)
	require.NoError(t, err)
}

func TestDialWorkerLoopHolePunching(t *testing.T) {
	s1 := makeSwarmWithNoListenAddrs(t)
	defer s1.Close()

	s2 := makeSwarmWithNoListenAddrs(t)
	defer s2.Close()

	// t1 will accept and keep the other end waiting
	t1 := ma.StringCast("/ip4/127.0.0.1/tcp/10000")
	recvCh := make(chan struct{})
	list, ch := makeTCPListener(t, t1, recvCh) // ignore ch because we want to hang forever
	defer list.Close()
	defer func() { ch <- struct{}{} }() // close listener

	// t2 will succeed
	t2 := ma.StringCast("/ip4/127.0.0.1/tcp/10001")

	err := s2.AddListenAddr(t2)
	if err != nil {
		t.Error(err)
	}

	s1.dialRanker = func(addrs []ma.Multiaddr) (res []network.AddrDelay) {
		res = make([]network.AddrDelay, len(addrs))
		for i := 0; i < len(addrs); i++ {
			delay := 10 * time.Second
			if addrs[i].Equal(t1) {
				// fire t1 immediately
				delay = 0
			} else if addrs[i].Equal(t2) {
				// delay t2 by 100ms
				// without holepunch this call will not happen
				delay = 100 * time.Millisecond
			}
			res[i] = network.AddrDelay{Addr: addrs[i], Delay: delay}
		}
		return
	}
	s1.Peerstore().AddAddrs(s2.LocalPeer(), []ma.Multiaddr{t1, t2}, peerstore.PermanentAddrTTL)

	reqch := make(chan dialRequest)
	resch := make(chan dialResponse, 2)

	cl := newMockClock()
	worker := newDialWorker(s1, s2.LocalPeer(), reqch, cl)
	go worker.loop()
	defer worker.wg.Wait()
	defer close(reqch)

	reqch <- dialRequest{ctx: context.Background(), resch: resch}
	<-recvCh // received connection on t1

	select {
	case <-resch:
		t.Errorf("didn't expect connection to succeed")
	case <-time.After(100 * time.Millisecond):
	}

	hpCtx := network.WithSimultaneousConnect(context.Background(), true, "testing")
	// with holepunch request, t2 will be dialed immediately
	reqch <- dialRequest{ctx: hpCtx, resch: resch}
	select {
	case r := <-resch:
		require.NoError(t, r.err)
	case <-time.After(5 * time.Second):
		t.Errorf("expected conn to succeed")
	}

	select {
	case r := <-resch:
		require.NoError(t, r.err)
	case <-time.After(5 * time.Second):
		t.Errorf("expected conn to succeed")
	}
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

	worker := newDialWorker(s1, s2.LocalPeer(), reqch, nil)
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

func TestDialWorkerLoopTCPConnUpgradeWait(t *testing.T) {
	s1 := makeSwarmWithNoListenAddrs(t, WithDialTimeout(10*time.Second))
	s2 := makeSwarmWithNoListenAddrs(t, WithDialTimeout(10*time.Second))
	defer s1.Close()
	defer s2.Close()
	// Connection to a1 will fail but a1 is a public address so we can test waiting for tcp
	// connection established dial update. ipv4only.arpa reserved address.
	a1 := ma.StringCast(fmt.Sprintf("/ip4/192.0.0.170/tcp/%d", 10001))
	// Connection to a2 will succeed.
	a2 := ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", 10002))
	s2.Listen(a2)

	s1.Peerstore().AddAddrs(s2.LocalPeer(), []ma.Multiaddr{a1, a2}, peerstore.PermanentAddrTTL)

	rankerCalled := make(chan struct{})
	s1.dialRanker = func(addrs []ma.Multiaddr) []network.AddrDelay {
		defer close(rankerCalled)
		return []network.AddrDelay{{Addr: a1, Delay: 0}, {Addr: a2, Delay: 100 * time.Millisecond}}
	}

	reqch := make(chan dialRequest)
	resch := make(chan dialResponse, 2)
	cl := newMockClock()
	worker := newDialWorker(s1, s2.LocalPeer(), reqch, cl)
	go worker.loop()
	defer worker.wg.Wait()
	defer close(reqch)

	reqch <- dialRequest{ctx: context.Background(), resch: resch}

	<-rankerCalled
	// Wait a bit to let the loop make the dial attempt to a1
	time.Sleep(1 * time.Second)
	// Send conn established for a1
	worker.resch <- transport.DialUpdate{Kind: transport.UpdateKindHandshakeProgressed, Addr: a1}
	// Dial to a2 shouldn't happen even if a2 is scheduled to dial by now
	cl.AdvanceBy(200 * time.Millisecond)
	select {
	case r := <-resch:
		t.Fatalf("didn't expect any event on resch %s %s", r.err, r.conn)
	case <-time.After(500 * time.Millisecond):
	}

	// Dial to a2 should happen now
	// This number is high because there's a race between this goroutine advancing the clock
	// and the worker loop goroutine processing the TCPConnectionEstablished event.
	// In case it processes the event after the previous clock advancement we need to wait
	// 2 * PublicTCPDelay.
	cl.AdvanceBy(2 * PublicTCPDelay)
	select {
	case r := <-resch:
		require.NoError(t, r.err)
		require.NotNil(t, r.conn)
	case <-time.After(3 * time.Second):
		t.Errorf("expected a fail response")
	}
}

func BenchmarkDialRanker(b *testing.B) {
	const N = 10000

	benchDialQueue := func(adelays []network.AddrDelay) {
		dq := newDialQueue()
		for _, a := range adelays {
			dq.Add(a)
		}
		for {
			batch := dq.NextBatch()
			if len(batch) == 0 {
				return
			}
		}
	}
	addrs := make([]ma.Multiaddr, N)
	for i := 0; i < N; i++ {
		addrs[i] = ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", i))
	}

	b.Run("equal delay", func(b *testing.B) {
		b.ReportAllocs()
		addrDelays := make([]network.AddrDelay, N)
		for i := 0; i < N; i++ {
			addrDelays[i] = network.AddrDelay{
				Addr:  addrs[i],
				Delay: 0,
			}
		}
		for i := 0; i < b.N; i++ {
			benchDialQueue(addrDelays)
		}
	})
	b.Run("sorted delay", func(b *testing.B) {
		b.ReportAllocs()
		addrDelays := make([]network.AddrDelay, N)
		for i := 0; i < N; i++ {
			addrDelays[i] = network.AddrDelay{
				Addr:  addrs[i],
				Delay: time.Millisecond * time.Duration(i),
			}
		}
		for i := 0; i < b.N; i++ {
			benchDialQueue(addrDelays)
		}
	})
}
