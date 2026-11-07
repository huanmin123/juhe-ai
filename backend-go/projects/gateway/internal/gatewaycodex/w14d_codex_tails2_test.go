// w14d_codex_tails2_test.go covers strategy protocol detection, codex usage
// header plumbing, encrypted-content sanitizers, compaction contract search
// arms and source identity fallbacks. 2026-11-07 bridge retirement removed
// the bridge tails cases and the contextstore/segments/chatbridgestate notes below.
//
// w14d 剩余未覆盖语句登记（go test ./internal/gatewaycodex/ -cover，共 125
// 条，均为防御分支、需单请求中途注入 store 故障、或需要真实 PostgreSQL
// driver 的路径，不构成业务缺口）：
//   - turnretry.go：CAS 错误透传与耗尽（437、543、549-551、621、627-629，
//     fake 注入的 CAS=false 已覆盖 remember 侧，clear 侧需先写入再换成
//     失败 store 的组合）、memory 清理循环（485-499、644、669、722）、
//     观察窗口尾部（835/855/875 需 32+ 条观测）。
//   - turnprobe 协调错误臂（166-212、302/326/380 需 handoff 故障
//     注入）、compactioncontract 前缀匹配（116，前缘字节序依赖）、
//     strategy.go derive state key（274，需已解析的 official session）、
//     gemini 空路径归一（581）、sourceidentity gemini interaction 回调
//     （119/122，需注入回调）、codexheaders secondary window（57，表头已在
//     56 断言）、clock rand 失败（39）。
package gatewaycodex

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestW14dStrategyProtocolArms(t *testing.T) {
	// A path outside every protocol family falls back to openai_v1 (L130).
	fallback := newTestRequest(t, "POST", "/v1/other", nil, nil)
	if got := ResponseProtocolForRequest(fallback); got != "openai_v1" {
		t.Fatalf("fallback protocol = %q", got)
	}

	// Native anthropic request routes to the anthropic resolver (L175) and
	// explicit claude-code headers mark the profile (L246); the avoidance
	// state key propagates when an identity endpoint is present (L274).
	anthropic := newTestRequest(t, "POST", "/v1/messages", nil, map[string]string{
		"anthropic-version": "2023-06-01",
		"x-api-key":         "key",
	})
	if got := ResponseProtocolForRequest(anthropic); got != "anthropic_v1" {
		t.Fatalf("anthropic protocol = %q", got)
	}
	anthropicExplicit := newTestRequest(t, "POST", "/v1/messages", nil, map[string]string{
		"anthropic-version":        "2023-06-01",
		GatewayClientProfileHeader: "claude_code",
	})
	deps := &ClientStrategyDeps{}
	strategy := deps.ResolveOpenAIGatewayClientStrategy(anthropicExplicit, ClientStrategyIdentity{Endpoint: "/v1/messages"})
	if strategy.ClientProfile != ClientProfileClaudeCode || strategy.RequestClientCompatibility != CompatibilityClaudeCode {
		t.Fatalf("anthropic strategy = %+v", strategy)
	}

	// Native gemini request routes to the gemini resolver (L178) with the
	// explicit gemini_cli profile (L293).
	gemini := newTestRequest(t, "POST", "/v1beta/models/gemini:generatecontent", nil, map[string]string{
		GatewayClientProfileHeader: "gemini_cli",
	})
	if got := ResponseProtocolForRequest(gemini); got != "gemini_v1beta" {
		t.Fatalf("gemini protocol = %q", got)
	}
	geminiStrategy := deps.ResolveOpenAIGatewayClientStrategy(gemini, ClientStrategyIdentity{Endpoint: "/v1beta/models/gemini:generatecontent"})
	if geminiStrategy.ClientProfile != ClientProfileGeminiCLI {
		t.Fatalf("gemini strategy = %+v", geminiStrategy)
	}
}

