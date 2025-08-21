package gologshim

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

var lvlToLower = map[slog.Level]slog.Value{
	slog.LevelDebug: slog.StringValue("debug"),
	slog.LevelInfo:  slog.StringValue("info"),
	slog.LevelWarn:  slog.StringValue("warn"),
	slog.LevelError: slog.StringValue("error"),
}

// Logger returns a *slog.Logger with a logging level defined by the
// GOLOG_LOG_LEVEL env var. Supports different levels for different systems. e.g.
// GOLOG_LOG_LEVEL=foo=info,bar=debug,warn
// sets the foo system at level info, the bar system at level debug and the
// fallback level to warn.
//
// Prefer a parameterized logger over a global logger.
func Logger(system string) *slog.Logger {
	var h slog.Handler
	c := ConfigFromEnv()
	handlerOpts := &slog.HandlerOptions{
		Level:     c.LevelForSystem(system),
		AddSource: c.addSource,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				// ipfs go-log uses "ts" for time
				a.Key = "ts"
			} else if a.Key == slog.LevelKey {
				// ipfs go-log uses lowercase level names
				if lvl, ok := a.Value.Any().(slog.Level); ok {
					if s, ok := lvlToLower[lvl]; ok {
						a.Value = s
					}
				}
			}
			return a
		},
	}
	if c.format == logFormatText {
		h = slog.NewTextHandler(os.Stderr, handlerOpts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, handlerOpts)
	}
	attrs := make([]slog.Attr, 1+len(c.labels))
	attrs = append(attrs, slog.String("logger", system))
	attrs = append(attrs, c.labels...)
	h = h.WithAttrs(attrs)
	return slog.New(h)
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
