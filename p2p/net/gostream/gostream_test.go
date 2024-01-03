package gostream

import (
	"bufio"
	"context"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
)

// newHost illustrates how to build a libp2p host with secio using
// a randomly generated key-pair
func newHost(t *testing.T, listen multiaddr.Multiaddr) host.Host {
	h, err := libp2p.New(
		libp2p.ListenAddrs(listen),
	)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestServerClient(t *testing.T) {
	m1, _ := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/10000")
	m2, _ := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/10001")
	srvHost := newHost(t, m1)
	clientHost := newHost(t, m2)
	defer srvHost.Close()
	defer clientHost.Close()

	srvHost.Peerstore().AddAddrs(clientHost.ID(), clientHost.Addrs(), peerstore.PermanentAddrTTL)
	clientHost.Peerstore().AddAddrs(srvHost.ID(), srvHost.Addrs(), peerstore.PermanentAddrTTL)

	var tag protocol.ID = "/testitytest"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		listener, err := Listen(srvHost, tag)
		if err != nil {
			t.Error(err)
			return
		}
		defer listener.Close()

		if listener.Addr().String() != srvHost.ID().String() {
			t.Error("bad listener address")
			return
		}

		servConn, err := listener.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		defer servConn.Close()

		reader := bufio.NewReader(servConn)
		for {
			msg, err := reader.ReadString('\n')
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Error(err)
				return
			}
			if msg != "is libp2p awesome?\n" {
				t.Errorf("Bad incoming message: %s", msg)
				return
			}

			_, err = servConn.Write([]byte("yes it is\n"))
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()

	clientConn, err := Dial(ctx, clientHost, srvHost.ID(), tag)
	if err != nil {
		t.Fatal(err)
	}

	if clientConn.LocalAddr().String() != clientHost.ID().String() {
		t.Fatal("Bad LocalAddr")
	}

	if clientConn.RemoteAddr().String() != srvHost.ID().String() {
		t.Fatal("Bad RemoteAddr")
	}

	if clientConn.LocalAddr().Network() != Network {
		t.Fatal("Bad Network()")
	}

	err = clientConn.SetDeadline(time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	err = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	err = clientConn.SetWriteDeadline(time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	_, err = clientConn.Write([]byte("is libp2p awesome?\n"))
	if err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(clientConn)
	resp, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}

	if resp != "yes it is\n" {
		t.Errorf("Bad response: %s", resp)
	}

	err = clientConn.Close()
	if err != nil {
		t.Fatal(err)
	}
	<-done
}
