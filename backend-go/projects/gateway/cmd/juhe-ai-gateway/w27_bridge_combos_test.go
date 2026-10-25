package main

// T27（BUG-0274 残余收口）：桥接七组合的阶段级行为验收
//（docs/functions/调度内核通用化设计.md §5.4 七组合清单 + D3「不得用 none 推断」）。
//
// oracle：真实 bridge preflight/restore（codexPreflightAdapter →
// gatewaycodex.ChatBridgeStateService / CompactPreflightService，组合根真实
// 装配）；阶段级观察点为 dispatch 准备链
// gatewaydispatch.Engine.BuildPreparedUpstreamRequestParts 的三阶段——
// Front（sanitize_inline，改写请求上下文）、Adapter（OAuth codex 适配器出站
// 构造，含无条件已处理标记写入）、Post（序列化 body 清理，先查
// IsGatewayCodexHistorySanitized）。
//
// 两个如实断言的结构现状（结构性等价的一部分，不得解读为「无需清理」）：
//  1. 清理算法 hook 生产恒 nil——gatewayoauthcodex.SanitizeCodexHistory 全仓
//     仅测试赋值；各组合分别以 hook 注入（取证阶段命中与调用次数）与
//     hook nil（生产现状 no-op、body 字节不因清理维度改变）两形态驱动。
//  2. 引擎侧 per-account 桥准备端口 engine.CodexBridge 已在本批（T32）接线，
//     与 preauth 共用同一 ChatBridgeStateService + registry；bridge restore 的
//     registry 产物会进入真实 dispatch 请求体（见 TestW32EngineBridgeWired...）。
//     未经过 preflight 的 fresh request 在 registry miss 时保持 no-op。
//
// 逐组合结论（与 docs/bug/问题-0274-Codex桥状态从未装配.md 验收记录对应）：
//  1. Responses 新会话 → Chat bridge → API Key：可达；Front+Post 各 1 次、
//     Adapter 0 次、出站不写标记。
//  2. Responses 携带内部 previous_response_id（restore 成功）→ Chat bridge →
//     API Key：可达；恢复产物（合并 input、删 previous_response_id）经 T32
//     接线的 engine.CodexBridge 进入真实 dispatch body；清理阶段形态同组合 1。
//  3. Responses 新会话 → Chat bridge → OAuth：可达（映射使 OAuth 账户成
//     bridge-kind）；Adapter 1 次 + 无条件标记写入、Front/Post 0 次。
//  4. Responses 携带内部 previous_response_id（restore 成功）→ Chat bridge →
//     OAuth：同组合 4 恢复事实 + 组合 3 清理形态。
//  5. /responses/compact 恢复已有 bridge snapshot → API Key：可达且真实派发
//     成功（晚绑定 chainCompactSummaryDispatcher → 真实引擎 → httptest 上
//     游）；合成 chat 请求全阶段 preserve（0 次清理调用、出站不写标记）。
//  6. /responses/compact 恢复已有 bridge snapshot → OAuth：账户过滤层可达
//     （OAuth 账户按映射归 bridge-kind 并进入 bridge 派发窗），但真实派发在
//     OAuth codex 适配器 body 校验处必败（合成 chat body 无 input 字段 →
//     「请求体必须包含 input 字段」→ 502 codex_bridge_compact_summary_failed，
//     上游 0 请求、校验先于清理调用故 0 次）——OAuth 分支恒产出 Codex
//     Responses 形态出站，chat-bridge 合成摘要在该形态下语义不可达，如登记。
//  7. 恢复失败/无快照：外部 previous（无桥快照）→ preflight 继续、请求按
//     普通请求进入 API Key / OAuth 派发准备（API Key：Front+Post 2 次；
//     OAuth：Adapter 1 次 + 标记）；内部 previous 无快照 → preflight 以 404
//     codex_bridge_previous_response_not_found 终结（restore_failed），不继续
//     派发——源码事实与组合标题「继续普通请求」的差异如实登记。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayoauthcodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w27 组合验收的固定身份三元组（bridge boundary 按 sys/key/group 严格比对，
// 种子行与还原请求必须同形；ProviderCode 保持零值对齐生产 codexContextBoundary）。
const (
	w27SysID   = "sys-w27"
	w27KeyID   = "key-w27"
	w27GroupID = "group-w27"
	w27Model   = "gpt-5"
)

func w27Boundary() gatewaycodex.CodexContextStateBoundary {
	return gatewaycodex.CodexContextStateBoundary{SystemAccountID: w27SysID, APIKeyID: w27KeyID, GroupID: w27GroupID}
}

// w27AuditCapture 记录 label→metadata 与 finalize，供 restore/compact 审计
// oracle 断言（chainTestAuditCapture 只记 label，不满足本验收的取值断言）。
type w27AuditCapture struct {
	metadata  map[string]map[string]any
	finalizes []gatewaypreauth.AuditFinalizeInput
}

func (c *w27AuditCapture) BindContext(gatewaypreauth.AuditGatewayContext) {}

func (c *w27AuditCapture) AddGatewayMetadata(label string, value map[string]any) {
	if c.metadata == nil {
		c.metadata = make(map[string]map[string]any)
	}
	c.metadata[label] = value
}

func (c *w27AuditCapture) Finalize(input gatewaypreauth.AuditFinalizeInput) {
	c.finalizes = append(c.finalizes, input)
}

// w27SanitizeProbe 清理算法探针：每次调用向 items 追加序号标记项并返回
// Changed=true。阶段归属由标记落点取证——Front 落请求上下文、Post 落序列化
// 出站 body、Adapter 落 OAuth 归一化序列化 body（并伴随无条件标记写入）。
type w27SanitizeProbe struct {
	calls int
}

func (p *w27SanitizeProbe) sanitize(items []any, _ gatewayoauthcodex.SanitizeCodexHistoryOptions) gatewayoauthcodex.CodexHistorySanitizeResult {
	p.calls++
	probed := append(append([]any{}, items...), map[string]any{"w27_sanitize_probe_call": p.calls})
	return gatewayoauthcodex.CodexHistorySanitizeResult{Items: probed, Changed: true}
}

// w27NoopProbe 只计数不改写（用于标记去重与 nil 对照臂的干净形态）。
type w27NoopProbe struct {
	calls int
}

func (p *w27NoopProbe) sanitize(items []any, _ gatewayoauthcodex.SanitizeCodexHistoryOptions) gatewayoauthcodex.CodexHistorySanitizeResult {
	p.calls++
	return gatewayoauthcodex.CodexHistorySanitizeResult{Items: items, Changed: false}
}

