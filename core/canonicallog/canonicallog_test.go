package canonicallog

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"testing"

	"github.com/libp2p/go-libp2p/core/test"

	"github.com/multiformats/go-multiaddr"
)

func TestLogs(t *testing.T) {
	originalLogger := log
	defer func() {
		log = originalLogger
	}()
	// Override to print debug logs
	log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo, AddSource: true}))

	LogMisbehavingPeer(test.RandPeerIDFatal(t), multiaddr.StringCast("/ip4/1.2.3.4"), "somecomponent", fmt.Errorf("something"), "hi")

	netAddr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 80}
	LogMisbehavingPeerNetAddr(test.RandPeerIDFatal(t), netAddr, "somecomponent", fmt.Errorf("something"), "hello \"world\"")

	LogPeerStatus(1, test.RandPeerIDFatal(t), multiaddr.StringCast("/ip4/1.2.3.4"), "extra", "info")
}
