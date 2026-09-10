package gatewaydispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 准备层与协议层残余分支的单元测试：bodypreparation / accountpreparation /
// oauthnormalizer / oauthadapter / builtintools / usageheaders / headerpolicy /
// transport 的纯函数面。

// ---------------------------------------------------------------------------
// bodypreparation.go
// ---------------------------------------------------------------------------

func TestPrepareAnthropicMessagesBodyForAttempt(t *testing.T) {
	// 非 Anthropic 请求透传。
	headers := http.Header{}
	if got := PrepareAnthropicMessagesBodyForAttempt(nil, headers, "https://u.example/v1/chat/completions", []byte(`{"a":1}`)); string(got) != `{"a":1}` {
		t.Fatalf("非 Anthropic 请求应透传, got %s", got)
	}
	// nil body 透传 nil。
	if got := PrepareAnthropicMessagesBodyForAttempt(nil, anthropicHeaders(), "https://u.example/v1/messages", nil); got != nil {
		t.Fatalf("nil body 应返回 nil, got %s", got)
	}
	// Anthropic 请求：text 块合并 + stream:false 删除。
	headers = anthropicHeaders()
	body := `{"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"你"},{"type":"text","text":"好"}]}]}`
	normalized := PrepareAnthropicMessagesBodyForAttempt(nil, headers, "https://u.example/v1/messages", []byte(body))
	parsed := mustJSONObject(t, string(normalized))
	messages := parsed["messages"].([]any)
	content := messages[0].(map[string]any)["content"]
	if content != "你好" {
		t.Fatalf("合并 content = %#v", content)
	}
	if _, ok := parsed["stream"]; ok {
		t.Fatal("stream:false 必须删除")
	}
	// 无变化时透传原文。
	same := `{"messages":[{"role":"user","content":"plain"}]}`
	if got := PrepareAnthropicMessagesBodyForAttempt(nil, anthropicHeaders(), "https://u.example/v1/messages", []byte(same)); string(got) != same {
		t.Fatalf("无变化应透传原文, got %s", got)
	}
	// 非 JSON body 透传。
	if got := PrepareAnthropicMessagesBodyForAttempt(nil, anthropicHeaders(), "https://u.example/v1/messages", []byte("not-json")); string(got) != "not-json" {
		t.Fatal("非 JSON body 应透传")
	}
	// 与请求原始 body 一致时透传。
	req := newTestRequest(t, same)
	if got := PrepareAnthropicMessagesBodyForAttempt(req, anthropicHeaders(), "https://u.example/v1/messages", []byte(same)); string(got) != same {
		t.Fatal("与原始 body 一致应透传")
	}
	// 非 text 块不合并。
	mixed := `{"messages":[{"role":"user","content":[{"type":"image","source":"x"},{"type":"text","text":"hi"}]}]}`
	if got := PrepareAnthropicMessagesBodyForAttempt(nil, anthropicHeaders(), "https://u.example/v1/messages", []byte(mixed)); string(got) != mixed {
		t.Fatal("含非 text 块应透传")
	}
}

func anthropicHeaders() http.Header {
	headers := http.Header{}
	headers.Set("Anthropic-Version", "2023-06-01")
	headers.Set("X-Api-Key", "key")
	return headers
}

func TestNormalizeAnthropicMessageJoin(t *testing.T) {
	value := map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "a"},
			map[string]any{"type": "text", "text": "b"},
		},
	}
	normalized := normalizeAnthropicMessage(value)
	if normalized.(map[string]any)["content"] != "ab" {
		t.Fatalf("normalized = %#v", normalized)
	}
	// name 字段保留。
	value["name"] = "keep"
	normalized = normalizeAnthropicMessage(value)
	if normalized.(map[string]any)["name"] != "keep" {
		t.Fatal("name 字段应保留")
	}
	// 非对象/非数组/非 text 块原样返回。
	if normalizeAnthropicMessage("str") != "str" {
		t.Fatal("非对象原样返回")
	}
	noContent := map[string]any{"role": "user"}
	if normalizeAnthropicMessage(noContent) != normalizeAnthropicMessage(noContent) {
		t.Fatal("无 content 原样返回")
	}
}

func TestIsAnthropicMessagesRequestParts(t *testing.T) {
	if !isAnthropicMessagesRequestHeaders(anthropicHeaders()) {
		t.Fatal("Anthropic 头识别失败")
	}
	if isAnthropicMessagesRequestHeaders(http.Header{"X-Api-Key": []string{"k"}}) {
		t.Fatal("缺 Anthropic-Version 不识别")
	}
	if !isAnthropicMessagesPath("https://u.example/v1/messages") {
		t.Fatal("messages 路径识别失败")
	}
	if isAnthropicMessagesPath("https://u.example/v1/messages/count_tokens") {
		t.Fatal("count_tokens 不是 messages 路径")
	}
	if isAnthropicMessagesPath("://bad") {
		t.Fatal("非法 URL 不识别")
	}
}

