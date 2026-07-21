package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

func New(level string, output io.Writer) (*slog.Logger, error) {
	runtime, err := NewRuntime(level, output, RuntimeOptions{Role: "go"})
	if err != nil {
		return nil, err
	}
	return runtime.Logger, nil
}

func NewRuntime(level string, output io.Writer, options RuntimeOptions) (*Runtime, error) {
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

	role := options.Role
	if role == "" {
		role = "go"
	}
	base := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: parsed}).WithAttrs([]slog.Attr{
		slog.Int("version", EventVersion),
		slog.String("service", "juhe-ai"),
		slog.String("role", role),
	})
	return newAsyncLogRuntime(base, options), nil
}

func Shutdown(ctx context.Context, runtime *Runtime) error {
	if runtime == nil {
		return nil
	}
	return runtime.Shutdown(ctx)
}
