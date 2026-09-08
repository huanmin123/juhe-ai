package gatewayusage

import (
	"fmt"
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Prometheus HTTP / gateway metrics family, line-by-line port of
// migration-backup/node/final-archive/backend/src/shared/prometheus-metrics.ts.
// Labels stay bounded (route group / method / status class / outcome /
// failure scope); no paths, identifiers or error text are ever attached.
//
// D-190 (BUG-0175 wave 4 W4-E): the classifier (classifier.go) and the
// UpstreamFailureMetricRecorder port (ports.go:185-189) existed without a
// production registry; this file supplies the registry and the render surface
// the /__aisys__/metrics endpoint serves.

const prometheusServiceName = "juhe-ai"

// Duration buckets mirror durationBuckets (seconds).
var prometheusDurationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, positiveInf}

// First-output buckets mirror firstOutputDurationBuckets (seconds).
var prometheusFirstOutputBuckets = []float64{0.25, 0.5, 1, 2, 5, 10, 30, 60, positiveInf}

const positiveInf = float64(9223372036854775807) // renders as "+Inf" via prometheusBucketLE

// HTTPMetricRouteGroup mirrors HttpMetricRouteGroup.
type HTTPMetricRouteGroup = string

// Route group values.
const (
	HTTPMetricRouteHealth        HTTPMetricRouteGroup = "health"
	HTTPMetricRouteManagement    HTTPMetricRouteGroup = "management"
	HTTPMetricRoutePublicAPI     HTTPMetricRouteGroup = "public_api"
	HTTPMetricRouteGateway       HTTPMetricRouteGroup = "gateway"
	HTTPMetricRouteOther         HTTPMetricRouteGroup = "other"
	HTTPMetricRouteObservability HTTPMetricRouteGroup = "observability"
)

// HTTPMetricOutcome mirrors HttpMetricOutcome.
type HTTPMetricOutcome = string

// Outcome values.
const (
	HTTPMetricOutcomeCompleted HTTPMetricOutcome = "completed"
	HTTPMetricOutcomeAborted   HTTPMetricOutcome = "aborted"
)

// HTTPMetricFailureScope mirrors HttpMetricFailureScope.
type HTTPMetricFailureScope = string

// Failure scope values.
const (
	HTTPMetricFailureScopeNone     HTTPMetricFailureScope = "none"
	HTTPMetricFailureScopeGateway  HTTPMetricFailureScope = "gateway"
	HTTPMetricFailureScopeUpstream HTTPMetricFailureScope = "upstream"
)

// HTTPMetricRequest mirrors HttpMetricRequest.
type HTTPMetricRequest struct {
	RouteGroup  HTTPMetricRouteGroup
	Method      string
	StartedAtMs int64
	Finished    bool
}

// ClassifyHTTPMetricRoute mirrors classifyHttpMetricRoute.
func ClassifyHTTPMetricRoute(path string) HTTPMetricRouteGroup {
	if path == "/__aisys__/metrics" {
		return HTTPMetricRouteObservability
	}
	if path == "/health" || path == "/__aisys__/health" || path == "/__aisys__/api/health" {
		return HTTPMetricRouteHealth
	}
	if path == "/__aipublic__" || strings.HasPrefix(path, "/__aipublic__/") {
		return HTTPMetricRoutePublicAPI
	}
	if path == "/__aisys__" || strings.HasPrefix(path, "/__aisys__/") {
		return HTTPMetricRouteManagement
	}
	if path == "/" || path == "/v1" || strings.HasPrefix(path, "/v1/") {
		return HTTPMetricRouteGateway
	}
	return HTTPMetricRouteOther
}

// prometheusRegistry holds the process-wide metric state. Node used module
// singletons; Go keeps one shared registry guarded by a single mutex (the
// critical sections are map increments on the request path).
type prometheusRegistry struct {
	mu                sync.Mutex
	startedAt         time.Time
	requestCounters   map[string]int64
	requestHistograms map[string]*prometheusHistogram
	firstOutput       map[string]*prometheusHistogram
	inFlight          map[string]int64
	upstreamFailures  map[string]int64
}

