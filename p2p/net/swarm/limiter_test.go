package swarm

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
)

func addrWithPort(p int) ma.Multiaddr {
	return ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", p))
}

// in these tests I use addresses with tcp ports over a certain number to
// signify 'good' addresses that will succeed, and addresses below that number
// will fail. This lets us more easily test these different scenarios.
func tcpPortOver(a ma.Multiaddr, n int) bool {
	port, err := a.ValueForProtocol(ma.P_TCP)
	if err != nil {
		panic(err)
	}

	pnum, err := strconv.Atoi(port)
	if err != nil {
		panic(err)
	}

	return pnum > n
}

func tryDialAddrs(ctx context.Context, l *dialLimiter, p peer.ID, addrs []ma.Multiaddr, res chan transport.DialUpdate) {
	for _, a := range addrs {
		l.AddDialJob(&dialJob{
			ctx:  ctx,
			peer: p,
			addr: a,
			resp: res,
		})
	}
}

func hangDialFunc(hang chan struct{}) dialfunc {
	return func(ctx context.Context, p peer.ID, a ma.Multiaddr, _ chan<- transport.DialUpdate) (transport.CapableConn, error) {
		if mafmt.UTP.Matches(a) {
			return transport.CapableConn(nil), nil
		}

		_, err := a.ValueForProtocol(ma.P_CIRCUIT)
		if err == nil {
			return transport.CapableConn(nil), nil
		}

		if tcpPortOver(a, 10) {
			return transport.CapableConn(nil), nil
		}

		<-hang
		return nil, fmt.Errorf("test bad dial")
	}
}

func TestLimiterBasicDials(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)

	l := newDialLimiterWithParams(hangDialFunc(hang), ConcurrentFdDials, 4)

	bads := []ma.Multiaddr{addrWithPort(1), addrWithPort(2), addrWithPort(3), addrWithPort(4)}
	good := addrWithPort(20)

	resch := make(chan transport.DialUpdate)
	pid := peer.ID("testpeer")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tryDialAddrs(ctx, l, pid, bads, resch)

	l.AddDialJob(&dialJob{
		ctx:  ctx,
		peer: pid,
		addr: good,
		resp: resch,
	})

	select {
	case <-resch:
		t.Fatal("no dials should have completed!")
	case <-time.After(time.Millisecond * 100):
	}

	// complete a single hung dial
	hang <- struct{}{}

	select {
	case r := <-resch:
		if r.Err == nil {
			t.Fatal("should have gotten failed dial result")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for dial completion")
	}

	select {
	case r := <-resch:
		if r.Err != nil {
			t.Fatal("expected second result to be success!")
		}
	case <-time.After(time.Second):
	}
}

func TestFDLimiting(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)
	l := newDialLimiterWithParams(hangDialFunc(hang), 16, 5)

	bads := []ma.Multiaddr{addrWithPort(1), addrWithPort(2), addrWithPort(3), addrWithPort(4)}
	pids := []peer.ID{"testpeer1", "testpeer2", "testpeer3", "testpeer4"}
	goodTCP := addrWithPort(20)

	ctx := context.Background()
	resch := make(chan transport.DialUpdate)

	// take all fd limit tokens with hang dials
	for _, pid := range pids {
		tryDialAddrs(ctx, l, pid, bads, resch)
	}

	// these dials should work normally, but will hang because we have taken
	// up all the fd limiting
	for _, pid := range pids {
		l.AddDialJob(&dialJob{
			ctx:  ctx,
			peer: pid,
			addr: goodTCP,
			resp: resch,
		})
	}

	select {
	case <-resch:
		t.Fatal("no dials should have completed!")
	case <-time.After(time.Millisecond * 100):
	}

	pid5 := peer.ID("testpeer5")
	utpaddr := ma.StringCast("/ip4/127.0.0.1/udp/7777/utp")

	// This should complete immediately since utp addresses arent blocked by fd rate limiting
	l.AddDialJob(&dialJob{ctx: ctx, peer: pid5, addr: utpaddr, resp: resch})

	select {
	case res := <-resch:
		if res.Err != nil {
			t.Fatal("should have gotten successful response")
		}
	case <-time.After(time.Second * 5):
		t.Fatal("timeout waiting for utp addr success")
	}

	// A relay address with tcp transport will complete because we do not consume fds for dials
	// with relay addresses as the fd will be consumed when we actually dial the relay server.
	pid6 := test.RandPeerIDFatal(t)
	relayAddr := ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/tcp/20/p2p-circuit/p2p/%s", pid6))
	l.AddDialJob(&dialJob{ctx: ctx, peer: pid6, addr: relayAddr, resp: resch})

	select {
	case res := <-resch:
		if res.Err != nil {
			t.Fatal("should have gotten successful response")
		}
	case <-time.After(time.Second * 5):
		t.Fatal("timeout waiting for relay addr success")
	}
}

