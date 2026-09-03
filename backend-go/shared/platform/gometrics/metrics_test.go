package gometrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestWritePrimesScalarGaugesWithoutSampler(t *testing.T) {
	collector := New("juhe-ai", "gateway")
	var body strings.Builder
	if err := collector.Write(&body); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"juhe_ai_go_heap_alloc_bytes", "juhe_ai_go_goroutines", "juhe_ai_go_threads"} {
		if !strings.Contains(body.String(), want) {
			t.Fatalf("first scrape must expose %s: %s", want, body.String())
		}
	}
}

func TestWriteRefreshesScalarsWithoutAdvancingRecordCPUState(t *testing.T) {
	collector := New("juhe-ai", "gateway")
	collector.Snapshot()
	collector.state.mu.Lock()
	recordAt := collector.state.lastAt
	recordCPU := collector.state.lastCPU
	collector.state.mu.Unlock()

	time.Sleep(2 * time.Millisecond)
	var first strings.Builder
	if err := collector.Write(&first); err != nil {
		t.Fatal(err)
	}
	firstAt := collector.latest.SampledAt
	if !firstAt.After(recordAt) {
		t.Fatalf("first scrape must refresh latest sample: record=%s scrape=%s", recordAt, firstAt)
	}

	collector.state.mu.Lock()
	if !collector.state.lastAt.Equal(recordAt) || collector.state.lastCPU != recordCPU {
		collector.state.mu.Unlock()
		t.Fatalf("scrape must not advance Record CPU baseline: before=(%s,%v) after=(%s,%v)", recordAt, recordCPU, collector.state.lastAt, collector.state.lastCPU)
	}
	collector.state.mu.Unlock()

	time.Sleep(2 * time.Millisecond)
	var second strings.Builder
	if err := collector.Write(&second); err != nil {
		t.Fatal(err)
	}
	secondAt := collector.latest.SampledAt
	if !secondAt.After(firstAt) {
		t.Fatalf("second scrape must refresh latest sample: first=%s second=%s", firstAt, secondAt)
	}
	for _, body := range []string{first.String(), second.String()} {
		for _, want := range []string{"juhe_ai_go_heap_alloc_bytes", "juhe_ai_go_heap_objects"} {
			if !strings.Contains(body, want) {
				t.Fatalf("scrape missing %s: %s", want, body)
			}
		}
	}
	if collector.latest.CPUSecondsTotal > 0 && (!strings.Contains(first.String(), "juhe_ai_go_runtime_cpu_seconds_total") || !strings.Contains(second.String(), "juhe_ai_go_runtime_cpu_seconds_total")) {
		t.Fatalf("supported runtime CPU counter must be present in every scrape")
	}
}
