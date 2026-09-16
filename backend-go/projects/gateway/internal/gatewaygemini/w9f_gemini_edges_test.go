package gatewaygemini

// w9f 覆盖收尾：补齐既有测试未触达的解析边界与错误分支，生产逻辑零改动。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// affinity.go
// ---------------------------------------------------------------------------

// TestW9FUpdateAfterSuccessResolveError 覆盖 Resolve 失败时按 none 结算。
func TestW9FUpdateAfterSuccessResolveError(t *testing.T) {
	ctx := context.Background()
	scope := AffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	store := newWaAffinityStore()
	store.getErr = errors.New("store down")
	affinity := NewInteractionAffinity(store).WithNowFunc(func() time.Time { return waFixedNow })
	request := httptest.NewRequest(http.MethodPost, "/v1beta/interactions/ix_1", nil)
	result, err := affinity.UpdateAfterSuccess(ctx, UpdateAfterSuccessInput{Request: request, Account: waGeminiAccount(), Scope: scope})
	if err != nil || result.Action != AffinityActionNone {
		t.Fatalf("resolve error = %+v/%v", result, err)
	}
}

// TestW9FRememberRejectsForeignProvider 覆盖 Remember 的 provider 前置。
func TestW9FRememberRejectsForeignProvider(t *testing.T) {
	affinity := NewInteractionAffinity(nil)
	result, err := affinity.Remember(context.Background(), "ix_1", UpstreamAccount{ProviderCode: "openai"}, AffinityScope{})
	if err != nil || result.Action != AffinityActionNone {
		t.Fatalf("foreign provider = %+v/%v", result, err)
	}
}

// TestW9FInteractionIDDetectionCoversDONEAndEmptyData 覆盖 SSE data 行空/DONE 分支。
func TestW9FInteractionIDDetectionCoversDONEAndEmptyData(t *testing.T) {
	body := "data: \ndata: [DONE]\n\ndata: {\"interaction\":{\"id\":\"ix_done\"}}\n\n"
	if id := InteractionIDFromResponseBody(body); id != "ix_done" {
		t.Fatalf("id = %q", id)
	}
}

// TestW9FNormalizedRequestPathFallsBackToRoot 覆盖空路径回退 "/"。
func TestW9FNormalizedRequestPathFallsBackToRoot(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://g.test/v1beta", nil)
	request.URL.Path = ""
	request.URL.RawPath = ""
	if got := normalizedRequestPath(request); got != "/" {
		t.Fatalf("normalized = %q, want /", got)
	}
}

// TestW9FInteractionIDFromJSONPrefixEdges 覆盖手写 JSON 前缀扫描器的边界。
func TestW9FInteractionIDFromJSONPrefixEdges(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"截断的空白", `{   `, ""},
		{"未知键未终结字符串", `{"other":"abc`, ""},
		{"逗号后结束", `{"a":1,`, ""},
		{"未知键字符串值", `{"other":"x","id":"ix_1"}`, "ix_1"},
		{"未知键数字值", `{"other":123,"id":"ix_2"}`, "ix_2"},
		{"未知键对象值", `{"other":{"n":[1,2]},"id":"ix_3"}`, "ix_3"},
		{"未知键对象值内转义", `{"other":{"t":"a\"b"},"id":"ix_4"}`, "ix_4"},
		{"嵌套括号错配", `{"other":[1}],"id":"x"`, ""},
		{"未闭合括号", `{"other":[1`, ""},
		{"空对象", `{}`, ""},
		{"值未终结", `{"other":"x","id":`, ""},
	}
	for _, item := range cases {
		if got := InteractionIDFromJSONPrefix([]byte(item.raw)); got != item.want {
			t.Fatalf("%s: id = %q, want %q", item.name, got, item.want)
		}
	}
}

// ---------------------------------------------------------------------------
// usage.go：text fragment 抽取
// ---------------------------------------------------------------------------

