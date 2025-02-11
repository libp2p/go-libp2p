package mux

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"io"
	mrand "math/rand"
	"net"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-testing/ci"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/stretchr/testify/require"
)

var randomness []byte
var Subtests map[string]TransportTest

func init() {
	// read 1MB of randomness
	randomness = make([]byte, 1<<20)
	if _, err := crand.Read(randomness); err != nil {
		panic(err)
	}

	Subtests = make(map[string]TransportTest)
	for _, f := range subtests {
		Subtests[getFunctionName(f)] = f
	}
}

func getFunctionName(i interface{}) string {
	return runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
}

type peerScope struct {
	mx     sync.Mutex
	memory int
}

func (p *peerScope) ReserveMemory(size int, _ uint8) error {
	p.mx.Lock()
	p.memory += size
	p.mx.Unlock()
	return nil
}

func (p *peerScope) ReleaseMemory(size int) {
	p.mx.Lock()
	defer p.mx.Unlock()
	if p.memory < size {
		panic(fmt.Sprintf("tried to release too much memory: %d (current: %d)", size, p.memory))
	}
	p.memory -= size
}

// Check checks that we don't have any more reserved memory.
func (p *peerScope) Check(t *testing.T) {
	p.mx.Lock()
	defer p.mx.Unlock()
	require.Zero(t, p.memory, "expected all reserved memory to have been released")
}

type peerScopeSpan struct {
	peerScope
}

func (p *peerScopeSpan) Done() {
	p.mx.Lock()
	defer p.mx.Unlock()
	p.memory = 0
}

func (p *peerScope) Stat() network.ScopeStat                       { return network.ScopeStat{} }
func (p *peerScope) BeginSpan() (network.ResourceScopeSpan, error) { return &peerScopeSpan{}, nil }
func (p *peerScope) Peer() peer.ID                                 { panic("implement me") }

var _ network.PeerScope = &peerScope{}

type Options struct {
	tr        network.Multiplexer
	connNum   int
	streamNum int
	msgNum    int
	msgMin    int
	msgMax    int
}

func randBuf(size int) []byte {
	n := len(randomness) - size
	if size < 1 {
		panic(fmt.Errorf("requested too large buffer (%d). max is %d", size, len(randomness)))
	}

	start := mrand.Intn(n)
	return randomness[start : start+size]
}

func checkErr(t *testing.T, err error) {
	if err != nil {
		debug.PrintStack()
		t.Fatal(err)
	}
}

func echoStream(s network.MuxedStream) {
	defer s.Close()
	io.Copy(s, s) // echo everything
}

func GoServe(t *testing.T, tr network.Multiplexer, l net.Listener) (done func()) {
	closed := make(chan struct{}, 1)

	go func() {
		for {
			c1, err := l.Accept()
			if err != nil {
				select {
				case <-closed:
					return // closed naturally.
				default:
					checkErr(t, err)
				}
			}

			sc1, err := tr.NewConn(c1, true, nil)
			checkErr(t, err)
			go func() {
				for {
					str, err := sc1.AcceptStream()
					if err != nil {
						break
					}
					go echoStream(str)
				}
			}()
		}
	}()

	return func() {
		closed <- struct{}{}
	}
}

func SubtestSimpleWrite(t *testing.T, tr network.Multiplexer) {
	l, err := net.Listen("tcp", "localhost:0")
	checkErr(t, err)
	done := GoServe(t, tr, l)
	defer done()

	nc1, err := net.Dial("tcp", l.Addr().String())
	checkErr(t, err)
	defer nc1.Close()

	scope := &peerScope{}
	c1, err := tr.NewConn(nc1, false, scope)
	checkErr(t, err)
	defer func() {
		c1.Close()
		scope.Check(t)
	}()

	// serve the outgoing conn, because some muxers assume
	// that we _always_ call serve. (this is an error?)
	go c1.AcceptStream()

	s1, err := c1.OpenStream(context.Background())
	checkErr(t, err)
	defer s1.Close()

	buf1 := randBuf(4096)
	_, err = s1.Write(buf1)
	checkErr(t, err)

	buf2 := make([]byte, len(buf1))
	_, err = io.ReadFull(s1, buf2)
	checkErr(t, err)

	require.Equal(t, buf1, buf2)
}