func TestW14dStrategyStreamArms(t *testing.T) {
	cases := []struct {
		name   string
		method string
		target string
		header string
		want   string
	}{
		// Non-messages openai path with an SSE accept (L396).
		{"openai other stream", "POST", "/v1/other", "Accept", "unknown_stream"},
		// Gemini interactions POST/GET with stream (L416/422).
		{"gemini interactions post", "POST", "/v1beta/interactions", "Accept", "gemini_interactions_sse"},
		{"gemini interactions get", "GET", "/v1beta/interactions/abc", "Accept", "gemini_interactions_sse"},
		// Gemini unknown stream path (L427).
		{"gemini other stream", "POST", "/v1beta/other", "Accept", "unknown_stream"},
	}
	for _, testCase := range cases {
		req := newTestRequest(t, testCase.method, testCase.target, nil, map[string]string{
			testCase.header: "text/event-stream",
		})
		got := ResolveGeminiGatewayDownstreamProtocol(req)
		if testCase.name == "openai other stream" {
			got = ResolveOpenAIGatewayDownstreamProtocol(req)
		}
		if got != testCase.want {
			t.Fatalf("%s: got %q want %q", testCase.name, got, testCase.want)
		}
	}

	// Profile header normalization (L507), gemini path edges (L581/584),
	// claude-code signature guards (L619/656) and query parsing (L736).
	if got := parseGatewayClientProfileHeader("gemini cli"); got != ClientProfileGeminiCLI {
		t.Fatalf("profile header = %q", got)
	}
	emptyPath := newTestRequest(t, "GET", "/", nil, nil)
	if got := normalizedGeminiRequestPath(emptyPath); got == "" {
		t.Fatal("root path must normalize")
	}
	relative := gatewaypreauth.NewGatewayRequest(&http.Request{Method: http.MethodGet, URL: &url.URL{Path: "models/x"}})
	if got := normalizedGeminiRequestPath(relative); got != "/models/x" {
		t.Fatalf("relative path = %q", got)
	}
	if isClaudeCodeAnthropicRequestSignature(newTestRequest(t, "GET", "/v1/messages", nil, nil)) {
		t.Fatal("GET must not be a claude code signature")
	}
	signature := newTestRequest(t, "POST", "/v1/messages", nil, map[string]string{
		"anthropic-beta": "prompt-caching",
	})
	if isClaudeCodeAnthropicRequestSignature(signature) {
		t.Fatal("beta header without claude-code marker must not match")
	}
	values := parseURLSearchParams("&&a=1&&b=2")
	if values["a"] != "1" || values["b"] != "2" || len(values) != 2 {
		t.Fatalf("values = %v", values)
	}
}

func TestW14dCodexHeaderTails(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	if ParseOpenAICodexUsageHeaders(nil, now) != nil {
		t.Fatal("nil headers must read as undefined")
	}
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "42.5")
	headers.Set("x-codex-secondary-reset-after-seconds", "30.9")
	headers.Set("x-codex-secondary-window-minutes", "  ")
	snapshot := ParseOpenAICodexUsageHeaders(headers, now)
	if snapshot == nil || snapshot.SecondaryResetAfterSeconds == nil || *snapshot.SecondaryResetAfterSeconds != 30 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	// Whitespace-only numbers read as missing (L79).
	empty := http.Header{}
	empty.Set("x-codex-primary-used-percent", "   ")
	if got := ParseOpenAICodexUsageHeaders(empty, now); got != nil {
		t.Fatalf("whitespace-only header must read as undefined, got %+v", got)
	}
	// Non-oauth accounts skip the persistence (L112 side of the guard).
	PersistOpenAICodexHeadersIfNeeded(context.Background(),
		gatewayruntimecache.OpenAIAccountSecret{ID: "acc", Type: "api_key", ProtocolCode: "openai"},
		headers, "probe", SystemClock{}, nil)
	// OAuth account with a nil dispatcher also skips.
	PersistOpenAICodexHeadersIfNeeded(context.Background(),
		gatewayruntimecache.OpenAIAccountSecret{ID: "acc", Type: "oauth", ProtocolCode: "openai", ProtocolVersion: "v1"},
		headers, "probe", SystemClock{}, nil)
}

