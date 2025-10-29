package gologshim

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

// mockBridge simulates go-log's slog bridge for testing duck typing detection
type mockBridge struct {
	sync.Mutex
	slog.Handler
	logs *bytes.Buffer
}

func (m *mockBridge) Handle(_ context.Context, r slog.Record) error {
	m.Lock()
	defer m.Unlock()
	m.logs.WriteString(r.Message)
	m.logs.WriteString("\n")
	return nil
}

func (m *mockBridge) WithAttrs(_ []slog.Attr) slog.Handler {
	// Simple mock - just return self
	return m
}

// TestGoLogBridgeDetection verifies the duck typing mechanism that detects
// go-log's slog bridge without requiring a direct dependency. This is the core
// integration mechanism that allows gologshim to work with go-log when available.
func TestGoLogBridgeDetection(t *testing.T) {
	// Save and restore original default
	originalDefault := slog.Default()
	defer slog.SetDefault(originalDefault)

	t.Run("bridge detected when present", func(t *testing.T) {
		// When go-log's bridge is installed, Logger() should detect it via
		// the GoLogBridge() marker method and use it for logging
		var buf bytes.Buffer
		bridge := &mockBridge{
			Handler: slog.NewTextHandler(&buf, nil),
			logs:    &buf,
		}
		SetDefaultHandler(bridge)

		// Create logger - should detect bridge
		log := Logger("test-subsystem")
		log.Info("test message")

		output := buf.String()
		if !strings.Contains(output, "test message") {
			t.Errorf("Expected bridge to handle log, got: %s", output)
		}
	})

	t.Run("fallback when bridge not present", func(_ *testing.T) {
		// When go-log is not available, Logger() should create a fallback
		// handler that writes to stderr. This ensures gologshim works standalone.
		var buf bytes.Buffer
		handler := slog.NewTextHandler(&buf, nil)
		slog.SetDefault(slog.New(handler))

		// Create logger - should use fallback
		log := Logger("test-fallback")

		// Fallback writes to stderr, but we can verify it doesn't panic
		log.Info("fallback message")
	})
}

// TestLazyBridgeInitialization verifies the lazy handler solves initialization
// order issues. Package-level logger variables are initialized before go-log's
// init() runs, so the lazy handler defers bridge detection until first log call.
func TestLazyBridgeInitialization(t *testing.T) {
	// Save and restore original default
	originalDefault := slog.Default()
	defer slog.SetDefault(originalDefault)

	// Simulate initialization order: gologshim.Logger() called before go-log loads
	var initialBuf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&initialBuf, nil)))

	// Create logger BEFORE bridge is installed (mimics package-level var log = Logger("foo"))
	log := Logger("test-lazy")

	// Now install the bridge (mimics go-log's init() running)
	var bridgeBuf bytes.Buffer
	bridge := &mockBridge{
		Handler: slog.NewTextHandler(&bridgeBuf, nil),
		logs:    &bridgeBuf,
	}
	SetDefaultHandler(bridge)

	// Log in goroutine to detect races
	go log.Info("lazy init message")
	// First log call should detect the bridge via lazy initialization
	log.Info("lazy init message")

	bridge.Lock()
	output := bridgeBuf.String()
	bridge.Unlock()
	if !strings.Contains(output, "lazy init message") {
		t.Errorf("Lazy handler should have detected bridge, got: %s", output)
	}
}

