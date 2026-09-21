package gatewaydispatch

// W17a 合并口径覆盖率补缺（gatewaydispatch + gatewayoauthcodex +
// gatewayupstream 以 coverpkg 合并计算 ≥95% 硬门）。只补纯函数与端口分支
// 的未覆盖臂，不改动生产代码；全局钩子（SanitizeCodexHistory）先保存后
// 经 t.Cleanup 恢复；全部测试串行执行（无 t.Parallel），不影响负载敏感
// 用例（TestEngineAdvancesOnlySwitchTargetServingAccounts 等）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayoauthcodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat/openaicompatbridge"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

func TestW17aIsRealUpstreamAttemptRejectsUnparsableURL(t *testing.T) {
	// url.Parse 失败臂：空格主机名。
	if IsRealUpstreamAttempt(UpstreamAttempt{UpstreamURL: "http://exa mple.com"}) {
		t.Fatal("无法解析的 URL 不应计为真实上游尝试")
	}
	if !IsRealUpstreamAttempt(UpstreamAttempt{UpstreamURL: "https://upstream.example/v1/chat/completions"}) {
		t.Fatal("https URL 应计为真实上游尝试")
	}
}

func TestW17aIsProvenUpstreamBodyTransportErrorRecursesIncompleteCause(t *testing.T) {
	err := &UpstreamBodyReadIncompleteError{Cause: errors.New("读取中断")}
	if IsProvenUpstreamBodyTransportError(err) {
		t.Fatal("未证明已开始的读取中断不应算已证明的正文传输失败")
	}
	if IsProvenUpstreamBodyTransportError(errors.New("普通错误")) {
		t.Fatal("普通错误不应算已证明的正文传输失败")
	}
}

func TestW17aNormalizeOpenAIReasoningFieldsEdges(t *testing.T) {
	// 非法 JSON：原样返回。
	invalid := []byte("{oops")
	if got := NormalizeOpenAIReasoningFieldsForUpstream("/v1/responses", invalid); string(got) != "{oops" {
		t.Fatalf("invalid json = %s", got)
	}
	// reasoning 为空对象：两层 effort 均为空 → 原样返回。
	unchanged := []byte(`{"model":"m","reasoning":{}}`)
	if got := NormalizeOpenAIReasoningFieldsForUpstream("/v1/chat/completions", unchanged); string(got) != string(unchanged) {
		t.Fatalf("empty reasoning = %s", got)
	}
	// reasoning.effort 仅空白：trim 后为空 → 原样返回。
	blank := []byte(`{"model":"m","reasoning":{"effort":"  "}}`)
	if got := NormalizeOpenAIReasoningFieldsForUpstream("/v1/chat/completions", blank); string(got) != string(blank) {
		t.Fatalf("blank effort = %s", got)
	}
	// responses 族：平铺 reasoning_effort 升级为 reasoning.effort。
	converted := NormalizeOpenAIReasoningFieldsForUpstream("/v1/responses", []byte(`{"model":"m","reasoning_effort":"high"}`))
	var object map[string]any
	if err := json.Unmarshal(converted, &object); err != nil {
		t.Fatalf("converted json: %v", err)
	}
	reasoning, ok := object["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("reasoning = %#v", object["reasoning"])
	}
	if _, exists := object["reasoning_effort"]; exists {
		t.Fatal("reasoning_effort 应被删除")
	}
	// url.Parse 失败 → 端点族为空。
	if family := openAIReasoningEndpointFamily("/v1/chat/%zz"); family != "" {
		t.Fatalf("family = %q", family)
	}
}