type prometheusHistogram struct {
	count   int64
	sum     float64
	buckets map[float64]int64
}

func newPrometheusHistogram() *prometheusHistogram {
	return &prometheusHistogram{buckets: map[float64]int64{}}
}

var prometheusDefault = &prometheusRegistry{
	startedAt:         time.Now(),
	requestCounters:   map[string]int64{},
	requestHistograms: map[string]*prometheusHistogram{},
	firstOutput:       map[string]*prometheusHistogram{},
	inFlight:          map[string]int64{},
	upstreamFailures:  map[string]int64{},
}

// HTTPMetrics is the process-wide registry handle. It implements
// UpstreamFailureMetricRecorder so the composition root can wire
// Service.WithMetrics(HTTPMetrics{}).
type HTTPMetrics struct{}

// RecordUpstreamFailure implements UpstreamFailureMetricRecorder.
func (HTTPMetrics) RecordUpstreamFailure(failureClass string, statusCode *int, reasonClass string) {
	RecordGatewayUpstreamFailureMetric(failureClass, statusCode, reasonClass)
}

// StartHTTPMetricRequest mirrors startHttpMetricRequest: observability routes
// are not measured and a nil request is the accepted no-op shape.
func StartHTTPMetricRequest(path string, method string, startedAtMs int64) *HTTPMetricRequest {
	routeGroup := ClassifyHTTPMetricRoute(path)
	if routeGroup == HTTPMetricRouteObservability {
		return nil
	}
	normalized := normalizeHTTPMetricMethod(method)
	key := httpMetricLabelsKey(map[string]string{
		"route_group": routeGroup,
		"method":      normalized,
	})
	prometheusDefault.mu.Lock()
	prometheusDefault.inFlight[key]++
	prometheusDefault.mu.Unlock()
	return &HTTPMetricRequest{RouteGroup: routeGroup, Method: normalized, StartedAtMs: startedAtMs}
}

// FinishHTTPMetricRequest mirrors finishHttpMetricRequest. A nil or already
// finished request is a no-op, so the deferred call stays single-shot.
func FinishHTTPMetricRequest(request *HTTPMetricRequest, statusCode *int, outcome HTTPMetricOutcome, finishedAtMs int64, failureScope HTTPMetricFailureScope) {
	if request == nil || request.Finished {
		return
	}
	request.Finished = true
	labels := map[string]string{
		"route_group":   request.RouteGroup,
		"method":        request.Method,
		"status_class":  classifyHTTPMetricStatus(statusCode),
		"outcome":       outcome,
		"failure_scope": classifyHTTPMetricFailureScope(statusCode, outcome, failureScope),
	}
	key := httpMetricLabelsKey(labels)
	durationSeconds := float64(maxInt64(0, finishedAtMs-request.StartedAtMs)) / 1_000
	prometheusDefault.mu.Lock()
	prometheusDefault.inFlight[keyInFlightOf(request)]--
	if prometheusDefault.inFlight[keyInFlightOf(request)] <= 0 {
		delete(prometheusDefault.inFlight, keyInFlightOf(request))
	}
	prometheusDefault.requestCounters[key]++
	histogram := prometheusDefault.requestHistograms[key]
	if histogram == nil {
		histogram = newPrometheusHistogram()
		prometheusDefault.requestHistograms[key] = histogram
	}
	histogram.count++
	histogram.sum += durationSeconds
	for _, bucket := range prometheusDurationBuckets {
		if durationSeconds <= bucket {
			histogram.buckets[bucket]++
		}
	}
	prometheusDefault.mu.Unlock()
}

func keyInFlightOf(request *HTTPMetricRequest) string {
	return httpMetricLabelsKey(map[string]string{
		"route_group": request.RouteGroup,
		"method":      request.Method,
	})
}

