package kernel

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// W1a（/v1 网关链路 traceId 统一 + timing_summary 补 attemptCount）：
// RecordGatewayAttempt 按发生次数累计网关派发尝试，timing_summary 输出两个
// 尝试累积器（RecordUpstreamAttempt 的 max 索引折算 / RecordGatewayAttempt
// 的逐次 +1）中的较大值，事件字段名保持 attemptCount。

// (c) handler 内逐次登记两次尝试，timing_summary 的 attemptCount 输出 2
// （而不是 max 索引语义在索引缺席时能给出的 1）。
func TestTimingSummaryReportsGatewayAttemptCount(t *testing.T) {
	sink := &recordingSink{}
	resetObservability(t, sink, nil)

	handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := Context(r)
		rc.RecordGatewayAttempt()
		rc.RecordGatewayAttempt()
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	summary := sink.byEvent("gateway.request.timing_summary")
	if len(summary) != 1 {
		t.Fatalf("timing summary events = %d, want 1 (gateway route)", len(summary))
	}
	if got, ok := summary[0].fields["attemptCount"].(int64); !ok || got != 2 {
		t.Fatalf("attemptCount = %v (%T), want int64(2)", summary[0].fields["attemptCount"], summary[0].fields["attemptCount"])
	}
}

// 绝对索引观测压过逐次计数：attemptIndex=4 折算 max 累积器为 5，两次逐次
// 计数取较大值仍输出 5。
func TestTimingSummaryAttemptCountTakesLargerAccumulator(t *testing.T) {
	sink := &recordingSink{}
	resetObservability(t, sink, nil)

	handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := Context(r)
		index := 4
		rc.RecordUpstreamAttempt(&index, nil)
		rc.RecordGatewayAttempt()
		rc.RecordGatewayAttempt()
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	summary := sink.byEvent("gateway.request.timing_summary")
	if len(summary) != 1 {
		t.Fatalf("timing summary events = %d, want 1", len(summary))
	}
	if got, ok := summary[0].fields["attemptCount"].(int64); !ok || got != 5 {
		t.Fatalf("attemptCount = %v, want 5（max(索引折算 5, 逐次 2)）", summary[0].fields["attemptCount"])
	}
}

// 索引缺席的观测地板（RecordUpstreamAttempt(nil, nil) 折算 1）不掩盖真实
// 逐次计数：三次尝试输出 3。
func TestTimingSummaryAttemptCountPrefersOccurrenceOverIndexFloor(t *testing.T) {
	sink := &recordingSink{}
	resetObservability(t, sink, nil)

	handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := Context(r)
		rc.RecordUpstreamAttempt(nil, nil)
		rc.RecordGatewayAttempt()
		rc.RecordGatewayAttempt()
		rc.RecordGatewayAttempt()
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	summary := sink.byEvent("gateway.request.timing_summary")
	if len(summary) != 1 {
		t.Fatalf("timing summary events = %d, want 1", len(summary))
	}
	if got, ok := summary[0].fields["attemptCount"].(int64); !ok || got != 3 {
		t.Fatalf("attemptCount = %v, want 3（max(索引地板 1, 逐次 3)）", summary[0].fields["attemptCount"])
	}
}

// RecordGatewayAttempt/GatewayAttemptCount 并发安全：并发登记计数守恒。
func TestRecordGatewayAttemptConcurrentCountMatches(t *testing.T) {
	rc := &RequestContext{}
	const goroutines, perGoroutine = 8, 50
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				rc.RecordGatewayAttempt()
			}
		}()
	}
	wg.Wait()
	if got := rc.GatewayAttemptCount(); got != goroutines*perGoroutine {
		t.Fatalf("GatewayAttemptCount = %d, want %d", got, goroutines*perGoroutine)
	}
}
