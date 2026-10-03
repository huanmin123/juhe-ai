package main

// 审计/用量观测缺口收口回归（A1-A6，2026-10-03 生产 13,691 行 audit_logs 取
// 证后的接线修复）。各测试为对应项"修复前必红"守卫：
//
//   - A1：newAuditCapture 注入 Pricing + SyncPricingAllowed 后，审计 attempt
//     行与主行 pricing_model 非空（此前两端口缺席，pricing_model 全 NULL）；
//   - A2：每请求 HTTP 完成 subject 的信号语义（先注册后完成 / 先完成后注册
//     / cancel 幂等不误伤 / observe 投递），audit flush 等待完成信号后
//     http_completed_at / http_duration_ms 非空（此前 HTTPCompletion 缺席恒
//     内联 flush，两列全 NULL），sink 失败 usage 的 CompletedAtMs 取真实完成
//     时刻（此前退化为 nowMs）；
//   - A3：chainResponseAuditCapture / preauthAuditCapture 实现
//     AuditFinalizeExtender，FinalizeExtended 透传 FirstTokenMs（此前 extras
//     恒丢，first_token_ms 全 NULL）；
//   - A4：CompleteAttempt 接受 http.Header（此前仅 map[string]any，响应层
//     传入的 http.Header 恒丢，upstream_response 头部 624/8,498）；
//   - A5：usageDispatchAdapter.RecordGatewayFailure 透传 model / stream /
//     failure_attribution / response_snapshot（此前 model 恒空，1,239 行仅
//     23 行有 model）；sink 侧从请求事实填充 Model / Stream；
//   - A6：chainFinalizationUsage.RecordFailedUpstreamAttempt 与成功路径同源
//     的身份与语义字段。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// 编译面断言：两个生产适配器必须实现响应层扩展 finalize 面（A3）。
var (
	_ gatewayresponse.AuditFinalizeExtender = preauthAuditCapture{}
	_ gatewayresponse.AuditFinalizeExtender = chainResponseAuditCapture{}
)

// auditGapListenerCounter 是 subject 单测的并发安全回调计数器。
type auditGapListenerCounter struct {
	mu     sync.Mutex
	values []int64
}

func (c *auditGapListenerCounter) record(value int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values = append(c.values, value)
}

func (c *auditGapListenerCounter) snapshot() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.values...)
}

// TestGatewayHTTPCompletionSubjectSignal 覆盖完成 subject 的信号契约（A2）：
// 先注册后完成、先完成后注册（同步回调）、幂等 complete、cancel 幂等且不
// 误伤其他监听者。
func TestGatewayHTTPCompletionSubjectSignal(t *testing.T) {
	// 先注册后完成：监听者收到一次完成值。
	subject := newGatewayHTTPCompletion()
	first := &auditGapListenerCounter{}
	cancelFirst := subject.OnCompleted(first.record)
	second := &auditGapListenerCounter{}
	_ = subject.OnCompleted(second.record)
	cancelFirst()
	cancelFirst() // cancel 幂等。
	subject.complete(1728000001000)
	subject.complete(1728000002000) // complete 幂等：不得二次通知。
	if got := first.snapshot(); len(got) != 0 {
		t.Fatalf("已取消监听者收到回调 %v， want 无", got)
	}
	if got := second.snapshot(); len(got) != 1 || got[0] != 1728000001000 {
		t.Fatalf("存活监听者回调 = %v，want 恰一次 1728000001000", got)
	}
	if value, ok := subject.CompletedAtMs(); !ok || value != 1728000001000 {
		t.Fatalf("CompletedAtMs = (%d, %v), want (1728000001000, true)", value, ok)
	}

	// 先完成后注册：OnCompleted 必须立即同步回调（audit 在完成后构造的
	// 场景不能丢完成值）。
	late := &auditGapListenerCounter{}
	noCancel := subject.OnCompleted(late.record)
	if got := late.snapshot(); len(got) != 1 || got[0] != 1728000001000 {
		t.Fatalf("完成后注册的监听者未同步回调：%v", got)
	}
	noCancel() // 完成后注册返回的 cancel 必须是安全的 no-op。
}

