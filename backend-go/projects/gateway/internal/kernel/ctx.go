package kernel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"time"
)

// Request context mirrors the consumed surface of shared/request-context.ts:
// per-request trace/request IDs, client IP and start time. Trace IDs are
// UUIDs; an incoming traceparent header with a valid 32-hex trace id is
// honored like normalizeTraceId.

type ctxKey struct{}

type responseWriterKey struct{}

// WithResponseWriter stashes the kernel's tracking writer so nested
// middlewares (guards) share the same status observation as the outer
// localizeWriter that serves the request.
func WithResponseWriter(ctx context.Context, lw *localizeWriter) context.Context {
	return context.WithValue(ctx, responseWriterKey{}, lw)
}

// ResponseWriterFromContext returns the shared tracking writer, if present.
func ResponseWriterFromContext(ctx context.Context) *localizeWriter {
	if lw, ok := ctx.Value(responseWriterKey{}).(*localizeWriter); ok {
		return lw
	}
	return nil
}

type RequestContext struct {
	TraceID   string
	RequestID string
	ClientIP  string
	Method    string
	Path      string
	StartedAt time.Time
}

// Context returns the request context attached by RequestContextMiddleware.
func Context(r *http.Request) *RequestContext {
	if value, ok := r.Context().Value(ctxKey{}).(*RequestContext); ok {
		return value
	}
	return &RequestContext{TraceID: newUUID(), RequestID: newUUID(), StartedAt: time.Now(), Method: r.Method, Path: r.URL.Path}
}

func RequestContextMiddleware(trustProxyCount int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := &RequestContext{
				TraceID:   normalizeTraceID(r.Header.Get("traceparent")),
				RequestID: newUUID(),
				ClientIP:  ExtractClientIP(r, trustProxyCount),
				Method:    r.Method,
				Path:      r.URL.Path,
				StartedAt: time.Now(),
			}
			if ctx.TraceID == "" {
				ctx.TraceID = newUUID()
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, ctx)))
		})
	}
}

func normalizeTraceID(traceparent string) string {
	// traceparent: version-traceid-parentid-flags (trace id = 32 hex chars)
	parts := strings.Split(strings.TrimSpace(traceparent), "-")
	if len(parts) < 3 {
		return ""
	}
	traceID := strings.ToLower(parts[1])
	if len(traceID) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(traceID); err != nil {
		return ""
	}
	return traceID
}

// ExtractClientIP mirrors extractClientIp: with trusted proxies, req.ip is
// express' remote-address resolution; Go equivalent takes the last trusted
// X-Forwarded-For entry when trustProxyCount > 0.
func ExtractClientIP(r *http.Request, trustProxyCount int) string {
	remote := normalizeClientIP(r.RemoteAddr)
	if trustProxyCount > 0 {
		forwarded := r.Header.Get("x-forwarded-for")
		if forwarded != "" {
			parts := strings.Split(forwarded, ",")
			index := len(parts) - trustProxyCount
			if index < 0 {
				index = 0
			}
			if candidate := normalizeClientIP(strings.TrimSpace(parts[index])); candidate != "" {
				return candidate
			}
		}
	}
	return remote
}

func normalizeClientIP(value string) string {
	if value == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		host = value
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func newUUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst, buf[:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], buf[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], buf[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], buf[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:], buf[10:])
	return string(dst)
}
