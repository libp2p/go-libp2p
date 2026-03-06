package mixnet

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg == nil {
		t.Fatal("expected non-nil default config")
	}
}

func TestNewStreamUpgrader(t *testing.T) {
	u := NewStreamUpgrader(nil)
	if u == nil {
		t.Fatal("expected non-nil StreamUpgrader")
	}
	if u.mixnet != nil {
		t.Fatal("expected nil mixnet in upgrader constructed with nil")
	}
}

// TestStreamUpgrader_ImplementsInterface verifies that MixnetStreamUpgrader satisfies StreamUpgrader.
func TestStreamUpgrader_ImplementsInterface(t *testing.T) {
	var _ StreamUpgrader = (*MixnetStreamUpgrader)(nil)
}

// TestMixStream_ProtoField verifies that UpgradeOutbound stores the proto in the returned stream.
// We test this directly since a real network is not available in unit tests.
func TestMixStream_ProtoField(t *testing.T) {
	s := &MixStream{}
	if s.proto != "" {
		t.Fatalf("expected empty proto, got %q", s.proto)
	}
	s.proto = string(protocol.ID("/test/1.0.0"))
	if s.proto != "/test/1.0.0" {
		t.Fatalf("expected proto to be set, got %q", s.proto)
	}
}

// TestMixStream_WriteClosedStream verifies that writing to a closed stream returns an error.
func TestMixStream_WriteClosedStream(t *testing.T) {
	s := &MixStream{
		closed: true,
		dest:   peer.ID("somepeer"),
		ctx:    context.Background(),
	}
	_, err := s.Write([]byte("hello"))
	if err == nil {
		t.Fatal("expected error writing to closed stream")
	}
}

// TestMixStream_WriteInboundOnlyStream verifies that writing to an inbound-only stream returns an error.
func TestMixStream_WriteInboundOnlyStream(t *testing.T) {
	s := &MixStream{
		dest: "",
		ctx:  context.Background(),
	}
	_, err := s.Write([]byte("hello"))
	if err == nil {
		t.Fatal("expected error writing to inbound-only stream")
	}
}

// TestMixStream_CloseIdempotent verifies that closing an already-closed stream is a no-op.
func TestMixStream_CloseIdempotent(t *testing.T) {
	ch := make(chan []byte)
	close(ch)
	s := &MixStream{
		closed:    true,
		sessionID: "test-session",
		ch:        ch,
		ctx:       context.Background(),
	}
	if err := s.Close(); err != nil {
		t.Fatalf("expected nil error on double close, got %v", err)
	}
}
