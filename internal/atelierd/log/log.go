// Package log appends structured log lines to ~/.atelier/atelierd.log (mode
// 0600). Format is one slog text record per line — INFO/WARN/ERROR levels.
//
// No Sentry: the Valian canon reserves Sentry for cloud components only. The
// daemon ships local logs only; rotation is manual (mv atelierd.log
// atelierd.log.1) and `atelierd status` warns when the file grows large.
package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

var (
	mu      sync.Mutex
	logger  *slog.Logger
	logFile *os.File
)

// Init opens ~/.atelier/atelierd.log for append (creating with mode 0600 if
// absent) and wires a slog text handler to it. Subsequent calls are no-ops.
//
// If the file cannot be opened (permissions, disk full), the logger falls
// back to stderr — better partial logs than no logs at all on a daemon.
func Init() error {
	mu.Lock()
	defer mu.Unlock()
	if logger != nil {
		return nil
	}
	if err := paths.EnsureDir(paths.MustRoot()); err != nil {
		return fmt.Errorf("ensure ~/.atelier: %w", err)
	}
	f, err := os.OpenFile(paths.Log(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, paths.FileMode)
	if err != nil {
		// Fallback: emit to stderr so we still have something to debug from.
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
		return fmt.Errorf("open log: %w", err)
	}
	_ = os.Chmod(paths.Log(), paths.FileMode)
	logFile = f
	logger = slog.New(slog.NewTextHandler(io.MultiWriter(f), &slog.HandlerOptions{Level: slog.LevelInfo}))
	return nil
}

// Close flushes and closes the underlying log file. Safe to call multiple times.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}
	logger = nil
}

func ensureLogger() *slog.Logger {
	if logger == nil {
		_ = Init()
	}
	return logger
}

// Info logs at INFO level with key/value attributes.
func Info(msg string, attrs ...any) {
	ensureLogger().LogAttrs(context.Background(), slog.LevelInfo, msg, toAttrs(attrs)...)
}

// Warn logs at WARN level with key/value attributes.
func Warn(msg string, attrs ...any) {
	ensureLogger().LogAttrs(context.Background(), slog.LevelWarn, msg, toAttrs(attrs)...)
}

// Error logs at ERROR level with key/value attributes.
func Error(msg string, attrs ...any) {
	ensureLogger().LogAttrs(context.Background(), slog.LevelError, msg, toAttrs(attrs)...)
}

func toAttrs(args []any) []slog.Attr {
	if len(args) == 0 {
		return nil
	}
	out := make([]slog.Attr, 0, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			continue
		}
		out = append(out, slog.Any(key, args[i+1]))
	}
	return out
}