func TestW9FExtractUsagePropertyFromTextFragmentEdges(t *testing.T) {
	// 属性存在但值不是 JSON 对象（字符串）→ 无 usage。
	if _, ok := extractJSONObjectPropertyFromTextFragment(`前缀 "usage": "x" 后缀`, "usage"); ok {
		t.Fatal("string usage must not extract")
	}
	// 冒号缺失 → 无 usage。
	if _, ok := extractJSONObjectPropertyFromTextFragment(`"usage" 123`, "usage"); ok {
		t.Fatal("missing colon must not extract")
	}
	// 对象未闭合 → 无 usage。
	if _, ok := extractJSONObjectPropertyFromTextFragment(`"usage": {"a":1`, "usage"); ok {
		t.Fatal("unclosed object must not extract")
	}
	// 值内含转义引号：抽取必须越过字符串边界。
	object, ok := extractJSONObjectPropertyFromTextFragment(`"usage": {"a":"x\"}y"} 结束`, "usage")
	if !ok || object != `{"a":"x\"}y"}` {
		t.Fatalf("escaped object = %q, %v", object, ok)
	}
	// 对象非法 JSON → ExtractUsageFromTextFragment 忽略。
	if usage := ParseUsageFromJSONTextFragment(`响应 usage: {invalid} 结束`, false); usageHasDefinedValue(usage) {
		t.Fatalf("invalid usage object = %+v", usage)
	}
	// 合法 usage + service_tier 组合。
	usage := ParseUsageFromJSONTextFragment(`"service_tier":"paid" 之外 "usageMetadata": {"promptTokenCount": 3}`, false)
	if usage.InputTokens == nil || *usage.InputTokens != 3 || usage.ServiceTier != "paid" {
		t.Fatalf("usage = %+v", usage)
	}
	// 无 usage → 空。
	if usage := ParseUsageFromJSONTextFragment("纯文本", false); usageHasDefinedValue(usage) {
		t.Fatalf("plain usage = %+v", usage)
	}
}

// ---------------------------------------------------------------------------
// routehelpers.go
// ---------------------------------------------------------------------------

func TestW9FEndpointFamilyEdges(t *testing.T) {
	if got := EndpointFamilyFromPath("v1beta/models"); got != EndpointFamilyModels {
		t.Fatalf("no leading slash = %q", got)
	}
	if got := EndpointFamilyFromPath("/models/gemini:unknownaction"); got != "" {
		t.Fatalf("unknown action = %q", got)
	}
}

func TestW9FRequestPathAndQueryEmptyPath(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://g.test", nil)
	request.URL.Path = ""
	request.URL.RawPath = ""
	if got := RequestPathAndQuery(request); got != "/" {
		t.Fatalf("empty path = %q", got)
	}
}

func TestW9FMapSlashToEmptyAndStripExact(t *testing.T) {
	if got := mapSlashToEmpty("/"); got != "" {
		t.Fatalf("mapSlashToEmpty(/) = %q", got)
	}
	if got := mapSlashToEmpty("/v1beta"); got != "/v1beta" {
		t.Fatalf("mapSlashToEmpty other = %q", got)
	}
	if got := stripV1BetaPrefixExact("/other"); got != "/other" {
		t.Fatalf("strip exact other = %q", got)
	}
	if got := stripV1BetaPrefixExact("/v1betaX"); got != "/v1betaX" {
		t.Fatalf("strip exact prefix-like = %q", got)
	}
}

func TestW9FNormalizedGeminiPathAndQueryRoot(t *testing.T) {
	path, _ := normalizedGeminiPathAndQuery("/v1beta")
	if path != "/v1beta" {
		t.Fatalf("root normalize = %q", path)
	}
	path, _ = normalizedGeminiPathAndQuery("/")
	if path != "/v1beta" {
		t.Fatalf("slash normalize = %q", path)
	}
}

// ---------------------------------------------------------------------------
// streaminspection.go / streamevents.go
// ---------------------------------------------------------------------------