func TestPreparedUpstreamBodyMetadata(t *testing.T) {
	if PreparedUpstreamBodyMetadata(nil, nil) != nil {
		t.Fatal("nil body 返回 nil")
	}
	body := []byte(`{"model":"gpt-test","service_tier":"flex","reasoning_effort":"high"}`)
	metadata := PreparedUpstreamBodyMetadata(nil, body)
	if metadata == nil || metadata.ServiceTier == nil || *metadata.ServiceTier != "flex" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.ReasoningEffort == nil || *metadata.ReasoningEffort != "high" {
		t.Fatalf("reasoning = %#v", metadata.ReasoningEffort)
	}
	// 与请求 body 一致且已扫描 → 使用请求状态。
	req := newTestRequest(t, `{"model":"gpt-test","service_tier":"default"}`)
	metadata = PreparedUpstreamBodyMetadata(req, req.Body.RawBody)
	if metadata == nil || metadata.ServiceTier == nil {
		t.Fatalf("scanned metadata = %#v", metadata)
	}
}

// ---------------------------------------------------------------------------
// accountpreparation.go
// ---------------------------------------------------------------------------

func TestSelectAccountApiKeyForDispatchNonAPIKeyPassthrough(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	account := testAccounts("a-1")[0]
	account.Type = "oauth"
	selected, ok, err := engine.SelectAccountApiKeyForDispatch(context.Background(), account, SelectApiKeyOptions{})
	if err != nil || !ok {
		t.Fatalf("non api_key 透传: %v %v", ok, err)
	}
	if selected.ID != "a-1" {
		t.Fatalf("selected = %s", selected.ID)
	}
}

func TestSelectAccountApiKeyForDispatchFixedFingerprintExcluded(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	account := multiKeyTestAccount("a-1", "key-a", "key-b")
	fpA := apiKeyFingerprint(engine.Config.Secret, "key-a")
	account.SelectedAPIKeyFingerprint = &fpA
	// 固定指纹被排除且无豁免 → 不选择。
	selected, ok, err := engine.SelectAccountApiKeyForDispatch(context.Background(), account, SelectApiKeyOptions{
		ExcludeFingerprints: map[string]struct{}{fpA: {}},
	})
	if err != nil || ok {
		t.Fatalf("排除的固定指纹不应选择: ok=%v err=%v", ok, err)
	}
	// 豁免指纹 → 选择固定 Key。
	selected, ok, err = engine.SelectAccountApiKeyForDispatch(context.Background(), account, SelectApiKeyOptions{
		ExcludeFingerprints:      map[string]struct{}{fpA: {}},
		AllowExcludedFingerprint: fpA,
	})
	if err != nil || !ok {
		t.Fatalf("豁免指纹应选择: ok=%v err=%v", ok, err)
	}
	if selected.SelectedAPIKeyFingerprint == nil || *selected.SelectedAPIKeyFingerprint != fpA {
		t.Fatalf("fingerprint = %#v", selected.SelectedAPIKeyFingerprint)
	}
	// 未知的固定指纹 → 不选择。
	unknown := "fp-unknown"
	account.SelectedAPIKeyFingerprint = &unknown
	if _, ok, _ := engine.SelectAccountApiKeyForDispatch(context.Background(), account, SelectApiKeyOptions{}); ok {
		t.Fatal("未知固定指纹不应选择")
	}
}

func TestSelectAccountApiKeyForDispatchSingleKeyKeepsEmptyFingerprint(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	account := testAccounts("a-1")[0] // 单 Key：无池隔离
	selected, ok, err := engine.SelectAccountApiKeyForDispatch(context.Background(), account, SelectApiKeyOptions{})
	if err != nil || !ok {
		t.Fatalf("单 Key 选择: %v %v", ok, err)
	}
	if selected.SelectedAPIKeyFingerprint != nil {
		t.Fatalf("无池隔离时指纹应为空, got %#v", selected.SelectedAPIKeyFingerprint)
	}
	if selected.APIKey != account.APIKey {
		t.Fatal("Key 保持不变")
	}
}

