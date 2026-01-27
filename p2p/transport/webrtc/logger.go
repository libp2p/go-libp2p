package libp2pwebrtc

import (
	"context"
	"fmt"
	"log/slog"

	logging "github.com/libp2p/go-libp2p/gologshim"
	pionLogging "github.com/pion/logging"
)

var log = logging.Logger("webrtc-transport")

// pionLog is the logger provided to pion for internal logging
var pionLog = logging.Logger("webrtc-transport-pion")

// pionLogger adapts pion's logger to go-libp2p's semantics.
// Pion logs routine connection events (client disconnects, protocol mismatches,
// state races) as ERROR/WARN, but these are normal operational noise from a
// service perspective. We downgrade all pion logs to DEBUG to prevent log spam
// while preserving debuggability when needed.
type pionLogger struct {
	*slog.Logger
}

var pLog = pionLogger{pionLog}

var _ pionLogging.LeveledLogger = pLog

func (l pionLogger) Debug(s string) {
	l.Logger.Debug(s)
}

func (l pionLogger) Debugf(s string, args ...interface{}) {
	if l.Logger.Enabled(context.Background(), slog.LevelDebug) {
		l.Logger.Debug(fmt.Sprintf(s, args...))
	}
}

func (l pionLogger) Error(s string) {
	l.Logger.Debug(s)
}

func (l pionLogger) Errorf(s string, args ...interface{}) {
	if l.Logger.Enabled(context.Background(), slog.LevelDebug) {
		l.Logger.Debug(fmt.Sprintf(s, args...))
	}
}

func (l pionLogger) Info(s string) {
	l.Logger.Debug(s)
}

func (l pionLogger) Infof(s string, args ...interface{}) {
	if l.Logger.Enabled(context.Background(), slog.LevelDebug) {
		l.Logger.Debug(fmt.Sprintf(s, args...))
	}
}

func (l pionLogger) Warn(s string) {
	l.Logger.Debug(s)
}

func (l pionLogger) Warnf(s string, args ...interface{}) {
	if l.Logger.Enabled(context.Background(), slog.LevelDebug) {
		l.Logger.Debug(fmt.Sprintf(s, args...))
	}
}

func (l pionLogger) Trace(s string) {
	l.Logger.Debug(s)
}
func (l pionLogger) Tracef(s string, args ...interface{}) {
	if l.Logger.Enabled(context.Background(), slog.LevelDebug) {
		l.Logger.Debug(fmt.Sprintf(s, args...))
	}
}

// loggerFactory returns pLog for all new logger instances
type loggerFactory struct{}

// NewLogger returns pLog for all new logger instances. Internally pion creates lots of
// separate logging objects unnecessarily. To avoid the allocations we use a single log
// object for all of pion logging.
func (loggerFactory) NewLogger(_ string) pionLogging.LeveledLogger {
	return pLog
}

var _ pionLogging.LoggerFactory = loggerFactory{}

var pionLoggerFactory = loggerFactory{}
