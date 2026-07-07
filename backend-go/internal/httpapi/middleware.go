package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"

	"github.com/google/uuid"
)

type contextKey string

const requestIDKey contextKey = "request_id"

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-Id")
		if requestID == "" {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", requestID)
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