func TestAccountAPIKeyCredentialHelpers(t *testing.T) {
	credentials := accountApiKeySelectionCredentials(AccountCandidate{
		ID: "a-1", APIKey: "solo", APIKeys: []string{"k1", "k1", " k2 "},
	})
	keys := credentials["api_keys"].([]any)
	if len(keys) != 3 {
		t.Fatalf("keys = %#v", keys)
	}
	entries := accountApiKeyEntries("secret", credentials)
	// 去空白去重后保留 2 个（k1 与 k2）。
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].weight != 1 {
		t.Fatalf("默认权重 = %d", entries[0].weight)
	}
	weights := []any{float64(5), 2.5, "x"}
	credentials["api_key_weights"] = weights
	entries = accountApiKeyEntries("secret", credentials)
	if entries[0].weight != 5 || entries[1].weight != 1 {
		t.Fatalf("weights = %d/%d", entries[0].weight, entries[1].weight)
	}
	// credentialFloatValue 数值面。
	if value, ok := credentialFloatValue(json.Number("3.5")); !ok || value != 3.5 {
		t.Fatalf("json.Number = %v %v", value, ok)
	}
	if _, ok := credentialFloatValue("x"); ok {
		t.Fatal("字符串不是数值")
	}
	if _, ok := credentialFloatValue(nil); ok {
		t.Fatal("nil 不是数值")
	}
	if credentialListValue([]any{"a"}, 5) != nil {
		t.Fatal("越界索引返回 nil")
	}
	// 空白 Key 去除。
	blankCredentials := map[string]any{"api_keys": []any{"  ", "real"}}
	if entries := accountApiKeyEntries("", blankCredentials); len(entries) != 1 {
		t.Fatalf("空白 Key 应去除, entries = %d", len(entries))
	}
}

func TestConvertBridgeGuidanceError(t *testing.T) {
	plain := errors.New("普通错误")
	if convertBridgeGuidanceError(plain) != plain {
		t.Fatal("非 guidance 错误透传")
	}
}

func TestWrapCodexPreparationErrorRecordsUsage(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	usage := engine.Usage.(*fakeUsage)
	req := newTestRequest(t, `{"model":"gpt-test"}`)
	usageContext := testUsageContext()
	// 非 adapter 错误透传。
	plain := errors.New("x")
	if got := engine.wrapCodexPreparationError(context.Background(), req, usageContext, testAccounts("a-1")[0], plain); got != plain {
		t.Fatal("非 adapter 错误应透传")
	}
	// adapter 错误 + oauth openai 画像 → openai-oauth 上游 URL。
	adapter := NewOpenAIOAuthCodexAdapterError("请求无效", WithCodexAdapterStatus(400))
	oauthAccount := testAccounts("a-1")[0]
	oauthAccount.Type = "oauth"
	oauthAccount.ProviderCode = "gpt"
	oauthAccount.ProtocolCode = "openai"
	oauthAccount.ProtocolVersion = "v1"
	if got := engine.wrapCodexPreparationError(context.Background(), req, usageContext, oauthAccount, adapter); got != adapter {
		t.Fatal("adapter 错误应透传")
	}
	if len(usage.records) != 1 || usage.records[0].UpstreamURL != "openai-oauth-codex:local-validation" {
		t.Fatalf("records = %#v", usage.records)
	}
	// 非 oauth 账户 → gateway 本地校验 URL。
	if got := engine.wrapCodexPreparationError(context.Background(), req, usageContext, testAccounts("a-1")[0], adapter); got != adapter {
		t.Fatal("adapter 错误应透传")
	}
	if usage.records[1].UpstreamURL != "gateway:local-validation" {
		t.Fatalf("第二记录 URL = %q", usage.records[1].UpstreamURL)
	}
}

func TestSanitizePreparedCodexResponsesHistoryForAccount(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	// body 为 nil → nil。
	if got := engine.SanitizePreparedCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], nil, "codex_responses"); got != nil {
		t.Fatal("nil body 返回 nil")
	}
	// 非 codex_responses 透传。
	original := []byte(`{"input":[]}`)
	if got := engine.SanitizePreparedCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], original, ""); string(got) != string(original) {
		t.Fatal("非 codex_responses 透传")
	}
	// 非 responses 端点透传。
	chatReq := newTestRequest(t, `{"model":"gpt-test"}`)
	if got := engine.SanitizePreparedCodexResponsesHistoryForAccount(chatReq, testAccounts("a-1")[0], original, "codex_responses"); string(got) != string(original) {
		t.Fatal("非 responses 端点透传")
	}
	// 无 sanitizer 时透传。
	if got := engine.SanitizePreparedCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], original, "codex_responses"); string(got) != string(original) {
		t.Fatal("无 sanitizer 透传")
	}
	// sanitizer 改写 input。
	previous := SanitizeCodexHistory
	SanitizeCodexHistory = func(items []any, options SanitizeCodexHistoryOptions) CodexHistorySanitizeResult {
		if options.TargetScopeKey != "account:a-1" {
			t.Fatalf("scope key = %q", options.TargetScopeKey)
		}
		return CodexHistorySanitizeResult{Items: []any{"sanitized"}, Changed: true}
	}
	t.Cleanup(func() { SanitizeCodexHistory = previous })
	sanitized := engine.SanitizePreparedCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], []byte(`{"model":"gpt-test","input":[{"role":"user"}]}`), "codex_responses")
	parsed := mustJSONObject(t, string(sanitized))
	if parsed["input"].([]any)[0] != "sanitized" {
		t.Fatalf("sanitized = %#v", parsed["input"])
	}
	// 已标记的 body 不再清洗。
	if got := engine.SanitizePreparedCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], MarkGatewayCodexHistorySanitized([]byte(`{"input":[1]}`)), "codex_responses"); string(got) != `{"input":[1]}` {
		t.Fatal("已标记 body 不再清洗")
	}
	// 非 JSON body 透传。
	if got := engine.SanitizePreparedCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], []byte("nope"), "codex_responses"); string(got) != "nope" {
		t.Fatal("非 JSON body 透传")
	}
	// input 非数组透传。
	if got := engine.SanitizePreparedCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], []byte(`{"input":"text"}`), "codex_responses"); string(got) != `{"input":"text"}` {
		t.Fatal("input 非数组透传")
	}
	// sanitizer 未改写 → 原文。
	SanitizeCodexHistory = func(items []any, options SanitizeCodexHistoryOptions) CodexHistorySanitizeResult {
		return CodexHistorySanitizeResult{Items: items, Changed: false}
	}
	if got := engine.SanitizePreparedCodexResponsesHistoryForAccount(req, testAccounts("a-1")[0], original, "codex_responses"); string(got) != string(original) {
		t.Fatal("未改写时透传原文")
	}
}

