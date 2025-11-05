// Package gologshim provides slog-based logging for go-libp2p that works
// standalone or integrates with go-log for unified log management across
// IPFS/libp2p applications, without adding go-log as a dependency.
//
// # Usage
//
// Create loggers using the Logger function:
//
//	var log = gologshim.Logger("subsystem")
//	log.Debug("message", "key", "value")
//
// # Integration with go-log
//
// Applications can optionally connect go-libp2p to go-log by calling SetDefaultHandler:
//
//	import golog "github.com/ipfs/go-log/v2"
//
//	func init() {
//		gologshim.SetDefaultHandler(golog.SlogHandler())
//	}
//
// When integrated, go-libp2p logs use go-log's formatting and can be controlled
// programmatically via go-log's SetLogLevel("subsystem", "level") API to adjust
// log verbosity per subsystem at runtime without restarting.
//
// Note: SlogHandler() works even when GOLOG_CAPTURE_DEFAULT_SLOG=false, making
// it more reliable than using slog.Default().Handler().
//
// # Standalone Usage
//
// Without calling SetDefaultHandler, gologshim creates standalone slog handlers
// writing to stderr. This mode is useful when go-log is not present or when you
// want independent log configuration via backward-compatible (go-log) environment variables:
//
//   - GOLOG_LOG_LEVEL: Set log levels per subsystem (e.g., "error,ping=debug")
//   - GOLOG_LOG_FORMAT/GOLOG_LOG_FMT: Output format ("json" or text)
//   - GOLOG_LOG_ADD_SOURCE: Include source location (default: true)
//   - GOLOG_LOG_LABELS: Add key=value labels to all logs
//
// For integration details, see: https://github.com/ipfs/go-log/blob/master/README.md#slog-integration
//
// Note: This package exists as an intermediate solution while go-log uses zap
// internally. If go-log migrates from zap to native slog, this bridge layer
// could be simplified or removed entirely.
package gologshim

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

var lvlToLower = map[slog.Level]slog.Value{
	slog.LevelDebug: slog.StringValue("debug"),
	slog.LevelInfo:  slog.StringValue("info"),
	slog.LevelWarn:  slog.StringValue("warn"),
	slog.LevelError: slog.StringValue("error"),
}

var defaultHandler atomic.Pointer[slog.Handler]

// SetDefaultHandler allows an application to change the underlying handler used
// by gologshim as long as it's changed *before* the first log by the logger.
func SetDefaultHandler(handler slog.Handler) {
	defaultHandler.Store(&handler)
}

// dynamicHandler delays bridge detection until first log call to handle init order issues
type dynamicHandler struct {
	system  string
	config  *Config
	once    sync.Once
	handler slog.Handler
}

func (h *dynamicHandler) ensureHandler() slog.Handler {
	h.once.Do(func() {
		if hPtr := defaultHandler.Load(); hPtr != nil {
			h.handler = *hPtr
		} else {
			h.handler = h.createFallbackHandler()
		}
		attrs := make([]slog.Attr, 0, 1+len(h.config.labels))
		// Use "logger" attribute for compatibility with go-log's Zap-based format
		// and existing IPFS/libp2p tooling and dashboards.
		attrs = append(attrs, slog.String("logger", h.system))
		attrs = append(attrs, h.config.labels...)
		h.handler = h.handler.WithAttrs(attrs)
	})

	return h.handler
}

func (h *dynamicHandler) createFallbackHandler() slog.Handler {
	opts := &slog.HandlerOptions{
		Level:     h.config.LevelForSystem(h.system),
		AddSource: h.config.addSource,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey:
				// ipfs go-log uses "ts" for time
				a.Key = "ts"
			case slog.LevelKey:
				if lvl, ok := a.Value.Any().(slog.Level); ok {
					// ipfs go-log uses lowercase level names
					if s, ok := lvlToLower[lvl]; ok {
						a.Value = s
					}
				}
			}
			return a
		},
	}
	if h.config.format == logFormatText {
		return slog.NewTextHandler(os.Stderr, opts)
	}

	return slog.NewJSONHandler(os.Stderr, opts)
}

func (h *dynamicHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.ensureHandler().Enabled(ctx, lvl)
}

func (h *dynamicHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.ensureHandler().Handle(ctx, r)
}

func (h *dynamicHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h.ensureHandler().WithAttrs(attrs)
}

func (h *dynamicHandler) WithGroup(name string) slog.Handler {
	return h.ensureHandler().WithGroup(name)
}

// Logger returns a *slog.Logger with a logging level defined by the
// GOLOG_LOG_LEVEL env var. Supports different levels for different systems. e.g.
// GOLOG_LOG_LEVEL=foo=info,bar=debug,warn
// sets the foo system at level info, the bar system at level debug and the
// fallback level to warn.
func Logger(system string) *slog.Logger {
	c := ConfigFromEnv()
	return slog.New(&dynamicHandler{
		system: system,
		config: c,
	})
}

type logFormat = int

const (
	logFormatText logFormat = iota
	logFormatJSON
)

type Config struct {
	fallbackLvl   slog.Level
	systemToLevel map[string]slog.Level
	format        logFormat
	addSource     bool
	labels        []slog.Attr
}

func (c *Config) LevelForSystem(system string) slog.Level {
	if lvl, ok := c.systemToLevel[system]; ok {
		return lvl
	}
	return c.fallbackLvl
}

var ConfigFromEnv func() *Config = sync.OnceValue(func() *Config {
	fallback, systemToLevel, err := parseIPFSGoLogEnv(os.Getenv("GOLOG_LOG_LEVEL"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse GOLOG_LOG_LEVEL: %v", err)
		fallback = slog.LevelInfo
	}
	c := &Config{
		fallbackLvl:   fallback,
		systemToLevel: systemToLevel,
		addSource:     true,
	}

	logFmt := os.Getenv("GOLOG_LOG_FORMAT")
	if logFmt == "" {
		logFmt = os.Getenv("GOLOG_LOG_FMT")
	}
	if logFmt == "json" {
		c.format = logFormatJSON
	}

	logFmt = os.Getenv("GOLOG_LOG_ADD_SOURCE")
	if logFmt == "0" || logFmt == "false" {
		c.addSource = false
	}

	labels := os.Getenv("GOLOG_LOG_LABELS")
	if labels != "" {
		labels := strings.Split(labels, ",")
		if len(labels) > 0 {
			for _, label := range labels {
				kv := strings.SplitN(label, "=", 2)
				if len(kv) == 2 {
					c.labels = append(c.labels, slog.String(kv[0], kv[1]))
				} else {
					fmt.Fprintf(os.Stderr, "Invalid label format: %s", label)
				}
			}
		}
	}

	return c
})

func parseIPFSGoLogEnv(loggingLevelEnvStr string) (slog.Level, map[string]slog.Level, error) {
	fallbackLvl := slog.LevelError
	var systemToLevel map[string]slog.Level
	if loggingLevelEnvStr != "" {
		for _, kvs := range strings.Split(loggingLevelEnvStr, ",") {
			kv := strings.SplitN(kvs, "=", 2)
			var lvl slog.Level
			err := lvl.UnmarshalText([]byte(kv[len(kv)-1]))
			if err != nil {
				return lvl, nil, err
			}
			switch len(kv) {
			case 1:
				fallbackLvl = lvl
			case 2:
				if systemToLevel == nil {
					systemToLevel = make(map[string]slog.Level)
				}
				systemToLevel[kv[0]] = lvl
			}
		}
	}
	return fallbackLvl, systemToLevel, nil
}
