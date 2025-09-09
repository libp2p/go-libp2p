package config

import (
	"context"
	"io"
	"testing"

	"git.sr.ht/~marcopolo/di"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestNilOption(t *testing.T) {
	var cfg Config
	optsRun := 0
	opt := func(_ *Config) error {
		optsRun++
		return nil
	}
	if err := cfg.Apply(nil); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(opt, nil, nil, opt, opt, nil); err != nil {
		t.Fatal(err)
	}
	if optsRun != 3 {
		t.Fatalf("expected to have handled 3 options, handled %d", optsRun)
	}
}

func newHost(t *testing.T) host.Host {
	type Result struct {
		Host host.Host
		L    *Lifecycle
		_    []di.SideEffect
	}
	var r Result
	if err := di.Build(DefaultConfig, &r); err != nil {
		t.Fatal(err)
	}

	if r.Host == nil {
		t.Fatal("swarm is nil")
	}

	if err := r.L.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.L.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return r.Host
}

func TestDi(t *testing.T) {
	a := newHost(t)
	b := newHost(t)

	b.SetStreamHandler("/echo/1", func(s network.Stream) {
		io.Copy(s, s)
		s.Close()
	})
	a.Connect(context.Background(), peer.AddrInfo{
		ID:    b.ID(),
		Addrs: b.Addrs(),
	})

	s, err := a.NewStream(context.Background(), b.ID(), "/echo/1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	msgBack, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(msgBack) != "hello" {
		t.Fatalf("expected 'hello', got '%s'", string(msgBack))
	}

	t.Logf("A Peer ID: %s\n", a.ID())
	t.Logf("B Peer ID: %s\n", b.ID())
}