// TestGatewayHTTPCompletionObserveDelivery 覆盖 sink 侧订阅投递（A2）：
// 完成前订阅在 complete 后收到值；完成后订阅立即收到值；无人 Wait 不阻塞。
func TestGatewayHTTPCompletionObserveDelivery(t *testing.T) {
	subject := newGatewayHTTPCompletion()
	before := subject.observe()
	select {
	case value := <-before.Wait():
		t.Fatalf("完成前订阅不应收到值：%d", value)
	default:
	}
	subject.complete(5000)
	select {
	case value := <-before.Wait():
		if value != 5000 {
			t.Fatalf("完成前订阅值 = %d, want 5000", value)
		}
	case <-time.After(time.Second):
		t.Fatal("完成前订阅未在 complete 后收到值")
	}
	after := subject.observe()
	select {
	case value := <-after.Wait():
		if value != 5000 {
			t.Fatalf("完成后订阅值 = %d, want 5000", value)
		}
	case <-time.After(time.Second):
		t.Fatal("完成后订阅未立即收到值")
	}
}

// auditGapChain 构造启用审计的链替身（定价面按需注入）。
func auditGapChain(dispatcher *mergeCapturingAuditDispatcher, pricing gatewayusage.PricingCatalog) *gatewayChain {
	return &gatewayChain{
		auditSettings: gatewayusage.FixedAuditLogSettingsSource{Settings: gatewayusage.AuditLogSettings{
			Enabled:                true,
			SuccessSampleRate:      1,
			FullBodyCaptureEnabled: true,
		}},
		auditDispatcher:                dispatcher,
		finalizationPricing:            pricing,
		finalizationSyncPricingAllowed: pricing != nil,
	}
}

// TestNewAuditCaptureFlushesHTTPCompletedAtOnCompleteSignal 是 A2 审计侧修复
// 前必红的守卫：Finalize 先到、完成信号后到时，flush 必须等信号到达后才
// 投递，且 http_completed_at / http_duration_ms 落值；complete 先于
// CancelAuditCapture（生产 defer LIFO 顺序）时不得二次投递。
func TestNewAuditCaptureFlushesHTTPCompletedAtOnCompleteSignal(t *testing.T) {
	const startedAt = int64(1728000000000)
	dispatcher := &mergeCapturingAuditDispatcher{}
	chain := auditGapChain(dispatcher, nil)
	req := mergeRouteRequest("glm-5.3-flash")
	subject := newGatewayHTTPCompletion()
	capture := chain.newAuditCapture(req, "trace_http_completion", startedAt, subject)
	concrete := auditCaptureConcrete(capture)
	if concrete == nil {
		t.Fatal("newAuditCapture 必须产出具体 G17 capture")
	}

	statusCode := 200
	concrete.Finalize(gatewayusage.FinalizeAuditInput{Success: true, StatusCode: &statusCode})
	// 完成信号未到：flush 必须挂起（此前 HTTPCompletion 缺席时这里立即内联
	// flush 且两列恒空）。
	if logs := dispatcher.snapshot(); len(logs) != 0 {
		t.Fatalf("完成信号前不得投递审计，实际 %d 条", len(logs))
	}

	// 生产 defer 顺序：complete 先执行，CancelAuditCapture 后执行。
	subject.complete(startedAt + 1234)
	logs := dispatcher.snapshot()
	if len(logs) != 1 {
		t.Fatalf("完成信号后审计投递 = %d 条，want 1", len(logs))
	}
	gatewaypreauth.CancelAuditCapture(capture)
	if logs := dispatcher.snapshot(); len(logs) != 1 {
		t.Fatalf("cancel 后不得二次投递，实际 %d 条", len(logs))
	}
	if logs[0].HTTPCompletedAt == "" {
		t.Fatal("http_completed_at 必须落值")
	}
	if logs[0].HTTPDurationMs == nil || *logs[0].HTTPDurationMs != 1234 {
		t.Fatalf("http_duration_ms = %v, want 1234", logs[0].HTTPDurationMs)
	}
}