func TestW17aNormalizeAnthropicMessageNonTextBlockGuards(t *testing.T) {
	// content block 不是对象 → 原样返回。
	plain := map[string]any{"type": "message", "role": "user", "content": []any{"plain-text"}}
	got := normalizeAnthropicMessage(plain)
	if _, ok := got.(map[string]any)["content"].([]any); !ok {
		t.Fatalf("非对象 block 应原样返回, got %#v", got)
	}
	// text block 缺少 text 字段 → 原样返回。
	missingText := map[string]any{"type": "message", "role": "user", "content": []any{
		map[string]any{"type": "text"},
	}}
	got = normalizeAnthropicMessage(missingText)
	if _, ok := got.(map[string]any)["content"].([]any); !ok {
		t.Fatalf("缺 text 字段应原样返回, got %#v", got)
	}
}

func TestW17aPreparedUpstreamBodyMetadataScannedStateBranch(t *testing.T) {
	req := newTestRequest(t, "")
	model := "gpt-scanned"
	stream := true
	effort := "high"
	req.Body.State = &gatewaybody.BodyState{
		JSONParseStatus: gatewaybody.JSONParseStatusScannedJSON,
		Model:           &model,
		Stream:          &stream,
		ServiceTier:     "flex",
		ReasoningEffort: &effort,
	}
	metadata := PreparedUpstreamBodyMetadata(req, req.Body.RawBody)
	if metadata == nil || metadata.Model == nil || *metadata.Model != model {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.ServiceTier == nil || *metadata.ServiceTier != "flex" {
		t.Fatalf("service tier = %#v", metadata)
	}
	if metadata.ReasoningEffort == nil || *metadata.ReasoningEffort != effort {
		t.Fatalf("reasoning effort = %#v", metadata)
	}
}

func TestW17aBytesEqualContentMismatch(t *testing.T) {
	if bytesEqual([]byte("abc"), []byte("abd")) {
		t.Fatal("同长不同内容应判不等")
	}
	if !bytesEqual([]byte("abc"), []byte("abc")) {
		t.Fatal("相同内容应判相等")
	}
}

func TestW17aCapabilityFilterAppliesModelOverride(t *testing.T) {
	driver := &fakeDriver{}
	req := newTestRequest(t, "")
	result := FilterGatewayAccountsByRequestCapability(req, testAccounts("w17a-cap"), driver, "", "override-model")
	if result.SkippedCount != 0 || len(result.Accounts) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestW17aGatewayRequestWithModelOverrideNonObjectBody(t *testing.T) {
	req := newTestRequest(t, "")
	req.Body.Body = "not-a-json-object"
	req.Body.State = nil
	if _, ok := gatewaybodyJSONObject(req); ok {
		t.Fatal("非对象 body 不应解析为对象")
	}
	clone := gatewayRequestWithModelOverride(req, "override-model")
	body, ok := clone.Body.Body.(map[string]any)
	if !ok || body["model"] != "override-model" {
		t.Fatalf("clone body = %#v", clone.Body.Body)
	}
}

func TestW17aModelFilterMappingBlockedBySupportedModels(t *testing.T) {
	routeRule := "rule-w17a"
	accounts := []AccountCandidate{{
		ID:              "w17a-mapping",
		SupportedModels: []string{"unrelated-model"},
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "gpt-test",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
			UpstreamModel:          "mapped-model",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
			Enabled:                true,
			RuntimeRouteRuleID:     &routeRule,
		}},
	}}
	result := FilterGatewayAccountsByRequestedModel(accounts, "gpt-test", gatewayrouting.EndpointFamilyChatCompletions)
	if result.MappingMatchedCount != 0 || result.SkippedCount != 1 {
		t.Fatalf("result = %#v", result)
	}
	if rank := result.ModelPriority.RankByAccountID["w17a-mapping"]; rank != ModelPriorityRankUnsupported {
		t.Fatalf("rank = %d", rank)
	}
	// RuntimeRouteRuleID 指针分支：映射仍按 Source/Upstream 解析。
	mapping := resolveAccountModelMapping(accounts[0], "gpt-test", gatewayrouting.EndpointFamilyChatCompletions)
	if mapping == nil || mapping.UpstreamModel != "mapped-model" {
		t.Fatalf("mapping = %#v", mapping)
	}
}

