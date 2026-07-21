package logging

import (
	"context"
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
	base := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: parsed}).WithAttrs([]slog.Attr{
		slog.Int("version", EventVersion),
		slog.String("service", "juhe-ai"),
		slog.String("role", "go"),
	})
	return slog.New(&contextHandler{base: base}), nil
}

type contextHandler struct {
	base slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, record slog.Record) error {
	record.AddAttrs(logContextAttrs(ctx)...)
	return h.base.Handle(ctx, record)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{base: h.base.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{base: h.base.WithGroup(name)}
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
