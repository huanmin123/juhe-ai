package logging

import (
	"context"
	"log/slog"
)

type logContextKey struct{}

type LogContext struct {
	TraceID   string
	RequestID string
	JobID     string
	ParentID  string
}

func WithLogContext(ctx context.Context, fields LogContext) context.Context {
	return context.WithValue(ctx, logContextKey{}, fields)
}

func LogContextFrom(ctx context.Context) LogContext {
	fields, _ := ctx.Value(logContextKey{}).(LogContext)
	return fields
}

func logContextAttrs(ctx context.Context) []slog.Attr {
	fields := LogContextFrom(ctx)
	attrs := make([]slog.Attr, 0, 4)
	if fields.TraceID != "" {
		attrs = append(attrs, slog.String("traceId", fields.TraceID))
	}
	if fields.RequestID != "" {
		attrs = append(attrs, slog.String("requestId", fields.RequestID))
	}
	if fields.JobID != "" {
		attrs = append(attrs, slog.String("jobId", fields.JobID))
	}
	if fields.ParentID != "" {
		attrs = append(attrs, slog.String("parentId", fields.ParentID))
	}
	return attrs
}
