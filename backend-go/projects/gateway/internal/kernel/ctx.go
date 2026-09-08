package kernel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
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
	TraceID     string
	RequestID   string
	ClientIP    string
	Method      string
	Path        string
	OriginalURL string
	StartedAt   time.Time

	// D-191（BUG-0175）：请求生命周期事件状态。metricHandle 是经
	// HTTPMetricHooks.Start 创建的 prometheus 请求句柄（未计量路由为 nil）；
	// summaryLogged 保证 finish/closed/timing_summary 只发一次。
	mu            sync.Mutex
	metricHandle  any
	summaryLogged bool
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
			startedAt := time.Now()
			ctx := &RequestContext{
				TraceID:     normalizeTraceID(r),
				RequestID:   newUUID(),
				ClientIP:    ExtractClientIP(r, trustProxyCount),
				Method:      r.Method,
				Path:        r.URL.Path,
				OriginalURL: r.URL.RequestURI(),
				StartedAt:   startedAt,
			}
			if ctx.TraceID == "" {
				ctx.TraceID = newUUID()
			}
			// requestContextMiddleware sets x-trace-id on every response
			// before the chain descends, so success, business errors and
			// gateway errors all carry the trace back to the client.
			w.Header().Set("X-Trace-Id", ctx.TraceID)

			// startHttpMetricRequest（D-190）：observability 路由返回 nil，
			// Finish 钩子按 Node 语义对 nil 请求保持 no-op。
			if hooks := httpMetricHooks.Load(); hooks != nil && hooks.Start != nil {
				ctx.metricHandle = hooks.Start(ctx.Path, ctx.Method, startedAt.UnixMilli())
			}
			emitHTTPRequestStarted(ctx)

			tracking := &statusTrackingWriter{ResponseWriter: w}
			next.ServeHTTP(tracking, r.WithContext(context.WithValue(r.Context(), ctxKey{}, ctx)))
			finishRequestObservability(ctx, tracking, r, startedAt)
		})
	}
}

// finishRequestObservability mirrors the res.once('finish'/'close') pair of
// requestContextMiddleware (request-context.ts:184-189): a handler return
// with a live request context is the completed contract; a canceled context
// means the connection dropped before the response settled and renders the
// closed (aborted) contract instead. Exactly one metric finish / event set
// runs per request.
func finishRequestObservability(ctx *RequestContext, tracking *statusTrackingWriter, r *http.Request, startedAt time.Time) {
	ctx.mu.Lock()
	alreadyLogged := ctx.summaryLogged
	ctx.summaryLogged = true
	ctx.mu.Unlock()
	if alreadyLogged {
		return
	}

	finishedAtMs := time.Now().UnixMilli()
	statusCode := tracking.statusPointer()
	hooks := httpMetricHooks.Load()
	if r.Context().Err() != nil {
		// logRequestClosed: finishHttpMetricRequest(..., 'aborted') + the
		// closed warn record (request-context.ts:519-560).
		if hooks != nil && hooks.Finish != nil {
			hooks.Finish(ctx.metricHandle, statusCode, "aborted", finishedAtMs, "none")
		}
		durationMs := finishedAtMs - startedAt.UnixMilli()
		emitHTTPRequestClosed(ctx, statusCode, durationMs)
		// Node logRequestClosed also schedules the aborted timing summary
		// (request-context.ts:549); the same gateway-route gate applies.
		if ClassifyGatewayRoutePath(ctx.Path) {
			emitHTTPRequestTimingSummary(ctx, statusCode, "aborted", durationMs)
		}
		return
	}
	failureScope := httpMetricFailureScope(statusCode, "completed")
	if hooks != nil && hooks.Finish != nil {
		hooks.Finish(ctx.metricHandle, statusCode, "completed", finishedAtMs, failureScope)
	}
	emitHTTPRequestCompleted(ctx, statusCode, failureScope, finishedAtMs-startedAt.UnixMilli())
	// logRequestTimingSummary (request-context.ts:491 setImmediate): Node
	// emits the summary on the completed path; only gateway-route requests
	// carry stage summaries, so the summary gates to that route group exactly
	// like the Node stage-less early return.
	if ClassifyGatewayRoutePath(ctx.Path) {
		emitHTTPRequestTimingSummary(ctx, statusCode, resolveRequestSummaryOutcome(statusCode), time.Since(startedAt).Milliseconds())
	}
}

// ClassifyGatewayRoutePath reports whether the path belongs to the /v1
// gateway family (classifyHttpMetricRoute's 'gateway' group).
func ClassifyGatewayRoutePath(path string) bool {
	return path == "/" || path == "/v1" || strings.HasPrefix(path, "/v1/")
}