// TestNewAuditCaptureResolvesPricingModelForAttemptAndMainRows 是 A1 修复前
// 必红的守卫：链上注入 Pricing + SyncPricingAllowed 后，审计 attempt 行与
// 主行的 pricing_model 必须经同步定价目录解析落值。
func TestNewAuditCaptureResolvesPricingModelForAttemptAndMainRows(t *testing.T) {
	catalog := &accountingPricingCatalog{resolvedPricingModel: "deepseek-v4.1-flash-priced"}
	dispatcher := &mergeCapturingAuditDispatcher{}
	chain := auditGapChain(dispatcher, catalog)
	subject := newGatewayHTTPCompletion()
	capture := chain.newAuditCapture(mergeRouteRequest("glm-5.3-flash"), "trace_audit_pricing", 1728000000000, subject)
	t.Cleanup(func() { gatewaypreauth.CancelAuditCapture(capture) })
	concrete := auditCaptureConcrete(capture)

	concrete.StartAttempt(gatewayusage.StartAttemptInput{
		Account:      usageModelAccountOf(newAccountingCandidate()),
		AttemptIndex: 0,
		UpstreamURL:  "https://upstream.example/v1/chat/completions",
		Method:       "POST",
	})
	subject.complete(1728000000200)
	statusCode := 200
	concrete.Finalize(gatewayusage.FinalizeAuditInput{Success: true, StatusCode: &statusCode})

	logs := dispatcher.snapshot()
	if len(logs) != 1 {
		t.Fatalf("审计派发日志 = %d, want 1", len(logs))
	}
	if len(logs[0].Attempts) != 1 {
		t.Fatalf("审计 attempt 行 = %d, want 1", len(logs[0].Attempts))
	}
	if logs[0].Attempts[0].PricingModel != "deepseek-v4.1-flash-priced" {
		t.Fatalf("attempt pricing_model = %q, want deepseek-v4.1-flash-priced", logs[0].Attempts[0].PricingModel)
	}
	if logs[0].PricingModel != "deepseek-v4.1-flash-priced" {
		t.Fatalf("主行 pricing_model = %q, want deepseek-v4.1-flash-priced", logs[0].PricingModel)
	}
	if len(catalog.resolveModels) != 1 || catalog.resolveModels[0] != "deepseek-v4.1-flash" {
		t.Fatalf("定价解析入参 = %v, want [deepseek-v4.1-flash]（映射目标）", catalog.resolveModels)
	}
}

// newAccountingCandidate 构造与 accountingMappedSecret 同形的派发候选
// （映射：glm-5.3-flash -> deepseek-v4.1-flash）。
func newAccountingCandidate() gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{
		ID:                        "acc-audit-gap",
		Name:                      "audit-gap-account",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "openai-default",
		ProtocolCode:              gatewayopenai.ProtocolCode,
		ProtocolVersion:           gatewayopenai.ProtocolVersion,
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "glm-5.3-flash",
			SourceEndpointFamily:   gatewayopenai.FamilyChatCompletions,
			UpstreamModel:          "deepseek-v4.1-flash",
			UpstreamEndpointFamily: gatewayopenai.FamilyChatCompletions,
			Enabled:                true,
		}},
	}
}

// anthropicProtocolSecret 构造 anthropic 协议档案的非 anthropic 供应商账号
// （A6/A8 语义：usage_semantic 按协议档案解析为 anthropic）。
func anthropicProtocolSecret() gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                        "acc-anthropic-bridge",
		Name:                      "anthropic-bridge-account",
		ProviderCode:              "anthropic-bridge",
		ProviderProtocolProfileID: "anthropic-default",
		ProtocolCode:              "anthropic",
		ProtocolVersion:           "v1",
	}
}