func SubtestStress(t *testing.T, opt Options) {
	msgsize := 1 << 11
	errs := make(chan error) // dont block anything.

	rateLimitN := 5000 // max of 5k funcs, because -race has 8k max.
	rateLimitChan := make(chan struct{}, rateLimitN)
	for i := 0; i < rateLimitN; i++ {
		rateLimitChan <- struct{}{}
	}

	rateLimit := func(f func()) {
		<-rateLimitChan
		f()
		rateLimitChan <- struct{}{}
	}

	writeStream := func(s network.MuxedStream, bufs chan<- []byte) {
		for i := 0; i < opt.msgNum; i++ {
			buf := randBuf(msgsize)
			bufs <- buf
			if _, err := s.Write(buf); err != nil {
				errs <- fmt.Errorf("s.Write(buf): %s", err)
				continue
			}
		}
	}

	readStream := func(s network.MuxedStream, bufs <-chan []byte) {
		buf2 := make([]byte, msgsize)
		for buf1 := range bufs {
			if _, err := io.ReadFull(s, buf2); err != nil {
				errs <- fmt.Errorf("io.ReadFull(s, buf2): %s", err)
				continue
			}
			if !bytes.Equal(buf1, buf2) {
				errs <- fmt.Errorf("buffers not equal (%x != %x)", buf1[:3], buf2[:3])
			}
		}
	}

	openStreamAndRW := func(c network.MuxedConn) {
		s, err := c.OpenStream(context.Background())
		if err != nil {
			errs <- fmt.Errorf("failed to create NewStream: %s", err)
			return
		}

		bufs := make(chan []byte, opt.msgNum)
		go func() {
			writeStream(s, bufs)
			close(bufs)
		}()

		readStream(s, bufs)
		s.Close()
	}

	openConnAndRW := func() {
		l, err := net.Listen("tcp", "localhost:0")
		checkErr(t, err)
		done := GoServe(t, opt.tr, l)
		defer done()

		nla := l.Addr()
		nc, err := net.Dial(nla.Network(), nla.String())
		checkErr(t, err)
		if err != nil {
			t.Fatal(fmt.Errorf("net.Dial(%s, %s): %s", nla.Network(), nla.String(), err))
			return
		}

		scope := &peerScope{}
		c, err := opt.tr.NewConn(nc, false, scope)
		if err != nil {
			t.Fatal(fmt.Errorf("a.AddConn(%s <--> %s): %s", nc.LocalAddr(), nc.RemoteAddr(), err))
			return
		}

		// serve the outgoing conn, because some muxers assume
		// that we _always_ call serve. (this is an error?)
		go func() {
			for {
				str, err := c.AcceptStream()
				if err != nil {
					break
				}
				go echoStream(str)
			}
		}()

		var wg sync.WaitGroup
		for i := 0; i < opt.streamNum; i++ {
			wg.Add(1)
			go rateLimit(func() {
				defer wg.Done()
				openStreamAndRW(c)
			})
		}
		wg.Wait()
		c.Close()
		scope.Check(t)
	}

	openConnsAndRW := func() {
		var wg sync.WaitGroup
		for i := 0; i < opt.connNum; i++ {
			wg.Add(1)
			go rateLimit(func() {
				defer wg.Done()
				openConnAndRW()
			})
		}
		wg.Wait()
	}

	go func() {
		openConnsAndRW()
		close(errs) // done
	}()

	for err := range errs {
		t.Error(err)
	}

}

func tcpPipe(t *testing.T) (net.Conn, net.Conn) {
	list, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}

	con1, err := net.Dial("tcp", list.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	con2, err := list.Accept()
	if err != nil {
		t.Fatal(err)
	}

	return con1, con2
}

