//go:build go1.25

package basichost_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	basichost "github.com/libp2p/go-libp2p/p2p/host/basic"
	"github.com/libp2p/go-libp2p/x/simlibp2p"

	"github.com/stretchr/testify/require"
)

// TestStreamCloseDoesNotHangOnUnresponsivePeer verifies that stream.Close()
// returns within DefaultNegotiationTimeout even when the remote peer never
// completes the multistream handshake. Without the read deadline fix in
// streamWrapper.Close(), this would hang indefinitely.
func TestStreamCloseDoesNotHangOnUnresponsivePeer_synctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()

		h1, h2 := simlibp2p.GetBasicHostPair(t)
		defer h1.Close()
		defer h2.Close()

		const testProto = "/test/hang"

		// Manually add protocol to peerstore so h1 thinks h2 supports it.
		// This makes NewStream use lazy multistream (skipping negotiation until Close).
		h1.Peerstore().AddProtocols(h2.ID(), testProto)

		// h2 accepts streams at the network level but never responds to
		// multistream protocol negotiation, simulating an unresponsive peer.
		h2.Network().SetStreamHandler(func(s network.Stream) {
			// Read incoming data but never write back - simulates unresponsive peer
			buf := make([]byte, 1024)
			for {
				_, err := s.Read(buf)
				if err != nil {
					return
				}
			}
		})

		// Open stream to h2 - uses lazy multistream because protocol is "known"
		s, err := h1.NewStream(ctx, h2.ID(), testProto)
		require.NoError(t, err)

		// Trigger the lazy handshake by writing data.
		// The write succeeds (buffered), but the read handshake will block
		// because h2 never sends a response.
		_, err = s.Write([]byte("trigger handshake"))
		require.NoError(t, err)

		// Close() should return within DefaultNegotiationTimeout because the fix
		// sets a read deadline before calling the underlying Close().
		// Without the fix, this would hang indefinitely.
		elapsedCh := make(chan time.Duration)
		go func() {
			start := time.Now()
			_ = s.Close()
			elapsedCh <- time.Since(start)
		}()

		maxExpected := basichost.DefaultNegotiationTimeout
		var elapsed time.Duration
		select {
		case elapsed = <-elapsedCh:
		case <-time.After(maxExpected + time.Second):
			t.Fatal("timeout waiting for Close()")
		}

		require.Equal(t, elapsed, maxExpected,
			"Close() took %v, expected < %v (DefaultNegotiationTimeout + margin)", elapsed, maxExpected)

		t.Logf("Close() returned in %v (limit: %v)", elapsed, maxExpected)
	})
}
