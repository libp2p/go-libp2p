package canonicallog

import (
	"context"
	"log/slog"
	"math/rand"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	logging "github.com/libp2p/go-libp2p/gologshim"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var log = slog.New(
	slog.NewTextHandler(
		os.Stderr,
		&slog.HandlerOptions{
			Level:     logging.ConfigFromEnv().LevelForSystem("canonical-log"),
			AddSource: true}))

// logWithSkip logs at level with AddSource pointing to the caller `skip` frames up
// from *this* function’s caller (so skip=0 => the immediate caller of logWithSkip).
func logWithSkip(ctx context.Context, l *slog.Logger, level slog.Level, skip int, msg string, args ...any) {
	if !l.Enabled(ctx, level) {
		return
	}

	var pcs [1]uintptr
	// +2 to skip runtime.Callers and logWithSkip itself.
	runtime.Callers(skip+2, pcs[:])

	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	r.Add(args...)
	_ = l.Handler().Handle(ctx, r)
}

// LogMisbehavingPeer is the canonical way to log a misbehaving peer.
// Protocols should use this to identify a misbehaving peer to allow the end
// user to easily identify these nodes across protocols and libp2p.
func LogMisbehavingPeer(p peer.ID, peerAddr multiaddr.Multiaddr, component string, err error, msg string) {
	logWithSkip(context.Background(), log, slog.LevelWarn, 1, "CANONICAL_MISBEHAVING_PEER",
		"peer", p,
		"addr", peerAddr,
		"component", component,
		"err", err,
		"msg", msg)
}

// LogMisbehavingPeerNetAddr is the canonical way to log a misbehaving peer.
// Protocols should use this to identify a misbehaving peer to allow the end
// user to easily identify these nodes across protocols and libp2p.
func LogMisbehavingPeerNetAddr(p peer.ID, peerAddr net.Addr, component string, originalErr error, msg string) {
	ma, err := manet.FromNetAddr(peerAddr)
	if err != nil {
		logWithSkip(context.Background(), log, slog.LevelWarn, 1, "CANONICAL_MISBEHAVING_PEER",
			"peer", p,
			"net_addr", peerAddr.String(),
			"component", component,
			"err", originalErr,
			"msg", msg)
		return
	}

	LogMisbehavingPeer(p, ma, component, originalErr, msg)
}

// LogPeerStatus logs any useful information about a peer. It takes in a sample
// rate and will only log one in every sampleRate messages (randomly). This is
// useful in surfacing events that are normal in isolation, but may be abnormal
// in large quantities. For example, a successful connection from an IP address
// is normal. 10,000 connections from that same IP address is not normal. libp2p
// itself does nothing besides emitting this log. Hook this up to another tool
// like fail2ban to action on the log.
func LogPeerStatus(sampleRate int, p peer.ID, peerAddr multiaddr.Multiaddr, keyVals ...string) {
	if rand.Intn(sampleRate) == 0 {
		args := []any{
			"peer", p,
			"addr", peerAddr.String(),
			"sample_rate", sampleRate,
		}
		// Add the additional key-value pairs
		for _, kv := range keyVals {
			args = append(args, kv)
		}
		logWithSkip(context.Background(), log, slog.LevelInfo, 1, "CANONICAL_PEER_STATUS", args...)
	}
}