// RecordGatewayUpstreamFailureMetric mirrors recordGatewayUpstreamFailureMetric.
func RecordGatewayUpstreamFailureMetric(failureClass string, statusCode *int, reasonClass string) {
	key := httpMetricLabelsKey(map[string]string{
		"failure_class": failureClass,
		"reason_class":  reasonClass,
		"status_class":  classifyHTTPMetricStatus(statusCode),
	})
	prometheusDefault.mu.Lock()
	prometheusDefault.upstreamFailures[key]++
	prometheusDefault.mu.Unlock()
}

// RecordGatewayFirstOutputMetric mirrors recordGatewayFirstOutputMetric.
func RecordGatewayFirstOutputMetric(durationMs int64, method string) {
	if durationMs < 0 {
		return
	}
	key := httpMetricLabelsKey(map[string]string{"method": normalizeHTTPMetricMethod(method)})
	durationSeconds := float64(durationMs) / 1_000
	prometheusDefault.mu.Lock()
	histogram := prometheusDefault.firstOutput[key]
	if histogram == nil {
		histogram = newPrometheusHistogram()
		prometheusDefault.firstOutput[key] = histogram
	}
	histogram.count++
	histogram.sum += durationSeconds
	for _, bucket := range prometheusFirstOutputBuckets {
		if durationSeconds <= bucket {
			histogram.buckets[bucket]++
		}
	}
	prometheusDefault.mu.Unlock()
}

// ResetPrometheusMetricsForTest mirrors resetPrometheusMetricsForTest.
func ResetPrometheusMetricsForTest() {
	prometheusDefault.mu.Lock()
	defer prometheusDefault.mu.Unlock()
	prometheusDefault.requestCounters = map[string]int64{}
	prometheusDefault.requestHistograms = map[string]*prometheusHistogram{}
	prometheusDefault.firstOutput = map[string]*prometheusHistogram{}
	prometheusDefault.inFlight = map[string]int64{}
	prometheusDefault.upstreamFailures = map[string]int64{}
}

// RenderPrometheusMetrics mirrors renderPrometheusMetrics: the exposition
// order and the HELP/TYPE lines follow the Node contract. The Redis Stream
// queue family renders the disabled gauge only — the Node queue monitor has
// no Go counterpart yet, and the Node default (snapshot unset) renders
// exactly this shape.
func RenderPrometheusMetrics() string {
	var builder strings.Builder
	renderPrometheusMetricsTo(&builder)
	return builder.String()
}

