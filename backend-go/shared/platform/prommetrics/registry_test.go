package prommetrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// NewRegistry 必须自带 Go collector 与 process collector：标准 go_*/process_*
// 家族由 client_golang 默认收集器取代旧的 Node 兼容 juhe_ai_process_* 族。
func TestNewRegistryExposesGoAndProcessCollectors(t *testing.T) {
	registry := NewRegistry()
	text, err := registry.GatherText()
	if err != nil {
		t.Fatalf("GatherText: %v", err)
	}
	if !strings.Contains(text, "# TYPE go_goroutines gauge") {
		t.Fatalf("missing default Go collector family:\n%s", text)
	}
	if !strings.Contains(text, "# TYPE process_resident_memory_bytes gauge") {
		t.Fatalf("missing default process collector family:\n%s", text)
	}
}

func TestCounterVecShape(t *testing.T) {
	registry := NewRegistry()
	requests := NewCounterVec(registry, "test_requests_total", "Test requests.", nil, []string{"route_group", "method"})
	requests.WithLabelValues("gateway", "POST").Inc()
	requests.WithLabelValues("gateway", "POST").Add(2)
	text, err := registry.GatherText()
	if err != nil {
		t.Fatalf("GatherText: %v", err)
	}
	for _, want := range []string{
		"# HELP test_requests_total Test requests.",
		"# TYPE test_requests_total counter",
		`test_requests_total{method="POST",route_group="gateway"} 3`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q:\n%s", want, text)
		}
	}
}

func TestGaugeVecStaysResidentAtZero(t *testing.T) {
	registry := NewRegistry()
	inFlight := NewGaugeVec(registry, "test_in_flight", "Test in-flight.", nil, []string{"method"})
	child := inFlight.WithLabelValues("POST")
	child.Inc()
	child.Dec()
	text, err := registry.GatherText()
	if err != nil {
		t.Fatalf("GatherText: %v", err)
	}
	// series 归零常驻：Dec 到 0 后不删除 series。
	if !strings.Contains(text, `test_in_flight{method="POST"} 0`) {
		t.Fatalf("zero gauge series must stay resident:\n%s", text)
	}
	child.Set(4)
	if text, err = registry.GatherText(); err != nil {
		t.Fatalf("GatherText: %v", err)
	}
	if !strings.Contains(text, `test_in_flight{method="POST"} 4`) {
		t.Fatalf("Set(4) not rendered:\n%s", text)
	}
}

func TestHistogramVecShape(t *testing.T) {
	registry := NewRegistry()
	duration := NewHistogramVec(registry, "test_duration_seconds", "Test duration.", nil, []string{"method"}, []float64{0.5, 1, 2})
	duration.WithLabelValues("POST").Observe(1.5)
	text, err := registry.GatherText()
	if err != nil {
		t.Fatalf("GatherText: %v", err)
	}
	for _, want := range []string{
		"# TYPE test_duration_seconds histogram",
		`test_duration_seconds_bucket{method="POST",le="0.5"} 0`,
		`test_duration_seconds_bucket{method="POST",le="1"} 0`,
		`test_duration_seconds_bucket{method="POST",le="2"} 1`,
		`test_duration_seconds_bucket{method="POST",le="+Inf"} 1`,
		`test_duration_seconds_sum{method="POST"} 1.5`,
		`test_duration_seconds_count{method="POST"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q:\n%s", want, text)
		}
	}
}

// constLabels 语义：固定标签并入每个 series（gateway 的 service="juhe-ai"
// 契约依赖它），与动态标签共同参与排序。
func TestConstLabelsJoinEverySeries(t *testing.T) {
	registry := NewRegistry()
	counter := NewCounterVec(registry, "test_const_total", "Test const counter.", map[string]string{"service": "juhe-ai"}, []string{"method"})
	counter.WithLabelValues("POST").Inc()
	gauge := NewGaugeVec(registry, "test_const_gauge", "Test const gauge.", map[string]string{"service": "juhe-ai"}, []string{"method"})
	gauge.WithLabelValues("POST").Set(2)
	histogram := NewHistogramVec(registry, "test_const_duration_seconds", "Test const histogram.", map[string]string{"service": "juhe-ai"}, []string{"method"}, []float64{1})
	histogram.WithLabelValues("POST").Observe(0.5)
	text, err := registry.GatherText()
	if err != nil {
		t.Fatalf("GatherText: %v", err)
	}
	for _, want := range []string{
		`test_const_total{method="POST",service="juhe-ai"} 1`,
		`test_const_gauge{method="POST",service="juhe-ai"} 2`,
		`test_const_duration_seconds_bucket{method="POST",service="juhe-ai",le="1"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q:\n%s", want, text)
		}
	}
}

func TestNewCounterFuncReadsThroughTheCallback(t *testing.T) {
	registry := NewRegistry()
	value := 7
	NewCounterFunc(registry, "test_dropped_total", "Test dropped.", map[string]string{"service": "juhe-ai"}, func() float64 {
		return float64(value)
	})
	text, err := registry.GatherText()
	if err != nil {
		t.Fatalf("GatherText: %v", err)
	}
	if !strings.Contains(text, `test_dropped_total{service="juhe-ai"} 7`) {
		t.Fatalf("callback counter missing:\n%s", text)
	}
	value = 9
	if text, err = registry.GatherText(); err != nil {
		t.Fatalf("GatherText: %v", err)
	}
	if !strings.Contains(text, `test_dropped_total{service="juhe-ai"} 9`) {
		t.Fatalf("callback counter must re-read fn per scrape:\n%s", text)
	}
}

func TestHandlerServesExposition(t *testing.T) {
	registry := NewRegistry()
	NewCounterVec(registry, "test_handler_total", "Test handler.", nil, []string{"code"}).WithLabelValues("200").Inc()
	server := httptest.NewServer(registry.Handler())
	t.Cleanup(server.Close)
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("GET handler: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.Contains(contentType, "text/plain") {
		t.Fatalf("content type = %q, want text/plain family", contentType)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), `test_handler_total{code="200"} 1`) {
		t.Fatalf("handler body missing series:\n%s", string(body))
	}
}
