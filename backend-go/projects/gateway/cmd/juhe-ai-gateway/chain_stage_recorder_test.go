package main

// 阶段/尝试累积器接线测试：gatewaypreauth.Observability 是进程级单例且
// LogRequestStage 签名无 ctx/req，既有调用点经 traceId 进程槽零改动入库；
// upstream.* 埋点共用 chainEmitGatewayRequestStage 发射面。定向覆盖
// （既有预存失败 TestEnsureGatewaySQLiteStoragePreflight /
// TestW1BBootCoverOwnerFailFastArms 与本任务无关，不进本文件 pattern）。

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// stageEventSink 捕获 kernel 请求生命周期事件（全栈 timing_summary 断言用）。
type stageEventSink struct {
	mu     sync.Mutex
	levels []string
	fields []map[string]any
	msgs   []string
}

func (s *stageEventSink) EmitRequestEvent(level string, fields map[string]any, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.levels = append(s.levels, level)
	s.fields = append(s.fields, fields)
	s.msgs = append(s.msgs, message)
}

func (s *stageEventSink) byEvent(name string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := []map[string]any{}
	for index := range s.fields {
		if s.fields[index]["event"] == name {
			matched = append(matched, s.fields[index])
		}
	}
	return matched
}

func (s *stageEventSink) messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.msgs...)
}

// upstreamFetchHeadersStageCount 统计累积器里 upstream.fetch_headers 阶段数。
func upstreamFetchHeadersStageCount(ctx *kernel.RequestContext) int {
	count := 0
	for _, stage := range ctx.RequestStageAccumulation().Stages {
		if stage.Stage == "upstream.fetch_headers" {
			count++
		}
	}
	return count
}

func TestChainRequestStageRecorderRegistryLifecycle(t *testing.T) {
	if got := chainRequestStageRecorderOf(""); got != nil {
		t.Fatalf("空 traceId 必须返回 nil，got %+v", got)
	}
	if got := chainRequestStageRecorderOf("trace_missing"); got != nil {
		t.Fatalf("未注册 traceId 必须返回 nil，got %+v", got)
	}
	ctx := &kernel.RequestContext{TraceID: "trace_life"}
	chainRegisterRequestStageRecorder("trace_life", ctx)
	if got := chainRequestStageRecorderOf("trace_life"); got != ctx {
		t.Fatalf("注册后反查 = %+v, want 原上下文", got)
	}
	chainUnregisterRequestStageRecorder("trace_life", ctx)
	if got := chainRequestStageRecorderOf("trace_life"); got != nil {
		t.Fatalf("注销后反查 = %+v, want nil", got)
	}
	chainUnregisterRequestStageRecorder("trace_life", ctx) // 幂等
	chainRegisterRequestStageRecorder("", ctx)             // 空 traceId 注册是 no-op
}

// TestChainRequestStageRecorderRegistrySameTraceIDConcurrent：traceId 可来自
// 客户端头，同进程并发请求复用同一 traceId 时注册表必须按请求指针成组——
// 先完成者的注销只移除自己，后者的映射与其后续 stage 入库不受影响。
func TestChainRequestStageRecorderRegistrySameTraceIDConcurrent(t *testing.T) {
	first := &kernel.RequestContext{TraceID: "trace_dup"}
	second := &kernel.RequestContext{TraceID: "trace_dup"}
	chainRegisterRequestStageRecorder("trace_dup", first)
	chainRegisterRequestStageRecorder("trace_dup", second)
	chainUnregisterRequestStageRecorder("trace_dup", first)
	if got := chainRequestStageRecorderOf("trace_dup"); got != second {
		t.Fatalf("先完成者注销后反查 = %+v, want 后注册上下文", got)
	}
	chainUnregisterRequestStageRecorder("trace_dup", second)
	if got := chainRequestStageRecorderOf("trace_dup"); got != nil {
		t.Fatalf("全部注销后反查 = %+v, want nil", got)
	}
}

