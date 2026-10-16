package gatewayusage

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/huanminabc/juhe-ai/backend-go-platform/prommetrics"
)

// Prometheus HTTP / gateway metrics family, migrated from the line-by-line
// port of Node 归档 shared/prometheus-metrics.ts
// (archive moved out of the repo; retrievable from git history) onto the
// shared platform prommetrics surface (github.com/prometheus/client_golang
// behind the platform wrapper). Metric names and label sets stay frozen —
// including the fixed service="juhe-ai" label the former renderer merged
// into every series; exposition rendering and the standard go_*/process_*
// families now come from client_golang, and the Node-compat juhe_ai_process_*,
// juhe_ai_redis_stream_queue_enabled and juhe_ai_http_failure_scope_metrics_enabled
// families are gone.
//
// Labels stay bounded (route group / method / status class / outcome /
// failure scope); no paths, identifiers or error text are ever attached.
//
// D-190 (BUG-0175 wave 4 W4-E): the classifier (classifier.go) and the
// UpstreamFailureMetricRecorder port (ports.go:185-189) are wired here into
// the registry the /__aisys__/metrics endpoint serves.

const prometheusServiceName = "juhe-ai"

// prometheusServiceLabels pins the fixed service label every gateway metric
// family carries (the former renderPrometheusLabels merge).
var prometheusServiceLabels = map[string]string{"service": prometheusServiceName}

// Duration buckets mirror durationBuckets (seconds). client_golang appends
// the +Inf bucket itself.
var prometheusDurationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10}

// First-output buckets mirror firstOutputDurationBuckets (seconds).
var prometheusFirstOutputBuckets = []float64{0.25, 0.5, 1, 2, 5, 10, 30, 60}

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

// prometheusMetrics owns one registry and its registered families. The
// process singleton is prometheusDefault; tests build isolated instances
// (client_golang counters cannot be reset, so per-test construction replaces
// the former global reset seam).
type prometheusMetrics struct {
	registry         *prommetrics.Registry
	requestsTotal    prommetrics.CounterVec
	requestDuration  prommetrics.HistogramVec
	inFlight         prommetrics.GaugeVec
	upstreamFailures prommetrics.CounterVec
	firstOutput      prommetrics.HistogramVec

	auditDropOnce     sync.Once
	auditDropSlot     atomic.Pointer[func() int64]
	operationDropOnce sync.Once
	operationDropSlot atomic.Pointer[func() int64]
	dbPoolOnce        sync.Once
	dbPoolSlot        atomic.Pointer[func() sql.DBStats]
}

func newPrometheusMetrics() *prometheusMetrics {
	registry := prommetrics.NewRegistry()
	metrics := &prometheusMetrics{
		registry:         registry,
		requestsTotal:    prommetrics.NewCounterVec(registry, "juhe_ai_http_requests_total", "Completed HTTP requests grouped without paths, identifiers, or error text.", prometheusServiceLabels, []string{"route_group", "method", "status_class", "outcome", "failure_scope"}),
		requestDuration:  prommetrics.NewHistogramVec(registry, "juhe_ai_http_request_duration_seconds", "HTTP request duration grouped without paths, identifiers, or error text.", prometheusServiceLabels, []string{"route_group", "method", "status_class", "outcome", "failure_scope"}, prometheusDurationBuckets),
		inFlight:         prommetrics.NewGaugeVec(registry, "juhe_ai_http_requests_in_flight", "In-flight HTTP requests grouped without paths or identifiers.", prometheusServiceLabels, []string{"route_group", "method"}),
		upstreamFailures: prommetrics.NewCounterVec(registry, "juhe_ai_gateway_upstream_failures_total", "Gateway upstream attempt failures grouped by bounded failure, reason, and status classes.", prometheusServiceLabels, []string{"failure_class", "reason_class", "status_class"}),
		firstOutput:      prommetrics.NewHistogramVec(registry, "juhe_ai_gateway_first_output_duration_seconds", "Time to first upstream output for gateway requests, excluding full stream duration.", prometheusServiceLabels, []string{"method"}, prometheusFirstOutputBuckets),
	}
	// The upstream-failure capability gauges keep the D-190 contract
	// declaration series (client_golang gauges stay resident at their set
	// value). service stays a dynamic label here (one child per service).
	prommetrics.NewGaugeVec(registry, "juhe_ai_gateway_upstream_failure_metrics_enabled", "Whether this process exposes the gateway upstream failure metric contract.", nil, []string{"service"}).WithLabelValues(prometheusServiceName).Set(1)
	prommetrics.NewGaugeVec(registry, "juhe_ai_gateway_upstream_failure_reason_metrics_enabled", "Whether this process exposes bounded gateway upstream failure reason classes.", nil, []string{"service"}).WithLabelValues(prometheusServiceName).Set(1)
	return metrics
}

var prometheusDefault = newPrometheusMetrics()

// MetricsHandler returns the exposition handler for the process registry
// (promhttp rendering). The composition root mounts it on the health
// listener's /__aisys__/metrics route.
func MetricsHandler() http.Handler {
	return prometheusDefault.registry.Handler()
}

