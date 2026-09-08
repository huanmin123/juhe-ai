package kernel

import (
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
)

// Request lifecycle observability for the kernel boundary (BUG-0175 D-190 /
// D-191): the prometheus HTTP metric start/finish hooks and the
// http_request_started / http_request_completed / http_request_closed /
// gateway.request.timing_summary event stream the runtime-log grep face
// (logreads runtime_grep.go) parses. The sink and the metric hooks are
// injected by the composition root; without wiring the middleware degrades to
// the plain request-context contract (no events, no metrics), mirroring the
// Go default of observable degradation instead of a hard dependency.

// HTTPMetricHooks carries the prometheus HTTP metric seam the middleware
// drives. Start mirrors startHttpMetricRequest(path, method, startedAtMs);
// Finish mirrors finishHttpMetricRequest(request, statusCode, outcome,
// finishedAtMs, failureScope). The *int status keeps the Node
// `statusCode: number | undefined` shape (nil = unknown).
type HTTPMetricHooks struct {
	Start  func(path string, method string, startedAtMs int64) any
	Finish func(request any, statusCode *int, outcome string, finishedAtMs int64, failureScope string)
}

var httpMetricHooks atomic.Pointer[HTTPMetricHooks]

// SetHTTPMetricHooks wires the prometheus hooks (composition root; nil
// restores the no-metric default).
func SetHTTPMetricHooks(hooks *HTTPMetricHooks) {
	httpMetricHooks.Store(hooks)
}

// RequestEventSink consumes one structured request lifecycle log line. The
// composition root adapts slog; fields must render as top-level JSON keys so
// runtimeLogFieldsFromLine (logreads) sees event/traceId/level verbatim.
type RequestEventSink interface {
	EmitRequestEvent(level string, fields map[string]any, message string)
}

var requestEventSink atomic.Pointer[RequestEventSink]

// SetRequestEventSink wires the event sink (composition root; nil disables
// event emission). A nil argument clears the stored slot entirely so
// emitRequestEvent's pointer check stays the single nil gate.
func SetRequestEventSink(sink RequestEventSink) {
	if sink == nil {
		requestEventSink.Store(nil)
		return
	}
	requestEventSink.Store(&sink)
}

// slogRequestEventSink adapts *slog.Logger: attributes become top-level JSON
// keys under the JSON handler, matching the Node pino record shape the
// runtime-log reader parses.
type slogRequestEventSink struct{ logger *slog.Logger }

// NewSlogRequestEventSink builds the composition-root sink over slog.
func NewSlogRequestEventSink(logger *slog.Logger) RequestEventSink {
	if logger == nil {
		logger = slog.Default()
	}
	return slogRequestEventSink{logger: logger}
}

func (s slogRequestEventSink) EmitRequestEvent(level string, fields map[string]any, message string) {
	args := make([]any, 0, len(fields)*2)
	for key, value := range fields {
		args = append(args, key, value)
	}
	switch level {
	case "debug":
		s.logger.Debug(message, args...)
	case "warn":
		s.logger.Warn(message, args...)
	case "error":
		s.logger.Error(message, args...)
	default:
		s.logger.Info(message, args...)
	}
}

// emitRequestEvent is the nil-tolerant sink invocation helper.
func emitRequestEvent(level string, fields map[string]any, message string) {
	if sink := requestEventSink.Load(); sink != nil {
		(*sink).EmitRequestEvent(level, fields, message)
	}
}

// statusTrackingWriter records the response status the way res.statusCode
// answers in Node, so the completed/closed events and the metric finish carry
// the actual status even though the kernel writer chain re-wraps the
// ResponseWriter below this middleware.
type statusTrackingWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusTrackingWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusTrackingWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusTrackingWriter) statusPointer() *int {
	if !w.wroteHeader {
		return nil
	}
	status := w.status
	return &status
}

