package logx

import (
	"log/slog"
	"os"
	"strings"
)

var base = newBase()

func newBase() *slog.Logger {
	level := new(slog.LevelVar)
	level.Set(parseLevel(os.Getenv("LOG_LEVEL")))

	opts := &slog.HandlerOptions{Level: level}
	format := strings.TrimSpace(strings.ToLower(os.Getenv("LOG_FORMAT")))
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
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

func Base() *slog.Logger {
	return base
}

func L(component string) *slog.Logger {
	return base.With("component", component)
}