// normalizeTraceID mirrors request-context.ts normalizeTraceId: strict
// traceparent parsing first, then the first legal x-trace-id, then
// x-correlation-id. An empty result lets the caller generate a UUID.
func normalizeTraceID(r *http.Request) string {
	if traceParent := ParseTraceParent(r.Header.Get("Traceparent")); traceParent != "" {
		return traceParent
	}
	if traceID := normalizeHeaderID(r.Header.Get("X-Trace-Id")); traceID != "" {
		return traceID
	}
	return normalizeHeaderID(r.Header.Get("X-Correlation-Id"))
}

// traceParentPattern mirrors the strict four-segment traceparent grammar of
// request-context.ts parseTraceParent (version-traceid-parentid-flags with
// 2/32/16/2 hex characters).
var traceParentPattern = regexp.MustCompile(`^([\da-fA-F]{2})-([\da-fA-F]{32})-([\da-fA-F]{16})-([\da-fA-F]{2})$`)

// ParseTraceParent mirrors request-context.ts parseTraceParent: version ff,
// all-zero trace ids and all-zero parent ids are rejected; a valid trace id
// is returned lowercased.
func ParseTraceParent(value string) string {
	if value == "" {
		return ""
	}
	match := traceParentPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return ""
	}
	if strings.ToLower(match[1]) == "ff" {
		return ""
	}
	if isAllZeroHex(match[2]) || isAllZeroHex(match[3]) {
		return ""
	}
	return strings.ToLower(match[2])
}

func isAllZeroHex(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char != '0' {
			return false
		}
	}
	return true
}

// headerIDPattern mirrors normalizeHeaderId's character set.
var headerIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// normalizeHeaderID mirrors request-context.ts normalizeHeaderId: the first
// non-empty comma value, trimmed, at most 128 characters of
// [A-Za-z0-9._:-].
func normalizeHeaderID(value string) string {
	text := firstHeaderValue(value)
	if text == "" || len(text) > 128 || !headerIDPattern.MatchString(text) {
		return ""
	}
	return text
}

func firstHeaderValue(value string) string {
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// ExtractClientIP mirrors extractClientIp (shared/request-context.ts:456):
// normalizeClientIp(req.ip) ?? normalizeClientIp(req.socket.remoteAddress).
// The chain is IPv4-only and an empty result carries the Node undefined
// semantics. req.ip is the Express trust-proxy resolution: with
// trustProxyCount trusted hops the X-Forwarded-For entry at
// len(parts)-trustProxyCount answers; fewer entries than trusted hops cannot
// identify an untrusted client, so the socket address answers instead of the
// client-controlled first entry (防伪造: a direct caller forging a short XFF
// chain must not pick its own leftmost value).
func ExtractClientIP(r *http.Request, trustProxyCount int) string {
	remote := normalizeClientIP(r.RemoteAddr)
	if trustProxyCount > 0 {
		forwarded := r.Header.Get("x-forwarded-for")
		if forwarded != "" {
			parts := strings.Split(forwarded, ",")
			if len(parts) >= trustProxyCount {
				index := len(parts) - trustProxyCount
				if candidate := normalizeClientIP(strings.TrimSpace(parts[index])); candidate != "" {
					return candidate
				}
			}
		}
	}
	return remote
}

// ipv4WithPortPattern mirrors the Node /^\d{1,3}(?:\.\d{1,3}){3}:\d+$/ check.
var ipv4WithPortPattern = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}:\d+$`)

// normalizeClientIP mirrors the Node helper (shared/request-context.ts:716):
// trim, strip [..] brackets, strip a ":port" suffix from dotted-quad text,
// strip the "::ffff:" mapped-address prefix, and keep only IPv4 results
// (isIP(ip) === 4); everything else — including IPv6 — normalizes to "",
// exactly like the Node undefined.
func normalizeClientIP(value string) string {
	if value == "" {
		return ""
	}
	ip := strings.TrimSpace(value)
	if ip == "" {
		return ""
	}
	if strings.HasPrefix(ip, "[") {
		end := strings.Index(ip, "]")
		if end > 0 {
			ip = ip[1:end]
		}
	}
	if ipv4WithPortPattern.MatchString(ip) {
		ip = ip[:strings.LastIndex(ip, ":")]
	}
	if strings.HasPrefix(ip, "::ffff:") {
		ip = ip[len("::ffff:"):]
	}
	if !isIPv4Text(ip) {
		return ""
	}
	return ip
}

// isIPv4Text mirrors isIP(ip) === 4: dotted-quad only.
func isIPv4Text(value string) bool {
	parsed := net.ParseIP(value)
	return parsed != nil && parsed.To4() != nil && !strings.Contains(value, ":")
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
