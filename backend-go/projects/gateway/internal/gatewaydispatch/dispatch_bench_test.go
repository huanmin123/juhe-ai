package gatewaydispatch

// R5 性能基准（docs/plans/计划-20260918T064119294Z-后端架构与性能优化改革.md
// 波次 R5）：/v1 网关链 dispatch 侧热路径的 go test -bench 基线。
//
// 可重放约束：固定输入（循环外构造一次）、无真实时间等待（happy path 无
// sleep/定时器触发）、随机源不参与；每个 Benchmark 在循环外做一次正确性
// sanity check，避免测到被优化的空路径。
//
// 注意：本机初测存在并行负载，数字供热点排序参考；正式基线需空载复测。

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// benchSink / benchSinkMap 防止纯函数调用被编译器消除。
var (
	benchSink    []byte
	benchSinkMap map[string]any
	benchOutcome CandidateFilterOutput
	benchPrepRes gatewaypreauth.DispatchPreparationResult
)

// benchDispatchAccounts 是候选过滤/准备基准共享的 8 账户固定候选集。
func benchDispatchAccounts() []AccountCandidate {
	return testAccounts("a-1", "a-2", "a-3", "a-4", "a-5", "a-6", "a-7", "a-8")
}

// BenchmarkDispatchFetchFirstAvailableUpstreamHappyPath 量化 /v1 全链主入口：
// 候选过滤 → 账户准备 → attempt 生命周期 → loopback 上游传输 → 成功收尾。
// per-iteration 需要 fresh RequestCoordinationContext（attempt tracker 会拒绝
// 重复 identity 注册），用 StopTimer/StartTimer 移出计量区。
func BenchmarkDispatchFetchFirstAvailableUpstreamHappyPath(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-bench-ok"}`))
	}))
	defer server.Close()

	engine, driver, _ := benchNewEngine()
	driver.urlByAccount = map[string][]string{
		"a-1": {server.URL + "/v1/chat/completions"},
	}
	accounts := testAccounts("a-1")
	req := benchNewRequest(`{"model":"gpt-test","stream":false}`)

	// sanity：单账户成功链路正确（URL / 账户 / 状态），避免测到空路径。
	sanity, err := engine.FetchFirstAvailableUpstream(context.Background(), FetchFirstAvailableUpstreamArgs{
		Req:                         req,
		Accounts:                    accounts,
		Settings:                    gatewaySettingsForTest(),
		UsageContext:                testUsageContext(),
		AuditCapture:                benchAuditCapture,
		Signal:                      context.Background(),
		RequestLane:                 "text",
		AccountStateMutationEnabled: true,
		RequestCoordination:         benchNewCoordination(),
		WaitForRecoverableFailures:  true,
	})
	if err != nil {
		b.Fatalf("sanity dispatch: %v", err)
	}
	if !sanity.Response.OK() || sanity.Account.ID != "a-1" || sanity.UpstreamURL != server.URL+"/v1/chat/completions" {
		b.Fatalf("sanity result: ok=%v account=%s url=%s", sanity.Response.OK(), sanity.Account.ID, sanity.UpstreamURL)
	}
	sanity.ReleaseConcurrency()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		args := FetchFirstAvailableUpstreamArgs{
			Req:                         req,
			Accounts:                    accounts,
			Settings:                    gatewaySettingsForTest(),
			UsageContext:                testUsageContext(),
			AuditCapture:                benchAuditCapture,
			Signal:                      context.Background(),
			RequestLane:                 "text",
			AccountStateMutationEnabled: true,
			RequestCoordination:         benchNewCoordination(),
			WaitForRecoverableFailures:  true,
		}
		b.StartTimer()
		result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
		if err != nil {
			b.Fatalf("dispatch: %v", err)
		}
		if !result.Response.OK() {
			b.Fatalf("dispatch status = %d", result.Response.Status())
		}
		result.ReleaseConcurrency()
	}
}

