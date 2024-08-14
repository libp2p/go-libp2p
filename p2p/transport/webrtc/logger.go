package libp2pwebrtc

import (
	logging "github.com/ipfs/go-log/v2"
	pionLogging "github.com/pion/logging"
)

var log = logging.Logger("webrtc-transport")

// pionLog is the logger provided to pion for internal logging
var pionLog = logging.Logger("webrtc-transport-pion")

// pionLogger wraps the StandardLogger interface to provide a LeveledLogger interface
// as expected by pion
// Pion logs are too noisy and have invalid log levels. pionLogger downgrades all the
// logs to debug
type pionLogger struct {
	logging.StandardLogger
}

var pLog = pionLogger{pionLog}

var _ pionLogging.LeveledLogger = pLog

func (l pionLogger) Debug(s string) {
	l.StandardLogger.Debug(s)
}

func (l pionLogger) Error(s string) {
	l.StandardLogger.Debug(s)
}

func (l pionLogger) Errorf(s string, args ...interface{}) {
	l.StandardLogger.Debugf(s, args...)
}

func (l pionLogger) Info(s string) {
	l.StandardLogger.Debug(s)
}

func (l pionLogger) Infof(s string, args ...interface{}) {
	l.StandardLogger.Debugf(s, args...)
}

func (l pionLogger) Warn(s string) {
	l.StandardLogger.Debug(s)
}

func (l pionLogger) Warnf(s string, args ...interface{}) {
	l.StandardLogger.Debugf(s, args...)
}

func (l pionLogger) Trace(s string) {
	l.StandardLogger.Debug(s)
}
func (l pionLogger) Tracef(s string, args ...interface{}) {
	l.StandardLogger.Debugf(s, args...)
}

// loggerFactory returns pLog for all new logger instances
type loggerFactory struct{}

// NewLogger returns pLog for all new logger instances. Internally pion creates lots of
// separate logging objects unnecessarily. To avoid the allocations we use a single log
// object for all of pion logging.
func (loggerFactory) NewLogger(scope string) pionLogging.LeveledLogger {
	return pLog
}

var _ pionLogging.LoggerFactory = loggerFactory{}

var pionLoggerFactory = loggerFactory{}