func TestSlogObservabilityLogRequestStageRecordsIntoKernelContext(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	observability := newSlogObservability(logger, gatewaypreauth.SystemClock{})

	ctx := &kernel.RequestContext{TraceID: "trace_stage", StartedAt: time.Now().Add(-time.Second)}
	chainRegisterRequestStageRecorder("trace_stage", ctx)
	defer chainUnregisterRequestStageRecorder("trace_stage", ctx)

	// 既有调用点形态：fields 带 traceId 与尝试索引，入库 + 日志行保持。
	observability.LogRequestStage("request.accepted", map[string]any{
		"traceId": "trace_stage",
	}, "success", time.Now().Add(-100*time.Millisecond))
	observability.LogRequestStage("upstream.fetch_headers", map[string]any{
		"traceId":           "trace_stage",
		"attemptIndex":      1,
		"auditAttemptIndex": 2,
	}, "expected_failure", time.Now().Add(-200*time.Millisecond))

	accumulation := ctx.RequestStageAccumulation()
	if accumulation.StageCount != 2 || len(accumulation.Stages) != 2 {
		t.Fatalf("stage accumulation = %+v, want 2 条", accumulation)
	}
	if accumulation.AttemptCount != 2 {
		t.Fatalf("attemptCount = %d, want 2（max(1+1, 2)）", accumulation.AttemptCount)
	}
	if accumulation.Stages[0].Stage != "request.accepted" || accumulation.Stages[0].Outcome != "success" {
		t.Fatalf("stages[0] = %+v", accumulation.Stages[0])
	}
	if accumulation.Stages[1].Outcome != "expected_failure" || accumulation.Stages[1].DurationMs <= 0 {
		t.Fatalf("stages[1] = %+v", accumulation.Stages[1])
	}
	if accumulation.Stages[0].StartedOffsetMs < 0 || accumulation.Stages[1].EndedOffsetMs <= accumulation.Stages[1].StartedOffsetMs {
		t.Fatalf("offsets 异常：[%+v %+v]", accumulation.Stages[0], accumulation.Stages[1])
	}
	// gateway.request.stage 独立日志行保持既有输出。
	logs := buffer.String()
	for _, want := range []string{"gateway.request.stage", "request.accepted", "upstream.fetch_headers", "请求阶段预期失败：upstream.fetch_headers"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("日志缺少 %q：\n%s", want, logs)
		}
	}

	// 无 traceId（如 runtime_resolution 的空 traceId 形态）：只写日志，不入库、
	// 不触碰 attemptCount。
	before := ctx.RequestStageAccumulation()
	observability.LogRequestStage("runtime_resolution", map[string]any{"traceId": ""}, "success", time.Now())
	after := ctx.RequestStageAccumulation()
	if after.StageCount != before.StageCount || after.AttemptCount != before.AttemptCount {
		t.Fatalf("空 traceId 不得入库：before %+v after %+v", before, after)
	}
}

func TestChainEmitGatewayRequestStageAttemptIndexes(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx := &kernel.RequestContext{TraceID: "trace_attempt", StartedAt: time.Now().Add(-time.Minute)}
	chainRegisterRequestStageRecorder("trace_attempt", ctx)
	defer chainUnregisterRequestStageRecorder("trace_attempt", ctx)

	chainEmitGatewayRequestStage(logger, "upstream.fetch_headers", map[string]any{
		"traceId":           "trace_attempt",
		"accountId":         "acc_1",
		"attemptIndex":      2,
		"auditAttemptIndex": 5,
		"statusCode":        503,
		"ok":                false,
	}, "success", time.Now().Add(-50*time.Millisecond))

	accumulation := ctx.RequestStageAccumulation()
	if accumulation.AttemptCount != 5 {
		t.Fatalf("attemptCount = %d, want 5（auditAttemptIndex 直接作计数）", accumulation.AttemptCount)
	}
	if accumulation.StageCount != 1 || accumulation.Stages[0].Stage != "upstream.fetch_headers" {
		t.Fatalf("stage accumulation = %+v", accumulation)
	}
	// success/慢阶段策略：该 stage 耗时 <1s → debug 行；event/stage 字段在行内。
	logs := buffer.String()
	if !strings.Contains(logs, "gateway.request.stage") || !strings.Contains(logs, "upstream.fetch_headers") {
		t.Fatalf("独立日志行缺失：\n%s", logs)
	}
}