func TestBuildPreparedUpstreamRequestPartsAppliesSanitizerAndTier(t *testing.T) {
	okDriver := &partsEchoDriver{}
	engine := NewEngine(okDriver, &fakeFailureDispatcher{})
	engine.Suppression = &fakeSuppression{}
	engine.Usage = &fakeUsage{}
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	req.Body = newTestRequestBody(t, `{"model":"gpt-test","input":[],"service_tier":"flex","reasoning_effort":"low"}`)
	previous := SanitizeCodexHistory
	SanitizeCodexHistory = func(items []any, options SanitizeCodexHistoryOptions) CodexHistorySanitizeResult {
		return CodexHistorySanitizeResult{Items: []any{"clean"}, Changed: true}
	}
	t.Cleanup(func() { SanitizeCodexHistory = previous })
	parts, err := engine.BuildPreparedUpstreamRequestParts(context.Background(), req, testAccounts("a-1")[0], testUsageContext(), "codex_responses")
	if err != nil {
		t.Fatalf("BuildPreparedUpstreamRequestParts: %v", err)
	}
	if !bytes.Contains(parts.Body, []byte("clean")) {
		t.Fatalf("sanitized body = %s", parts.Body)
	}
	if parts.EffectiveServiceTier != "flex" {
		t.Fatalf("service tier = %q", parts.EffectiveServiceTier)
	}
	if parts.EffectiveReasoningEffort != "low" {
		t.Fatalf("reasoning effort = %q", parts.EffectiveReasoningEffort)
	}
}

// partsEchoDriver 返回固定部件（用于 BuildPreparedUpstreamRequestParts 直测）。
type partsEchoDriver struct {
	fakeDriver
}

func (f *partsEchoDriver) BuildGatewayUpstreamRequestParts(ctx context.Context, req *gatewaypreauth.GatewayRequest, account AccountCandidate, identity UsageIdentity, requestClientCompatibility string) (PreparedRequestParts, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer test")
	return PreparedRequestParts{Headers: header, Body: []byte(`{"model":"gpt-test","input":[],"service_tier":"flex","reasoning_effort":"low"}`)}, nil
}

func TestDefersCodexResponsesHistorySanitization(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	account := testAccounts("a-1")[0]
	if engine.defersCodexResponsesHistorySanitizationToOpenAIOAuthWorker(req, account, "") {
		t.Fatal("普通账户不延迟清洗")
	}
	oauth := testAccounts("a-1")[0]
	oauth.Type = "oauth"
	oauth.ProviderCode = "gpt"
	oauth.ProtocolCode = "openai"
	oauth.ProtocolVersion = "v1"
	oauth.ProviderProtocolProfileID = GPTOpenAIV1ProfileID
	if !engine.defersCodexResponsesHistorySanitizationToOpenAIOAuthWorker(req, oauth, "codex_responses") {
		t.Fatal("openai oauth codex responses 请求延迟清洗")
	}
	// 非 responses 端点不延迟。
	chatReq := newTestRequest(t, `{"model":"gpt-test"}`)
	if engine.defersCodexResponsesHistorySanitizationToOpenAIOAuthWorker(chatReq, oauth, "codex_responses") {
		t.Fatal("非 responses 端点不延迟")
	}
	// 非 codex_responses 兼容不延迟。
	if engine.defersCodexResponsesHistorySanitizationToOpenAIOAuthWorker(req, oauth, "") {
		t.Fatal("非 codex_responses 不延迟")
	}
}

// ---------------------------------------------------------------------------
// oauthnormalizer.go / oauthadapter.go
// ---------------------------------------------------------------------------