func TestW14dEncryptedContentSanitizerArms(t *testing.T) {
	// parseJSONRecord non-object payload (L261).
	if parseJSONRecord("[1,2]") != nil {
		t.Fatal("array payload must read as nil")
	}
	// Input neither array nor object (L304).
	body := map[string]any{"input": "scalar"}
	if result := removeRejectedCodexEncryptedContent(body); result.changed {
		t.Fatal("scalar input must be unchanged")
	}
	// Non-object input items pass through (L311).
	changed := map[string]any{"input": []any{"scalar", map[string]any{
		"type": "reasoning", "encrypted_content": "sealed", "summary": "kept",
	}}}
	result := removeRejectedCodexEncryptedContent(changed)
	if !result.changed || result.removedReasoningEncryptedContentCount != 1 {
		t.Fatalf("reasoning strip = %+v", result)
	}
	// function_call_output / agent_message replacements (L361).
	outputs := map[string]any{"input": []any{map[string]any{
		"type": "function_call_output", "call_id": "c1",
		"output": []any{map[string]any{"type": "encrypted_content", "encrypted_content": "sealed"}, map[string]any{"type": "other"}},
	}}}
	outputResult := removeRejectedCodexEncryptedContent(outputs)
	if !outputResult.changed || outputResult.removedFunctionOutputEncryptedContentCount != 1 {
		t.Fatalf("output strip = %+v", outputResult)
	}
	agent := map[string]any{"input": []any{map[string]any{
		"type": "agent_message", "content": []any{map[string]any{"type": "encrypted_content", "encrypted_content": "sealed"}},
	}}}
	agentResult := removeRejectedCodexEncryptedContent(agent)
	if !agentResult.changed || agentResult.removedAgentMessageItemCount != 1 {
		t.Fatalf("agent strip = %+v", agentResult)
	}
	// Single object input collapses back (L393).
	objectInput := map[string]any{"input": map[string]any{
		"type": "reasoning", "encrypted_content": "sealed", "summary": "kept",
	}}
	objectResult := removeRejectedCodexEncryptedContent(objectInput)
	if !objectResult.changed {
		t.Fatal("object input strip must change")
	}
	if _, isArray := objectInput["input"].([]any); isArray {
		t.Fatal("object input must collapse back to the object")
	}
	// Item predicates (L439) and empty reasoning detection (L453/459/460).
	if isEncryptedContentItem("scalar") {
		t.Fatal("scalar is not an encrypted content item")
	}
	if !isEmptyReasoningItem(map[string]any{"type": "reasoning", "summary": nil}) {
		t.Fatal("nil-only extras read as empty")
	}
	if !isEmptyReasoningItem(map[string]any{"type": "reasoning", "summary": "  "}) {
		t.Fatal("blank-only extras read as empty")
	}
	if isEmptyReasoningItem(map[string]any{"type": "reasoning", "summary": "text"}) {
		t.Fatal("text extras are not empty")
	}

	// Endpoint family helpers (L489/497/508/517).
	if got := gatewayRequestEndpointFamily(nil, ""); got != "" {
		t.Fatalf("nil req family = %q", got)
	}
	if got := openAIRequestEndpointFamily("/v1/chat/completions?x=1"); got != gatewayopenai.FamilyChatCompletions {
		t.Fatalf("chat family = %q", got)
	}
	if got := openAIEndpointFamilyFromPath("   "); got != "" {
		t.Fatalf("blank path family = %q", got)
	}
	if got := openAIEndpointFamilyFromPath("/v1/embeddings"); got != "" {
		t.Fatalf("unknown family = %q", got)
	}
}

