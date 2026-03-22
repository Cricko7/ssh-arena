package clientlog

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	once sync.Once
	base *slog.Logger
	path string
)

func Base() *slog.Logger {
	once.Do(initLogger)
	return base
}

func L(component string) *slog.Logger {
	return Base().With("component", component)
}

func Path() string {
	once.Do(initLogger)
	return path
}

func initLogger() {
	level := new(slog.LevelVar)
	level.Set(parseLevel(envOr("CLIENT_LOG_LEVEL", envOr("LOG_LEVEL", "info"))))
	writer, resolvedPath := openWriter()
	path = resolvedPath
	opts := &slog.HandlerOptions{Level: level}
	format := strings.TrimSpace(strings.ToLower(envOr("CLIENT_LOG_FORMAT", envOr("LOG_FORMAT", "text"))))
	if format == "json" {
		base = slog.New(slog.NewJSONHandler(writer, opts))
		return
	}
	base = slog.New(slog.NewTextHandler(writer, opts))
}

func openWriter() (io.Writer, string) {
	candidates := make([]string, 0, 3)
	if explicit := strings.TrimSpace(os.Getenv("CLIENT_LOG_PATH")); explicit != "" {
		candidates = append(candidates, explicit)
	}
	if configDir, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(configDir, "ssh-arena", "client.log"))
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), "ssh-arena-client.log"))

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
			continue
		}
		file, err := os.OpenFile(candidate, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			return file, candidate
		}
	}
	return io.Discard, ""
}

func parseLevel(value string) slog.Level {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