func TestChainEmitGatewayRequestStageExhaustedOutcome(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx := &kernel.RequestContext{TraceID: "trace_exhausted", StartedAt: time.Now().Add(-time.Minute)}
	chainRegisterRequestStageRecorder("trace_exhausted", ctx)
	defer chainUnregisterRequestStageRecorder("trace_exhausted", ctx)

	chainEmitGatewayRequestStage(logger, "upstream.dispatch.failed", map[string]any{
		"traceId":       "trace_exhausted",
		"failureReason": "upstream_5xx",
	}, "expected_failure", time.Now().Add(-2*time.Second))

	accumulation := ctx.RequestStageAccumulation()
	if accumulation.StageCount != 1 {
		t.Fatalf("stageCount = %d, want 1", accumulation.StageCount)
	}
	// attemptIndex/auditAttemptIndex 缺席不触碰 attemptCount（保持 0）。
	if accumulation.AttemptCount != 0 {
		t.Fatalf("attemptCount = %d, want 0（无索引字段不编造计数）", accumulation.AttemptCount)
	}
	logs := buffer.String()
	if !strings.Contains(logs, "请求阶段预期失败：upstream.dispatch.failed") {
		t.Fatalf("耗尽阶段 warn 行缺失：\n%s", logs)
	}
	if !strings.Contains(logs, "expected_failure") {
		t.Fatalf("outcome 字段缺失：\n%s", logs)
	}
}

// ---------------------------------------------------------------------------
// 复审修复锁定：同一 attempt 恰一条 upstream.fetch_headers stage；
// 失败 K 次后成功 attemptCount = K+1（真实索引折算）。
// ---------------------------------------------------------------------------

// upstreamFetchHeadersDispatched 构造链面成功埋点输入：真实进程内上游响应
// + 引擎带出的索引。
func upstreamFetchHeadersDispatched(t *testing.T, status int, attemptIndex, auditAttemptIndex int, stageRecorded bool) gatewaydispatch.UpstreamDispatchResult {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	response, err := gatewaydispatch.RequestUpstream(context.Background(), server.URL+"/v1/chat/completions",
		gatewaydispatch.UpstreamRequestOptions{Method: http.MethodPost}, gatewaydispatch.TransportDeps{})
	if err != nil {
		t.Fatalf("request upstream: %v", err)
	}
	return gatewaydispatch.UpstreamDispatchResult{
		Account:               gatewaydispatch.AccountCandidate{ID: "acc_stage"},
		Response:              response,
		AttemptStartedAt:      time.Now().Add(-time.Second).UnixMilli(),
		AttemptIndex:          attemptIndex,
		AuditAttemptIndex:     auditAttemptIndex,
		UpstreamStageRecorded: stageRecorded,
	}
}

// TestChainRecordUpstreamFetchHeadersStageOncePerAttempt：链面门控——已由失败
// 派发器入库的尝试（UpstreamStageRecorded）不再发射；未记录的恰好发射一条，
// 真实索引按 max(attemptIndex+1, auditAttemptIndex) 折算 attemptCount。
func TestChainRecordUpstreamFetchHeadersStageOncePerAttempt(t *testing.T) {
	chain := &gatewayChain{observability: newSlogObservability(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), gatewaypreauth.SystemClock{})}
	context := &gatewaypreauth.DispatchContext{UsageContext: gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace_gate"}}
	recorder := &kernel.RequestContext{TraceID: "trace_gate", StartedAt: time.Now().Add(-time.Minute)}
	chainRegisterRequestStageRecorder("trace_gate", recorder)
	defer chainUnregisterRequestStageRecorder("trace_gate", recorder)

	// 失败 2 次后成功：第 3 次尝试 rotation=0、序数=3。
	chain.recordUpstreamFetchHeadersStage(context, upstreamFetchHeadersDispatched(t, http.StatusOK, 0, 3, false))
	if got := upstreamFetchHeadersStageCount(recorder); got != 1 {
		t.Fatalf("fetch_headers stage 数 = %d, want 1", got)
	}
	if got := recorder.RequestStageAccumulation().AttemptCount; got != 3 {
		t.Fatalf("attemptCount = %d, want 3（失败2次后成功）", got)
	}

	// 已由失败派发器入库的同一 attempt：门控拦截，零新增 stage、零计数漂移。
	before := recorder.RequestStageAccumulation()
	chain.recordUpstreamFetchHeadersStage(context, upstreamFetchHeadersDispatched(t, http.StatusTooManyRequests, 0, 3, true))
	after := recorder.RequestStageAccumulation()
	if got := upstreamFetchHeadersStageCount(recorder); got != 1 {
		t.Fatalf("标记后 fetch_headers stage 数 = %d, want 仍 1（同一 attempt 恰一条）", got)
	}
	if after.StageCount != before.StageCount || after.AttemptCount != before.AttemptCount {
		t.Fatalf("标记后计数漂移：before %+v after %+v", before, after)
	}
}

