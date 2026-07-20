package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
)

type contextKey string

const requestIDKey contextKey = "request_id"
const traceIDKey contextKey = "trace_id"

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if requestID == "" {
			requestID = uuid.NewString()
		}
		traceID := traceIDFromHeaders(r)
		w.Header().Set("X-Request-Id", requestID)
		w.Header().Set("X-Trace-Id", traceID)
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		ctx = context.WithValue(ctx, traceIDKey, traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestLoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			fields := []any{
				slog.String("event", "http.request.start"),
				slog.String("service", "juhe-ai"),
				slog.String("role", "go"),
				slog.String("traceId", traceIDFromContext(r.Context())),
				slog.String("requestId", requestIDFromContext(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
			}
			if logger != nil {
				logger.Info("HTTP 请求开始", fields...)
			}
			next.ServeHTTP(w, r)
			if logger != nil {
				logger.Info("HTTP 请求完成",
					slog.String("event", "http.request.complete"),
					slog.String("service", "juhe-ai"),
					slog.String("role", "go"),
					slog.String("traceId", traceIDFromContext(r.Context())),
					slog.String("requestId", requestIDFromContext(r.Context())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int64("durationMs", time.Since(started).Milliseconds()),
				)
			}
		})
	}
}

func recoverMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					if logger != nil {
						logger.Error("HTTP handler panic",
							slog.Any("error", recovered),
							slog.String("path", r.URL.Path),
							slog.String("request_id", requestIDFromContext(r.Context())),
							slog.String("stack", string(debug.Stack())),
						)
					}
					writeError(w, http.StatusInternalServerError, "服务内部错误")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func loopbackOnlyMiddleware(clientIPs clientIPResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip, err := netip.ParseAddr(clientIPs.FromRequest(r))
			if err != nil || !ip.IsLoopback() {
				writeError(w, http.StatusForbidden, "诊断入口只允许本机访问")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func noStoreMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func requestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

func traceIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(traceIDKey).(string)
	return value
}

func traceIDFromHeaders(r *http.Request) string {
	traceparent := strings.TrimSpace(r.Header.Get("traceparent"))
	parts := strings.Split(traceparent, "-")
	if len(parts) == 4 && len(parts[1]) == 32 {
		return parts[1]
	}
	if traceID := strings.TrimSpace(r.Header.Get("X-Trace-Id")); traceID != "" && len(traceID) <= 128 {
		return traceID
	}
	if correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-Id")); correlationID != "" && len(correlationID) <= 128 {
		return correlationID
	}
	return uuid.NewString()
}
