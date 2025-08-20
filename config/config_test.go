package config

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/di"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
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

func TestDi(t *testing.T) {
	type Result struct {
		Swarm *swarm.Swarm
		Host  host.Host
		L     *Lifecycle
	}
	var r Result
	if err := di.Build(DefaultConfig2, &r); err != nil {
		t.Fatal(err)
	}

	if r.Swarm == nil {
		t.Fatal("swarm is nil")
	}
	if r.Swarm.LocalPeer() == "" {
		t.Fatal("local peer is empty")
	}

	if err := r.L.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.L.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Logf("Swarm Peer ID: %s\n", r.Swarm.LocalPeer())
	t.Logf("Host Peer ID: %s\n", r.Host.ID())
}