func TestNormalizeOpenAIOAuthCodexRawBodyVariants(t *testing.T) {
	input := OpenAIOAuthCodexNormalizeInput{Account: OpenAIOAuthCodexAccount{ID: "a-1"}}
	// 非法 JSON → adapter 错误。
	if _, err := NormalizeOpenAIOAuthCodexRawBody([]byte("nope"), input); !IsOpenAIOAuthCodexAdapterError(err) {
		t.Fatalf("expected adapter error, got %v", err)
	}
	// 缺 model → 校验错误。
	if _, err := NormalizeOpenAIOAuthCodexRawBody([]byte(`{"input":[]}`), input); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("expected model validation, got %v", err)
	}
	// 数组 body → 非对象错误。
	if _, err := NormalizeOpenAIOAuthCodexRawBody([]byte(`[1]`), input); !IsOpenAIOAuthCodexAdapterError(err) {
		t.Fatalf("expected adapter error, got %v", err)
	}
	// 合法请求。
	result, err := NormalizeOpenAIOAuthCodexRawBody([]byte(`{"model":"gpt-test","input":"hi"}`), input)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if result.Stream != true || result.Body == "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestEnsureOpenAIOAuthCodexPlainJsonObject(t *testing.T) {
	object, err := EnsureOpenAIOAuthCodexPlainJsonObject(map[string]any{"a": 1})
	if err != nil || object["a"] != float64(1) {
		t.Fatalf("object = %#v err = %v", object, err)
	}
	// 克隆语义：修改克隆不影响原对象。
	object["a"] = 2
	if EnsureOpenAIOAuthCodexPlainJsonObject(map[string]any{"a": 1})["a"] != float64(1) {
		t.Fatal("克隆必须隔离")
	}
	if _, err := EnsureOpenAIOAuthCodexPlainJsonObject("str"); !IsOpenAIOAuthCodexAdapterError(err) {
		t.Fatalf("expected adapter error, got %v", err)
	}
}

func TestSanitizeOpenAIOAuthCodexHistoryHook(t *testing.T) {
	// nil sanitizer 保持原样。
	body := map[string]any{"input": []any{"a"}}
	sanitizeOpenAIOAuthCodexHistory(body, "a-1")
	if len(body["input"].([]any)) != 1 {
		t.Fatal("nil sanitizer 保持原样")
	}
	// input 非数组不处理。
	sanitizeOpenAIOAuthCodexHistory(map[string]any{"input": "text"}, "a-1")
	// 注入 sanitizer 后改写。
	previous := SanitizeCodexHistory
	SanitizeCodexHistory = func(items []any, options SanitizeCodexHistoryOptions) CodexHistorySanitizeResult {
		return CodexHistorySanitizeResult{Items: []any{"x"}, Changed: true}
	}
	t.Cleanup(func() { SanitizeCodexHistory = previous })
	target := map[string]any{"input": []any{"a"}}
	sanitizeOpenAIOAuthCodexHistory(target, "a-1")
	if target["input"].([]any)[0] != "x" {
		t.Fatalf("input = %#v", target["input"])
	}
}

func TestNormalizeOpenAIOAuthCodexLegacyFunctions(t *testing.T) {
	body := map[string]any{
		"functions": []any{
			map[string]any{"name": "f", "parameters": map[string]any{"type": "object"}, "description": "d"},
		},
		"function_call": "auto",
	}
	normalizeOpenAIOAuthCodexLegacyFunctions(body)
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "f" {
		t.Fatalf("tool = %#v", tool)
	}
	if body["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v", body["tool_choice"])
	}
}

func TestParseOpenAIOAuthCodexJsonObjectBodyBranches(t *testing.T) {
	// 解析态对象直接返回。
	parsed := newTestRequest(t, `{"model":"gpt-test"}`)
	value, err := parseOpenAIOAuthCodexJsonObjectBody(parsed)
	if err != nil {
		t.Fatalf("parsed body: %v", err)
	}
	if value.(map[string]any)["model"] != "gpt-test" {
		t.Fatalf("value = %#v", value)
	}
	// 非对象 body 原样返回。
	nonObject := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	nonObject.Body = newTestRequestBody(t, `[1,2]`)
	nonObject.Body.Body = []any{float64(1), float64(2)}
	value, err = parseOpenAIOAuthCodexJsonObjectBody(nonObject)
	if err != nil {
		t.Fatalf("non object: %v", err)
	}
	if _, ok := value.([]any); !ok {
		t.Fatalf("value = %#v", value)
	}
	// 非法原始 body → adapter 错误。
	invalid := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	invalid.Body = &gatewaybody.Request{RawBody: []byte("not-json")}
	if _, err := parseOpenAIOAuthCodexJsonObjectBody(invalid); !IsOpenAIOAuthCodexAdapterError(err) {
		t.Fatalf("expected adapter error, got %v", err)
	}
	// 空 body → 空对象。
	empty := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	value, err = parseOpenAIOAuthCodexJsonObjectBody(empty)
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if len(value.(map[string]any)) != 0 {
		t.Fatalf("value = %#v", value)
	}
	// 无 body 的 raw 读取。
	if rawBodyOf(nil) != nil {
		t.Fatal("nil 请求 raw 为 nil")
	}
	if gatewaypreauthJSONParseStatusInvalid() != "invalid_json" {
		t.Fatal("parse status 常量不符")
	}
}

