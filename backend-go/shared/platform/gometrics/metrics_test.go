package gometrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteExposesLowCardinalityRuntimeMetrics(t *testing.T) {
	c := New("juhe-ai", "jobs")
	snapshot := c.Snapshot()
	if snapshot.Service != "juhe-ai" || snapshot.Role != "jobs" || snapshot.ProcessPID <= 0 || snapshot.SampledAt.IsZero() {
		t.Fatalf("invalid runtime snapshot identity: %#v", snapshot)
	}
	if snapshot.RSSBytes != nil || snapshot.FDCount != nil {
		t.Fatalf("portable Go runtime snapshot must not depend on host RSS/FD APIs: %#v", snapshot)
	}
	var body strings.Builder
	if err := c.Write(&body); err != nil {
		t.Fatalf("write metrics: %v", err)
	}
	text := body.String()
	for _, want := range []string{
		`juhe_ai_go_build_info{service="juhe-ai",role="jobs"`,
		`juhe_ai_go_process_uptime_seconds{service="juhe-ai",role="jobs",runtimeKind="go"}`,
		`juhe_ai_go_heap_alloc_bytes{service="juhe-ai",role="jobs",runtimeKind="go"}`,
		`juhe_ai_go_goroutines{service="juhe-ai",role="jobs",runtimeKind="go"}`,
		`juhe_ai_go_scheduler_latency_seconds_bucket{service="juhe-ai",role="jobs",runtimeKind="go"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "eventLoopLagMs") || strings.Contains(text, "accountId") {
		t.Fatalf("runtime metrics leaked Node or high-cardinality fields: %s", text)
	}
	if snapshot.CPUSecondsTotal > 0 && !strings.Contains(text, `juhe_ai_go_runtime_cpu_seconds_total{service="juhe-ai",role="jobs",runtimeKind="go"}`) {
		t.Fatalf("portable runtime CPU counter missing from scrape: %s", text)
	}
}

func TestHandlerRouteAndMethod(t *testing.T) {
	collector := New("juhe-ai", "gateway")
	h := collector.Handler()
	badPath := httptest.NewRecorder()
	h.ServeHTTP(badPath, httptest.NewRequest(http.MethodGet, "/health", nil))
	if badPath.Code != http.StatusNotFound {
		t.Fatalf("unexpected bad path status=%d", badPath.Code)
	}
	badMethod := httptest.NewRecorder()
	h.ServeHTTP(badMethod, httptest.NewRequest(http.MethodPost, "/__aisys__/metrics", nil))
	if badMethod.Code != http.StatusNotFound {
		t.Fatalf("unexpected bad method status=%d", badMethod.Code)
	}
	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, httptest.NewRequest(http.MethodGet, "/__aisys__/metrics", nil))
	if ok.Code != http.StatusOK || !strings.Contains(ok.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics response status=%d content-type=%q", ok.Code, ok.Header().Get("Content-Type"))
	}
	if got := len(collector.Windows()); got != 0 {
		t.Fatalf("metrics scrape must not record a runtime window: %d", got)
	}
}