// w27InstallSanitizeHook 注入全局清理算法 hook 并返回恢复函数（hook 为进程级
// 全局；本文件测试不使用 t.Parallel，串行运行与既有套件一致）。
func w27InstallSanitizeHook(t *testing.T, hook gatewayoauthcodex.SanitizeCodexHistoryItems) func() {
	t.Helper()
	orig := gatewayoauthcodex.SanitizeCodexHistory
	t.Cleanup(func() { gatewayoauthcodex.SanitizeCodexHistory = orig })
	gatewayoauthcodex.SanitizeCodexHistory = hook
	return func() { gatewayoauthcodex.SanitizeCodexHistory = orig }
}

// w27WiredChain 组装依赖齐备的真实组合链（真实 ChatBridgeStateService /
// CompactPreflightService / registry / 晚绑定合成摘要 dispatcher）。codex
// context 行存储恒 PostgreSQL（juhe_codex_context，pgtestkit 隔离库载具），
// 句柄与库生命周期由 w1CodexBridgeChainDeps 内注册的 t.Cleanup 收口。
func w27WiredChain(t *testing.T) (*gatewayChain, codexPreflightAdapter) {
	t.Helper()
	deps := w1CodexBridgeChainDeps(t, true)
	chain, shutdown, assembleErr := composeGatewayChain(deps)
	if assembleErr != nil {
		t.Fatalf("composeGatewayChain: %v", assembleErr)
	}
	t.Cleanup(shutdown)
	adapter, ok := chain.preauth.Codex.(codexPreflightAdapter)
	if !ok {
		t.Fatalf("chain.preauth.Codex 类型 = %T，want codexPreflightAdapter", chain.preauth.Codex)
	}
	if adapter.bridge == nil || adapter.compact == nil || adapter.registry == nil {
		t.Fatalf("wired 链必须装配真实桥：bridge=%v compact=%v registry=%v",
			adapter.bridge != nil, adapter.compact != nil, adapter.registry != nil)
	}
	return chain, adapter
}

// w27RequestWithRecorder 构造带 JSON body 的 GatewayRequest 并同时返回
// TrackingWriter 与底层 recorder（读取失败响应的状态码与报文）。
func w27RequestWithRecorder(method, path string, body map[string]any) (*gatewaypreauth.GatewayRequest, *gatewaypreauth.TrackingWriter, *httptest.ResponseRecorder) {
	raw := gatewaybody.SerializeGatewayJSONObject(body).Raw
	httpReq := httptest.NewRequest(method, path, bytes.NewReader(raw))
	httpReq.Header.Set("Content-Type", "application/json")
	req := gatewaypreauth.NewGatewayRequest(httpReq)
	req.Body = &gatewaybody.Request{RawBody: raw, Body: body}
	recorder := httptest.NewRecorder()
	return req, gatewaypreauth.NewTrackingWriter(recorder), recorder
}

// w27RunContextStatePreflight 以生产同型输入驱动真实 bridge context-state
// preflight（chain preflight.go:881 的调用形态）。
func w27RunContextStatePreflight(t *testing.T, adapter codexPreflightAdapter, req *gatewaypreauth.GatewayRequest, res gatewaypreauth.GatewayResponseWriter, audit *w27AuditCapture) (bool, error) {
	t.Helper()
	signal, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return adapter.ApplyContextStatePreflight(context.Background(), gatewaypreauth.CodexContextStateInput{
		Req:             req,
		Res:             res,
		AuditCapture:    audit,
		UsageContext:    gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace-w27", TrafficSource: gatewayTrafficSource},
		StartedAt:       1,
		SystemAccountID: w27SysID,
		APIKeyID:        w27KeyID,
		GroupID:         w27GroupID,
		Signal:          signal,
	})
}

// w27AccountModelMapping 是 bridge-kind 账户的 responses→chat_completions
// 显式映射（BUG-0178 桥映射形态）。
func w27AccountModelMapping() []gatewayruntimecache.AccountModelMapping {
	return []gatewayruntimecache.AccountModelMapping{{
		SourceModel:            w27Model,
		SourceEndpointFamily:   "responses",
		UpstreamModel:          "chat-model",
		UpstreamEndpointFamily: "chat_completions",
		Enabled:                true,
	}}
}

func w27APIKeyBridgeAccount(baseURL string) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-w27-apikey-bridge", ProtocolCode: "openai", ProtocolVersion: "v1",
		Type: "api_key", BaseURL: baseURL, APIKey: "sk-w27-apikey-bridge",
		ModelMappings: w27AccountModelMapping(),
	}
}

func w27OAuthBridgeAccount(baseURL string) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-w27-oauth-bridge", ProtocolCode: "openai", ProtocolVersion: "v1",
		Type: "oauth", VendorCode: "gpt", BaseURL: baseURL, APIKey: "w27-oauth-access-token",
		ModelMappings: w27AccountModelMapping(),
	}
}

func w27APIKeyNativeAccount(baseURL string) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-w27-apikey-native", ProtocolCode: "openai", ProtocolVersion: "v1",
		Type: "api_key", BaseURL: baseURL, APIKey: "sk-w27-apikey-native",
	}
}

func w27OAuthNativeAccount(baseURL string) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-w27-oauth-native", ProtocolCode: "openai", ProtocolVersion: "v1",
		Type: "oauth", VendorCode: "gpt", BaseURL: baseURL, APIKey: "w27-oauth-native-token",
	}
}

// w27SeedBridgeChainRound 经真实 CompletionHandler 落一行桥响应（row store +
// segments），返回可被后续请求 restore 的内部 previous_response_id。
func w27SeedBridgeChainRound(t *testing.T, adapter codexPreflightAdapter, account gatewayruntimecache.OpenAIAccountSecret) string {
	t.Helper()
	seedInput := []any{map[string]any{"type": "message", "role": "user", "content": "w27 seed turn"}}
	seedReq, _ := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model,
		"input": seedInput,
	})
	adapter.registry.Set(seedReq, &gatewaycodex.CodexResponsesContextRequestState{
		RequestKind:           gatewaycodex.RequestKindResponses,
		Boundary:              w27Boundary(),
		CanonicalBody:         map[string]any{"model": w27Model, "input": seedInput},
		CurrentInput:          seedInput,
		MaterializedInput:     seedInput,
		PreviousResponseKind:  gatewaycodex.PreviousKindNone,
		ActiveBridgeAccountID: account.ID,
	})
	handler := adapter.bridge.CompletionHandlerForRequest(adapter.registry, seedReq, account, "chat-model")
	if handler == nil {
		t.Fatal("ActiveBridgeAccountID 命中时 CompletionHandlerForRequest 必须返回非 nil（真实桥装配）")
	}
	handler(gatewaycodex.CodexResponsesChatBridgeCompletion{
		ResponseID:  "resp_chat_bridge_w27_seed1",
		CreatedAt:   time.Now(),
		Model:       "chat-model",
		OutputItems: []any{map[string]any{"type": "message", "role": "assistant", "content": "w27 seed answer"}},
	})
	return "resp_chat_bridge_w27_seed1"
}