func renderPrometheusMetricsTo(w io.Writer) {
	prometheusDefault.mu.Lock()
	requestCounters := sortedIntEntries(prometheusDefault.requestCounters)
	requestHistograms := sortedHistogramEntries(prometheusDefault.requestHistograms)
	firstOutput := sortedHistogramEntries(prometheusDefault.firstOutput)
	inFlight := sortedIntEntries(prometheusDefault.inFlight)
	upstreamFailures := sortedIntEntries(prometheusDefault.upstreamFailures)
	prometheusDefault.mu.Unlock()

	write := func(format string, args ...any) {
		_, _ = fmt.Fprintf(w, format, args...)
	}
	write("# HELP juhe_ai_http_requests_total Completed HTTP requests grouped without paths, identifiers, or error text.\n")
	write("# TYPE juhe_ai_http_requests_total counter\n")
	for _, entry := range requestCounters {
		write("juhe_ai_http_requests_total{%s} %d\n", renderPrometheusLabels(parsePrometheusLabels(entry.key)), entry.value)
	}
	write("# HELP juhe_ai_http_failure_scope_metrics_enabled Whether this process exposes bounded HTTP failure scope labels.\n")
	write("# TYPE juhe_ai_http_failure_scope_metrics_enabled gauge\n")
	write("juhe_ai_http_failure_scope_metrics_enabled{service=%q} 1\n", prometheusServiceName)

	write("# HELP juhe_ai_gateway_upstream_failures_total Gateway upstream attempt failures grouped by bounded failure, reason, and status classes.\n")
	write("# TYPE juhe_ai_gateway_upstream_failures_total counter\n")
	for _, entry := range upstreamFailures {
		write("juhe_ai_gateway_upstream_failures_total{%s} %d\n", renderPrometheusLabels(parsePrometheusLabels(entry.key)), entry.value)
	}
	write("# HELP juhe_ai_gateway_upstream_failure_metrics_enabled Whether this process exposes the gateway upstream failure metric contract.\n")
	write("# TYPE juhe_ai_gateway_upstream_failure_metrics_enabled gauge\n")
	write("juhe_ai_gateway_upstream_failure_metrics_enabled{service=%q} 1\n", prometheusServiceName)
	write("# HELP juhe_ai_gateway_upstream_failure_reason_metrics_enabled Whether this process exposes bounded gateway upstream failure reason classes.\n")
	write("# TYPE juhe_ai_gateway_upstream_failure_reason_metrics_enabled gauge\n")
	write("juhe_ai_gateway_upstream_failure_reason_metrics_enabled{service=%q} 1\n", prometheusServiceName)

	write("# HELP juhe_ai_gateway_first_output_duration_seconds Time to first upstream output for gateway requests, excluding full stream duration.\n")
	write("# TYPE juhe_ai_gateway_first_output_duration_seconds histogram\n")
	for _, entry := range firstOutput {
		labels := parsePrometheusLabels(entry.key)
		for _, bucket := range prometheusFirstOutputBuckets {
			write("juhe_ai_gateway_first_output_duration_seconds_bucket{%s} %d\n",
				renderPrometheusLabelsWithLe(labels, bucket), entry.value.buckets[bucket])
		}
		write("juhe_ai_gateway_first_output_duration_seconds_sum{%s} %s\n", renderPrometheusLabels(labels), prometheusFloat(entry.value.sum))
		write("juhe_ai_gateway_first_output_duration_seconds_count{%s} %d\n", renderPrometheusLabels(labels), entry.value.count)
	}

	write("# HELP juhe_ai_http_request_duration_seconds HTTP request duration grouped without paths, identifiers, or error text.\n")
	write("# TYPE juhe_ai_http_request_duration_seconds histogram\n")
	for _, entry := range requestHistograms {
		labels := parsePrometheusLabels(entry.key)
		for _, bucket := range prometheusDurationBuckets {
			write("juhe_ai_http_request_duration_seconds_bucket{%s} %d\n",
				renderPrometheusLabelsWithLe(labels, bucket), entry.value.buckets[bucket])
		}
		write("juhe_ai_http_request_duration_seconds_sum{%s} %s\n", renderPrometheusLabels(labels), prometheusFloat(entry.value.sum))
		write("juhe_ai_http_request_duration_seconds_count{%s} %d\n", renderPrometheusLabels(labels), entry.value.count)
	}

	write("# HELP juhe_ai_http_requests_in_flight In-flight HTTP requests grouped without paths or identifiers.\n")
	write("# TYPE juhe_ai_http_requests_in_flight gauge\n")
	for _, entry := range inFlight {
		write("juhe_ai_http_requests_in_flight{%s} %d\n", renderPrometheusLabels(parsePrometheusLabels(entry.key)), entry.value)
	}

	// Node process gauges; Go approximates RSS with the runtime Sys figure
	// (memory reserved from the OS) and heapUsed with HeapAlloc.
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	write("# HELP juhe_ai_process_resident_memory_bytes Node process resident memory.\n")
	write("# TYPE juhe_ai_process_resident_memory_bytes gauge\n")
	write("juhe_ai_process_resident_memory_bytes{service=%q} %d\n", prometheusServiceName, memStats.Sys)
	write("# HELP juhe_ai_process_heap_used_bytes Node process heap currently used.\n")
	write("# TYPE juhe_ai_process_heap_used_bytes gauge\n")
	write("juhe_ai_process_heap_used_bytes{service=%q} %d\n", prometheusServiceName, memStats.HeapAlloc)
	write("# HELP juhe_ai_process_uptime_seconds Node process uptime.\n")
	write("# TYPE juhe_ai_process_uptime_seconds gauge\n")
	write("juhe_ai_process_uptime_seconds{service=%q} %s\n", prometheusServiceName, prometheusFloat(time.Since(prometheusDefault.startedAt).Seconds()))

	write("# HELP juhe_ai_redis_stream_queue_enabled Whether Redis Stream queue monitoring is enabled for this process.\n")
	write("# TYPE juhe_ai_redis_stream_queue_enabled gauge\n")
	write("juhe_ai_redis_stream_queue_enabled{service=%q} 0\n", prometheusServiceName)
}