// TestChainResponseAuditCaptureFinalizeExtendedCarriesFirstTokenMs 是 A3 修复
// 前必红的守卫：响应层适配器实现 AuditFinalizeExtender，FinalizeExtended 把
// extras.FirstTokenMs 写进 finalize 输入，审计行 first_token_ms 落值；扩展
// 面与 Finalize 二选一（响应层先探扩展面），不产生第二次 finalize 投递。
func TestChainResponseAuditCaptureFinalizeExtendedCarriesFirstTokenMs(t *testing.T) {
	dispatcher := &mergeCapturingAuditDispatcher{}
	chain := auditGapChain(dispatcher, nil)
	subject := newGatewayHTTPCompletion()
	capture := chain.newAuditCapture(mergeRouteRequest("glm-5.3-flash"), "trace_first_token", 1728000000000, subject)
	t.Cleanup(func() { gatewaypreauth.CancelAuditCapture(capture) })

	responseCapture, ok := responseAuditCaptureOf(capture).(gatewayresponse.AuditFinalizeExtender)
	if !ok {
		t.Fatal("chainResponseAuditCapture 必须实现 AuditFinalizeExtender（此前 extras 恒丢）")
	}
	firstTokenMs := int64(1234)
	responseCapture.FinalizeExtended(gatewaypreauth.AuditFinalizeInput{
		Outcome:    "success",
		Success:    true,
		StatusCode: 200,
	}, gatewayresponse.AuditFinalizeExtras{FirstTokenMs: &firstTokenMs, AccountID: "acc-first-token"})
	// 完成信号未到前 flush 挂起；信号到达后投递（生产 defer 顺序）。
	if logs := dispatcher.snapshot(); len(logs) != 0 {
		t.Fatalf("完成信号前不得投递审计，实际 %d 条", len(logs))
	}
	subject.complete(1728000000300)

	logs := dispatcher.snapshot()
	if len(logs) != 1 {
		t.Fatalf("审计投递 = %d 条, want 1（扩展面与 Finalize 二选一，不得双写）", len(logs))
	}
	if logs[0].FirstTokenMs == nil || *logs[0].FirstTokenMs != 1234 {
		t.Fatalf("first_token_ms = %v, want 1234", logs[0].FirstTokenMs)
	}
	if logs[0].AccountID != "acc-first-token" {
		t.Fatalf("accountId = %q, want acc-first-token", logs[0].AccountID)
	}
}

// TestChainResponseAuditCaptureCompleteAttemptCarriesUpstreamHeaders 是 A4
// 修复前必红的守卫：响应层传入 http.Header 时 attempt 的 upstream_response
// payload 必须携带头部（键保持 http.Header 规范化形态，每键取首值）；
// map[string]any 分支保持透传。
func TestChainResponseAuditCaptureCompleteAttemptCarriesUpstreamHeaders(t *testing.T) {
	// http.Header 分支。
	dispatcher := &mergeCapturingAuditDispatcher{}
	chain := auditGapChain(dispatcher, nil)
	subject := newGatewayHTTPCompletion()
	capture := chain.newAuditCapture(mergeRouteRequest("glm-5.3-flash"), "trace_attempt_headers", 1728000000000, subject)
	t.Cleanup(func() { gatewaypreauth.CancelAuditCapture(capture) })
	concrete := auditCaptureConcrete(capture)
	responseCapture := responseAuditCaptureOf(capture)
	attemptID := concrete.StartAttempt(gatewayusage.StartAttemptInput{
		Account:      usageModelAccountOf(newAccountingCandidate()),
		AttemptIndex: 0,
		UpstreamURL:  "https://upstream.example/v1/chat/completions",
		Method:       "POST",
	})
	responseCapture.CompleteAttempt(attemptID, gatewayresponse.AttemptAuditInput{
		StatusCode: 502,
		ResponseHeaders: http.Header{
			"Content-Type":   []string{"application/json"},
			"X-Request-Id":   []string{"req-1", "req-2"},
			"X-Rate-Limited": []string{"true"},
		},
		Success:      false,
		ErrorPhase:   "upstream_response",
		ErrorMessage: "upstream boom",
	})
	subject.complete(1728000000400)
	concrete.Finalize(gatewayusage.FinalizeAuditInput{Success: false, ErrorPhase: "upstream_response"})

	logs := dispatcher.snapshot()
	if len(logs) != 1 {
		t.Fatalf("审计投递 = %d 条, want 1", len(logs))
	}
	var upstreamHeaders map[string]any
	for _, payload := range logs[0].Payloads {
		if payload.PartType == gatewayusage.AuditPartUpstreamResponse {
			upstreamHeaders = payload.Headers
			break
		}
	}
	if upstreamHeaders == nil {
		t.Fatal("upstream_response payload 缺失（此前 http.Header 分支恒丢）")
	}
	if upstreamHeaders["Content-Type"] != "application/json" {
		t.Fatalf("Content-Type = %v, want application/json（规范化键形态）", upstreamHeaders["Content-Type"])
	}
	if upstreamHeaders["X-Request-Id"] != "req-1" {
		t.Fatalf("X-Request-Id = %v, want req-1（每键取首值）", upstreamHeaders["X-Request-Id"])
	}
	if upstreamHeaders["X-Rate-Limited"] != "true" {
		t.Fatalf("X-Rate-Limited = %v, want true", upstreamHeaders["X-Rate-Limited"])
	}

	// map[string]any 分支保持透传。
	dispatcher2 := &mergeCapturingAuditDispatcher{}
	chain2 := auditGapChain(dispatcher2, nil)
	subject2 := newGatewayHTTPCompletion()
	capture2 := chain2.newAuditCapture(mergeRouteRequest("glm-5.3-flash"), "trace_attempt_headers_map", 1728000000000, subject2)
	t.Cleanup(func() { gatewaypreauth.CancelAuditCapture(capture2) })
	concrete2 := auditCaptureConcrete(capture2)
	responseCapture2 := responseAuditCaptureOf(capture2)
	attemptID2 := concrete2.StartAttempt(gatewayusage.StartAttemptInput{
		Account:      usageModelAccountOf(newAccountingCandidate()),
		AttemptIndex: 0,
		UpstreamURL:  "https://upstream.example/v1/chat/completions",
		Method:       "POST",
	})
	responseCapture2.CompleteAttempt(attemptID2, gatewayresponse.AttemptAuditInput{
		StatusCode:      502,
		ResponseHeaders: map[string]any{"retry-after": "30"},
		Success:         false,
	})
	subject2.complete(1728000000500)
	concrete2.Finalize(gatewayusage.FinalizeAuditInput{Success: false})
	for _, payload := range dispatcher2.snapshot()[0].Payloads {
		if payload.PartType == gatewayusage.AuditPartUpstreamResponse {
			if payload.Headers["retry-after"] != "30" {
				t.Fatalf("map 分支透传不符：%v", payload.Headers)
			}
			return
		}
	}
	t.Fatal("map[string]any 分支的 upstream_response payload 缺失")
}

