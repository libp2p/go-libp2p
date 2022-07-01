package yamux

import (
	"testing"

	tmux "github.com/libp2p/go-libp2p/p2p/muxer/testsuite"
)

func TestDefaultTransport(t *testing.T) {
	// Yamux doesn't have any backpressure when it comes to opening streams.
	// If the peer opens too many streams, those are just reset.
	delete(tmux.Subtests, "github.com/libp2p/go-libp2p-testing/suites/mux.SubtestStress1Conn1000Stream10Msg")

	tmux.SubtestAll(t, DefaultTransport)
}
