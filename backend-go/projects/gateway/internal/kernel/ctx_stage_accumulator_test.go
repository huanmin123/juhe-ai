package kernel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 请求级阶段/尝试累积器（对齐 Node request-context.ts stageSummaries /
// attemptCount 语义）：timing_summary 的 attemptCount / stageCount / stages /
// droppedStageSummaries 必须来自真实累积，成功请求保持 stages 空列表，
// 失败与中断请求携带真实有界 stages。

func recordStagesForTest(ctx *RequestContext, count int, stage string) {
	for index := 0; index < count; index++ {
		ctx.RecordRequestStage(stage, "success", int64(10+index), time.Now())
	}
}

func TestTimingSummaryCarriesAccumulatedStagesAndAttemptsOnFailure(t *testing.T) {
	sink := &recordingSink{}
	resetObservability(t, sink, nil)

	handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := Context(r)
		base := time.Now().Add(-time.Minute)
		ctx.RecordRequestStage("request.accepted", "success", 3, base)
		ctx.RecordRequestStage("upstream.fetch_headers", "expected_failure", 120, base.Add(10*time.Millisecond))
		ctx.RecordUpstreamAttempt(testIntPtr(1), testIntPtr(2))
		w.WriteHeader(http.StatusBadGateway)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	summary := sink.byEvent("gateway.request.timing_summary")
	if len(summary) != 1 {
		t.Fatalf("timing summary events = %d, want 1", len(summary))
	}
	fields := summary[0].fields
	if fields["attemptCount"] != int64(2) {
		t.Fatalf("attemptCount = %v (%T), want 2 (max(attemptIndex+1, auditAttemptIndex))", fields["attemptCount"], fields["attemptCount"])
	}
	if fields["stageCount"] != int64(2) {
		t.Fatalf("stageCount = %v, want 2", fields["stageCount"])
	}
	if fields["droppedStageSummaries"] != int64(0) {
		t.Fatalf("droppedStageSummaries = %v, want 0", fields["droppedStageSummaries"])
	}
	stages, ok := fields["stages"].([]any)
	if !ok || len(stages) != 2 {
		t.Fatalf("stages = %#v, want 2 real entries", fields["stages"])
	}
	first, _ := stages[0].(RequestStageSummary)
	second, _ := stages[1].(RequestStageSummary)
	if first.Stage != "request.accepted" || first.Sequence != 1 || first.DurationMs != 3 {
		t.Fatalf("stages[0] = %+v", first)
	}
	if second.Stage != "upstream.fetch_headers" || second.Outcome != "expected_failure" || second.Sequence != 2 {
		t.Fatalf("stages[1] = %+v", second)
	}
	if !strings.Contains(summary[0].message, "2 次上游尝试") {
		t.Fatalf("summary message = %q", summary[0].message)
	}
	// outcome 语义保持只看状态码：502 → unexpected_failure。
	if fields["outcome"] != "unexpected_failure" {
		t.Fatalf("outcome = %v", fields["outcome"])
	}
}

func TestTimingSummarySuccessKeepsStagesEmptyWithRealCounts(t *testing.T) {
	sink := &recordingSink{}
	resetObservability(t, sink, nil)

	handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := Context(r)
		ctx.RecordRequestStage("request.accepted", "success", 2, time.Now().Add(-time.Second))
		ctx.RecordRequestStage("preflight.completed", "success", 5, time.Now())
		ctx.RecordUpstreamAttempt(testIntPtr(0), nil)
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	summary := sink.byEvent("gateway.request.timing_summary")
	if len(summary) != 1 {
		t.Fatalf("timing summary events = %d, want 1", len(summary))
	}
	fields := summary[0].fields
	if fields["attemptCount"] != int64(1) || fields["stageCount"] != int64(2) {
		t.Fatalf("counts = attemptCount %v stageCount %v, want 1/2", fields["attemptCount"], fields["stageCount"])
	}
	stages := fields["stages"].([]any)
	if len(stages) != 0 {
		t.Fatalf("success stages = %#v, want empty (未采样成功不附 stages)", stages)
	}
	if fields["stageDetailsSampled"] != false {
		t.Fatalf("stageDetailsSampled = %v, want false", fields["stageDetailsSampled"])
	}
}