func TestBuildOpenAIOAuthCodexRequestPartsVariants(t *testing.T) {
	account := OpenAIOAuthCodexAccount{ID: "a-1", APIKey: "key-1", Credentials: map[string]any{"account_id": "acc-1"}}
	identity := OpenAIOAuthCodexIdentity{}
	headers := http.Header{"Content-Type": []string{"application/json"}}
	// GET 请求：空部件。
	getReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	parts, err := BuildOpenAIOAuthCodexRequestParts(getReq, headers, account, identity, OpenAIOAuthCodexRequestOptions{})
	if err != nil {
		t.Fatalf("GET parts: %v", err)
	}
	if parts.Body != nil {
		t.Fatalf("GET body = %s", parts.Body)
	}
	// POST 合法请求。
	postReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	postReq.Body = newTestRequestBody(t, `{"model":"gpt-test","input":"hi"}`)
	parts, err = BuildOpenAIOAuthCodexRequestParts(postReq, headers, account, identity, OpenAIOAuthCodexRequestOptions{})
	if err != nil {
		t.Fatalf("POST parts: %v", err)
	}
	if !bytes.Contains(parts.Body, []byte("gpt-test")) {
		t.Fatalf("body = %s", parts.Body)
	}
	if parts.Headers.Get("Authorization") != "Bearer key-1" {
		t.Fatalf("authorization = %q", parts.Headers.Get("Authorization"))
	}
	if parts.Headers.Get("Chatgpt-Account-Id") != "acc-1" {
		t.Fatalf("account id = %q", parts.Headers.Get("Chatgpt-Account-Id"))
	}
	// 非法证明头 → adapter 错误。
	badHeaders := http.Header{"X-Oai-Attestation": []string{"bad\r\nheader"}}
	if _, err := BuildOpenAIOAuthCodexRequestParts(postReq, badHeaders, account, identity, OpenAIOAuthCodexRequestOptions{}); !IsOpenAIOAuthCodexAdapterError(err) {
		t.Fatalf("expected attestation error, got %v", err)
	}
	// compact 请求 Accept 为 JSON。
	compactReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil))
	compactReq.Body = newTestRequestBody(t, `{"model":"gpt-test","input":"hi"}`)
	parts, err = BuildOpenAIOAuthCodexRequestParts(compactReq, headers, account, identity, OpenAIOAuthCodexRequestOptions{})
	if err != nil {
		t.Fatalf("compact parts: %v", err)
	}
	if parts.Headers.Get("Accept") != "application/json" {
		t.Fatalf("accept = %q", parts.Headers.Get("Accept"))
	}
	// 空 path 的 compact 判定为 false。
	emptyReq := gatewaypreauth.NewGatewayRequest(&http.Request{Method: http.MethodPost, URI: func() string { return "" }})
	if IsOpenAIOAuthCodexCompactRequest(emptyReq) {
		t.Fatal("空 path 不判 compact")
	}
}

// ---------------------------------------------------------------------------
// builtintools.go
// ---------------------------------------------------------------------------