// BenchmarkDispatchFilterOpenAIGatewayRequestCandidateAccounts 量化候选选择：
// 模型过滤 → 能力兼容 → 本地屏蔽预检 → 降级/亲和/配额排序（全 fake、纯内存）。
// 输入固定一次复用；fake 端口与 nop 审计在 happy path 均零状态增长。
func BenchmarkDispatchFilterOpenAIGatewayRequestCandidateAccounts(b *testing.B) {
	engine, _, _ := benchNewEngine()
	pipeline := NewCandidatePipeline(engine)
	accounts := benchDispatchAccounts()
	args := CandidateFilterArgs{
		Req:                  benchNewRequest(`{"model":"gpt-test","stream":true}`),
		AuditCapture:         benchAuditCapture,
		UsageContext:         testUsageContext(),
		StartedAt:            gatewayupstream.NowMs(),
		RawCandidateAccounts: accounts,
		SystemAccountID:      "system-1",
		APIKeyID:             "apikey-1",
		GroupID:              "group-1",
		ClientIP:             "203.0.113.9",
		Endpoint:             "/v1/chat/completions",
		RouteCoordinator:     benchRouteCoordinator{},
	}

	output, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
	if err != nil {
		b.Fatalf("sanity filter: %v", err)
	}
	if output.Outcome != gatewaypreauth.CandidateOutcomeAccounts || len(output.Accounts) != len(accounts) {
		b.Fatalf("sanity outcome=%q accounts=%d", output.Outcome, len(output.Accounts))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchOutcome, err = pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
		if err != nil || len(benchOutcome.Accounts) != len(accounts) {
			b.Fatalf("filter: err=%v accounts=%d", err, len(benchOutcome.Accounts))
		}
	}
}

// BenchmarkDispatchPrepareDispatchAccounts 量化账户准备覆盖层：配额批量检查 →
// 容量/并发槽 → 高并发与客户端 IP 并发门控 → 热度质量排序（全 fake、纯内存）。
func BenchmarkDispatchPrepareDispatchAccounts(b *testing.B) {
	engine, _, _ := benchNewEngine()
	pipeline := NewCandidatePipeline(engine)
	accounts := benchDispatchAccounts()
	input := gatewaypreauth.DispatchPreparationInput{
		Req:               benchNewRequest(`{"model":"gpt-test","stream":true}`),
		AuditCapture:      benchNopAuditContext{},
		UsageContext:      testUsageContext(),
		StartedAt:         gatewayupstream.NowMs(),
		CandidateAccounts: accounts,
		ModelPriority:     &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{}},
		GroupAccess:       gatewayruntimecache.GroupUsageAccessMetadata{},
		SystemAccountID:   "system-1",
		APIKeyID:          "apikey-1",
		GroupID:           "group-1",
		ClientStrategy:    gatewaypreauth.ClientStrategyContext{},
		RequestLane:       "text",
		ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{}),
		RouteCoordinator:  benchRouteCoordinator{},
		Signal:            context.Background(),
	}

	prepared, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		b.Fatalf("sanity prepare: %v", err)
	}
	if prepared.Outcome != gatewaypreauth.CandidateOutcomeAccounts || len(prepared.Accounts) != len(accounts) {
		b.Fatalf("sanity outcome=%q accounts=%d", prepared.Outcome, len(prepared.Accounts))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchPrepRes, err = pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil || len(benchPrepRes.Accounts) != len(accounts) {
			b.Fatalf("prepare: err=%v accounts=%d", err, len(benchPrepRes.Accounts))
		}
	}
}