func TestTimingSummaryDroppedStagesBeyondCapacity(t *testing.T) {
	sink := &recordingSink{}
	resetObservability(t, sink, nil)

	handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := Context(r)
		recordStagesForTest(ctx, 70, "upstream.fetch_headers")
		ctx.RecordUpstreamAttempt(nil, nil)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	summary := sink.byEvent("gateway.request.timing_summary")
	if len(summary) != 1 {
		t.Fatalf("timing summary events = %d, want 1", len(summary))
	}
	fields := summary[0].fields
	if fields["stageCount"] != int64(70) {
		t.Fatalf("stageCount = %v, want 70（总数含丢弃）", fields["stageCount"])
	}
	if fields["droppedStageSummaries"] != int64(6) {
		t.Fatalf("droppedStageSummaries = %v, want 6", fields["droppedStageSummaries"])
	}
	stages, ok := fields["stages"].([]any)
	if !ok || len(stages) != requestStageSummaryCapacity {
		t.Fatalf("stages length = %#v, want %d", fields["stages"], requestStageSummaryCapacity)
	}
	last, _ := stages[len(stages)-1].(RequestStageSummary)
	if last.Sequence != int64(requestStageSummaryCapacity) {
		t.Fatalf("last retained sequence = %d, want %d", last.Sequence, requestStageSummaryCapacity)
	}
}

func TestTimingSummaryAbortedCarriesRealAccumulation(t *testing.T) {
	sink := &recordingSink{}
	resetObservability(t, sink, nil)

	handler := RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := Context(r)
		ctx.RecordRequestStage("upstream.fetch_headers", "aborted", 40, time.Now().Add(-time.Second))
		ctx.RecordUpstreamAttempt(nil, testIntPtr(3))
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request = request.WithContext(canceledContext())
	handler.ServeHTTP(httptest.NewRecorder(), request)

	summary := sink.byEvent("gateway.request.timing_summary")
	if len(summary) != 1 || summary[0].fields["outcome"] != "aborted" {
		t.Fatalf("aborted timing summary = %+v", summary)
	}
	fields := summary[0].fields
	if fields["attemptCount"] != int64(3) {
		t.Fatalf("attemptCount = %v, want 3", fields["attemptCount"])
	}
	stages, ok := fields["stages"].([]any)
	if !ok || len(stages) != 1 {
		t.Fatalf("aborted stages = %#v, want the real stage", fields["stages"])
	}
	stage, _ := stages[0].(RequestStageSummary)
	if stage.Stage != "upstream.fetch_headers" {
		t.Fatalf("aborted stage = %+v", stage)
	}
}

func TestRecordUpstreamAttemptSemantics(t *testing.T) {
	ctx := &RequestContext{StartedAt: time.Now()}
	// 无索引的尝试事实：至少 1。
	ctx.RecordUpstreamAttempt(nil, nil)
	if got := ctx.RequestStageAccumulation().AttemptCount; got != 1 {
		t.Fatalf("attemptCount = %d, want 1", got)
	}
	// attemptIndex +1 语义。
	ctx.RecordUpstreamAttempt(testIntPtr(2), nil)
	if got := ctx.RequestStageAccumulation().AttemptCount; got != 3 {
		t.Fatalf("attemptCount = %d, want 3", got)
	}
	// auditAttemptIndex 直接作为计数（request-context.ts:632-635）。
	ctx.RecordUpstreamAttempt(nil, testIntPtr(5))
	if got := ctx.RequestStageAccumulation().AttemptCount; got != 5 {
		t.Fatalf("attemptCount = %d, want 5", got)
	}
	// 更小值不回退。
	ctx.RecordUpstreamAttempt(testIntPtr(0), testIntPtr(1))
	if got := ctx.RequestStageAccumulation().AttemptCount; got != 5 {
		t.Fatalf("attemptCount = %d, want 5（只增不减）", got)
	}
	// 空请求无尝试保持 0。
	empty := &RequestContext{StartedAt: time.Now()}
	if got := empty.RequestStageAccumulation().AttemptCount; got != 0 {
		t.Fatalf("empty attemptCount = %d, want 0", got)
	}
	if got := empty.RequestStageAccumulation().StageCount; got != 0 {
		t.Fatalf("empty stageCount = %d, want 0", got)
	}
}

func testIntPtr(value int) *int { return &value }