func TestW14dCompactionContractArms(t *testing.T) {
	// Nil request and empty paths read as non-responses (L75/87).
	if isOpenAIResponsesPostRequest(nil) {
		t.Fatal("nil request must not match")
	}
	blankBody := gatewaybody.Request{}
	blankReq := gatewaypreauth.NewGatewayRequest(&http.Request{Method: http.MethodPost, URL: &url.URL{}})
	blankReq.Body = &blankBody
	if isOpenAIResponsesCompactPostRequest(blankReq) {
		t.Fatal("blank path must not match compact")
	}

	// Raw body scan: trigger inside the leading edge (L116).
	edge := codexCompactionRawBodyScanEdgeBytes
	prefix := []byte(`{"pad":"` + strings.Repeat("p", edge/2) + `","type":"compaction_trigger"}`)
	prefix = append(prefix, make([]byte, edge)...)
	request := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	request.Body = &gatewaybody.Request{RawBody: prefix}
	if !requestBodyHasCompactionTrigger(request) {
		t.Fatal("leading edge trigger must match")
	}
	// Deep cycle keys in maps and slices (L134/152) and the visited cap
	// (L165).
	cyclicMap := map[string]any{}
	cyclicMap["self"] = cyclicMap
	if jsonValueHasCompactionTrigger(cyclicMap, 0, map[uintptr]struct{}{}) {
		t.Fatal("cyclic map has no trigger")
	}
	cyclicSlice := make([]any, 1)
	cyclicSlice[0] = cyclicSlice
	if jsonValueHasCompactionTrigger(cyclicSlice, 0, map[uintptr]struct{}{}) {
		t.Fatal("cyclic slice has no trigger")
	}
	wide := map[string]any{}
	for index := 0; index < 300; index++ {
		wide[string(rune('a'+index%26))+string(rune(index))] = index
	}
	if jsonValueHasCompactionTrigger(wide, 0, map[uintptr]struct{}{}) {
		t.Fatal("wide map has no trigger")
	}
}

func TestW14dSourceIdentityTails(t *testing.T) {
	// Empty endpoint yields an empty state key (L172).
	resolver := &SourceIdentityResolver{Secret: "secret"}
	if got := resolver.DeriveGatewayClientSourceStateKey(GatewayClientSourceIdentity{SourceKey: "sk"}, struct {
		ClientProfile      string
		Endpoint           string
		DownstreamProtocol string
	}{Endpoint: ""}); got != "" {
		t.Fatalf("state key = %q", got)
	}
	// Blank secret trips the panic guard (L236).
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("blank secret must trip the guard")
			}
		}()
		gatewayClientSourceHMAC("  ", "state:v1", []string{"a"})
	}()
}

func TestW14dHistorySanitizerReplayableKinds(t *testing.T) {
	if !IsReplayableCodexHistoryItem(map[string]any{
		"type": "custom_tool_call", "name": "n", "input": "{}", "call_id": "c1",
	}) {
		t.Fatal("custom_tool_call is replayable")
	}
	if !IsReplayableCodexHistoryItem(map[string]any{
		"type": "tool_search_output", "execution": "done", "tools": []any{},
	}) {
		t.Fatal("tool_search_output is replayable")
	}
	if !IsReplayableCodexHistoryItem(map[string]any{
		"type": "image_generation_call", "status": "done", "result": "png",
	}) {
		t.Fatal("image_generation_call is replayable")
	}
	if IsReplayableCodexHistoryItem(map[string]any{"type": "custom_tool_call", "name": "n"}) {
		t.Fatal("partial custom_tool_call is not replayable")
	}
}

func TestW14dPortsAuditMetadataFallback(t *testing.T) {
	port := &ClientStrategyPort{}
	metadata := port.AuditMetadata(gatewaypreauth.ClientStrategyContext{Opaque: "unexpected"})
	if metadata == nil {
		t.Fatal("fallback metadata must render")
	}
}