func TestNormalizeOpenAICodexBuiltinToolsVariants(t *testing.T) {
	body := map[string]any{
		"tools": []any{
			map[string]any{"type": "web_search_preview"},
			map[string]any{"type": "function", "name": "f"},
		},
		"tool_choice": map[string]any{
			"type": "web_search_preview_2025_03_11",
			"tools": []any{
				map[string]any{"type": "web_search_preview"},
			},
		},
	}
	NormalizeOpenAICodexBuiltinTools(body)
	tools := body["tools"].([]any)
	if tools[0].(map[string]any)["type"] != "web_search" {
		t.Fatalf("tool[0] = %#v", tools[0])
	}
	if tools[1].(map[string]any)["type"] != "function" {
		t.Fatalf("tool[1] = %#v", tools[1])
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["type"] != "web_search" {
		t.Fatalf("choice type = %#v", choice["type"])
	}
	choiceTools := choice["tools"].([]any)
	if choiceTools[0].(map[string]any)["type"] != "web_search" {
		t.Fatalf("choice tools = %#v", choiceTools)
	}
	// tool_choice 无 tools 键时整体替换。
	choiceOnly := map[string]any{"tool_choice": map[string]any{"type": "web_search_preview"}}
	NormalizeOpenAICodexBuiltinTools(choiceOnly)
	if choiceOnly["tool_choice"].(map[string]any)["type"] != "web_search" {
		t.Fatalf("choice only = %#v", choiceOnly["tool_choice"])
	}
	// 非 map 工具原样保留。
	rawTools := map[string]any{"tools": []any{"str"}}
	NormalizeOpenAICodexBuiltinTools(rawTools)
	if rawTools["tools"].([]any)[0] != "str" {
		t.Fatal("非对象工具原样保留")
	}
	// 无 tools/tool_choice 不 panic。
	NormalizeOpenAICodexBuiltinTools(map[string]any{})
}

// ---------------------------------------------------------------------------
// usageheaders.go
// ---------------------------------------------------------------------------

func TestBuildOpenAICodexUsageSnapshotPayloadNormalization(t *testing.T) {
	snapshot := OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:         ptrFloat64(41.5),
		PrimaryResetAfterSeconds:   ptrInt64V(3600),
		PrimaryWindowMinutes:       ptrInt64V(300),
		SecondaryUsedPercent:       ptrFloat64(12.5),
		SecondaryResetAfterSeconds: ptrInt64V(604800),
		SecondaryWindowMinutes:     ptrInt64V(10080),
	}
	payload := buildOpenAICodexUsageSnapshotPayload(snapshot, timeNowForTest(), "test-source")
	if payload["codex_5h_used_percent"] != 41.5 {
		t.Fatalf("5h = %#v", payload["codex_5h_used_percent"])
	}
	if payload["codex_7d_reset_after_seconds"] != int64(604800) {
		t.Fatalf("7d = %#v", payload["codex_7d_reset_after_seconds"])
	}
	if payload["source"] != "test-source" {
		t.Fatalf("source = %#v", payload["source"])
	}
	if _, ok := payload["codex_5h_reset_at"]; !ok {
		t.Fatal("5h reset_at 缺失")
	}
	// 无窗口数据时只有基础字段。
	empty := buildOpenAICodexUsageSnapshotPayload(OpenAICodexUsageSnapshot{UpdatedAt: "1970-01-01T00:00:01Z"}, timeNowForTest(), "")
	if _, ok := empty["codex_5h_used_percent"]; ok {
		t.Fatal("空窗口不应有 5h 字段")
	}
	// 无 codex 头 → 无任务。
	if BuildOpenAICodexUsageRecordMaintenanceJob("a-1", http.Header{}, "test") != nil {
		t.Fatal("无 codex 头不生成任务")
	}
	// loose ISO 解析（毫秒格式）。
	if parseIsoDate("2026-01-02T03:04:05.999Z") == nil {
		t.Fatal("loose ISO 解析失败")
	}
}

func ptrFloat64(value float64) *float64 { return &value }

func ptrInt64V(value int64) *int64 { return &value }

// ---------------------------------------------------------------------------
// headerpolicy.go
// ---------------------------------------------------------------------------

func TestGeminiScopedHeadersAndHeadersToObject(t *testing.T) {
	if !IsGeminiGenerateContentScopedHeaderName("X-Goog-Api-Key") {
		t.Fatal("已知 gemini scoped 头")
	}
	if !IsGeminiGenerateContentScopedHeaderName("x-vertex-ai-llm-request-type") {
		t.Fatal("前缀匹配 scoped 头")
	}
	if IsGeminiGenerateContentScopedHeaderName("content-type") {
		t.Fatal("普通头不匹配")
	}
	headers := http.Header{
		"X-Goog-Api-Key": []string{"secret"},
		"Content-Type":   []string{"application/json"},
	}
	StripGeminiGenerateContentScopedHeaders(headers)
	if headers.Get("X-Goog-Api-Key") != "" {
		t.Fatal("gemini scoped 头必须剥离")
	}
	if headers.Get("Content-Type") != "application/json" {
		t.Fatal("普通头保留")
	}
	object := HeadersToObject(http.Header{"X-Multi": []string{"a", "b"}, "X-Empty": {}})
	if object["X-Multi"] != "a, b" {
		t.Fatalf("object = %#v", object)
	}
	if _, ok := object["X-Empty"]; ok {
		t.Fatal("空值头不输出")
	}
}

// ---------------------------------------------------------------------------
// transport.go 纯函数面
// ---------------------------------------------------------------------------

func TestUpstreamTimeoutHelpers(t *testing.T) {
	profile := gatewayrouting.GatewayTimeoutProfile{
		FirstResponseTimeoutMs: 30_000,
		IdleTimeoutMs:          120_000,
	}
	if UpstreamSocketTimeoutMs(nil, profile, nil) == nil {
		t.Fatal("非禁用超时应返回值")
	}
	disabled := profile
	disabled.TimeoutsDisabled = true
	if UpstreamSocketTimeoutMs(nil, disabled, nil) != nil {
		t.Fatal("禁用超时返回 nil")
	}
	if UpstreamRequestTimeoutMs(disabled) != nil {
		t.Fatal("禁用请求超时返回 nil")
	}
	if got := UpstreamRequestTimeoutMs(profile); got == nil || *got != 30_000 {
		t.Fatalf("request timeout = %#v", got)
	}
	// 流式请求 socket 超时覆盖 idle+15s。
	streamReq := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	socket := UpstreamSocketTimeoutMs(streamReq, profile, &UpstreamHeaderAccount{})
	if socket == nil || *socket != 135_000 {
		t.Fatalf("stream socket timeout = %#v", socket)
	}
	// oauth compact 规则：compact 请求非流式。
	compactAccount := &UpstreamHeaderAccount{Type: "oauth"}
	compactReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil))
	if IsEffectiveOpenAIStreamRequest(compactReq, compactAccount) {
		t.Fatal("compact 请求非流式")
	}
	if !IsEffectiveOpenAIStreamRequest(newTestRequest(t, `{"model":"gpt-test","stream":true}`), nil) {
		t.Fatal("普通流式请求识别")
	}
	// 非 oauth 账户不触发 compact 规则。
	if !IsEffectiveOpenAIStreamRequest(compactReq, &UpstreamHeaderAccount{Type: "api_key"}) {
		t.Fatal("非 oauth 账户按 body stream 判定")
	}
}