// w27PrepareParts 驱动 dispatch 准备链三阶段（engine.CodexBridge 已接线；
// 未经 preflight 的请求在 registry miss 时保持 no-op）。
func w27PrepareParts(t *testing.T, chain *gatewayChain, req *gatewaypreauth.GatewayRequest, account gatewayruntimecache.OpenAIAccountSecret, compat string) gatewaydispatch.PreparedRequestParts {
	t.Helper()
	parts, err := chain.engine.BuildPreparedUpstreamRequestParts(context.Background(), req, account,
		gatewaypreauth.GatewayFailureUsageContext{SystemAccountID: w27SysID, APIKeyID: w27KeyID, GroupID: w27GroupID},
		compat)
	if err != nil {
		t.Fatalf("BuildPreparedUpstreamRequestParts: %v", err)
	}
	return parts
}

func w27ParseBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("出站 body 不是有效 JSON 对象: %v（%s）", err, raw)
	}
	return parsed
}

func w27BodyInputItems(t *testing.T, raw []byte) []any {
	t.Helper()
	items, _ := w27ParseBody(t, raw)["input"].([]any)
	return items
}

// w27CountProbeMarkers 统计出站 input 里的探针标记数：Front/Post/Adapter 每
// 次真实清理调用恰好留下一个序号标记。
func w27CountProbeMarkers(t *testing.T, items []any) int {
	t.Helper()
	count := 0
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			if _, hit := object["w27_sanitize_probe_call"]; hit {
				count++
			}
		}
	}
	return count
}

// w27AssertUnmarked 钉住 API Key 目标出站永不携带已处理标记（标记写入唯一
// 发生在 OAuth codex 适配器出站构造处）。
func w27AssertUnmarked(t *testing.T, raw []byte, stage string) {
	t.Helper()
	if gatewayoauthcodex.IsGatewayCodexHistorySanitized(raw) {
		t.Fatalf("%s 出站 body 不得携带已处理标记（%s 路径无标记写入点）", stage, stage)
	}
}

// ---------------------------------------------------------------------------
// 组合 1：Responses 新会话 → Chat bridge → API Key
// ---------------------------------------------------------------------------

func TestW27Combo1ResponsesNewSessionChatBridgeAPIKey(t *testing.T) {
	chain, adapter := w27WiredChain(t)
	account := w27APIKeyBridgeAccount("https://w27-upstream.invalid/v1")

	// ① 真实 bridge preflight oracle：新会话记录请求态、不完成请求。
	req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo1 turn"}},
	})
	audit := &w27AuditCapture{}
	completed, err := w27RunContextStatePreflight(t, adapter, req, res, audit)
	if err != nil || completed {
		t.Fatalf("新会话 preflight 必须 继续（completed=%v err=%v）", completed, err)
	}
	state, ok := adapter.registry.Get(req)
	if !ok || state.RequestKind != gatewaycodex.RequestKindResponses ||
		state.PreviousResponseKind != gatewaycodex.PreviousKindNone || state.Restored {
		t.Fatalf("registry 请求态 = %+v ok=%v，want responses/none/未恢复", state, ok)
	}
	if audit.metadata["codex_responses_chat_bridge_state"]["mode"] != "new_session" {
		t.Fatalf("preflight 审计 = %+v，want new_session", audit.metadata)
	}

	// ② per-account 桥准备 oracle：引擎端口已接线到同一真实桥服务。
	if chain.engine.CodexBridge == nil {
		t.Fatal("生产组合根 engine.CodexBridge 必须装配真实桥（T32）")
	}
	explicitBridge, err := adapter.bridge.PrepareCodexResponsesContextForAccount(adapter.registry, req, account)
	if err != nil || !explicitBridge {
		t.Fatalf("桥账户准备 = %v, %v；want explicitBridge=true", explicitBridge, err)
	}
	if state.ActiveBridgeAccountID != account.ID {
		t.Fatalf("ActiveBridgeAccountID = %q，want %q", state.ActiveBridgeAccountID, account.ID)
	}

	// ③ 阶段级取证（hook 注入）：策略行 Codex→API Key = Front sanitize_inline +
	// Post sanitize + Adapter preserve → Front 1 次 + Post 1 次 + Adapter 0 次。
	probe := &w27SanitizeProbe{}
	uninstall := w27InstallSanitizeHook(t, probe.sanitize)

	freshReq, _ := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo1 turn"}},
	})
	parts := w27PrepareParts(t, chain, freshReq, account, "codex_responses")
	if probe.calls != 2 {
		t.Fatalf("清理调用次数 = %d，want 2（Front+Post，Adapter 在 API Key 路径不存在）", probe.calls)
	}
	markers := w27CountProbeMarkers(t, w27BodyInputItems(t, parts.Body))
	if markers != 2 {
		t.Fatalf("出站 input 探针标记数 = %d，want 2（第 1 个来自 Front 请求上下文改写、第 2 个来自 Post 序列化改写）", markers)
	}
	w27AssertUnmarked(t, parts.Body, "API Key 桥")

	// ④ 生产现状（hook 恒 nil）：清理 no-op、input 语义原样、依旧不写标记。
	uninstall()
	freshReq2, _ := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo1 turn"}},
	})
	parts2 := w27PrepareParts(t, chain, freshReq2, account, "codex_responses")
	items2 := w27BodyInputItems(t, parts2.Body)
	if len(items2) == 0 || w27CountProbeMarkers(t, items2) != 0 {
		t.Fatalf("nil hook 下出站 input = %v，want 原样单条且无探针标记", items2)
	}
	w27AssertUnmarked(t, parts2.Body, "API Key 桥（nil hook）")
}

// ---------------------------------------------------------------------------
// 组合 2：Responses 携带内部 previous_response_id（restore 成功）→ Chat bridge → API Key
// ---------------------------------------------------------------------------