// BenchmarkDispatchNormalizeOpenAIReasoningFieldsChat 量化请求体 JSON 编解码
// 热路径的改写分支：chat 请求嵌套 reasoning.effort 拉平为 reasoning_effort
// （decode → mutate → encode 全量执行）。
func BenchmarkDispatchNormalizeOpenAIReasoningFieldsChat(b *testing.B) {
	body := []byte(`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}],"reasoning":{"effort":"high"}}`)

	out := NormalizeOpenAIReasoningFieldsForUpstream("/v1/chat/completions", body)
	if bytes.Equal(out, body) || !bytes.Contains(out, []byte(`"reasoning_effort":"high"`)) {
		b.Fatalf("sanity rewrite failed: %s", out)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSink = NormalizeOpenAIReasoningFieldsForUpstream("/v1/chat/completions", body)
		if len(benchSink) == 0 {
			b.Fatal("empty body")
		}
	}
}

// BenchmarkDispatchNormalizeOpenAIReasoningFieldsPassthrough 量化同函数的
// 无需改写分支：无 reasoning 字段时应原样返回（衡量早退前的 JSON decode 成本）。
func BenchmarkDispatchNormalizeOpenAIReasoningFieldsPassthrough(b *testing.B) {
	body := []byte(`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	out := NormalizeOpenAIReasoningFieldsForUpstream("/v1/chat/completions", body)
	if !bytes.Equal(out, body) {
		b.Fatalf("sanity passthrough violated: %s", out)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSink = NormalizeOpenAIReasoningFieldsForUpstream("/v1/chat/completions", body)
	}
}

// BenchmarkDispatchApplyGptAccountRequestOverridesBody 量化账户请求覆盖层：
// credentials 覆盖读取 → 值断言 → 能力门控 → clone 后 chat 分支改写。
// 函数内部 cloneJSONObject，固定输入 map 复用安全。
func BenchmarkDispatchApplyGptAccountRequestOverridesBody(b *testing.B) {
	body := map[string]any{
		"model":     "gpt-test",
		"stream":    true,
		"input":     "hi",
		"reasoning": map[string]any{"effort": "low"},
	}
	input := GptAccountOverrideInput{
		Credentials:    map[string]any{"service_tier_override": "priority", "reasoning_effort_override": "high"},
		EndpointFamily: "chat_completions",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{
			SupportedServiceTiers:     []string{"default", "priority", "flex"},
			SupportedReasoningEfforts: []string{"low", "medium", "high"},
		},
	}

	overridden, err := ApplyGptAccountRequestOverridesBody(body, input)
	if err != nil {
		b.Fatalf("sanity override: %v", err)
	}
	if overridden["service_tier"] != "priority" || overridden["reasoning_effort"] != "high" {
		b.Fatalf("sanity override = %#v", overridden)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSinkMap, err = ApplyGptAccountRequestOverridesBody(body, input)
		if err != nil || benchSinkMap == nil {
			b.Fatalf("override: err=%v", err)
		}
	}
}

// benchTargetHeader 是 CopyResponseHeaders 基准的固定写入目标（Set 语义为
// 替换，跨迭代零增长）。
var benchTargetHeader = http.Header{}

func benchSetHeader(name, value string) { benchTargetHeader.Set(name, value) }

// BenchmarkDispatchCopyResponseHeaders 量化上游响应头转发：Connection 令牌
// 解析 + 12 个上游头逐项拷贝（response 包 prepareUpstreamResponseForDownstream
// 的热路径依赖）。
func BenchmarkDispatchCopyResponseHeaders(b *testing.B) {
	upstreamHeader := http.Header{}
	upstreamHeader.Set("Content-Type", "application/json")
	upstreamHeader.Set("Connection", "keep-alive")
	upstreamHeader.Set("X-Request-Id", "req-bench")
	upstreamHeader.Set("Openai-Organization", "org-bench")
	upstreamHeader.Set("X-Model-Observed", "gpt-test")
	for i := 1; i <= 7; i++ {
		upstreamHeader.Set("X-Upstream-Extra-"+string(rune('0'+i)), "value")
	}
	response := &GatewayUpstreamResponse{Header: upstreamHeader}

	CopyResponseHeaders(response, benchSetHeader)
	if len(benchTargetHeader) == 0 {
		b.Fatal("sanity: no header copied")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CopyResponseHeaders(response, benchSetHeader)
	}
}
