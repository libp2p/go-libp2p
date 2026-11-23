package holepunch

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A call waiting for the host's first public address returns when its context
// ends, and when the service closes first (it used to block for ever then).
func TestDirectConnectContextWaitEnds(t *testing.T) {
	newSvc := func() *Service {
		ctx, cancel := context.WithCancel(context.Background())
		return &Service{ctx: ctx, ctxCancel: cancel, host: closeTestHost{}, hasPublicAddrsChan: make(chan struct{})}
	}

	t.Run("context", func(t *testing.T) {
		s := newSvc()
		defer s.ctxCancel()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := s.DirectConnectContext(ctx, "p"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("DirectConnectContext = %v, want %v", err, context.DeadlineExceeded)
		}
	})

	t.Run("close", func(t *testing.T) {
		s := newSvc()
		done := make(chan error, 1)
		go func() { done <- s.DirectConnectContext(context.Background(), "p") }()
		s.ctxCancel()
		select {
		case err := <-done:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("DirectConnectContext = %v, want %v", err, ErrClosed)
			}
		case <-time.After(time.Second):
			t.Fatal("DirectConnectContext still waiting after the service closed")
		}
	})
}