func TestW27Combo2ResponsesInternalPreviousChatBridgeAPIKey(t *testing.T) {
	chain, adapter := w27WiredChain(t)
	account := w27APIKeyBridgeAccount("https://w27-upstream.invalid/v1")
	prevID := w27SeedBridgeChainRound(t, adapter, account)

	// ① 真实 bridge restore oracle：内部链恢复（row store + segments 全真实）。
	req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model, "previous_response_id": prevID,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo2 turn"}},
	})
	audit := &w27AuditCapture{}
	completed, err := w27RunContextStatePreflight(t, adapter, req, res, audit)
	if err != nil || completed {
		t.Fatalf("内部链恢复 preflight 必须继续（completed=%v err=%v）", completed, err)
	}
	state, _ := adapter.registry.Get(req)
	if !state.Restored || state.PreviousResponseKind != gatewaycodex.PreviousKindInternal {
		t.Fatalf("恢复态 = restored=%v prevKind=%q，want true/internal", state.Restored, state.PreviousResponseKind)
	}
	restoredInput, _ := state.MaterializedInput.([]any)
	if len(restoredInput) < 2 {
		t.Fatalf("恢复合并 input 长度 = %d，want ≥2（种子 + 当前轮）", len(restoredInput))
	}
	if audit.metadata["codex_responses_chat_bridge_state"]["mode"] != "restored" {
		t.Fatalf("preflight 审计 = %+v，want restored", audit.metadata)
	}

	// ② 桥准备 oracle：合并 input 写回 body、内部 previous_response_id 删除。
	explicitBridge, err := adapter.bridge.PrepareCodexResponsesContextForAccount(adapter.registry, req, account)
	if err != nil || !explicitBridge {
		t.Fatalf("桥账户准备 = %v, %v；want explicitBridge=true", explicitBridge, err)
	}
	preparedBody := w27ParseBody(t, gatewaybody.SerializeGatewayJSONObject(req.Body.Body.(map[string]any)).Raw)
	if _, still := preparedBody["previous_response_id"]; still {
		t.Fatal("内部链桥准备必须删除 previous_response_id（真实桥服务行为）")
	}
	if items, _ := preparedBody["input"].([]any); len(items) != len(restoredInput) {
		t.Fatalf("桥准备后 body input 长度 = %d，want 恢复合并长度 %d", len(items), len(restoredInput))
	}

	// ③ 派发准备链阶段取证（生产形态：引擎端口 nil → 恢复产物不落 dispatch body）。
	probe := &w27SanitizeProbe{}
	_ = w27InstallSanitizeHook(t, probe.sanitize)
	freshReq, _ := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model, "previous_response_id": prevID,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo2 turn"}},
	})
	parts := w27PrepareParts(t, chain, freshReq, account, "codex_responses")
	if probe.calls != 2 {
		t.Fatalf("清理调用次数 = %d，want 2（Front+Post）", probe.calls)
	}
	dispatchBody := w27ParseBody(t, parts.Body)
	if _, still := dispatchBody["previous_response_id"]; !still {
		t.Fatal("未经 preflight 的 fresh request 在 registry miss 时应保持原 body")
	}
	w27AssertUnmarked(t, parts.Body, "API Key 桥恢复")
}

// ---------------------------------------------------------------------------
// 组合 3：Responses 新会话 → Chat bridge → OAuth
// ---------------------------------------------------------------------------

func TestW27Combo3ResponsesNewSessionChatBridgeOAuth(t *testing.T) {
	chain, adapter := w27WiredChain(t)
	account := w27OAuthBridgeAccount("https://w27-upstream.invalid/v1")

	// ① 真实 bridge preflight oracle（新会话）。
	req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo3 turn"}},
	})
	audit := &w27AuditCapture{}
	completed, err := w27RunContextStatePreflight(t, adapter, req, res, audit)
	if err != nil || completed {
		t.Fatalf("新会话 preflight 必须继续（completed=%v err=%v）", completed, err)
	}
	state, _ := adapter.registry.Get(req)
	if state.RequestKind != gatewaycodex.RequestKindResponses || state.PreviousResponseKind != gatewaycodex.PreviousKindNone {
		t.Fatalf("registry 请求态 kind=%q prevKind=%q", state.RequestKind, state.PreviousResponseKind)
	}

	// ② 桥准备 oracle：responses→chat 映射使 OAuth 账户成 bridge-kind。
	explicitBridge, err := adapter.bridge.PrepareCodexResponsesContextForAccount(adapter.registry, req, account)
	if err != nil || !explicitBridge {
		t.Fatalf("OAuth 桥账户准备 = %v, %v；want explicitBridge=true（映射判定与账户类型无关）", explicitBridge, err)
	}

	// ③ 阶段级取证（hook 注入）：策略行 Codex→OAuth = Adapter sanitize →
	// 恰 1 次调用，落在 OAuth 归一化序列化 body 上，并伴随无条件标记写入。
	probe := &w27SanitizeProbe{}
	uninstall := w27InstallSanitizeHook(t, probe.sanitize)
	freshReq, _ := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo3 turn"}},
	})
	parts := w27PrepareParts(t, chain, freshReq, account, "codex_responses")
	if probe.calls != 1 {
		t.Fatalf("清理调用次数 = %d，want 1（Adapter；Front/Post 均preserve）", probe.calls)
	}
	if requestItems, ok := freshReq.Body.Body.(map[string]any)["input"].([]any); ok && w27CountProbeMarkers(t, requestItems) != 0 {
		t.Fatal("Front=preserve：请求上下文不得被清理改写（唯一调用必须落在适配器序列化 body）")
	}
	if !gatewayoauthcodex.IsGatewayCodexHistorySanitized(parts.Body) {
		t.Fatal("OAuth 适配器出站必须写入已处理标记（SanitizeCodexHistory=true 无条件写）")
	}
	oauthBody := w27ParseBody(t, parts.Body)
	if oauthBody["store"] != false || oauthBody["stream"] != true {
		t.Fatalf("OAuth codex 出站形态 store=%v stream=%v，want false/true（Codex 归一化）", oauthBody["store"], oauthBody["stream"])
	}
	if markers := w27CountProbeMarkers(t, oauthBody["input"].([]any)); markers != 1 {
		t.Fatalf("适配器清理标记数 = %d，want 1（hook 结果序列化进出站 body）", markers)
	}
	uninstall()

	// ④ 生产现状（hook 恒 nil）：算法 no-op 但标记照写——「hook 恒 nil 下的
	// 结构性等价」的直接证据（清理维度对出站字节无贡献，标记维度独立生效）。
	freshReq2, _ := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo3 turn"}},
	})
	parts2 := w27PrepareParts(t, chain, freshReq2, account, "codex_responses")
	if !gatewayoauthcodex.IsGatewayCodexHistorySanitized(parts2.Body) {
		t.Fatal("hook 恒 nil 下已处理标记仍必须写入（与算法无关的无条件行为）")
	}
	nilBody := w27ParseBody(t, parts2.Body)
	if nilBody["store"] != false || nilBody["stream"] != true {
		t.Fatalf("nil hook 出站形态 store=%v stream=%v", nilBody["store"], nilBody["stream"])
	}
	if items := nilBody["input"].([]any); len(items) != 1 || w27CountProbeMarkers(t, items) != 0 {
		t.Fatalf("nil hook 下 input 必须原样（无改写、无探针标记），got %v", items)
	}
}

// ---------------------------------------------------------------------------
// 组合 4：Responses 携带内部 previous_response_id（restore 成功）→ Chat bridge → OAuth
// ---------------------------------------------------------------------------