// Flush forwards the flusher capability so SSE handlers below keep streaming.
func (w *statusTrackingWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// httpMetricFailureScope mirrors the kernel-side default of
// logRequestFinished (request-context.ts:482-483): explicit upstream scope
// marking rides the response layer; the middleware default is gateway for 5xx
// and none otherwise.
func httpMetricFailureScope(statusCode *int, outcome string) string {
	if outcome == "completed" && statusCode != nil && *statusCode >= 500 && *statusCode <= 599 {
		return "gateway"
	}
	return "none"
}

// isHealthRequestPath mirrors isHealthPath (request-context.ts:733): health
// completions demote to debug so probes do not flood the runtime log.
func isHealthRequestPath(path string) bool {
	return path == "/__aisys__/health" || path == "/__aisys__/api/health"
}

// resolveRequestSummaryOutcome mirrors resolveRequestSummaryOutcome
// (request-context.ts:675) over the fields the kernel observes. The kernel
// sees no stage summaries, so the unexpected_failure branch (a stage outcome)
// is unreachable here and the status classes decide.
func resolveRequestSummaryOutcome(statusCode *int) string {
	if statusCode != nil && *statusCode >= 500 {
		return "unexpected_failure"
	}
	if statusCode != nil && *statusCode >= 400 {
		return "expected_failure"
	}
	return "success"
}

// emitHTTPRequestStarted mirrors the requestContextMiddleware started log
// (request-context.ts:173-183).
func emitHTTPRequestStarted(context *RequestContext) {
	emitRequestEvent("info", map[string]any{
		"event":       "http_request_started",
		"version":     1, // LOG_EVENT_VERSION
		"service":     "juhe-ai",
		"role":        "gateway",
		"traceId":     context.TraceID,
		"requestId":   context.RequestID,
		"method":      context.Method,
		"path":        context.Path,
		"originalUrl": context.OriginalURL,
		"clientIp":    context.ClientIP,
	}, "HTTP 请求开始")
}

// emitHTTPRequestCompleted mirrors logRequestFinished's completion record
// (request-context.ts:493-516) including the health-path debug demotion and
// the 4xx/5xx level policy.
func emitHTTPRequestCompleted(context *RequestContext, statusCode *int, failureScope string, durationMs int64) {
	level := "info"
	if statusCode != nil && *statusCode >= 500 && failureScope != "upstream" {
		level = "error"
	} else if statusCode != nil && *statusCode >= 400 {
		level = "warn"
	}
	if isHealthRequestPath(context.Path) && (statusCode == nil || *statusCode < 400) {
		level = "debug"
	}
	fields := map[string]any{
		"event":        "http_request_completed",
		"traceId":      context.TraceID,
		"requestId":    context.RequestID,
		"method":       context.Method,
		"path":         context.Path,
		"originalUrl":  context.OriginalURL,
		"failureScope": failureScope,
		"durationMs":   durationMs,
		"clientIp":     context.ClientIP,
	}
	if statusCode != nil {
		fields["statusCode"] = *statusCode
		fields["responseCommitted"] = true
	}
	emitRequestEvent(level, fields, "HTTP 请求已结束")
}

// emitHTTPRequestClosed mirrors logRequestClosed (request-context.ts:519-560)
// in the non-protocol-terminal branch: the downstream connection dropped
// before the response completed.
func emitHTTPRequestClosed(context *RequestContext, statusCode *int, durationMs int64) {
	fields := map[string]any{
		"event":           "http_request_closed",
		"traceId":         context.TraceID,
		"requestId":       context.RequestID,
		"method":          context.Method,
		"path":            context.Path,
		"originalUrl":     context.OriginalURL,
		"downstreamClose": true,
		"durationMs":      durationMs,
		"clientIp":        context.ClientIP,
	}
	if statusCode != nil {
		fields["statusCode"] = *statusCode
	}
	emitRequestEvent("warn", fields, "下游连接关闭")
}

// emitHTTPRequestTimingSummary mirrors logRequestTimingSummary
// (request-context.ts:568-622) over the kernel-observed fields. Stage
// summaries stay a response-layer concept (the /v1 chain logs stages through
// its own observability port), so stages render as the empty list — the same
// shape Node emits when timing detail is not sampled. Only gateway-route
// requests carry Node stage summaries, so the summary is gated to that route
// group exactly like the Node early-return for stage-less requests.
func emitHTTPRequestTimingSummary(context *RequestContext, statusCode *int, outcome string, totalDurationMs int64) {
	fields := map[string]any{
		"event":                 "gateway.request.timing_summary",
		"version":               1, // LOG_EVENT_VERSION
		"service":               "juhe-ai",
		"role":                  "gateway",
		"traceId":               context.TraceID,
		"requestId":             context.RequestID,
		"method":                context.Method,
		"path":                  context.Path,
		"outcome":               outcome,
		"attemptCount":          0,
		"totalDurationMs":       totalDurationMs,
		"durationMs":            totalDurationMs,
		"stageCount":            0,
		"timingLogDroppedCount": 0,
		"droppedStageSummaries": 0,
		"stageDetailsSampled":   false,
		"stages":                []any{},
	}
	if context.ClientIP != "" {
		fields["clientIp"] = context.ClientIP
	}
	if statusCode != nil {
		fields["statusCode"] = *statusCode
	}
	emitRequestEvent("info", fields, "网关请求耗时汇总："+
		strconv.FormatInt(totalDurationMs, 10)+"ms，0 次上游尝试，"+outcome)
}