// gatewayFailureUsageCapture 是 gatewayresponse.FailureUsageRecorder 替身。
type gatewayFailureUsageCapture struct {
	mu      sync.Mutex
	records []gatewayresponse.FailureUsageRecordInput
}

func (c *gatewayFailureUsageCapture) RecordGatewayFailure(input gatewayresponse.FailureUsageRecordInput) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, input)
}

func (c *gatewayFailureUsageCapture) snapshot() []gatewayresponse.FailureUsageRecordInput {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]gatewayresponse.FailureUsageRecordInput(nil), c.records...)
}

// TestSinkFailureUsageCarriesRequestFactsAndCompletion 是 A2 sink 侧 + A5
// sink 侧修复前必红的守卫：SendGatewayFailureResponse 必须从请求事实填充
// Model / Stream，且 CompletedAtMs 取请求上下文 subject 的真实完成时刻
// （此前 HTTPCompletion 缺席退化为 nowMs、Model/Stream 不传）。
func TestSinkFailureUsageCarriesRequestFactsAndCompletion(t *testing.T) {
	body := `{"model":"glm-5.3-flash","stream":true}`
	stream := true
	model := "glm-5.3-flash"
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(httpReq)
	req.Body = &gatewaybody.Request{
		RawBody: []byte(body),
		State:   &gatewaybody.BodyState{Model: &model, Stream: &stream},
	}
	subject := newGatewayHTTPCompletion()
	req.HTTP = req.HTTP.WithContext(withChainHTTPCompletionKey(req.HTTP.Context(), subject))

	usage := &gatewayFailureUsageCapture{}
	sink := gatewayresponse.NewSink(gatewayresponse.SinkDeps{
		UsageRecords:   usage,
		HTTPCompletion: chainHTTPCompletionObserver{},
		NowMs:          func() int64 { return 90000 },
	})
	chain := &gatewayChain{}
	auditCapture := chain.newAuditCapture(req, "trace_sink_completion", 1000, subject)
	t.Cleanup(func() { gatewaypreauth.CancelAuditCapture(auditCapture) })
	res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())
	recordUsage := true
	sink.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             req,
		Res:             res,
		AuditCapture:    auditCapture,
		UsageContext:    gatewaypreauthGatewayFailureContext("trace_sink_completion"),
		StartedAt:       1000,
		StatusCode:      429,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf("请求过于频繁", "rate_limit_exceeded"),
		Audit:           gatewaypreauth.FailureAudit{Outcome: "gateway_failed", ErrorPhase: "quota"},
		RecordUsage:     &recordUsage,
	})
	subject.complete(9000)

	deadline := time.Now().Add(2 * time.Second)
	for len(usage.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	records := usage.snapshot()
	if len(records) != 1 {
		t.Fatalf("失败 usage = %d 条, want 1", len(records))
	}
	record := records[0]
	if record.Model != "glm-5.3-flash" {
		t.Fatalf("Model = %q, want glm-5.3-flash", record.Model)
	}
	if !record.Stream {
		t.Fatal("Stream = false, want true（请求体 stream:true）")
	}
	if record.CompletedAtMs != 9000 {
		t.Fatalf("CompletedAtMs = %d, want 9000（subject 完成时刻，非 nowMs 兜底）", record.CompletedAtMs)
	}
}