func TestW17aAnthropicFamilyFromPathWithoutLeadingSlash(t *testing.T) {
	// 无前导斜杠的请求目标（RequestURI 携带原始形态）。
	raw := &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "v1/messages"}, RequestURI: "v1/messages"}
	req := gatewaypreauth.NewGatewayRequest(raw)
	if family := GatewayRequestEndpointFamily(req); family != gatewayrouting.EndpointFamilyMessages {
		t.Fatalf("family = %q", family)
	}
}

func TestW17aRequestBodyCompactionTriggerFromState(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test"}`)
	req.Body.State.CodexCompactionTrigger = true
	if !requestBodyHasCompactionTrigger(req) {
		t.Fatal("state 标记应识别 compaction trigger")
	}
}

func TestW17aConcurrencyLimitsSkipEmptyIdentity(t *testing.T) {
	limits := GatewayAccountConcurrencyLimitsByAccountID([]AccountCandidate{{}})
	if len(limits) != 0 {
		t.Fatalf("limits = %#v", limits)
	}
}

// w17aOverflowRotationCounter 恒返回等于模数的下标，命中加权选择的兜底臂。
type w17aOverflowRotationCounter struct{}

func (w17aOverflowRotationCounter) NextIndex(context.Context, string, string, int) (int, error) {
	return 2, nil
}

func TestW17aAPIKeyRotationNilExcludePlusFallbacks(t *testing.T) {
	engine := newRotationTestEngine()
	credentials := poolCredentials([]string{"k1"}, "", nil)
	entries := accountApiKeyEntries(engine.Config.Secret, credentials)
	// ExcludeFingerprints 为 nil 的归一化分支。
	selected, err := selectAccountRuntimeApiKeyEntry(context.Background(), apiKeySelectionInput{
		AccountID:           "w17a-rotation",
		credentials:         credentials,
		RuntimeStates:       nil,
		entries:             entries,
		counter:             engine.keyRotationOf(),
		ExcludeFingerprints: nil,
	})
	if err != nil || selected == nil {
		t.Fatalf("selected = %#v, err = %v", selected, err)
	}
	// previous 指纹不在 pool 中 → 回退 candidates[0]。
	pool := []apiKeyEntry{
		{key: "k1", fingerprint: "fp-w17a-1", index: 0},
		{key: "k2", fingerprint: "fp-w17a-2", index: 1},
	}
	if got := selectNextAPIKeyAfterFingerprint(pool, pool[:1], "missing-fp"); got.fingerprint != "fp-w17a-1" {
		t.Fatalf("fallback = %#v", got)
	}
	// 加权计数越界（index == 总权重）→ 兜底 entries[0]。
	weighted := []apiKeyEntry{
		{key: "k1", fingerprint: "wf-w17a-1", weight: 1},
		{key: "k2", fingerprint: "wf-w17a-2", weight: 1},
	}
	picked, err := selectWeightedAPIKeyEntry(context.Background(), w17aOverflowRotationCounter{}, "w17a-weighted", weighted)
	if err != nil || picked.key != "k1" {
		t.Fatalf("picked = %#v, err = %v", picked, err)
	}
}

func TestW17aKeyModelCapabilityUsesRevisionAndCredentialSource(t *testing.T) {
	revision := int64(3)
	fingerprint := "fp-w17a-dispatch"
	sourceAccount := "source-account-w17a"
	account := AccountCandidate{
		ID:                        "w17a-keymodel",
		DispatchRevision:          &revision,
		SelectedAPIKeyFingerprint: &fingerprint,
		CredentialSourceAccountID: &sourceAccount,
	}
	capability := ResolveGatewayKeyModelAttemptCapability(newTestRequest(t, ""), account)
	if capability == nil {
		t.Fatal("capability 不应为空")
	}
	if capability.Capability.CredentialSourceAccountID != sourceAccount {
		t.Fatalf("credential source = %q", capability.Capability.CredentialSourceAccountID)
	}
}

