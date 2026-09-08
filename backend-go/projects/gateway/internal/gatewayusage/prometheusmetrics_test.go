package gatewayusage

import (
	"strings"
	"testing"
)

// Golden shapes locked against
// migration-backup/node/final-archive/backend/src/shared/prometheus-metrics.ts.

func TestClassifyHTTPMetricRouteGolden(t *testing.T) {
	golden := map[string]HTTPMetricRouteGroup{
		"/__aisys__/metrics":          HTTPMetricRouteObservability,
		"/health":                     HTTPMetricRouteHealth,
		"/__aisys__/health":           HTTPMetricRouteHealth,
		"/__aisys__/api/health":       HTTPMetricRouteHealth,
		"/__aipublic__":               HTTPMetricRoutePublicAPI,
		"/__aipublic__/v1/whatever":   HTTPMetricRoutePublicAPI,
		"/__aisys__":                  HTTPMetricRouteManagement,
		"/__aisys__/api/runtime-logs": HTTPMetricRouteManagement,
		"/":                           HTTPMetricRouteGateway,
		"/v1":                         HTTPMetricRouteGateway,
		"/v1/chat/completions":        HTTPMetricRouteGateway,
		"/vX/unknown":                 HTTPMetricRouteOther,
	}
	for path, want := range golden {
		if got := ClassifyHTTPMetricRoute(path); got != want {
			t.Errorf("ClassifyHTTPMetricRoute(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestHTTPMetricsRequestLifecycle(t *testing.T) {
	ResetPrometheusMetricsForTest()
	request := StartHTTPMetricRequest("/v1/chat/completions", "POST", 1_000)
	if request == nil {
		t.Fatal("gateway request must be measured")
	}
	render := RenderPrometheusMetrics()
	if !strings.Contains(render, "juhe_ai_http_requests_in_flight{method=\"POST\",route_group=\"gateway\",service=\"juhe-ai\"} 1") {
		t.Fatalf("in-flight gauge missing after start:\n%s", render)
	}
	FinishHTTPMetricRequest(request, intPtr(200), HTTPMetricOutcomeCompleted, 2_500, HTTPMetricFailureScopeNone)
	render = RenderPrometheusMetrics()
	for _, want := range []string{
		"juhe_ai_http_requests_total{failure_scope=\"none\",method=\"POST\",outcome=\"completed\",route_group=\"gateway\",service=\"juhe-ai\",status_class=\"2xx\"} 1",
		"juhe_ai_http_request_duration_seconds_bucket{failure_scope=\"none\",le=\"1\",method=\"POST\",outcome=\"completed\",route_group=\"gateway\",service=\"juhe-ai\",status_class=\"2xx\"} 0",
		"juhe_ai_http_request_duration_seconds_bucket{failure_scope=\"none\",le=\"2\",method=\"POST\",outcome=\"completed\",route_group=\"gateway\",service=\"juhe-ai\",status_class=\"2xx\"} 1",
		"juhe_ai_http_request_duration_seconds_bucket{failure_scope=\"none\",le=\"+Inf\",method=\"POST\",outcome=\"completed\",route_group=\"gateway\",service=\"juhe-ai\",status_class=\"2xx\"} 1",
		"juhe_ai_http_request_duration_seconds_sum{failure_scope=\"none\",method=\"POST\",outcome=\"completed\",route_group=\"gateway\",service=\"juhe-ai\",status_class=\"2xx\"} 1.5",
		"juhe_ai_http_request_duration_seconds_count{failure_scope=\"none\",method=\"POST\",outcome=\"completed\",route_group=\"gateway\",service=\"juhe-ai\",status_class=\"2xx\"} 1",
	} {
		if !strings.Contains(render, want) {
			t.Fatalf("render missing %q:\n%s", want, render)
		}
	}
	// The in-flight entry drains at zero (Node decrement deletes the key).
	if strings.Contains(render, "juhe_ai_http_requests_in_flight{") {
		t.Fatalf("in-flight gauge must drain after finish:\n%s", render)
	}
	// Double finish stays single-shot.
	FinishHTTPMetricRequest(request, intPtr(500), HTTPMetricOutcomeCompleted, 3_000, HTTPMetricFailureScopeNone)
	if !strings.Contains(RenderPrometheusMetrics(), "juhe_ai_http_requests_total{failure_scope=\"none\",method=\"POST\",outcome=\"completed\",route_group=\"gateway\",service=\"juhe-ai\",status_class=\"2xx\"} 1") {
		t.Fatal("double finish must not double count")
	}
}

func TestHTTPMetricsFiveHundredDefaultsToGatewayScope(t *testing.T) {
	ResetPrometheusMetricsForTest()
	request := StartHTTPMetricRequest("/v1/models", "GET", 0)
	FinishHTTPMetricRequest(request, intPtr(503), HTTPMetricOutcomeCompleted, 10, HTTPMetricFailureScopeNone)
	render := RenderPrometheusMetrics()
	if !strings.Contains(render, "juhe_ai_http_requests_total{failure_scope=\"gateway\",method=\"GET\",outcome=\"completed\",route_group=\"gateway\",service=\"juhe-ai\",status_class=\"5xx\"} 1") {
		t.Fatalf("5xx must default to gateway failure scope:\n%s", render)
	}
}

func TestHTTPMetricsAbortKeepsScopeNone(t *testing.T) {
	ResetPrometheusMetricsForTest()
	request := StartHTTPMetricRequest("/v1/chat/completions", "POST", 0)
	FinishHTTPMetricRequest(request, nil, HTTPMetricOutcomeAborted, 5, HTTPMetricFailureScopeNone)
	render := RenderPrometheusMetrics()
	if !strings.Contains(render, "juhe_ai_http_requests_total{failure_scope=\"none\",method=\"POST\",outcome=\"aborted\",route_group=\"gateway\",service=\"juhe-ai\",status_class=\"unknown\"} 1") {
		t.Fatalf("abort contract mismatch:\n%s", render)
	}
}

func TestHTTPMetricsObservabilityRouteNotMeasured(t *testing.T) {
	ResetPrometheusMetricsForTest()
	if request := StartHTTPMetricRequest("/__aisys__/metrics", "GET", 0); request != nil {
		t.Fatal("observability route must not be measured")
	}
	if strings.Contains(RenderPrometheusMetrics(), "juhe_ai_http_requests_in_flight{") {
		t.Fatal("observability route leaked into the gauges")
	}
}

func TestRecordGatewayUpstreamFailureMetric(t *testing.T) {
	ResetPrometheusMetricsForTest()
	HTTPMetrics{}.RecordUpstreamFailure(FailureClassTransport, intPtr(429), MetricReasonRateLimit)
	HTTPMetrics{}.RecordUpstreamFailure(FailureClassTransport, nil, MetricReasonTimeout)
	render := RenderPrometheusMetrics()
	for _, want := range []string{
		"juhe_ai_gateway_upstream_failures_total{failure_class=\"transport\",reason_class=\"rate_limit\",service=\"juhe-ai\",status_class=\"4xx\"} 1",
		"juhe_ai_gateway_upstream_failures_total{failure_class=\"transport\",reason_class=\"timeout\",service=\"juhe-ai\",status_class=\"unknown\"} 1",
		"juhe_ai_gateway_upstream_failure_metrics_enabled{service=\"juhe-ai\"} 1",
		"juhe_ai_http_failure_scope_metrics_enabled{service=\"juhe-ai\"} 1",
	} {
		if !strings.Contains(render, want) {
			t.Fatalf("render missing %q:\n%s", want, render)
		}
	}
}

func TestRecordGatewayFirstOutputMetric(t *testing.T) {
	ResetPrometheusMetricsForTest()
	RecordGatewayFirstOutputMetric(2_500, "POST")
	RecordGatewayFirstOutputMetric(-1, "POST") // invalid, dropped
	render := RenderPrometheusMetrics()
	for _, want := range []string{
		"juhe_ai_gateway_first_output_duration_seconds_bucket{le=\"5\",method=\"POST\",service=\"juhe-ai\"} 1",
		"juhe_ai_gateway_first_output_duration_seconds_count{method=\"POST\",service=\"juhe-ai\"} 1",
	} {
		if !strings.Contains(render, want) {
			t.Fatalf("render missing %q:\n%s", want, render)
		}
	}
}

func TestNormalizeHTTPMetricMethodGolden(t *testing.T) {
	golden := map[string]string{
		"get": "GET", "POST": "POST", "PATCH": "PATCH", "TRACE": "OTHER", "": "OTHER",
	}
	for input, want := range golden {
		if got := normalizeHTTPMetricMethod(input); got != want {
			t.Errorf("normalizeHTTPMetricMethod(%q) = %q, want %q", input, got, want)
		}
	}
}

func intPtr(value int) *int { return &value }
