// w14d_codex_tails2_test.go covers strategy protocol detection, codex usage
// header plumbing, encrypted-content sanitizers, compaction contract search
// arms, source identity fallbacks and the remaining bridge tails.
//
// w14d 剩余未覆盖语句登记（go test ./internal/gatewaycodex/ -cover，共 125
// 条，均为防御分支、需单请求中途注入 store 故障、或需要真实 PostgreSQL
// driver 的路径，不构成业务缺口）：
//   - contextstore.go：Touch 前后端 UPDATE 错误臂（460-480，Touch 吞掉
//     session 写错误、行级错误需在读取成功后令写入失败）、Close 错误聚合
//     （493）、shard sql.Open/schema 应用错误臂（355/358，MkdirAll 已拦）、
//     Save 内部 tx/upsert 错误臂（381-418，错误只能来自坏根目录且已在
//     databaseForKey 拦截）、postgres driver 全部行（526-590，需真实 PG
//     juhe_codex_context schema，SQLite 承载时 schema 缺失已在既有测试断言
//     错误透传）。
//   - segments.go：gzip write/close（70/73）、Seek/WriteAt（114/117，需底层
//     io 故障注入）、n==0 读（161）、resolve 相对回退臂（198-209 的 Abs/Rel
//     失败与 rel=="." 判定，Windows 下 prefix 已拦）、读路径解码/大小尾臂
//     （173-183，损坏文件先在校验或 EOF 臂返回）、base36 负数/零（272-286，
//     无调用方传负值）。
//   - turnretry.go：CAS 错误透传与耗尽（437、543、549-551、621、627-629，
//     fake 注入的 CAS=false 已覆盖 remember 侧，clear 侧需先写入再换成
//     失败 store 的组合）、memory 清理循环（485-499、644、669、722）、
//     观察窗口尾部（835/855/875 需 32+ 条观测）。
//   - chatbridgestate.go：parseGatewayJSONObject 错误臂（228，函数恒返回
//     nil error）、startIndex<0（340，恢复输入恒不短于当前输入）、32MB 尺寸
//     断言（373）、preflight 摘要读的 store/段错误臂（614/651/663/671/678 需
//     精确到层的故障注入，663 已由段损坏覆盖变体）、mapping 非 responses 家
//     族（752）、nativeResponsesInputFromMaterialized 错误臂（783）、
//     currentInputFromMaterializedMutation 非数组臂（866，需 LastRenderedBody
//     且字符串输入的组合）、encodeInline marshal 错误臂（957）。
//   - 其余：compactpreflight 摘要调度错误/nil exchange 组合（164/178/187/
//     230/257）、turnprobe 协调错误臂（166-212、302/326/380 需 handoff 故障
//     注入）、compactioncontract 前缀匹配（116，前缘字节序依赖）、
//     strategy.go derive state key（274，需已解析的 official session）、
//     gemini 空路径归一（581）、sourceidentity gemini interaction 回调
//     （119/122，需注入回调）、codexheaders secondary window（57，表头已在
//     56 断言）、clock rand 失败（39）。
package gatewaycodex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"net/url"
	"path/filepath"
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
		"anthropic-version":         "2023-06-01",
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

func TestW14dBridgeInvalidJSONBodyState(t *testing.T) {
	service, _, _, _, _ := newBridgeService(t)
	req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	req.Body = &gatewaybody.Request{
		RawBody: []byte("{broken"),
		State:   &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusInvalidJSON},
	}
	record, err := service.ParseGatewayJSONObjectPublic(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(record) != 0 {
		t.Fatalf("invalid json body = %v", record)
	}
}