// HTTPMetrics is the process-wide registry handle. It implements
// UpstreamFailureMetricRecorder so the composition root can wire
// Service.WithMetrics(HTTPMetrics{}).
type HTTPMetrics struct{}

// RecordUpstreamFailure implements UpstreamFailureMetricRecorder.
func (HTTPMetrics) RecordUpstreamFailure(failureClass string, statusCode *int, reasonClass string) {
	RecordGatewayUpstreamFailureMetric(failureClass, statusCode, reasonClass)
}

// recordUpstreamFailureOn drives an isolated metrics instance (tests bind a
// dedicated registry instead of resetting the process singleton).
func (HTTPMetrics) recordUpstreamFailureOn(metrics *prometheusMetrics, failureClass string, statusCode *int, reasonClass string) {
	metrics.recordUpstreamFailure(failureClass, statusCode, reasonClass)
}

// startRequest mirrors startHttpMetricRequest: observability routes are not
// measured and a nil request is the accepted no-op shape.
func (m *prometheusMetrics) startRequest(path string, method string, startedAtMs int64) *HTTPMetricRequest {
	routeGroup := ClassifyHTTPMetricRoute(path)
	if routeGroup == HTTPMetricRouteObservability {
		return nil
	}
	normalized := normalizeHTTPMetricMethod(method)
	m.inFlight.WithLabelValues(routeGroup, normalized).Inc()
	return &HTTPMetricRequest{RouteGroup: routeGroup, Method: normalized, StartedAtMs: startedAtMs}
}

// finishRequest mirrors finishHttpMetricRequest. A nil or already finished
// request is a no-op, so the deferred call stays single-shot.
func (m *prometheusMetrics) finishRequest(request *HTTPMetricRequest, statusCode *int, outcome HTTPMetricOutcome, finishedAtMs int64, failureScope HTTPMetricFailureScope) {
	if request == nil || request.Finished {
		return
	}
	request.Finished = true
	statusClass := classifyHTTPMetricStatus(statusCode)
	failureScopeValue := classifyHTTPMetricFailureScope(statusCode, outcome, failureScope)
	durationSeconds := float64(maxInt64(0, finishedAtMs-request.StartedAtMs)) / 1_000
	m.inFlight.WithLabelValues(request.RouteGroup, request.Method).Dec()
	labels := []string{request.RouteGroup, request.Method, statusClass, outcome, failureScopeValue}
	m.requestsTotal.WithLabelValues(labels...).Inc()
	m.requestDuration.WithLabelValues(labels...).Observe(durationSeconds)
}

// recordUpstreamFailure mirrors recordGatewayUpstreamFailureMetric.
func (m *prometheusMetrics) recordUpstreamFailure(failureClass string, statusCode *int, reasonClass string) {
	m.upstreamFailures.WithLabelValues(failureClass, reasonClass, classifyHTTPMetricStatus(statusCode)).Inc()
}

// recordFirstOutput mirrors recordGatewayFirstOutputMetric.
func (m *prometheusMetrics) recordFirstOutput(durationMs int64, method string) {
	if durationMs < 0 {
		return
	}
	m.firstOutput.WithLabelValues(normalizeHTTPMetricMethod(method)).Observe(float64(durationMs) / 1_000)
}

// gatherText renders the instance registry in the classic exposition format
// (tests make content-level assertions through it; a gather error surfaces as
// the rendered text so content assertions fail with the cause attached).
func (m *prometheusMetrics) gatherText() string {
	text, err := m.registry.GatherText()
	if err != nil {
		return "<prometheus gather error: " + err.Error() + ">"
	}
	return text
}

// StartHTTPMetricRequest mirrors startHttpMetricRequest: observability routes
// are not measured and a nil request is the accepted no-op shape.
func StartHTTPMetricRequest(path string, method string, startedAtMs int64) *HTTPMetricRequest {
	return prometheusDefault.startRequest(path, method, startedAtMs)
}

// FinishHTTPMetricRequest mirrors finishHttpMetricRequest. A nil or already
// finished request is a no-op, so the deferred call stays single-shot.
func FinishHTTPMetricRequest(request *HTTPMetricRequest, statusCode *int, outcome HTTPMetricOutcome, finishedAtMs int64, failureScope HTTPMetricFailureScope) {
	prometheusDefault.finishRequest(request, statusCode, outcome, finishedAtMs, failureScope)
}

// RecordGatewayUpstreamFailureMetric mirrors recordGatewayUpstreamFailureMetric.
func RecordGatewayUpstreamFailureMetric(failureClass string, statusCode *int, reasonClass string) {
	prometheusDefault.recordUpstreamFailure(failureClass, statusCode, reasonClass)
}

// RecordGatewayFirstOutputMetric mirrors recordGatewayFirstOutputMetric.
func RecordGatewayFirstOutputMetric(durationMs int64, method string) {
	prometheusDefault.recordFirstOutput(durationMs, method)
}