func TestW27Combo4ResponsesInternalPreviousChatBridgeOAuth(t *testing.T) {
	chain, adapter := w27WiredChain(t)
	account := w27OAuthBridgeAccount("https://w27-upstream.invalid/v1")
	prevID := w27SeedBridgeChainRound(t, adapter, account)

	// ① 真实 bridge restore oracle（同组合 2 的恢复面）。
	req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model, "previous_response_id": prevID,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo4 turn"}},
	})
	audit := &w27AuditCapture{}
	completed, err := w27RunContextStatePreflight(t, adapter, req, res, audit)
	if err != nil || completed {
		t.Fatalf("内部链恢复 preflight 必须继续（completed=%v err=%v）", completed, err)
	}
	state, _ := adapter.registry.Get(req)
	if !state.Restored || state.PreviousResponseKind != gatewaycodex.PreviousKindInternal {
		t.Fatalf("恢复态 = restored=%v prevKind=%q", state.Restored, state.PreviousResponseKind)
	}
	restoredInput, _ := state.MaterializedInput.([]any)

	// ② 桥准备 oracle：合并 input + 删内部 previous_response_id（OAuth 桥账户同形态）。
	explicitBridge, err := adapter.bridge.PrepareCodexResponsesContextForAccount(adapter.registry, req, account)
	if err != nil || !explicitBridge {
		t.Fatalf("OAuth 桥账户准备 = %v, %v", explicitBridge, err)
	}
	preparedBody := w27ParseBody(t, gatewaybody.SerializeGatewayJSONObject(req.Body.Body.(map[string]any)).Raw)
	if _, still := preparedBody["previous_response_id"]; still {
		t.Fatal("内部链桥准备必须删除 previous_response_id")
	}
	if items, _ := preparedBody["input"].([]any); len(items) != len(restoredInput) {
		t.Fatalf("桥准备后 input 长度 = %d，want %d", len(items), len(restoredInput))
	}

	// ③ 派发准备链阶段取证：Adapter 1 次 + 标记写入；fresh request 未经
	// preflight，registry miss 保持原 body。
	probe := &w27SanitizeProbe{}
	_ = w27InstallSanitizeHook(t, probe.sanitize)
	freshReq, _ := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model, "previous_response_id": prevID,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo4 turn"}},
	})
	parts := w27PrepareParts(t, chain, freshReq, account, "codex_responses")
	if probe.calls != 1 {
		t.Fatalf("清理调用次数 = %d，want 1（Adapter）", probe.calls)
	}
	if !gatewayoauthcodex.IsGatewayCodexHistorySanitized(parts.Body) {
		t.Fatal("OAuth 出站必须携带已处理标记")
	}
	dispatchBody := w27ParseBody(t, parts.Body)
	if _, still := dispatchBody["previous_response_id"]; !still {
		t.Fatal("未经 preflight 的 fresh request 在 registry miss 时应保持原 body")
	}
}

// ---------------------------------------------------------------------------
// 组合 5：/responses/compact 恢复已有 bridge snapshot → API Key（真实派发）
// ---------------------------------------------------------------------------

// w27CompactUpstream 记录合成摘要上游收到的完整出站（含原始字节，供已处理
// 标记的端到端取证）。
type w27CompactUpstream struct {
	server    *httptest.Server
	reqCount  int
	gotMethod string
	gotPath   string
	gotAuth   string
	gotRaw    []byte
	payload   string
	status    int
}

func w27NewCompactUpstream(t *testing.T, status int, payload string) *w27CompactUpstream {
	t.Helper()
	upstream := &w27CompactUpstream{payload: payload, status: status}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.reqCount++
		upstream.gotMethod = r.Method
		upstream.gotPath = r.URL.Path
		upstream.gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		upstream.gotRaw = raw
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upstream.status)
		_, _ = io.WriteString(w, upstream.payload)
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// w27CompactDispatchCoordination 镜像生产 compact preflight 的请求协调输入
// （chain preflight.go:1091-1097：gateway_request scope + 重试/墙钟/尝试预算）。
func w27CompactDispatchCoordination(t *testing.T) gatewaypreauth.CodexRequestCoordination {
	t.Helper()
	wallBudgetMs := int64(60_000)
	nowMs := func() int64 { return time.Now().UnixMilli() }
	wall, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: nowMs(),
		BudgetMs:            &wallBudgetMs,
		Now:                 nowMs,
	}, nil)
	if err != nil {
		t.Fatalf("create wall budget: %v", err)
	}
	tracker, err := gatewayrouting.NewGatewayRequestAttemptTracker(nil)
	if err != nil {
		t.Fatalf("create attempt tracker: %v", err)
	}
	return gatewaypreauth.CodexRequestCoordination{
		Scope:                    "gateway_request",
		ServerRetryBudget:        gatewaypreauth.NewServerRetryBudget(0, gatewaypreauth.SystemClock{}),
		GatewayRequestWallBudget: wall,
		RequestAttemptTracker:    tracker,
	}
}

func w27CompactPreflightInput(t *testing.T, req *gatewaypreauth.GatewayRequest, res gatewaypreauth.GatewayResponseWriter, audit *w27AuditCapture, accounts []gatewayruntimecache.OpenAIAccountSecret) gatewaypreauth.CodexCompactPreflightInput {
	t.Helper()
	return gatewaypreauth.CodexCompactPreflightInput{
		Req: req, Res: res, AuditCapture: audit,
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID:       "trace-w27-compact",
			TrafficSource: gatewayTrafficSource,
		},
		StartedAt:                  time.Now().UnixMilli(),
		SystemAccountID:            w27SysID,
		APIKeyID:                   w27KeyID,
		GroupID:                    w27GroupID,
		RequestClientCompatibility: "codex_responses",
		DispatchAccounts:           accounts,
		ActiveGatewaySettings: gatewayruntimecache.GatewaySettings{
			TextFirstResponseTimeoutSeconds:          30,
			TextStreamIdleTimeoutSeconds:             300,
			TextUncommittedAttemptMaxLifetimeSeconds: 60,
			NoAvailableAccountWaitTimeoutSeconds:     10,
		},
		RequestLane:         "text",
		RequestCoordination: w27CompactDispatchCoordination(t),
		Signal:              context.Background(),
	}
}