// w17aFailingCodexBridge 恒返回错误，覆盖 BuildPreparedUpstreamRequestParts
// 的 CodexBridge 失败臂与 wrapCodexPreparationError 的非 adapter 透传臂。
type w17aFailingCodexBridge struct{}

func (w17aFailingCodexBridge) GetContextState(*gatewaypreauth.GatewayRequest) *CodexBridgeState {
	return nil
}

func (w17aFailingCodexBridge) CompletionHandlerForRequest(*gatewaypreauth.GatewayRequest, AccountCandidate) CodexBridgeCompletionHandler {
	return nil
}

func (w17aFailingCodexBridge) PrepareContextForAccount(*gatewaypreauth.GatewayRequest, AccountCandidate) error {
	return errors.New("bridge failed")
}

func TestW17aAccountPreparationSmallBranches(t *testing.T) {
	// accountProxyDispatchKey：ProxyURL 分支。
	proxyURL := "http://proxy.example:8080"
	if key := accountProxyDispatchKey(AccountCandidate{ProxyURL: &proxyURL}); key != "url:"+proxyURL {
		t.Fatalf("key = %q", key)
	}

	// convertBridgeGuidanceError：guidance 错误 → GatewayAgentGuidanceResponse。
	converted := convertBridgeGuidanceError(&openaicompatbridge.BridgeGuidanceError{Message: "引导消息", Code: "guide"})
	var guidance *gatewaypreauth.GatewayAgentGuidanceResponse
	if !errors.As(converted, &guidance) || guidance.Message != "引导消息" {
		t.Fatalf("guidance = %#v", converted)
	}
	// 非 guidance 错误原样透传。
	plain := errors.New("普通错误")
	if convertBridgeGuidanceError(plain) != plain {
		t.Fatal("普通错误应原样返回")
	}

	// wrapCodexPreparationError：非 adapter 错误原样返回。
	engine := &Engine{}
	if wrapped := engine.wrapCodexPreparationError(context.Background(), nil, gatewaypreauth.GatewayFailureUsageContext{}, AccountCandidate{}, plain); wrapped != plain {
		t.Fatalf("wrapped = %v", wrapped)
	}

	// sanitizeCodexResponsesHistoryForAccount 分支。
	sanitizeEngine := &Engine{}
	// a) input 非数组（字符串）→ 直接返回。
	reqStringInput := newCodexRequest(t, "/v1/responses")
	sanitizeEngine.sanitizeCodexResponsesHistoryForAccount(reqStringInput, AccountCandidate{ID: "w17a-acc"}, "codex_responses")
	// b) 钩子为 nil → 直接返回。
	previousHook := gatewayoauthcodex.SanitizeCodexHistory
	t.Cleanup(func() { gatewayoauthcodex.SanitizeCodexHistory = previousHook })
	reqNilHook := newCodexRequest(t, "/v1/responses")
	reqNilHook.Body.Body.(map[string]any)["input"] = []any{map[string]any{"type": "message"}}
	gatewayoauthcodex.SanitizeCodexHistory = nil
	sanitizeEngine.sanitizeCodexResponsesHistoryForAccount(reqNilHook, AccountCandidate{ID: "w17a-acc"}, "codex_responses")
	// c) 钩子结果未变化 → 直接返回。
	reqUnchanged := newCodexRequest(t, "/v1/responses")
	reqUnchanged.Body.Body.(map[string]any)["input"] = []any{map[string]any{"type": "message", "role": "user"}}
	gatewayoauthcodex.SanitizeCodexHistory = func(list []any, options gatewayoauthcodex.SanitizeCodexHistoryOptions) gatewayoauthcodex.CodexHistorySanitizeResult {
		return gatewayoauthcodex.CodexHistorySanitizeResult{Items: list, Changed: false}
	}
	sanitizeEngine.sanitizeCodexResponsesHistoryForAccount(reqUnchanged, AccountCandidate{ID: "w17a-acc"}, "codex_responses")

	// BuildPreparedUpstreamRequestParts：CodexBridge 失败臂。
	bridgeEngine, _, _ := newTestEngine(t)
	bridgeEngine.CodexBridge = w17aFailingCodexBridge{}
	_, err := bridgeEngine.BuildPreparedUpstreamRequestParts(
		context.Background(), newTestRequest(t, ""), testAccounts("w17a-bridge")[0], testUsageContext(), "")
	if err == nil || err.Error() != "bridge failed" {
		t.Fatalf("err = %v", err)
	}
}