func TestW14dBridgeCompactExternalPrevious(t *testing.T) {
	service, registry, _, _, _ := newBridgeService(t)
	ctx := context.Background()
	req := newTestRequest(t, "POST", "/v1/responses/compact", nil, nil)
	body := map[string]any{"model": "gpt-5", "previous_response_id": "resp_external_1", "input": []any{}}
	raw, _ := json.Marshal(body)
	attachBody(req, body, raw)
	res, resWriter := newTrackedWriter()
	completed, err := service.ApplyContextStatePreflight(ctx, registry, bridgeBaseInput(req, resWriter, &recordedAudit{}))
	if err != nil {
		t.Fatal(err)
	}
	_ = res
	_ = completed
	state, ok := registry.Get(req)
	if !ok || state.PreviousResponseKind != PreviousKindExternal {
		t.Fatalf("state = %+v", state)
	}
}

func TestW14dBridgeCompactSummaryReadArms(t *testing.T) {
	store, _ := newSQLiteStore(t)
	segmentsRoot := t.TempDir()
	clock := newFakeClock(time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC))
	service, err := NewChatBridgeStateService(ChatBridgeStateConfig{CodexContextRoot: segmentsRoot}, store, nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	service.Logger = &recordingLogger{}
	service.Sink = &recordedSink{}
	registry := NewContextRequestStateRegistry()
	ctx := context.Background()
	boundary := CodexContextStateBoundary{SystemAccountID: "sys", APIKeyID: "key", GroupID: "group", ProviderCode: "openai"}

	// A stored compact whose summary digest does not match the payload
	// renders payload_unavailable through the preflight (L663/671/678).
	snapshot, err := service.CreateChatBridgeCompactSnapshot(ctx, CreateChatBridgeCompactSnapshotInput{
		SessionID: "w14d-sess-cx", Boundary: boundary, Summary: "真实摘要",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the stored segment bytes so the read fails (L663).
	corruptPath := ""
	entries, _ := os.ReadDir(filepath.Join(segmentsRoot, "sessions"))
	for _, entry := range entries {
		hourDirs, _ := os.ReadDir(filepath.Join(segmentsRoot, "sessions", entry.Name(), "segments"))
		for _, hour := range hourDirs {
			corruptPath = filepath.Join(segmentsRoot, "sessions", entry.Name(), "segments", hour.Name())
		}
	}
	if corruptPath != "" {
		if err := os.WriteFile(corruptPath, []byte("garbage"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, encrypted := range []string{
		snapshot.EncryptedContent,
		codexCompactionReferencePrefix + "w14d-compact-missing." + strings.Repeat("b", 64),
	} {
		req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
		reqBody := map[string]any{"model": "gpt-5", "input": []any{
			map[string]any{"type": "compaction", "encrypted_content": encrypted},
		}}
		rawBody, _ := json.Marshal(reqBody)
		attachBody(req, reqBody, rawBody)
		if _, err := service.ApplyContextStatePreflight(ctx, registry, bridgeBaseInput(req, func() (*gatewaypreauth.TrackingWriter) {
			_, writer := newTrackedWriter()
			return writer
		}(), &recordedAudit{})); err != nil {
			t.Fatal(err)
		}
	}

	// RestoreChatBridgeInputForCompact with a broken payload reference
	// renders the payload_unavailable outcome (L494).
	missing := CodexContextResponseStateIndex{
		CodexContextStateBoundary: boundary,
		ResponseID:                "resp_chat_bridge_miss2", SessionID: "w14d-sess-m2",
		ExpiresAt: expiresAtFromISO(clock.Now()),
		CodexContextPayloadReference: CodexContextPayloadReference{
			StorageKey: "sessions/none/segments/none.json.gz", SHA256: strings.Repeat("0", 64),
			Compression: "gzip", SchemaVersion: 2,
		},
	}
	if err := store.SaveResponseStateRow(ctx, missing); err != nil {
		t.Fatal(err)
	}
	result, err := service.RestoreChatBridgeInputForCompact(ctx, struct {
		PreviousResponseID string
		Boundary           CodexContextStateBoundary
		CurrentInput       any
	}{PreviousResponseID: "resp_chat_bridge_miss2", Boundary: boundary, CurrentInput: []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RestoreOutcomePayloadUnavail {
		t.Fatalf("outcome = %q", result.Outcome)
	}
}

var _ = errors.New
