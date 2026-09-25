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
	"sync/atomic"
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

// RequestStageSummary mirrors the Node RequestStageSummary entry shape
// (request-context.ts:251-258): the minimal per-stage facts the
// gateway.request.timing_summary consumer reads.
type RequestStageSummary struct {
	Sequence        int64  `json:"sequence"`
	Stage           string `json:"stage"`
	Outcome         string `json:"outcome"`
	DurationMs      int64  `json:"durationMs"`
	StartedOffsetMs int64  `json:"startedOffsetMs"`
	EndedOffsetMs   int64  `json:"endedOffsetMs"`
}

// RequestStageAccumulation is the immutable snapshot the timing summary
// reads at request end.
type RequestStageAccumulation struct {
	Stages                []RequestStageSummary
	StageCount            int64
	DroppedStageSummaries int64
	AttemptCount          int64
}

// requestStageSummaryCapacity mirrors the Node stageSummaries bound
// (request-context.ts:250): at most 64 entries ride the summary; the excess
// only increments the dropped counter.
const requestStageSummaryCapacity = 64

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

	// 请求级阶段/尝试累积（对齐 Node request-context 的 stageSummaries /
	// stageSequence / stageSummaryDropped / attemptCount）：stage 记录经
	// cmd 侧 gateway.request.stage 观测面写入，emission 点在请求结束后
	// 读取快照。mu 同时守护本组字段与上方生命周期字段。
	stageSummaries []RequestStageSummary
	stageTotal     int64
	stageDropped   int64
	attemptCount   int64

	// gatewayAttempts 是 /v1 链的网关派发尝试计数（RecordGatewayAttempt
	// 每次 +1）。与上方 max 语义的 attemptCount（RecordUpstreamAttempt 的
	// 绝对索引折算）分开累计：同一观测点两者并存，混合 max/+1 会互相污染
	// （每个观测点 max 到 1 再逐次 +1 会双计）。经 sync/atomic 访问，不
	// 参与 mu 守护；timing_summary 取两个累积器的较大值。
	gatewayAttempts int64

	// failureReason 是 5xx 响应的处理根因（如底层 SQL 错误原文），由
	// WriteErrorCause 在响应写入前记录，随 http_request_completed 事件以
	// failureReason 字段输出。mu 守护；只保留首次记录（首个根因即最早
	// 失败点），后续覆盖不生效。
	failureReason string
}

// RecordFailureReason records the root cause behind a 5xx response so the
// http_request_completed log line can explain itself. Only the first reason
// wins (the earliest failure point is the primary one); the value is
// truncated and stripped of control characters for safe single-line logging.
func (ctx *RequestContext) RecordFailureReason(reason string) {
	if ctx == nil {
		return
	}
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		return
	}
	if len(reason) > 400 {
		reason = reason[:400]
	}
	ctx.mu.Lock()
	if ctx.failureReason == "" {
		ctx.failureReason = reason
	}
	ctx.mu.Unlock()
}

// FailureReason returns the recorded 5xx root cause, if any.
func (ctx *RequestContext) FailureReason() string {
	if ctx == nil {
		return ""
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	return ctx.failureReason
}

// RecordRequestStage accumulates one gateway.request.stage observation
// (mirrors logRequestStage's context bookkeeping, request-context.ts:248-266):
// entries ride the summary until requestStageSummaryCapacity, the excess only
// bumps the dropped counter, and the total sequence counts every stage.
func (ctx *RequestContext) RecordRequestStage(stage string, outcome string, durationMs int64, startedAt time.Time) {
	if ctx == nil {
		return
	}
	startedOffsetMs := int64(0)
	if ctx.StartedAt.After(time.Time{}) {
		if offset := startedAt.Sub(ctx.StartedAt).Milliseconds(); offset > 0 {
			startedOffsetMs = offset
		}
	}
	summary := RequestStageSummary{
		Stage:           stage,
		Outcome:         outcome,
		DurationMs:      durationMs,
		StartedOffsetMs: startedOffsetMs,
		EndedOffsetMs:   startedOffsetMs + durationMs,
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.stageTotal++
	summary.Sequence = ctx.stageTotal
	if len(ctx.stageSummaries) < requestStageSummaryCapacity {
		ctx.stageSummaries = append(ctx.stageSummaries, summary)
	} else {
		ctx.stageDropped++
	}
}

// RecordUpstreamAttempt accumulates one upstream attempt observation
// (mirrors captureRequestTimingFields, request-context.ts:624-636):
// attemptCount = max(attemptIndex+1, auditAttemptIndex) over the observed
// values. A nil index means that index shape is unavailable at the
// observation point; with both absent only the bare attempt fact registers
// (attemptCount at least 1) — no count is invented beyond that.
func (ctx *RequestContext) RecordUpstreamAttempt(attemptIndex, auditAttemptIndex *int) {
	if ctx == nil {
		return
	}
	observed := int64(0)
	if attemptIndex != nil && *attemptIndex >= 0 {
		observed = int64(*attemptIndex) + 1
	}
	if auditAttemptIndex != nil && int64(*auditAttemptIndex) > observed {
		observed = int64(*auditAttemptIndex)
	}
	if observed == 0 {
		observed = 1
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if observed > ctx.attemptCount {
		ctx.attemptCount = observed
	}
}

// RecordGatewayAttempt accumulates one gateway dispatch attempt (+1) at the
// chain-level observation point (cmd 侧每次拿到上游响应头的 upstream
// attempt)。它按发生次数累计，与 RecordUpstreamAttempt 的 max-索引语义互
// 不覆盖：索引字段缺席的重复观测（引擎内部计数不外露）也能加出真实尝试
// 次数。W1b 并行面依赖本方法与 GatewayAttemptCount。
func (ctx *RequestContext) RecordGatewayAttempt() {
	if ctx == nil {
		return
	}
	atomic.AddInt64(&ctx.gatewayAttempts, 1)
}

// GatewayAttemptCount returns the accumulated gateway dispatch attempt count.
func (ctx *RequestContext) GatewayAttemptCount() int {
	if ctx == nil {
		return 0
	}
	return int(atomic.LoadInt64(&ctx.gatewayAttempts))
}

// RequestStageAccumulation snapshots the accumulated stage/attempt facts.
func (ctx *RequestContext) RequestStageAccumulation() RequestStageAccumulation {
	if ctx == nil {
		return RequestStageAccumulation{}
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	stages := make([]RequestStageSummary, len(ctx.stageSummaries))
	copy(stages, ctx.stageSummaries)
	return RequestStageAccumulation{
		Stages:                stages,
		StageCount:            ctx.stageTotal,
		DroppedStageSummaries: ctx.stageDropped,
		AttemptCount:          ctx.attemptCount,
	}
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

// normalizeClientIP 源自 Node helper（shared/request-context.ts:716），2026-09-25
// 有意分叉：生产链路（CF→Edge→Caddy→Traefik）透传后 IPv6 客户端（如 2408::/中国
// 联通 6）曾被"仅保留 IPv4"的旧语义归空、回落到内网 RemoteAddr，导致全部 IP 机制
// 拿不到真实客户端。现行为：trim、剥 [..] 括号、剥点分四段的 ":port"、剥
// "::ffff:" 映射前缀后，IPv4/映射地址返回点分四段，纯 IPv6 返回规范压缩形式；
// 无法解析的输入仍归空。
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
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	if with4 := parsed.To4(); with4 != nil {
		return with4.String()
	}
	return parsed.String()
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