// w27CompactComboFixture 为组合 5/6 备齐：真实桥链 + 真实快照链 + compact
// 请求完成真实 context-state preflight（记录 compact/internal 请求态）。
func w27CompactComboFixture(t *testing.T, account gatewayruntimecache.OpenAIAccountSecret) (*gatewayChain, codexPreflightAdapter, *gatewaypreauth.GatewayRequest, *gatewaypreauth.TrackingWriter, *httptest.ResponseRecorder, *w27AuditCapture, string) {
	t.Helper()
	chain, adapter := w27WiredChain(t)
	prevID := w27SeedBridgeChainRound(t, adapter, account)
	req, res, recorder := w27RequestWithRecorder(http.MethodPost, "/v1/responses/compact", map[string]any{
		"model": w27Model, "previous_response_id": prevID,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 compact current turn"}},
	})
	audit := &w27AuditCapture{}
	completed, err := w27RunContextStatePreflight(t, adapter, req, res, audit)
	if err != nil || completed {
		t.Fatalf("compact 请求态记录必须继续（completed=%v err=%v）", completed, err)
	}
	state, _ := adapter.registry.Get(req)
	if state.RequestKind != gatewaycodex.RequestKindCompact || state.PreviousResponseKind != gatewaycodex.PreviousKindInternal {
		t.Fatalf("compact 请求态 kind=%q prevKind=%q，want compact/internal", state.RequestKind, state.PreviousResponseKind)
	}
	return chain, adapter, req, res, recorder, audit, prevID
}

func TestW27Combo5CompactRestoreBridgeSnapshotAPIKey(t *testing.T) {
	upstream := w27NewCompactUpstream(t, http.StatusOK, chatCompletionPayload)
	account := w27APIKeyBridgeAccount(upstream.server.URL)
	_, adapter, req, res, recorder, audit, prevID := w27CompactComboFixture(t, account)

	// 晚绑定 dispatcher 必须就位（接线事实），合成摘要走真实引擎派发。
	if _, ok := adapter.compact.Dispatcher.(*chainCompactSummaryDispatcher); !ok {
		t.Fatalf("compact 合成摘要 dispatcher = %T，want *chainCompactSummaryDispatcher", adapter.compact.Dispatcher)
	}

	probe := &w27SanitizeProbe{}
	_ = w27InstallSanitizeHook(t, probe.sanitize)
	result, err := adapter.ApplyChatBridgeCompactPreflight(context.Background(),
		w27CompactPreflightInput(t, req, res, audit, []gatewayruntimecache.OpenAIAccountSecret{account}))
	if err != nil {
		t.Fatalf("ApplyChatBridgeCompactPreflight: %v", err)
	}
	if !result.Completed {
		t.Fatal("compact 恢复 + 合成摘要成功后必须在 preflight 内完成请求")
	}

	// restore/派发 oracle：真实快照链恢复 + 真实上游交换。
	meta := audit.metadata["codex_responses_chat_bridge_compact"]
	if meta == nil || meta["mode"] != "gateway_summary_compact" {
		t.Fatalf("compact 审计 = %+v，want gateway_summary_compact", meta)
	}
	if meta["previousResponseId"] != prevID {
		t.Fatalf("previousResponseId = %v，want %v", meta["previousResponseId"], prevID)
	}
	if meta["accountId"] != account.ID {
		t.Fatalf("派发账户 = %v，want %v", meta["accountId"], account.ID)
	}
	if count, _ := meta["restoredResponseCount"].(int); count < 1 {
		t.Fatalf("restoredResponseCount = %v，want ≥1（真实快照链恢复）", meta["restoredResponseCount"])
	}
	if res.StatusCode() != http.StatusOK {
		t.Fatalf("compact 响应状态 = %d，want 200", res.StatusCode())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("compact 响应不是 JSON: %v", err)
	}
	if payload["object"] != "response.compaction" {
		t.Fatalf("compact 响应 object = %v，want response.compaction", payload["object"])
	}

	// 真实派发路径：合成 chat completions 请求恰好一次出站；API Key 目标全
	// 阶段 preserve（策略行 1）→ 0 次清理调用、出站不带已处理标记。
	if upstream.reqCount != 1 {
		t.Fatalf("上游请求数 = %d，want 1", upstream.reqCount)
	}
	if upstream.gotMethod != http.MethodPost || !strings.HasSuffix(upstream.gotPath, "/v1/chat/completions") {
		t.Fatalf("上游请求形态 = %s %s，want POST */v1/chat/completions", upstream.gotMethod, upstream.gotPath)
	}
	if upstream.gotAuth != "Bearer "+account.APIKey {
		t.Fatalf("上游凭据头 = %q，want Bearer %s", upstream.gotAuth, account.APIKey)
	}
	if probe.calls != 0 {
		t.Fatalf("清理调用次数 = %d，want 0（合成 chat 请求 + API Key 目标 → 全阶段 preserve）", probe.calls)
	}
	w27AssertUnmarked(t, upstream.gotRaw, "compact 合成摘要")
	// 终态/释放闭包由 #T5 批范式钉住（TestCompactSummaryDispatchUpstreamSuccess
	// 的 RecordTerminal / ReleaseConcurrency 断言）；本组合锁定它们在完整
	// preflight 路径中被服务消费：审计 finalize 已以 200 结束。
	if len(audit.finalizes) == 0 || audit.finalizes[len(audit.finalizes)-1].StatusCode != http.StatusOK {
		t.Fatalf("compact 成功必须写审计 finalize（200），got %+v", audit.finalizes)
	}
}

// ---------------------------------------------------------------------------
// 组合 6：/responses/compact 恢复已有 bridge snapshot → OAuth（过滤层可达、
// 适配器层语义不可达：如实登记）
// ---------------------------------------------------------------------------

func TestW27Combo6CompactRestoreBridgeSnapshotOAuth(t *testing.T) {
	upstream := w27NewCompactUpstream(t, http.StatusOK, chatCompletionPayload)
	account := w27OAuthBridgeAccount(upstream.server.URL)
	_, adapter, req, res, recorder, audit, _ := w27CompactComboFixture(t, account)

	probe := &w27SanitizeProbe{}
	_ = w27InstallSanitizeHook(t, probe.sanitize)
	result, err := adapter.ApplyChatBridgeCompactPreflight(context.Background(),
		w27CompactPreflightInput(t, req, res, audit, []gatewayruntimecache.OpenAIAccountSecret{account}))
	if err != nil {
		t.Fatalf("适配器层失败必须收口为 preflight 内失败响应，不得外抛: %v", err)
	}
	if !result.Completed {
		t.Fatal("失败路径必须在 preflight 内完成请求（502 收口）")
	}

	// 过滤层可达证据：OAuth 账户按 responses→chat 映射归 bridge-kind，进入
	// bridge 派发窗（CompactDispatchMode=bridge）。
	state, _ := adapter.registry.Get(req)
	if state.CompactDispatchMode != gatewaycodex.CompactDispatchBridge {
		t.Fatalf("CompactDispatchMode = %q，want bridge（映射使 OAuth 账户进入桥派发窗）", state.CompactDispatchMode)
	}

	// 语义不可达证据：OAuth codex 适配器要求 Codex Responses 形态（必须含
	// input 字段），合成 chat body 在出站构造处被拒——请求从未离开网关。
	if upstream.reqCount != 0 {
		t.Fatalf("上游请求数 = %d，want 0（OAuth 分支不存在桥改写，适配器校验先于出站）", upstream.reqCount)
	}
	if res.StatusCode() != http.StatusBadGateway {
		t.Fatalf("失败响应状态 = %d，want 502", res.StatusCode())
	}
	if !strings.Contains(recorder.Body.String(), "codex_bridge_compact_summary_failed") {
		t.Fatalf("失败响应 = %s，want codex_bridge_compact_summary_failed", recorder.Body.String())
	}
	if probe.calls != 0 {
		t.Fatalf("清理调用次数 = %d，want 0（body 校验先于 SanitizeOpenAIOAuthCodexHistory 调用点）", probe.calls)
	}
}

