package test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	v1 "github.com/libp2p/go-libp2p-circuit"

	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	tptu "github.com/libp2p/go-libp2p-transport-upgrader"
	ma "github.com/multiformats/go-multiaddr"
)

func addTransportV1(t *testing.T, ctx context.Context, h host.Host, upgrader *tptu.Upgrader) {
	err := v1.AddRelayTransport(ctx, h, upgrader)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRelayCompatV2DialV1(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts, upgraders := getNetHosts(t, ctx, 3)
	addTransportV1(t, ctx, hosts[0], upgraders[0])
	addTransport(t, ctx, hosts[2], upgraders[2])

	rch := make(chan []byte, 1)
	hosts[0].SetStreamHandler("test", func(s network.Stream) {
		defer s.Close()
		defer close(rch)

		buf := make([]byte, 1024)
		nread := 0
		for nread < len(buf) {
			n, err := s.Read(buf[nread:])
			nread += n
			if err != nil {
				if err == io.EOF {
					break
				}
				t.Fatal(err)
			}
		}

		rch <- buf[:nread]
	})

	_, err := v1.NewRelay(ctx, hosts[1], upgraders[1], v1.OptHop)
	if err != nil {
		t.Fatal(err)
	}

	connect(t, hosts[0], hosts[1])
	connect(t, hosts[1], hosts[2])

	raddr, err := ma.NewMultiaddr(fmt.Sprintf("/p2p/%s/p2p-circuit/p2p/%s", hosts[1].ID(), hosts[0].ID()))
	if err != nil {
		t.Fatal(err)
	}

	err = hosts[2].Connect(ctx, peer.AddrInfo{ID: hosts[0].ID(), Addrs: []ma.Multiaddr{raddr}})
	if err != nil {
		t.Fatal(err)
	}

	conns := hosts[2].Network().ConnsToPeer(hosts[0].ID())
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, but got %d", len(conns))
	}
	if conns[0].Stat().Transient {
		t.Fatal("expected non transient connection")
	}

	s, err := hosts[2].NewStream(ctx, hosts[0].ID(), "test")
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("relay works!")
	nwritten, err := s.Write(msg)
	if err != nil {
		t.Fatal(err)
	}
	if nwritten != len(msg) {
		t.Fatalf("expected to write %d bytes, but wrote %d instead", len(msg), nwritten)
	}
	s.CloseWrite()

	got := <-rch
	if !bytes.Equal(msg, got) {
		t.Fatalf("Wrong echo; expected %s but got %s", string(msg), string(got))
	}
}

func TestRelayCompatV1DialV2(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts, upgraders := getNetHosts(t, ctx, 3)
	addTransport(t, ctx, hosts[0], upgraders[0])
	addTransportV1(t, ctx, hosts[2], upgraders[2])

	rch := make(chan []byte, 1)
	hosts[0].SetStreamHandler("test", func(s network.Stream) {
		defer s.Close()
		defer close(rch)

		buf := make([]byte, 1024)
		nread := 0
		for nread < len(buf) {
			n, err := s.Read(buf[nread:])
			nread += n
			if err != nil {
				if err == io.EOF {
					break
				}
				t.Fatal(err)
			}
		}

		rch <- buf[:nread]
	})

	_, err := v1.NewRelay(ctx, hosts[1], upgraders[1], v1.OptHop)
	if err != nil {
		t.Fatal(err)
	}

	connect(t, hosts[0], hosts[1])
	connect(t, hosts[1], hosts[2])

	raddr, err := ma.NewMultiaddr(fmt.Sprintf("/p2p/%s/p2p-circuit/p2p/%s", hosts[1].ID(), hosts[0].ID()))
	if err != nil {
		t.Fatal(err)
	}

	err = hosts[2].Connect(ctx, peer.AddrInfo{ID: hosts[0].ID(), Addrs: []ma.Multiaddr{raddr}})
	if err != nil {
		t.Fatal(err)
	}

	conns := hosts[2].Network().ConnsToPeer(hosts[0].ID())
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, but got %d", len(conns))
	}
	if conns[0].Stat().Transient {
		t.Fatal("expected non transient connection")
	}

	s, err := hosts[2].NewStream(ctx, hosts[0].ID(), "test")
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("relay works!")
	nwritten, err := s.Write(msg)
	if err != nil {
		t.Fatal(err)
	}
	if nwritten != len(msg) {
		t.Fatalf("expected to write %d bytes, but wrote %d instead", len(msg), nwritten)
	}
	s.CloseWrite()

	got := <-rch
	if !bytes.Equal(msg, got) {
		t.Fatalf("Wrong echo; expected %s but got %s", string(msg), string(got))
	}
}