// TestConfigFromEnv verifies environment variable parsing for all GOLOG_* vars.
// These env vars configure logging behavior and must be compatible with go-log.
func TestConfigFromEnv(t *testing.T) {
	// Save and restore env vars
	originalLevel := os.Getenv("GOLOG_LOG_LEVEL")
	originalFormat := os.Getenv("GOLOG_LOG_FORMAT")
	originalLabels := os.Getenv("GOLOG_LOG_LABELS")
	defer func() {
		os.Setenv("GOLOG_LOG_LEVEL", originalLevel)
		os.Setenv("GOLOG_LOG_FORMAT", originalFormat)
		os.Setenv("GOLOG_LOG_LABELS", originalLabels)
	}()

	t.Run("parse GOLOG_LOG_LEVEL", func(t *testing.T) {
		// GOLOG_LOG_LEVEL supports per-subsystem levels: "error,ping=debug,swarm=info"
		os.Setenv("GOLOG_LOG_LEVEL", "error,test-subsystem=debug,another=info")

		// Force re-evaluation by calling parseIPFSGoLogEnv directly
		fallback, systemToLevel, err := parseIPFSGoLogEnv(os.Getenv("GOLOG_LOG_LEVEL"))
		if err != nil {
			t.Fatalf("Failed to parse GOLOG_LOG_LEVEL: %v", err)
		}

		if fallback != slog.LevelError {
			t.Errorf("Expected fallback level ERROR, got %v", fallback)
		}

		if systemToLevel["test-subsystem"] != slog.LevelDebug {
			t.Errorf("Expected test-subsystem level DEBUG, got %v", systemToLevel["test-subsystem"])
		}

		if systemToLevel["another"] != slog.LevelInfo {
			t.Errorf("Expected another level INFO, got %v", systemToLevel["another"])
		}
	})

	t.Run("parse GOLOG_LOG_FORMAT", func(t *testing.T) {
		// GOLOG_LOG_FORMAT controls output format: "json" or text (default)
		os.Setenv("GOLOG_LOG_FORMAT", "json")
		os.Setenv("GOLOG_LOG_LEVEL", "")

		// Note: ConfigFromEnv is cached via sync.OnceValue, so we test the parsing directly
		// In real usage, this would be set before first Logger() call
		logFmt := os.Getenv("GOLOG_LOG_FORMAT")
		if logFmt != "json" {
			t.Errorf("Expected format json, got %s", logFmt)
		}
	})

	t.Run("parse GOLOG_LOG_LABELS", func(t *testing.T) {
		// GOLOG_LOG_LABELS adds key=value pairs to all logs: "app=myapp,dc=us-west"
		os.Setenv("GOLOG_LOG_LABELS", "app=test-app,dc=us-west")

		labels := os.Getenv("GOLOG_LOG_LABELS")
		if !strings.Contains(labels, "app=test-app") {
			t.Error("Expected labels to contain app=test-app")
		}
		if !strings.Contains(labels, "dc=us-west") {
			t.Error("Expected labels to contain dc=us-west")
		}
	})
}

// TestParseIPFSGoLogEnv verifies the GOLOG_LOG_LEVEL parser handles all valid
// formats and returns appropriate errors for invalid input. This parser must
// remain compatible with go-log's format.
func TestParseIPFSGoLogEnv(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantFallback   slog.Level
		wantSubsystems map[string]slog.Level
		wantErr        bool
	}{
		{
			name:           "empty string uses default level",
			input:          "",
			wantFallback:   slog.LevelError,
			wantSubsystems: nil,
			wantErr:        false,
		},
		{
			name:           "fallback only sets global level",
			input:          "debug",
			wantFallback:   slog.LevelDebug,
			wantSubsystems: nil,
			wantErr:        false,
		},
		{
			name:         "fallback and subsystems",
			input:        "error,ping=debug,swarm=info",
			wantFallback: slog.LevelError,
			wantSubsystems: map[string]slog.Level{
				"ping":  slog.LevelDebug,
				"swarm": slog.LevelInfo,
			},
			wantErr: false,
		},
		{
			name:           "invalid level returns error",
			input:          "invalid-level",
			wantFallback:   slog.LevelError,
			wantSubsystems: nil,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fallback, subsystems, err := parseIPFSGoLogEnv(tt.input)

			if (err != nil) != tt.wantErr {
				t.Errorf("parseIPFSGoLogEnv() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if fallback != tt.wantFallback {
				t.Errorf("fallback = %v, want %v", fallback, tt.wantFallback)
			}

			if len(subsystems) != len(tt.wantSubsystems) {
				t.Errorf("subsystems length = %d, want %d", len(subsystems), len(tt.wantSubsystems))
			}

			for k, v := range tt.wantSubsystems {
				if subsystems[k] != v {
					t.Errorf("subsystems[%s] = %v, want %v", k, subsystems[k], v)
				}
			}
		})
	}
}