func TestNormalizeTransportErrorVariants(t *testing.T) {
	if normalizeTransportError(nil) != nil {
		t.Fatal("nil 透传")
	}
	normalized := normalizeTransportError(context.DeadlineExceeded)
	if _, ok := normalized.(*UpstreamRequestTimeoutError); !ok {
		t.Fatalf("deadline → timeout 错误, got %#v", normalized)
	}
	normalized = normalizeTransportError(errors.New("request TIMEOUT-ish"))
	if _, ok := normalized.(*UpstreamRequestTimeoutError); !ok {
		t.Fatalf("超时文本 → timeout 错误, got %#v", normalized)
	}
	plain := errors.New("connection refused")
	if normalizeTransportError(plain) != plain {
		t.Fatal("普通错误原样返回")
	}
}

func TestParseContentEncodingsAndIdentity(t *testing.T) {
	if parseContentEncodings("") != nil {
		t.Fatal("空编码为 nil")
	}
	encodings := parseContentEncodings("Gzip, identity , br")
	if len(encodings) != 3 || encodings[0] != "gzip" || encodings[2] != "br" {
		t.Fatalf("encodings = %#v", encodings)
	}
	if allIdentity([]string{"identity"}) != true || allIdentity([]string{"gzip"}) != false {
		t.Fatal("identity 判定不符")
	}
}

func TestDecodeUpstreamResponseBodyGzipError(t *testing.T) {
	// 非法 gzip 流 → 已开始的 body 传输错误。
	body := io.NopCloser(bytes.NewReader([]byte("not gzip")))
	_, err := decodeUpstreamResponseBody(body, "gzip")
	var started *StartedBodyTransportError
	if !errorsAs(err, &started) {
		t.Fatalf("expected started body error, got %v", err)
	}
	// 不支持的编码 → 编码错误 + 关闭。
	closed := false
	failing := &trackedCloser{reader: strings.NewReader("x"), closed: &closed}
	_, err = decodeUpstreamResponseBody(failing, "zstd")
	if err == nil || !strings.Contains(err.Error(), "不支持的上游响应压缩编码") {
		t.Fatalf("err = %v", err)
	}
	if !closed {
		t.Fatal("不支持的编码必须关闭 body")
	}
	// identity 透传。
	pass := strings.NewReader("plain")
	result, err := decodeUpstreamResponseBody(io.NopCloser(pass), "identity")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	data, _ := io.ReadAll(result)
	if string(data) != "plain" {
		t.Fatalf("data = %s", data)
	}
}

type trackedCloser struct {
	reader io.Reader
	closed *bool
}

func (t *trackedCloser) Read(buffer []byte) (int, error) { return t.reader.Read(buffer) }
func (t *trackedCloser) Close() error                    { *t.closed = true; return nil }

func TestReadersteamsChunkWithTimeouts(t *testing.T) {
	// EOF 读取。
	n, err := ReadStreamChunkWithAbort(context.Background(), strings.NewReader("ok"), make([]byte, 8))
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// 取消信号 → aborted。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ReadStreamChunkWithAbort(canceled, strings.NewReader("x"), make([]byte, 8))
	if !IsUpstreamRequestAbortedError(err) {
		t.Fatalf("err = %v", err)
	}
	// 阻塞 reader + 1s idle 超时 → 上游流超时（真实的超时分支语义）。
	blocked := newBlockingReader()
	t.Cleanup(blocked.close)
	_, err = ReadStreamChunkWithIdleTimeout(context.Background(), blocked, make([]byte, 8), 1)
	var timeoutErr *UpstreamRequestTimeoutError
	if !errorsAs(err, &timeoutErr) || !strings.Contains(timeoutErr.Message, "无数据") {
		t.Fatalf("idle timeout err = %v", err)
	}
	// 取消信号优先于 idle 计时。
	blocked2 := newBlockingReader()
	t.Cleanup(blocked2.close)
	_, err = ReadStreamChunkWithIdleTimeout(canceled, blocked2, make([]byte, 8), 30)
	if !IsUpstreamRequestAbortedError(err) {
		t.Fatalf("idle timeout with cancel: %v", err)
	}
}