// prometheusBucketLE renders the le label; +Inf mirrors Number.POSITIVE_INFINITY.
func prometheusBucketLE(bucket float64) string {
	if bucket == positiveInf {
		return "+Inf"
	}
	return strconv.FormatFloat(bucket, 'g', -1, 64)
}

// renderPrometheusLabelsWithLe mirrors renderLabels({ ...labels, le }): the le
// label joins the map before the alphabetical sort, not as a trailing entry.
func renderPrometheusLabelsWithLe(labels map[string]string, bucket float64) string {
	merged := make(map[string]string, len(labels)+1)
	for key, value := range labels {
		merged[key] = value
	}
	merged["le"] = prometheusBucketLE(bucket)
	return renderPrometheusLabels(merged)
}

// prometheusFloat renders sums like Node's default number interpolation.
func prometheusFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func normalizeHTTPMetricMethod(method string) string {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return strings.ToUpper(strings.TrimSpace(method))
	}
	return "OTHER"
}

func classifyHTTPMetricStatus(statusCode *int) string {
	if statusCode == nil {
		return "unknown"
	}
	status := *statusCode
	// Node: !Number.isInteger(statusCode) || !statusCode || <100 || >599.
	if status == 0 || status < 100 || status > 599 {
		return "unknown"
	}
	return strconv.Itoa(status/100) + "xx"
}

func classifyHTTPMetricFailureScope(statusCode *int, outcome HTTPMetricOutcome, failureScope HTTPMetricFailureScope) HTTPMetricFailureScope {
	if outcome == HTTPMetricOutcomeCompleted && classifyHTTPMetricStatus(statusCode) == "5xx" {
		if failureScope == HTTPMetricFailureScopeNone || failureScope == "" {
			return HTTPMetricFailureScopeGateway
		}
		return failureScope
	}
	return HTTPMetricFailureScopeNone
}

// httpMetricLabelsKey mirrors labelsKey: sorted "key=value" joined with '|'.
func httpMetricLabelsKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, "|")
}

func parsePrometheusLabels(key string) map[string]string {
	labels := map[string]string{}
	for _, entry := range strings.Split(key, "|") {
		if index := strings.IndexByte(entry, '='); index >= 0 {
			labels[entry[:index]] = entry[index+1:]
		}
	}
	return labels
}

// renderPrometheusLabels mirrors renderLabels: the service label joins first,
// entries sort by key and values escape backslash and quote.
func renderPrometheusLabels(labels map[string]string) string {
	merged := make(map[string]string, len(labels)+1)
	for key, value := range labels {
		merged[key] = value
	}
	merged["service"] = prometheusServiceName
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.ReplaceAll(merged[key], `\`, `\\`)
		value = strings.ReplaceAll(value, `"`, `\"`)
		parts = append(parts, key+"=\""+value+"\"")
	}
	return strings.Join(parts, ",")
}

type prometheusIntEntry struct {
	key   string
	value int64
}

func sortedIntEntries(source map[string]int64) []prometheusIntEntry {
	entries := make([]prometheusIntEntry, 0, len(source))
	for key, value := range source {
		entries = append(entries, prometheusIntEntry{key: key, value: value})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	return entries
}

type prometheusHistogramEntry struct {
	key   string
	value *prometheusHistogram
}

func sortedHistogramEntries(source map[string]*prometheusHistogram) []prometheusHistogramEntry {
	entries := make([]prometheusHistogramEntry, 0, len(source))
	for key, value := range source {
		entries = append(entries, prometheusHistogramEntry{key: key, value: value})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	return entries
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