// TestChainFailureDispatcherMarksGatewaySkipStageRecorded：gateway 流量分支
// 已发射 stage 的结果必须带 UpstreamStageRecorded；ReturnResponse 分支
// （诊断/非网关流量，发射点之前返回）不得带标记——链面据此各发射恰一条。
func TestChainFailureDispatcherMarksGatewaySkipStageRecorded(t *testing.T) {
	recorder := &kernel.RequestContext{TraceID: "trace_dispatch", StartedAt: time.Now().Add(-time.Minute)}
	chainRegisterRequestStageRecorder("trace_dispatch", recorder)
	defer chainUnregisterRequestStageRecorder("trace_dispatch", recorder)

	dispatcher := newFailureDispatcherForTest(&failureDispatchAffinity{})
	gatewayInput := gatewayFailedResponseInput(
		failureDispatchUpstreamResponse(t, http.StatusBadGateway, "application/json", `{"error":"bad gateway"}`),
		&failureDispatchAuditSink{}, gatewayTrafficSource)
	gatewayInput.UsageContext.TraceID = "trace_dispatch"
	gatewayResult, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), gatewayInput)
	if err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	if gatewayResult.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("gateway action = %s, want skip_account", gatewayResult.Action)
	}
	if !gatewayResult.UpstreamStageRecorded {
		t.Fatal("gateway 分支已入库 stage，必须带 UpstreamStageRecorded 标记")
	}
	if got := upstreamFetchHeadersStageCount(recorder); got != 1 {
		t.Fatalf("gateway 分支 fetch_headers stage 数 = %d, want 1", got)
	}

	// 诊断流量 ReturnResponse：派发器零发射、零标记（链面将发射唯一一条）。
	diagnosticRecorder := &kernel.RequestContext{TraceID: "trace_diag", StartedAt: time.Now().Add(-time.Minute)}
	chainRegisterRequestStageRecorder("trace_diag", diagnosticRecorder)
	defer chainUnregisterRequestStageRecorder("trace_diag", diagnosticRecorder)
	diagnosticInput := gatewayFailedResponseInput(
		failureDispatchUpstreamResponse(t, http.StatusTooManyRequests, "application/json", `{"error":"rate limited"}`),
		&failureDispatchAuditSink{}, "manual_account_test")
	diagnosticInput.UsageContext.TraceID = "trace_diag"
	diagnosticResult, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), diagnosticInput)
	if err != nil {
		t.Fatalf("handle diagnostic failed response: %v", err)
	}
	if diagnosticResult.Action != gatewaydispatch.FailedResponseActionReturnResponse {
		t.Fatalf("diagnostic action = %s, want return_response", diagnosticResult.Action)
	}
	if diagnosticResult.UpstreamStageRecorded {
		t.Fatal("ReturnResponse 分支在发射点之前返回，不得带 UpstreamStageRecorded")
	}
	if got := upstreamFetchHeadersStageCount(diagnosticRecorder); got != 0 {
		t.Fatalf("diagnostic 分支派发器 stage 数 = %d, want 0（唯一一条由链面发射）", got)
	}
}

// TestV1RetryThenSuccessAttemptCountThree：全栈锁定"失败 2 次后成功"——
// 候选账户对前两次上游调用返回 503、第三次返回 200，timing_summary 的
// attemptCount 必须为 3（第 1/2 次失败由失败派发器带序数入库，第 3 次成功
// 由链面带 AuditAttemptIndex=3 入库），且 fetch_headers 恰好 3 条。
func TestV1RetryThenSuccessAttemptCountThree(t *testing.T) {
	var callCount atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if callCount.Add(1) <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-retry-count","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"第三次成功"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	fixture := newChainFixture(t)
	secret := w1vSeedMultiGroupKey(t, fixture, []string{"w1v_retry_attempt_count"},
		map[int]bool{0: false}, map[int]string{0: upstream.URL})
	seedRetryAttemptCountGroupAccounts(t, fixture, "w1v_retry_attempt_count", upstream.URL, 3)
	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()

	sink := &stageEventSink{}
	kernel.SetRequestEventSink(sink)
	defer kernel.SetRequestEventSink(nil)

	wrapped := kernel.RequestContextMiddleware(0)(chain)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"重试计数"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+secret)
	recorder := httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s（前两次 503 后第三次必须成功）", recorder.Code, recorder.Body.String())
	}
	summaries := sink.byEvent("gateway.request.timing_summary")
	if len(summaries) != 1 {
		t.Fatalf("timing summary events = %d, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary["attemptCount"] != int64(3) {
		t.Fatalf("attemptCount = %v (%T), want 3", summary["attemptCount"], summary["attemptCount"])
	}
	// 成功（2xx）请求按契约不附 stages：标量计数如实、stages 恒空列表。
	stages, _ := summary["stages"].([]any)
	if len(stages) != 0 {
		t.Fatalf("成功请求 stages = %#v, want 空列表（安全与日志策略:121）", stages)
	}
	if outcome, _ := summary["outcome"].(string); outcome != "success" {
		t.Fatalf("outcome = %v, want success", summary["outcome"])
	}
	for _, message := range sink.messages() {
		if strings.Contains(message, "3 次上游尝试") {
			return
		}
	}
	t.Fatalf("汇总消息缺少真实尝试数： %v", sink.messages())
}