func SubtestStreamOpenStress(t *testing.T, tr network.Multiplexer) {
	wg := new(sync.WaitGroup)

	a, b := tcpPipe(t)
	defer a.Close()
	defer b.Close()

	defer wg.Wait()

	wg.Add(1)
	count := 10000
	workers := 5
	go func() {
		defer wg.Done()
		muxa, err := tr.NewConn(a, true, nil)
		if err != nil {
			t.Error(err)
			return
		}
		stress := func() {
			defer wg.Done()
			for i := 0; i < count; i++ {
				s, err := muxa.OpenStream(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				err = s.CloseWrite()
				if err != nil {
					t.Error(err)
				}
				n, err := s.Read([]byte{0})
				if n != 0 {
					t.Error("expected to read no bytes")
				}
				if err != io.EOF {
					t.Errorf("expected an EOF, got %s", err)
				}
			}
		}

		for i := 0; i < workers; i++ {
			wg.Add(1)
			go stress()
		}
	}()

	scope := &peerScope{}
	muxb, err := tr.NewConn(b, false, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		muxb.Close()
		scope.Check(t)
	}()

	time.Sleep(time.Millisecond * 50)

	wg.Add(1)
	recv := make(chan struct{}, count*workers)
	go func() {
		defer wg.Done()
		for i := 0; i < count*workers; i++ {
			str, err := muxb.AcceptStream()
			if err != nil {
				break
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				str.Close()
				select {
				case recv <- struct{}{}:
				default:
					t.Error("too many stream")
				}
			}()
		}
	}()

	timeout := time.Second * 10
	if ci.IsRunning() {
		timeout *= 10
	}

	limit := time.After(timeout)
	for i := 0; i < count*workers; i++ {
		select {
		case <-recv:
		case <-limit:
			t.Fatal("timed out receiving streams")
		}
	}

	wg.Wait()
}

func SubtestStreamReset(t *testing.T, tr network.Multiplexer) {
	wg := new(sync.WaitGroup)
	defer wg.Wait()

	a, b := tcpPipe(t)
	defer a.Close()
	defer b.Close()

	wg.Add(1)
	scopea := &peerScope{}
	muxa, err := tr.NewConn(a, true, scopea)
	if err != nil {
		t.Error(err)
		return
	}
	defer func() {
		muxa.Close()
		scopea.Check(t)
	}()

	go func() {
		defer wg.Done()
		s, err := muxa.OpenStream(context.Background())
		if err != nil {
			t.Error(err)
			return
		}
		time.Sleep(time.Millisecond * 50)

		_, err = s.Write([]byte("foo"))
		if !errors.Is(err, network.ErrReset) {
			t.Error("should have been stream reset")
		}
		s.Close()
	}()

	scopeb := &peerScope{}
	muxb, err := tr.NewConn(b, false, scopeb)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		muxb.Close()
		scopeb.Check(t)
	}()

	str, err := muxb.AcceptStream()
	checkErr(t, err)
	str.Reset()

	wg.Wait()
}

// check that Close also closes the underlying net.Conn
func SubtestWriteAfterClose(t *testing.T, tr network.Multiplexer) {
	a, b := tcpPipe(t)

	scopea := &peerScope{}
	muxa, err := tr.NewConn(a, true, scopea)
	checkErr(t, err)

	scopeb := &peerScope{}
	muxb, err := tr.NewConn(b, false, scopeb)
	checkErr(t, err)

	checkErr(t, muxa.Close())
	scopea.Check(t)
	checkErr(t, muxb.Close())
	scopeb.Check(t)

	// make sure the underlying net.Conn was closed
	if _, err := a.Write([]byte("foobar")); err == nil || !strings.Contains(err.Error(), "use of closed network connection") {
		t.Fatal("write should have failed")
	}
	if _, err := b.Write([]byte("foobar")); err == nil || !strings.Contains(err.Error(), "use of closed network connection") {
		t.Fatal("write should have failed")
	}
}