// SetAuditCapturedDroppedTotal wires the F3 audit producer drop counter into
// the exposition as a scrape-time CounterFunc. Call once during composition,
// before the HTTP server starts; the first call registers the family (an
// unset accessor keeps the family absent exactly like the former render-side
// branch).
func SetAuditCapturedDroppedTotal(fn func() int64) {
	prometheusDefault.setAuditCapturedDroppedTotal(fn)
}

// setAuditCapturedDroppedTotal is the per-instance seam (tests bind isolated
// instances; the package-level entry drives the process singleton).
func (m *prometheusMetrics) setAuditCapturedDroppedTotal(fn func() int64) {
	m.setDroppedTotalFunc(&m.auditDropOnce, &m.auditDropSlot,
		"juhe_ai_audit_log_captured_dropped_total",
		"Audit captures dropped because the bounded F3 producer queue was full.", fn)
}

// SetOperationLogDroppedTotal wires the F4 operation-log producer drop counter
// into the exposition as a scrape-time CounterFunc. Call once during
// composition, before the HTTP server starts (mirror of the F3 audit seam).
func SetOperationLogDroppedTotal(fn func() int64) {
	prometheusDefault.setOperationLogDroppedTotal(fn)
}

// setOperationLogDroppedTotal is the per-instance seam (mirror of the F3
// audit instance seam).
func (m *prometheusMetrics) setOperationLogDroppedTotal(fn func() int64) {
	m.setDroppedTotalFunc(&m.operationDropOnce, &m.operationDropSlot,
		"juhe_ai_operation_log_dropped_total",
		"Operation log records dropped because the bounded F4 producer queue was full.", fn)
}

// setDroppedTotalFunc stores the accessor and lazily registers the scrape-time
// counter exactly once per family (client_golang registrations are permanent;
// later calls only swap the accessor, mirroring the former var override).
func (m *prometheusMetrics) setDroppedTotalFunc(once *sync.Once, slot *atomic.Pointer[func() int64], name, help string, fn func() int64) {
	slot.Store(&fn)
	once.Do(func() {
		prommetrics.NewCounterFunc(m.registry, name, help, prometheusServiceLabels, func() float64 {
			if accessor := slot.Load(); accessor != nil && *accessor != nil {
				return float64((*accessor)())
			}
			return 0
		})
	})
}

// SetDBPoolStats wires the main business PostgreSQL pool stats into the
// exposition as six fixed, label-free series read at scrape time (four
// instantaneous gauges plus the two monotonic WaitCount/WaitDuration
// accumulators, which must stay counters). Call once during composition,
// before the HTTP server starts; composition roots without a managed pool
// (SQLite mode) skip the call and the family stays absent. Later calls only
// swap the provider (client_golang registrations are permanent).
func SetDBPoolStats(provider func() sql.DBStats) {
	prometheusDefault.setDBPoolStats(provider)
}

// setDBPoolStats is the per-instance seam (tests bind isolated instances; the
// package-level entry drives the process singleton).
func (m *prometheusMetrics) setDBPoolStats(provider func() sql.DBStats) {
	m.dbPoolSlot.Store(&provider)
	m.dbPoolOnce.Do(func() {
		read := func() (sql.DBStats, bool) {
			if accessor := m.dbPoolSlot.Load(); accessor != nil && *accessor != nil {
				return (*accessor)(), true
			}
			return sql.DBStats{}, false
		}
		gauge := func(name, help string, value func(sql.DBStats) float64) {
			prommetrics.NewGaugeFunc(m.registry, name, help, nil, func() float64 {
				if stats, ok := read(); ok {
					return value(stats)
				}
				return 0
			})
		}
		gauge("juhe_ai_gateway_db_pool_open_connections", "Open connections in the main business PostgreSQL pool.", func(stats sql.DBStats) float64 { return float64(stats.OpenConnections) })
		gauge("juhe_ai_gateway_db_pool_in_use_connections", "Connections in use in the main business PostgreSQL pool.", func(stats sql.DBStats) float64 { return float64(stats.InUse) })
		gauge("juhe_ai_gateway_db_pool_idle_connections", "Idle connections in the main business PostgreSQL pool.", func(stats sql.DBStats) float64 { return float64(stats.Idle) })
		gauge("juhe_ai_gateway_db_pool_max_open_connections", "Max open connections allowed in the main business PostgreSQL pool.", func(stats sql.DBStats) float64 { return float64(stats.MaxOpenConnections) })
		prommetrics.NewCounterFunc(m.registry, "juhe_ai_gateway_db_pool_wait_count_total", "Cumulative count of waits for a free main business PostgreSQL pool connection.", nil, func() float64 {
			if stats, ok := read(); ok {
				return float64(stats.WaitCount)
			}
			return 0
		})
		prommetrics.NewCounterFunc(m.registry, "juhe_ai_gateway_db_pool_wait_duration_seconds_total", "Cumulative time spent waiting for a free main business PostgreSQL pool connection.", nil, func() float64 {
			if stats, ok := read(); ok {
				return stats.WaitDuration.Seconds()
			}
			return 0
		})
	})
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

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
