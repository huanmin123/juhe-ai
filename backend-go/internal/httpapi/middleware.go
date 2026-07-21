package httpapi

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/logging"
)

type contextKey string

const requestIDKey contextKey = "request_id"
const traceIDKey contextKey = "trace_id"

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := normalizedCorrelationID(r.Header.Get("X-Request-Id"))
		if requestID == "" {
			requestID = uuid.NewString()
		}
		traceID := traceIDFromHeaders(r)
		w.Header().Set("X-Request-Id", requestID)
		w.Header().Set("X-Trace-Id", traceID)
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		ctx = context.WithValue(ctx, traceIDKey, traceID)
		ctx = logging.WithLogContext(ctx, logging.LogContext{TraceID: traceID, RequestID: requestID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestLoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			if logger != nil {
				logger.InfoContext(r.Context(), "HTTP 请求开始",
					slog.String("event", "http.request.start"),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				)
			}
			metrics := httpsnoop.CaptureMetrics(next, w, r)
			if logger != nil {
				routeTemplate := ""
				if routeContext := chi.RouteContext(r.Context()); routeContext != nil {
					routeTemplate = routeContext.RoutePattern()
				}
				logger.InfoContext(r.Context(), "HTTP 请求完成",
					slog.String("event", "http.request.complete"),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("routeTemplate", routeTemplate),
					slog.Int("statusCode", metrics.Code),
					slog.Int64("responseBytes", metrics.Written),
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
						logger.ErrorContext(r.Context(), "HTTP handler panic",
							slog.String("event", "http.request.panic"),
							slog.String("errorType", fmt.Sprintf("%T", recovered)),
							slog.String("errorMessage", boundedLogText(fmt.Sprint(recovered), 8*1024)),
							slog.String("path", r.URL.Path),
							slog.String("stack", boundedLogText(string(debug.Stack()), 64*1024)),
						)
					}
					writeError(w, http.StatusInternalServerError, "服务内部错误")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func boundedLogText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes] + " [truncated]"
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
	if len(parts) == 4 && validTraceparent(parts) {
		return strings.ToLower(parts[1])
	}
	if traceID := normalizedCorrelationID(r.Header.Get("X-Trace-Id")); traceID != "" {
		return traceID
	}
	if correlationID := normalizedCorrelationID(r.Header.Get("X-Correlation-Id")); correlationID != "" {
		return correlationID
	}
	return uuid.NewString()
}

func validTraceparent(parts []string) bool {
	return len(parts[0]) == 2 && parts[0] != "ff" && validHexValue(parts[0], false) &&
		len(parts[1]) == 32 && validHexValue(parts[1], true) &&
		len(parts[2]) == 16 && validHexValue(parts[2], true) &&
		len(parts[3]) == 2 && validHexValue(parts[3], false)
}

func validHexValue(value string, requireNonZero bool) bool {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return false
	}
	if !requireNonZero {
		return true
	}
	for _, item := range decoded {
		if item != 0 {
			return true
		}
	}
	return false
}

func normalizedCorrelationID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, item := range value {
		if (item >= 'a' && item <= 'z') || (item >= 'A' && item <= 'Z') ||
			(item >= '0' && item <= '9') || item == '-' || item == '_' || item == '.' || item == ':' {
			continue
		}
		return ""
	}
	return value
}