// ---------------------------------------------------------------------------
// 组合 7：bridge 恢复失败 / 无快照后继续普通请求（API Key、OAuth 各一例）
// ---------------------------------------------------------------------------

func TestW27Combo7RestoreFailureOrNoSnapshotContinuesPlain(t *testing.T) {
	t.Run("external_previous_no_snapshot_api_key_continues", func(t *testing.T) {
		chain, adapter := w27WiredChain(t)
		account := w27APIKeyNativeAccount("https://w27-upstream.invalid/v1")
		req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
			"model": w27Model, "previous_response_id": "resp_external_w27_not_a_bridge_id",
			"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo7 turn"}},
		})
		audit := &w27AuditCapture{}
		completed, err := w27RunContextStatePreflight(t, adapter, req, res, audit)
		if err != nil || completed {
			t.Fatalf("外部 previous（无桥快照可恢复）必须按普通请求继续（completed=%v err=%v）", completed, err)
		}
		state, _ := adapter.registry.Get(req)
		if state.PreviousResponseKind != gatewaycodex.PreviousKindExternal || state.Restored {
			t.Fatalf("请求态 prevKind=%q restored=%v，want external/false", state.PreviousResponseKind, state.Restored)
		}
		if audit.metadata["codex_responses_context_state"]["mode"] != "external_previous_response" {
			t.Fatalf("preflight 审计 = %+v，want external_previous_response", audit.metadata)
		}
		// 普通请求派发准备：API Key 原生账户 × codex_responses × responses 族
		// → Front+Post 各 1 次、不写标记；previous_response_id 原样出站。
		probe := &w27SanitizeProbe{}
		_ = w27InstallSanitizeHook(t, probe.sanitize)
		parts := w27PrepareParts(t, chain, req, account, "codex_responses")
		if probe.calls != 2 {
			t.Fatalf("清理调用次数 = %d，want 2（Front+Post）", probe.calls)
		}
		w27AssertUnmarked(t, parts.Body, "API Key 原生")
		if _, still := w27ParseBody(t, parts.Body)["previous_response_id"]; !still {
			t.Fatal("无快照继续普通请求时 previous_response_id 必须原样保留")
		}
	})

	t.Run("external_previous_no_snapshot_oauth_continues", func(t *testing.T) {
		chain, adapter := w27WiredChain(t)
		account := w27OAuthNativeAccount("https://w27-upstream.invalid/v1")
		req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
			"model": w27Model, "previous_response_id": "resp_external_w27_not_a_bridge_id",
			"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo7 turn"}},
		})
		audit := &w27AuditCapture{}
		completed, err := w27RunContextStatePreflight(t, adapter, req, res, audit)
		if err != nil || completed {
			t.Fatalf("外部 previous 必须继续（completed=%v err=%v）", completed, err)
		}
		// 普通请求派发准备：OAuth 账户恒走 codex adapter（无桥分支）→
		// Adapter 1 次 + 无条件标记写入。
		probe := &w27SanitizeProbe{}
		_ = w27InstallSanitizeHook(t, probe.sanitize)
		parts := w27PrepareParts(t, chain, req, account, "codex_responses")
		if probe.calls != 1 {
			t.Fatalf("清理调用次数 = %d，want 1（Adapter）", probe.calls)
		}
		if !gatewayoauthcodex.IsGatewayCodexHistorySanitized(parts.Body) {
			t.Fatal("OAuth 出站必须携带已处理标记")
		}
	})

	t.Run("internal_previous_missing_snapshot_completes_404", func(t *testing.T) {
		_, adapter := w27WiredChain(t)
		req, res, recorder := w27RequestWithRecorder(http.MethodPost, "/v1/responses", map[string]any{
			"model": w27Model, "previous_response_id": "resp_chat_bridge_w27_missing",
			"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo7 turn"}},
		})
		audit := &w27AuditCapture{}
		completed, err := w27RunContextStatePreflight(t, adapter, req, res, audit)
		if err != nil {
			t.Fatalf("restore 失败不外抛: %v", err)
		}
		// 源码事实：内部链快照缺失在 preflight 内以 404 restore 失败终结，
		// 请求不继续普通派发（与组合标题「继续普通请求」的差异如实登记；
		// 「继续」语义仅覆盖外部 previous/无桥状态形态，见上两例）。
		if !completed {
			t.Fatal("内部链快照缺失必须在 preflight 内终结（restore_failed）")
		}
		if res.StatusCode() != http.StatusNotFound {
			t.Fatalf("restore 失败状态 = %d，want 404", res.StatusCode())
		}
		if !strings.Contains(recorder.Body.String(), "codex_bridge_previous_response_not_found") {
			t.Fatalf("restore 失败响应 = %s，want codex_bridge_previous_response_not_found", recorder.Body.String())
		}
		if audit.metadata["codex_responses_chat_bridge_state"]["mode"] != "restore_failed" {
			t.Fatalf("preflight 审计 = %+v，want restore_failed", audit.metadata)
		}
	})

	t.Run("bridge_account_external_previous_rejected_by_real_service", func(t *testing.T) {
		_, adapter := w27WiredChain(t)
		account := w27APIKeyBridgeAccount("https://w27-upstream.invalid/v1")
		req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
			"model": w27Model, "previous_response_id": "resp_external_w27_not_a_bridge_id",
			"input": []any{map[string]any{"type": "message", "role": "user", "content": "w27 combo7 turn"}},
		})
		audit := &w27AuditCapture{}
		if _, err := w27RunContextStatePreflight(t, adapter, req, res, audit); err != nil {
			t.Fatalf("preflight: %v", err)
		}
		// per-account 桥准备的真实拒绝行为（T32 起同一实现在生产 dispatch 生效）。
		_, prepErr := adapter.bridge.PrepareCodexResponsesContextForAccount(adapter.registry, req, account)
		if prepErr == nil || prepErr.Error() != "外部 previous_response_id 只能发送给原生 Responses 账号" {
			t.Fatalf("外部 previous × 桥账户准备错误 = %v，want 固定文案", prepErr)
		}
	})
}

// ---------------------------------------------------------------------------
// 已处理标记去重（SkipIfProcessed）的 Post 阶段行为钉
// ---------------------------------------------------------------------------