func TestTokenRedistribution(t *testing.T) {
	var lk sync.Mutex
	hangchs := make(map[peer.ID]chan struct{})
	df := func(ctx context.Context, p peer.ID, a ma.Multiaddr, _ chan<- transport.DialUpdate) (transport.CapableConn, error) {
		if tcpPortOver(a, 10) {
			return (transport.CapableConn)(nil), nil
		}

		lk.Lock()
		ch := hangchs[p]
		lk.Unlock()
		<-ch
		return nil, fmt.Errorf("test bad dial")
	}
	l := newDialLimiterWithParams(df, 8, 4)

	bads := []ma.Multiaddr{addrWithPort(1), addrWithPort(2), addrWithPort(3), addrWithPort(4)}
	pids := []peer.ID{"testpeer1", "testpeer2"}

	ctx := context.Background()
	resch := make(chan transport.DialUpdate)

	// take all fd limit tokens with hang dials
	for _, pid := range pids {
		hangchs[pid] = make(chan struct{})
	}

	for _, pid := range pids {
		tryDialAddrs(ctx, l, pid, bads, resch)
	}

	// add a good dial job for peer 1
	l.AddDialJob(&dialJob{
		ctx:  ctx,
		peer: pids[1],
		addr: ma.StringCast("/ip4/127.0.0.1/tcp/1001"),
		resp: resch,
	})

	select {
	case <-resch:
		t.Fatal("no dials should have completed!")
	case <-time.After(time.Millisecond * 100):
	}

	// unblock one dial for peer 0
	hangchs[pids[0]] <- struct{}{}

	select {
	case res := <-resch:
		if res.Err == nil {
			t.Fatal("should have only been a failure here")
		}
	case <-time.After(time.Millisecond * 100):
		t.Fatal("expected a dial failure here")
	}

	select {
	case <-resch:
		t.Fatal("no more dials should have completed!")
	case <-time.After(time.Millisecond * 100):
	}

	// add a bad dial job to peer 0 to fill their rate limiter
	// and test that more dials for this peer won't interfere with peer 1's successful dial incoming
	l.AddDialJob(&dialJob{
		ctx:  ctx,
		peer: pids[0],
		addr: addrWithPort(7),
		resp: resch,
	})

	hangchs[pids[1]] <- struct{}{}

	// now one failed dial from peer 1 should get through and fail
	// which will in turn unblock the successful dial on peer 1
	select {
	case res := <-resch:
		if res.Err == nil {
			t.Fatal("should have only been a failure here")
		}
	case <-time.After(time.Millisecond * 100):
		t.Fatal("expected a dial failure here")
	}

	select {
	case res := <-resch:
		if res.Err != nil {
			t.Fatal("should have succeeded!")
		}
	case <-time.After(time.Millisecond * 100):
		t.Fatal("should have gotten successful dial")
	}
}

func TestStressLimiter(t *testing.T) {
	df := func(ctx context.Context, p peer.ID, a ma.Multiaddr, _ chan<- transport.DialUpdate) (transport.CapableConn, error) {
		if tcpPortOver(a, 1000) {
			return transport.CapableConn(nil), nil
		}

		time.Sleep(time.Millisecond * time.Duration(5+rand.Intn(100)))
		return nil, fmt.Errorf("test bad dial")
	}

	l := newDialLimiterWithParams(df, 20, 5)

	var bads []ma.Multiaddr
	for i := 0; i < 100; i++ {
		bads = append(bads, addrWithPort(i))
	}

	addresses := append(bads, addrWithPort(2000))
	success := make(chan struct{})

	for i := 0; i < 20; i++ {
		go func(id peer.ID) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			resp := make(chan transport.DialUpdate)
			time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)
			for _, i := range rand.Perm(len(addresses)) {
				l.AddDialJob(&dialJob{
					addr: addresses[i],
					ctx:  ctx,
					peer: id,
					resp: resp,
				})
			}

			for res := range resp {
				if res.Err == nil {
					success <- struct{}{}
					return
				}
			}
		}(peer.ID(fmt.Sprintf("testpeer%d", i)))
	}

	for i := 0; i < 20; i++ {
		select {
		case <-success:
		case <-time.After(time.Minute):
			t.Fatal("expected a success within five seconds")
		}
	}
}

func TestFDLimitUnderflow(t *testing.T) {
	df := func(ctx context.Context, p peer.ID, addr ma.Multiaddr, _ chan<- transport.DialUpdate) (transport.CapableConn, error) {
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
		return nil, fmt.Errorf("df timed out")
	}

	const fdLimit = 20
	l := newDialLimiterWithParams(df, fdLimit, 3)

	var addrs []ma.Multiaddr
	for i := 0; i <= 1000; i++ {
		addrs = append(addrs, addrWithPort(i))
	}

	wg := sync.WaitGroup{}
	const num = 3 * fdLimit
	wg.Add(num)
	errs := make(chan error, num)
	for i := 0; i < num; i++ {
		go func(id peer.ID, i int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			resp := make(chan transport.DialUpdate)
			l.AddDialJob(&dialJob{
				addr: addrs[i],
				ctx:  ctx,
				peer: id,
				resp: resp,
			})

			for res := range resp {
				if res.Err != nil {
					return
				}
				errs <- errors.New("got dial res, but shouldn't")
			}
		}(peer.ID(fmt.Sprintf("testpeer%d", i%20)), i)
	}

	go func() {
		wg.Wait()
		close(errs)
	}()

	for err := range errs {
		t.Fatal(err)
	}

	l.lk.Lock()
	fdConsuming := l.fdConsuming
	l.lk.Unlock()

	if fdConsuming < 0 {
		t.Fatalf("l.fdConsuming < 0")
	}
}