// TestV1ThreeFailuresStageCountThree：全失败终态（三账户各自瞬态重试到耗尽）
// 按契约附真实 stages 列表。断言与重试策略解耦的强不变量：每次真实上游尝试
// 恰一条 upstream.fetch_headers stage（非 2xx 响应头已收到按 Node 形态记
// success），attemptCount 与实际上游调用数一致。
func TestV1ThreeFailuresStageCountThree(t *testing.T) {
	var callCount atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable"}}`))
	}))
	defer upstream.Close()

	fixture := newChainFixture(t)
	secret := w1vSeedMultiGroupKey(t, fixture, []string{"w1v_retry_attempt_count"},
		map[int]bool{0: false}, map[int]string{0: upstream.URL})
	seedRetryAttemptCountGroupAccounts(t, fixture, "w1v_retry_attempt_count", upstream.URL, 3)
	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()

	sink := &stageEventSink{}
	kernel.SetRequestEventSink(sink)
	defer kernel.SetRequestEventSink(nil)

	wrapped := kernel.RequestContextMiddleware(0)(chain)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"全失败计数"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+secret)
	recorder := httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d（三账户全 503 必须耗尽为 503）body=%s", recorder.Code, recorder.Body.String())
	}
	summaries := sink.byEvent("gateway.request.timing_summary")
	if len(summaries) != 1 {
		t.Fatalf("timing summary events = %d, want 1", len(summaries))
	}
	summary := summaries[0]
	actualAttempts := callCount.Load()
	// 策略无关的强不变量：每次真实上游尝试（含同账户瞬态重试）恰一条
	// fetch_headers stage，attemptCount 与实际上游调用数一致。
	if summary["attemptCount"] != actualAttempts {
		t.Fatalf("attemptCount = %v, want %d（与实际上游调用数一致）", summary["attemptCount"], actualAttempts)
	}
	stages, _ := summary["stages"].([]any)
	fetchHeaders := 0
	for index := range stages {
		stage, _ := stages[index].(kernel.RequestStageSummary)
		if stage.Stage == "upstream.fetch_headers" {
			fetchHeaders++
			if stage.Outcome != "success" {
				t.Fatalf("非 2xx 响应头已收到，fetch_headers stage outcome = %q, want success（Node 形态）", stage.Outcome)
			}
		}
	}
	if int64(fetchHeaders) != actualAttempts {
		t.Fatalf("fetch_headers stage 数 = %d, want %d（同一 attempt 恰一条）", fetchHeaders, actualAttempts)
	}
}

// seedRetryAttemptCountGroupAccounts 在既有分组下追加 count 个候选账户
// （同一上游），构造账户级 failover：503 / 503 / 200。
func seedRetryAttemptCountGroupAccounts(t *testing.T, fixture *chainFixture, groupID, baseURL string, count int) {
	t.Helper()
	now := "2026-09-14T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", err, query)
		}
	}
	for index := 0; index < count; index++ {
		accountID := fmt.Sprintf("acc_%s_%d", groupID, index)
		seed(`INSERT INTO accounts (
				id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
				name, type, status, schedulable, concurrency_limit, credentials_encrypted, deleted_at, health_check_model
			) VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', ?, 'api_key', 'active', 1, 4, ?, NULL, 'gpt-test')`,
			accountID, fixture.systemAccount, fmt.Sprintf("重试账户 %d", index), w1vEncrypt(t, map[string]any{"api_key": "sk-retry-account-key", "base_url": baseURL}))
		seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
			groupID, fixture.systemAccount, accountID, now)
		seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES (?, 'openai', 'gpt-test', ?)`,
			accountID, now)
	}
}