// TestW27PostStageSkipIfProcessedMarkDedup：§5.4「后置遇到标记必须跳过」——
// Post 阶段在 policy.Post=sanitize 且 SkipIfProcessed 时以序列化 body 标记
// 去重：带标记 body 0 次清理调用且字节原样，无标记 body 恰 1 次调用。
func TestW27PostStageSkipIfProcessedMarkDedup(t *testing.T) {
	chain, _ := w27WiredChain(t)
	account := w27APIKeyBridgeAccount("https://w27-upstream.invalid/v1")
	policy := gatewaydispatch.HistoryPrepPolicyForRequest(account, "codex_responses", "responses")
	if policy.Post != gatewaydispatch.HistoryPrepStageSanitize || !policy.SkipIfProcessed {
		t.Fatalf("前置策略形态 Post=%q SkipIfProcessed=%v，want sanitize/true", policy.Post, policy.SkipIfProcessed)
	}

	// 已处理标记按序列化 body 字节内容为键（serializedBodyKey = string(body)），
	// 两臂必须使用互不相同的 body 字节，避免标记跨臂误命中。
	markedBody := gatewaybody.SerializeGatewayJSONObject(map[string]any{
		"model": w27Model,
		"input": []any{map[string]any{"w27_dedup": "marked"}},
	}).Raw

	t.Run("marked_body_skips_post_sanitize", func(t *testing.T) {
		marked := gatewayoauthcodex.MarkGatewayCodexHistorySanitized(bytes.Clone(markedBody))
		probe := &w27NoopProbe{}
		restore := w27InstallSanitizeHook(t, probe.sanitize)
		defer restore()
		result := chain.engine.SanitizePreparedCodexResponsesHistoryForAccount(account, marked, policy)
		if !bytes.Equal(result, marked) {
			t.Fatal("带标记 body 必须原样返回（Post 阶段跳过）")
		}
		if probe.calls != 0 {
			t.Fatalf("清理调用次数 = %d，want 0（标记命中 SkipIfProcessed）", probe.calls)
		}
	})

	t.Run("unmarked_body_runs_post_sanitize_once", func(t *testing.T) {
		unmarked := gatewaybody.SerializeGatewayJSONObject(map[string]any{
			"model": w27Model,
			"input": []any{map[string]any{"w27_dedup": "unmarked"}},
		}).Raw
		probe := &w27NoopProbe{}
		restore := w27InstallSanitizeHook(t, probe.sanitize)
		defer restore()
		result := chain.engine.SanitizePreparedCodexResponsesHistoryForAccount(account, unmarked, policy)
		if probe.calls != 1 {
			t.Fatalf("清理调用次数 = %d，want 1（无标记不跳过）", probe.calls)
		}
		if !bytes.Equal(result, unmarked) {
			t.Fatal("hook 不改写时 body 必须字节原样")
		}
	})
}

// TestW32EngineBridgeWiredDispatchRestores 钉住 T32 的生产接线：同一 preflight
// 请求进入真实 dispatch 时，engine.CodexBridge 必须把恢复历史合并进出站
// body、删除内部 previous_response_id（不再只停留在 registry/组件 oracle）。
func TestW32EngineBridgeWiredDispatchRestores(t *testing.T) {
	chain, adapter := w27WiredChain(t)
	account := w27APIKeyBridgeAccount("https://w27-upstream.invalid/v1")
	prevID := w27SeedBridgeChainRound(t, adapter, account)

	req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model, "previous_response_id": prevID,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w32 wired turn"}},
	})
	audit := &w27AuditCapture{}
	completed, err := w27RunContextStatePreflight(t, adapter, req, res, audit)
	if err != nil || completed {
		t.Fatalf("preflight = completed=%v err=%v", completed, err)
	}
	state, ok := adapter.registry.Get(req)
	if !ok || !state.Restored {
		t.Fatalf("registry state = %+v ok=%v, want restored", state, ok)
	}
	restoredInput, _ := state.MaterializedInput.([]any)
	if len(restoredInput) < 2 {
		t.Fatalf("restored input len = %d, want >=2", len(restoredInput))
	}
	if chain.engine.CodexBridge == nil {
		t.Fatal("engine.CodexBridge 未接线（T32）")
	}
	parts := w27PrepareParts(t, chain, req, account, "codex_responses")
	body := w27ParseBody(t, parts.Body)
	if _, still := body["previous_response_id"]; still {
		t.Fatal("接线后 dispatch body 必须删除内部 previous_response_id")
	}
	items, _ := body["input"].([]any)
	if len(items) != len(restoredInput) {
		t.Fatalf("dispatch input len = %d, want restored %d", len(items), len(restoredInput))
	}
}

// TestW32EngineBridgeWiredNativeAccountNotTakenOver：非 bridge 的 native
// Responses 账户不得被桥恢复逻辑接管（无 ActiveBridgeAccountID、无历史合并）。
func TestW32EngineBridgeWiredNativeAccountNotTakenOver(t *testing.T) {
	chain, adapter := w27WiredChain(t)
	account := w27APIKeyNativeAccount("https://w27-upstream.invalid/v1")
	req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w32 native turn"}},
	})
	audit := &w27AuditCapture{}
	if _, err := w27RunContextStatePreflight(t, adapter, req, res, audit); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	state, _ := adapter.registry.Get(req)
	if state.ActiveBridgeAccountID != "" {
		t.Fatalf("native 账户被桥接管: %+v", state)
	}
	parts := w27PrepareParts(t, chain, req, account, "codex_responses")
	items := w27BodyInputItems(t, parts.Body)
	if len(items) != 1 {
		t.Fatalf("native dispatch input len = %d, want 1（无历史合并）", len(items))
	}
}

// TestW32EngineBridgeWiredRejectsExternalPrevious：外部 previous_response_id
// 在真实 dispatch 的桥准备口即被拒绝，而不是只在组件 oracle 中拒绝。
func TestW32EngineBridgeWiredRejectsExternalPrevious(t *testing.T) {
	chain, adapter := w27WiredChain(t)
	account := w27APIKeyBridgeAccount("https://w27-upstream.invalid/v1")
	req, res := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": w27Model, "previous_response_id": "resp_external_w32_not_a_bridge_id",
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "w32 external"}},
	})
	audit := &w27AuditCapture{}
	if _, err := w27RunContextStatePreflight(t, adapter, req, res, audit); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	_, err := chain.engine.BuildPreparedUpstreamRequestParts(context.Background(), req, account,
		gatewaypreauth.GatewayFailureUsageContext{SystemAccountID: w27SysID, APIKeyID: w27KeyID, GroupID: w27GroupID},
		"codex_responses")
	if err == nil || !strings.Contains(err.Error(), "外部 previous_response_id 只能发送给原生 Responses 账号") {
		t.Fatalf("dispatch external previous error = %v", err)
	}
}