// TestUsageDispatchAdapterRecordGatewayFailureCarriesRequestFacts 是 A5
// adapter 侧修复前必红的守卫：RecordGatewayFailure 必须把 model / stream /
// failure_attribution / response_snapshot 透传给 usage 服务（真实 Service +
// FinalizationDispatch 投递），网关策略失败行的 model 不再恒空。
func TestUsageDispatchAdapterRecordGatewayFailureCarriesRequestFacts(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	dispatch := gatewayusage.NewFinalizationDispatch(recorder, nil, 64, 1)
	service := gatewayusage.NewService(dispatch, gatewayusage.ServiceConfig{SyncPricingAllowed: true}).
		WithUsageSemantics(chainUsageSemanticResolver{}).
		WithDefaultProviderCode(chainUsageDefaultProviderCode{})
	adapter := usageDispatchAdapter{service: service}

	snapshotBody := `{"error":{"message":"配额已用尽","code":"insufficient_quota"}}`
	adapter.RecordGatewayFailure(gatewayresponse.FailureUsageRecordInput{
		UsageContext:  gatewaypreauthGatewayFailureContext("trace_adapter_failure"),
		Model:         "glm-5.3-flash",
		Stream:        true,
		StatusCode:    502,
		StartedAtMs:   1728000000000,
		CompletedAtMs: 1728000000500,
		ResponsePayload: gatewayresponse.GatewayErrorPayloadCarrier{
			Error: map[string]any{"message": "upstream bad gateway", "type": "upstream_error"},
		},
		ErrorMessage:       "upstream bad gateway",
		FailureAttribution: "gateway_policy",
		ResponseSnapshot: &gatewayresponse.UsageResponseSnapshotView{
			StatusCode:   502,
			Headers:      map[string]string{"content-type": "application/json; charset=utf-8"},
			BodyText:     snapshotBody,
			ErrorMessage: "upstream bad gateway",
			GeneratedBy:  "gateway",
		},
	})
	waitForUsageDispatch(t, dispatch)

	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.Model != "glm-5.3-flash" {
		t.Fatalf("Model = %q, want glm-5.3-flash（此前 adapter 不透传，行内恒空）", record.Model)
	}
	if record.Stream == nil || !*record.Stream {
		t.Fatalf("Stream = %v, want true", record.Stream)
	}
	if record.FailureAttribution != "gateway_policy" {
		t.Fatalf("FailureAttribution = %q, want gateway_policy", record.FailureAttribution)
	}
	// capturingUsageRecorder 收到的是服务层原值（归一化在 MemoryUsageRecorder
	// 内部，不经 FinalizationDispatch）；bodyText 取测试专属文案以区分"透传
	// 视图"与"服务层回退构造"。
	snapshot, ok := record.ResponseSnapshot.(gatewayusage.UsageResponseSnapshot)
	if !ok {
		t.Fatalf("ResponseSnapshot 类型 = %T, want gatewayusage.UsageResponseSnapshot", record.ResponseSnapshot)
	}
	if snapshot.GeneratedBy != "gateway" || snapshot.ErrorMessage != "upstream bad gateway" ||
		snapshot.BodyText != snapshotBody {
		t.Fatalf("ResponseSnapshot 内容不符：%+v", snapshot)
	}
	if snapshot.StatusCode == nil || *snapshot.StatusCode != 502 {
		t.Fatalf("ResponseSnapshot.StatusCode = %v, want 502", snapshot.StatusCode)
	}
}