func SubtestStreamLeftOpen(t *testing.T, tr network.Multiplexer) {
	a, b := tcpPipe(t)

	const numStreams = 10
	const dataLen = 50 * 1024

	scopea := &peerScope{}
	muxa, err := tr.NewConn(a, true, scopea)
	checkErr(t, err)

	scopeb := &peerScope{}
	muxb, err := tr.NewConn(b, false, scopeb)
	checkErr(t, err)

	var wg sync.WaitGroup
	wg.Add(1 + numStreams)
	go func() {
		defer wg.Done()
		for i := 0; i < numStreams; i++ {
			stra, err := muxa.OpenStream(context.Background())
			checkErr(t, err)
			go func() {
				defer wg.Done()
				_, err = stra.Write(randBuf(dataLen))
				checkErr(t, err)
				// do NOT close or reset the stream
			}()
		}
	}()

	wg.Add(1 + numStreams)
	go func() {
		defer wg.Done()
		for i := 0; i < numStreams; i++ {
			str, err := muxb.AcceptStream()
			checkErr(t, err)
			go func() {
				defer wg.Done()
				_, err = io.ReadFull(str, make([]byte, dataLen))
				checkErr(t, err)
			}()
		}
	}()

	// Now we have a bunch of open streams.
	// Make sure that their memory is returned when we close the connection.
	wg.Wait()

	muxa.Close()
	scopea.Check(t)
	muxb.Close()
	scopeb.Check(t)
}

func SubtestStress1Conn1Stream1Msg(t *testing.T, tr network.Multiplexer) {
	SubtestStress(t, Options{
		tr:        tr,
		connNum:   1,
		streamNum: 1,
		msgNum:    1,
		msgMax:    100,
		msgMin:    100,
	})
}

func SubtestStress1Conn1Stream100Msg(t *testing.T, tr network.Multiplexer) {
	SubtestStress(t, Options{
		tr:        tr,
		connNum:   1,
		streamNum: 1,
		msgNum:    100,
		msgMax:    100,
		msgMin:    100,
	})
}

func SubtestStress1Conn100Stream100Msg(t *testing.T, tr network.Multiplexer) {
	SubtestStress(t, Options{
		tr:        tr,
		connNum:   1,
		streamNum: 100,
		msgNum:    100,
		msgMax:    100,
		msgMin:    100,
	})
}

func SubtestStress10Conn10Stream50Msg(t *testing.T, tr network.Multiplexer) {
	SubtestStress(t, Options{
		tr:        tr,
		connNum:   10,
		streamNum: 10,
		msgNum:    50,
		msgMax:    100,
		msgMin:    100,
	})
}

func SubtestStress1Conn1000Stream10Msg(t *testing.T, tr network.Multiplexer) {
	SubtestStress(t, Options{
		tr:        tr,
		connNum:   1,
		streamNum: 1000,
		msgNum:    10,
		msgMax:    100,
		msgMin:    100,
	})
}

func SubtestStress1Conn100Stream100Msg10MB(t *testing.T, tr network.Multiplexer) {
	SubtestStress(t, Options{
		tr:        tr,
		connNum:   1,
		streamNum: 100,
		msgNum:    100,
		msgMax:    10000,
		msgMin:    1000,
	})
}

// Subtests are all the subtests run by SubtestAll
var subtests = []TransportTest{
	SubtestSimpleWrite,
	SubtestWriteAfterClose,
	SubtestStress1Conn1Stream1Msg,
	SubtestStress1Conn1Stream100Msg,
	SubtestStress1Conn100Stream100Msg,
	SubtestStress10Conn10Stream50Msg,
	SubtestStress1Conn1000Stream10Msg,
	SubtestStress1Conn100Stream100Msg10MB,
	SubtestStreamOpenStress,
	SubtestStreamReset,
	SubtestStreamLeftOpen,
}

// SubtestAll runs all the stream multiplexer tests against the target
// transport.
func SubtestAll(t *testing.T, tr network.Multiplexer) {
	for name, f := range Subtests {
		t.Run(name, func(t *testing.T) {
			f(t, tr)
		})
	}
}

// TransportTest is a stream multiplex transport test case
type TransportTest func(t *testing.T, tr network.Multiplexer)