func TestW9FStreamInspectorFinishFlushesPendingLine(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushChunk([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}"))
	// 不带换行截断 → Finish 冲刷 pendingLine。
	snapshot := inspector.Finish()
	if !snapshot.OutputReceived || !snapshot.TerminalReceived {
		t.Fatalf("finish = %+v", snapshot)
	}
	// Skipped 后 Finish 直通。
	inspector = NewStreamInspector()
	inspector.PushChunk([]byte("data: "))
	for i := 0; i <= streamInspectorMaxEventBytes/1024; i++ {
		inspector.PushChunk([]byte(strings.Repeat("x", 1024)))
	}
	inspector.PushChunk([]byte("\n\n"))
	snapshot = inspector.Finish()
	if !snapshot.Skipped {
		t.Fatalf("oversize skipped = %+v", snapshot)
	}
}

func TestW9FStreamInspectorIgnoresNonDataLines(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushChunk([]byte(": comment\n\nignore me\n\nevent: error\n"))
	snapshot := inspector.Snapshot()
	if snapshot.EventCount != 0 || snapshot.FailedReceived {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestW9FStreamInspectorRecentEventTypesWindow(t *testing.T) {
	inspector := NewStreamInspector()
	for i := 0; i < streamInspectorRecentEventTypes+2; i++ {
		inspector.PushText("data: {}\n\n")
	}
	snapshot := inspector.Snapshot()
	if len(snapshot.RecentEventTypes) > streamInspectorRecentEventTypes {
		t.Fatalf("recent window = %v", snapshot.RecentEventTypes)
	}
}

func TestW9FPushParsedEventZeroBytesFallback(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushParsedEvent(ParseSSEEventText("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"t\"}]}}]}"), 0)
	snapshot := inspector.Snapshot()
	if !snapshot.OutputReceived {
		t.Fatalf("parsed push = %+v", snapshot)
	}
}

func TestW9FMaxIntAndPositiveTokenCount(t *testing.T) {
	if maxInt(3, 7) != 7 || maxInt(9, 2) != 9 {
		t.Fatal("maxInt")
	}
	positive := 5
	if !positiveTokenCount(&positive) || positiveTokenCount(nil) {
		t.Fatal("positiveTokenCount")
	}
	zero := 0
	if positiveTokenCount(&zero) {
		t.Fatal("zero is not positive")
	}
}

func TestW9FEstimateTextTokensEmpty(t *testing.T) {
	if got := EstimateTokenCountFromText(""); got < 0 {
		t.Fatalf("empty estimate = %d", got)
	}
	if got := EstimateTokenCountFromText("abcd"); got < 1 {
		t.Fatalf("ascii estimate = %d, want >= 1", got)
	}
}

func TestW9FParseStreamEventEventTypeName(t *testing.T) {
	// type 属性优先；缺失时 event_type 兜底。
	event := ParseSSEEventText("data: {\"event_type\":\"custom.event\"}")
	if event.EventType != "custom.event" {
		t.Fatalf("event_type fallback = %q", event.EventType)
	}
	event = ParseSSEEventText("data: {\"type\":\"typed.event\",\"event_type\":\"other\"}")
	if event.EventType != "typed.event" {
		t.Fatalf("type priority = %q", event.EventType)
	}
}

// ---------------------------------------------------------------------------
// semantics.go
// ---------------------------------------------------------------------------

func TestW9FExtractFramesMalformedRowsAreSkipped(t *testing.T) {
	// interactions：candidates/delta 行为非对象时跳过。
	frames := extractInteractionsSSEFrames(map[string]any{
		"interaction": "not-an-object",
		"steps":       []any{"row", map[string]any{"content": []any{"part"}}},
	}, EndpointFamilyInteractions, "step.delta", "")
	if len(frames) != 0 {
		t.Fatalf("malformed frames = %+v", frames)
	}
	// generateContent：candidate/part 非对象时跳过。
	frames = extractGenerateContentFrames(map[string]any{
		"candidates": []any{"row", map[string]any{"content": map[string]any{"parts": []any{"part"}}}},
	}, EndpointFamilyGenerateContent, TransportSSE, "x", "")
	if len(frames) != 0 {
		t.Fatalf("generate frames = %+v", frames)
	}
}

func TestW9FExtractInteractionsCompletedFramesFallbacks(t *testing.T) {
	// completed 事件缺 status → 从 eventType 推导。
	frames := extractInteractionsSSEFrames(map[string]any{
		"interaction": map[string]any{},
	}, EndpointFamilyInteractions, "interaction.completed", "")
	if len(frames) == 0 {
		t.Fatal("completed frame missing")
	}
	// failed 事件 error 落在顶层 data.error。
	frames = extractInteractionsSSEFrames(map[string]any{
		"interaction": map[string]any{"status": "failed"},
		"error":       map[string]any{"code": "boom"},
	}, EndpointFamilyInteractions, "interaction.failed", "")
	hasError := false
	for _, frame := range frames {
		if frame.FrameType == FrameTypeError {
			hasError = true
		}
	}
	if !hasError {
		t.Fatalf("error frame missing: %+v", frames)
	}
}
