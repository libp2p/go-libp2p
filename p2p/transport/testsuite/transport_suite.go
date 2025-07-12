package ttransport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
)

var testData = []byte("this is some test data")

func SubtestProtocols(t *testing.T, ta, tb transport.Transport, maddr ma.Multiaddr, _ peer.ID) {
	rawIPAddr, _ := ma.NewMultiaddr("/ip4/1.2.3.4")
	if ta.CanDial(rawIPAddr) || tb.CanDial(rawIPAddr) {
		t.Error("nothing should be able to dial raw IP")
	}

	tprotos := make(map[int]bool)
	for _, p := range ta.Protocols() {
		tprotos[p] = true
	}

	if !ta.Proxy() {
		protos := maddr.Protocols()
		proto := protos[len(protos)-1]
		if !tprotos[proto.Code] {
			t.Errorf("transport should have reported that it supports protocol '%s' (%d)", proto.Name, proto.Code)
		}
	} else {
		found := false
		for _, proto := range maddr.Protocols() {
			if tprotos[proto.Code] {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("didn't find any matching proxy protocols in maddr: %s", maddr)
		}
	}
}

func SubtestBasic(t *testing.T, ta, tb transport.Transport, maddr ma.Multiaddr, peerA peer.ID) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	list, err := ta.Listen(maddr)
	if err != nil {
		t.Fatal(err)
	}
	defer list.Close()

	var (
		connA, connB transport.CapableConn
		done         = make(chan struct{})
	)
	defer func() {
		<-done
		if connA != nil {
			connA.Close()
		}
		if connB != nil {
			connB.Close()
		}
	}()

	go func() {
		defer close(done)
		var err error
		connB, err = list.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		s, err := connB.AcceptStream()
		if err != nil {
			t.Error(err)
			return
		}

		buf, err := io.ReadAll(s)
		if err != nil {
			t.Error(err)
			return
		}

		if !bytes.Equal(testData, buf) {
			t.Errorf("expected %s, got %s", testData, buf)
		}

		n, err := s.Write(testData)
		if err != nil {
			t.Error(err)
			return
		}
		if n != len(testData) {
			t.Error(err)
			return
		}

		err = s.Close()
		if err != nil {
			t.Error(err)
		}
	}()

	if !tb.CanDial(list.Multiaddr()) {
		t.Error("CanDial should have returned true")
	}

	connA, err = tb.Dial(ctx, list.Multiaddr(), peerA)
	if err != nil {
		t.Fatal(err)
	}

	s, err := connA.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.Write(testData)
	if err != nil {
		t.Fatal(err)
		return
	}

	if n != len(testData) {
		t.Fatalf("failed to write enough data (a->b)")
		return
	}

	if err = s.CloseWrite(); err != nil {
		t.Fatal(err)
		return
	}

	buf, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
		return
	}
	if !bytes.Equal(testData, buf) {
		t.Errorf("expected %s, got %s", testData, buf)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
		return
	}
}

func SubtestPingPong(t *testing.T, ta, tb transport.Transport, maddr ma.Multiaddr, peerA peer.ID) {
	streams := 100

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	list, err := ta.Listen(maddr)
	if err != nil {
		t.Fatal(err)
	}
	defer list.Close()

	var (
		connA, connB transport.CapableConn
	)
	defer func() {
		if connA != nil {
			connA.Close()
		}
		if connB != nil {
			connB.Close()
		}
	}()

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		connA, err = list.Accept()
		if err != nil {
			t.Error(err)
			return
		}

		var sWg sync.WaitGroup
		for i := 0; i < streams; i++ {
			s, err := connA.AcceptStream()
			if err != nil {
				t.Error(err)
				return
			}

			sWg.Add(1)
			go func() {
				defer sWg.Done()

				data, err := io.ReadAll(s)
				if err != nil {
					s.Reset()
					t.Error(err)
					return
				}
				if !bytes.HasPrefix(data, testData) {
					t.Errorf("expected %q to have prefix %q", string(data), string(testData))
				}

				n, err := s.Write(data)
				if err != nil {
					s.Reset()
					t.Error(err)
					return
				}

				if n != len(data) {
					s.Reset()
					t.Error(err)
					return
				}
				s.Close()
			}()
		}
		sWg.Wait()
	}()

	if !tb.CanDial(list.Multiaddr()) {
		t.Error("CanDial should have returned true")
	}

	connB, err = tb.Dial(ctx, list.Multiaddr(), peerA)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < streams; i++ {
		s, err := connB.OpenStream(context.Background())
		if err != nil {
			t.Error(err)
			continue
		}

		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf("%s - %d", testData, i))
			n, err := s.Write(data)
			if err != nil {
				s.Reset()
				t.Error(err)
				return
			}

			if n != len(data) {
				s.Reset()
				t.Error("failed to write enough data (a->b)")
				return
			}
			if err = s.CloseWrite(); err != nil {
				t.Error(err)
				return
			}

			ret, err := io.ReadAll(s)
			if err != nil {
				s.Reset()
				t.Error(err)
				return
			}
			if !bytes.Equal(data, ret) {
				t.Errorf("expected %q, got %q", string(data), string(ret))
			}

			if err = s.Close(); err != nil {
				t.Error(err)
				return
			}
		}(i)
	}
	wg.Wait()
}

func SubtestCancel(t *testing.T, ta, tb transport.Transport, maddr ma.Multiaddr, peerA peer.ID) {
	list, err := ta.Listen(maddr)
	if err != nil {
		t.Fatal(err)
	}
	defer list.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, err := tb.Dial(ctx, list.Multiaddr(), peerA)
	if err == nil {
		c.Close()
		t.Fatal("dial should have failed")
	}
}
