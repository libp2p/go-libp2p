package mixnet

import (
	"testing"
)

func TestStreamUpgrader_NewStreamUpgrader(t *testing.T) {
	cfg := DefaultConfig()
	upgrader := NewStreamUpgrader(cfg)

	if upgrader == nil {
		t.Error("expected non-nil upgrader")
	}

	if upgrader.Config() == nil {
		t.Error("expected config to be set")
	}

	if upgrader.Config().HopCount != 2 {
		t.Errorf("expected hop count 2, got %d", upgrader.Config().HopCount)
	}
}

func TestStreamUpgrader_Protocol(t *testing.T) {
	cfg := DefaultConfig()
	upgrader := NewStreamUpgrader(cfg)

	if upgrader.Protocol() != "/lib-mix/1.0.0" {
		t.Errorf("expected protocol /lib-mix/1.0.0, got %s", upgrader.Protocol())
	}
}

func TestStreamUpgrader_CanUpgrade(t *testing.T) {
	cfg := DefaultConfig()
	upgrader := NewStreamUpgrader(cfg)

	if !upgrader.CanUpgrade("/ip4/127.0.0.1/tcp/4001") {
		t.Error("expected CanUpgrade to return true")
	}
}

func TestStreamUpgrader_Upgrade(t *testing.T) {
	cfg := &MixnetConfig{
		HopCount:     2,
		CircuitCount: 3,
		Compression:  "gzip",
	}
	cfg.InitDefaults()

	upgrader := NewStreamUpgrader(cfg)

	// Upgrade should fail since we don't have actual connections
	_, err := upgrader.Upgrade(nil, nil, 0)
	if err == nil {
		t.Error("expected error from Upgrade")
	}
}
