package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

func New(level string, output io.Writer) (*slog.Logger, error) {
	var parsed slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		parsed = slog.LevelInfo
	case "debug":
		parsed = slog.LevelDebug
	case "warn", "warning":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		return nil, fmt.Errorf("未知日志级别: %s", level)
	}

	handler := newAsyncLogHandler(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: parsed}).WithAttrs([]slog.Attr{
		slog.Int("version", EventVersion),
		slog.String("service", "juhe-ai"),
		slog.String("role", "go"),
	}))
	return slog.New(handler), nil
}