// withChainHTTPCompletionKey 测试侧把 subject 挂进请求上下文（对齐生产链
// 入口的传递方式）。
func withChainHTTPCompletionKey(ctx context.Context, subject *gatewayHTTPCompletion) context.Context {
	return context.WithValue(ctx, chainHTTPCompletionKey, subject)
}

// gatewaypreauthGatewayFailureContext 构造最小失败 usage 上下文。
func gatewaypreauthGatewayFailureContext(traceID string) gatewaypreauth.GatewayFailureUsageContext {
	return gatewaypreauth.GatewayFailureUsageContext{
		TraceID:         traceID,
		TrafficSource:   gatewayTrafficSource,
		SystemAccountID: "sys-audit-gap",
		Endpoint:        "POST /v1/chat/completions",
	}
}

// TestChainFinalizationUsageFailedAttemptCarriesAccountIdentity 是 A6 修复前
// 必红的守卫：失败尝试与成功路径同源——provider 身份取账号视图优先、协议
// 档案 id 与 usage_semantic 随账号档案解析、层级/档位直传 usage context。
func TestChainFinalizationUsageFailedAttemptCarriesAccountIdentity(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	usage := chainFinalizationUsage{
		recorder:             recorder,
		requestedModel:       "glm-5.3-flash",
		sourceEndpointFamily: gatewayopenai.FamilyChatCompletions,
		models:               usageModelResolverAdapter{},
	}
	statusCode := 502
	usage.RecordFailedUpstreamAttempt(gatewayresponse.FailedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID:                  "trace_failed_identity",
			ProviderCode:             "ctx-deepseek",
			RequestedServiceTier:     "flex",
			EffectiveServiceTier:     "priority",
			RequestedReasoningEffort: "low",
			EffectiveReasoningEffort: "high",
		},
		Account: gatewayresponse.OpenAIAccountView{Account: anthropicProtocolSecret()},
		// FailureAttribution 显式归因原样透传（input 字段存在）。
		FailureAttribution: gatewayusage.FailureAttributionDownstreamClosed,
		StatusCode:         &statusCode,
		ErrorMessage:       "上游 502",
	})
	records := recorder.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.ProviderCode != "anthropic-bridge" {
		t.Fatalf("ProviderCode = %q, want anthropic-bridge（账号视图优先于 context）", record.ProviderCode)
	}
	if record.ProviderProtocolProfileID != "anthropic-default" {
		t.Fatalf("ProviderProtocolProfileID = %q, want anthropic-default", record.ProviderProtocolProfileID)
	}
	if record.UsageSemantic != "anthropic" {
		t.Fatalf("UsageSemantic = %q, want anthropic（协议档案优先，此前恒 gateway_request）", record.UsageSemantic)
	}
	if record.RequestedServiceTier != "flex" || record.EffectiveServiceTier != "priority" ||
		record.RequestedReasoningEffort != "low" || record.EffectiveReasoningEffort != "high" {
		t.Fatalf("tier/effort 透传不符: requested=%q effective=%q reqEffort=%q effEffort=%q",
			record.RequestedServiceTier, record.EffectiveServiceTier,
			record.RequestedReasoningEffort, record.EffectiveReasoningEffort)
	}
	if record.FailureAttribution != gatewayusage.FailureAttributionDownstreamClosed {
		t.Fatalf("FailureAttribution = %q, want downstream_closed", record.FailureAttribution)
	}
}