func TestW17aCodexSanitizedRegistryCapacityDrop(t *testing.T) {
	// gatewayoauthcodex.gatewaySerializedFlagCapacity；容量压力下淘汰旧键。
	// 淘汰目标由 map 随机迭代决定，因此断言"至少一个早期键被淘汰"而非具体键。
	const capacity = 8192
	const extra = 16
	marked := make([][]byte, 0, capacity+extra)
	for i := 0; i < capacity+extra; i++ {
		key := []byte(fmt.Sprintf("w17a-registry-body-%d", i))
		gatewayoauthcodex.MarkGatewayCodexHistorySanitized(key)
		marked = append(marked, key)
	}
	last := []byte("w17a-registry-last")
	gatewayoauthcodex.MarkGatewayCodexHistorySanitized(last)
	if !gatewayoauthcodex.IsGatewayCodexHistorySanitized(last) {
		t.Fatal("最新标记应保留")
	}
	dropped := 0
	for _, key := range marked {
		if !gatewayoauthcodex.IsGatewayCodexHistorySanitized(key) {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("容量压力下应有标记被淘汰")
	}
}

func TestW17aGatewayUpstreamSmallGaps(t *testing.T) {
	// RunDeadlineHandler：nil handler → abort。
	action, err := gatewayupstream.RunDeadlineHandler(nil, gatewayupstream.FirstByteDeadlineDecisionInput{})
	if err != nil || action != gatewayupstream.FirstByteDeadlineActionAbort {
		t.Fatalf("action = %v, err = %v", action, err)
	}
	// anthropic 前缀族 header 命中（x-stainless- 前缀）。
	if !gatewayupstream.IsAnthropicMessagesScopedHeaderName("X-Stainless-Runtime") {
		t.Fatal("x-stainless- 前缀应命中")
	}
	// 官方 OAuth 客户端白名单：claude code 精确名 + 前缀名。
	if !gatewayupstream.IsAllowedOfficialOAuthClientHeader("Anthropic-Beta", gatewayupstream.OAuthHeaderProfileAnthropicClaude) {
		t.Fatal("anthropic-beta 应允许")
	}
	if !gatewayupstream.IsAllowedOfficialOAuthClientHeader("x-claude-code-session-id", gatewayupstream.OAuthHeaderProfileAnthropicClaude) {
		t.Fatal("x-claude-code- 前缀应允许")
	}
	// 空 path 的 compact 判定。
	emptyPathReq := gatewaypreauth.NewGatewayRequest(&http.Request{Method: http.MethodPost, URL: &url.URL{}})
	if gatewayupstream.IsOpenAIOAuthCodexCompactRequest(emptyPathReq) {
		t.Fatal("空 path 不应判为 compact")
	}
	// usage snapshot：无归一化数据时提前返回。
	payload := gatewayupstream.BuildOpenAICodexUsageSnapshotPayload(gatewayupstream.OpenAICodexUsageSnapshot{}, time.Now(), "w17a-unit")
	if _, ok := payload["codex_usage_updated_at"]; !ok {
		t.Fatal("payload 应包含更新时间")
	}
	if _, ok := payload["codex_5h_used_percent"]; ok {
		t.Fatal("空 snapshot 不应产生 5h 指标")
	}
	// 全局并发 governor：容量 0 → nil；取消的 ctx → 错误。
	if governor := gatewayupstream.NewBoundedConcurrencyGovernor(0); governor != nil {
		t.Fatal("容量 0 应返回 nil governor")
	}
	governor := gatewayupstream.NewBoundedConcurrencyGovernor(1)
	release, err := governor.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(release)
	// 保持占满容量，使取消的 ctx 成为唯一就绪分支。
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := governor.Acquire(cancelled); err == nil {
		t.Fatal("取消后应返回错误")
	}
	// URL 安全策略：非 http/https scheme 拒绝。
	policy := gatewayupstream.NewResolvedUpstreamURLPolicy(sharedupstreamhttp.URLSecurityConfig{})
	_, err = policy.PrepareSafeUpstreamRequestURL(context.Background(), "ftp://example.com/x")
	var unsafe *gatewayupstream.UnsafeUpstreamURLError
	if !errors.As(err, &unsafe) {
		t.Fatalf("err = %v", err)
	}
}

func TestW17aBuildOpenAIOAuthCodexRequestPartsEdges(t *testing.T) {
	identity := OpenAIOAuthCodexIdentity{SystemAccountID: "sys-w17a"}
	account := OpenAIOAuthCodexAccount{APIKey: "token", Credentials: map[string]any{}}

	// a) RawBody 非法 JSON → adapter 错误（normalize + build 错误臂）。
	invalidReq := newCodexRequest(t, "/v1/responses")
	invalidReq.Body = &gatewaybody.Request{RawBody: []byte("[oops")}
	if _, err := BuildOpenAIOAuthCodexRequestParts(invalidReq, http.Header{}, account, identity, OpenAIOAuthCodexRequestOptions{}); err == nil {
		t.Fatal("非法 JSON 应报错")
	}

	// b) RawBody 合法 JSON 对象（Body.Body 未预解析）→ 对象解析臂。
	rawObjectReq := newCodexRequest(t, "/v1/responses")
	rawObjectReq.Body = &gatewaybody.Request{RawBody: []byte(`{"model":"gpt-5","input":"hello"}`)}
	parts, err := BuildOpenAIOAuthCodexRequestParts(rawObjectReq, http.Header{}, account, identity, OpenAIOAuthCodexRequestOptions{})
	if err != nil {
		t.Fatalf("raw object parts: %v", err)
	}
	if len(parts.Body) == 0 {
		t.Fatal("body 不应为空")
	}

	// c) 合法 attestation：校验通过并透传到上游 header。
	attestationHeaders := http.Header{}
	attestationHeaders.Set("X-Oai-Attestation", "valid-attestation-token")
	parts, err = BuildOpenAIOAuthCodexRequestParts(newCodexRequest(t, "/v1/responses"), attestationHeaders, account, identity, OpenAIOAuthCodexRequestOptions{})
	if err != nil {
		t.Fatalf("attestation parts: %v", err)
	}
	if got := parts.Headers.Get("X-Oai-Attestation"); got != "valid-attestation-token" {
		t.Fatalf("attestation = %q", got)
	}

	// d) SanitizeCodexHistory=true：BodyBytes 标记路径。
	parts, err = BuildOpenAIOAuthCodexRequestParts(newCodexRequest(t, "/v1/responses"), http.Header{}, account, identity, OpenAIOAuthCodexRequestOptions{SanitizeCodexHistory: true})
	if err != nil {
		t.Fatalf("sanitized parts: %v", err)
	}
	if len(parts.Body) == 0 || !gatewayoauthcodex.IsGatewayCodexHistorySanitized(parts.Body) {
		t.Fatalf("sanitized body = %d bytes", len(parts.Body))
	}
}
