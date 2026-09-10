package gatewaygemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// DrainEventSummaries / DrainEventSummariesCanEndStream：一次性取出并清空。
func TestWAStreamInspectorDrainSummaries(t *testing.T) {
	stream := "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n" +
		"data: {\"type\":\"finish\"}\n\n"
	inspector := NewStreamInspector()
	inspector.PushText(stream)
	if !inspector.DrainEventSummariesCanEndStream() {
		t.Fatal("存在终止 summary 应可结束流")
	}
	if drained := inspector.DrainEventSummaries(); len(drained) != 0 {
		t.Fatalf("CanEndStream 已 drain，第二次应为空: %d", len(drained))
	}
	// 单独 drain 保留内容断言。
	replay := NewStreamInspector()
	replay.PushText(stream)
	drained := replay.DrainEventSummaries()
	if len(drained) != 2 {
		t.Fatalf("drain 数 = %d，期望 2", len(drained))
	}
	// Gemini 分类契约：CanEndStream = terminal && !failed，两类终止事件均可结束流。
	if !drained[0].Terminal || !drained[0].CanEndStream {
		t.Fatalf("finishReason summary = %+v", drained[0])
	}
	if !drained[1].Terminal || !drained[1].CanEndStream {
		t.Fatalf("finish summary = %+v", drained[1])
	}
}

// 跳过后的 PushParsedEvent / Finish 短路。
func TestWAStreamInspectorSkippedShortCircuit(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("data: " + strings.Repeat("a", 256*1024+1) + "\n\n")
	if !inspector.Snapshot().Skipped {
		t.Fatal("前置条件：应已跳过")
	}
	before := inspector.Snapshot()
	inspector.PushParsedEvent(ParseSSEEventData(`{"type":"finish"}`, "", "", 0), 0)
	snapshot := inspector.Finish()
	if snapshot.EventCount != before.EventCount {
		t.Fatal("跳过后不应再分类事件")
	}
}

// 短原始字节估算下限为 1。
func TestWAEstimateRequestInputTokensShortRaw(t *testing.T) {
	tokens, ok := EstimateRequestInputTokens(RequestFacts{RawBody: []byte("abc")})
	if !ok || tokens != 1 {
		t.Fatalf("3 字节估算 = %d/%v，期望 1/true", tokens, ok)
	}
}

// 上游 URL 拼接：base 带斜杠尾巴与路径斜杠折叠；stripV1BetaPrefixExact 空串。
func TestWABuildUpstreamURLSlashCollapse(t *testing.T) {
	got, err := BuildUpstreamURL("https://proxy.example//", "v1beta//models", false)
	if err != nil {
		t.Fatalf("BuildUpstreamURL error: %v", err)
	}
	if got != "https://proxy.example/v1beta/models" {
		t.Fatalf("斜杠折叠 = %q", got)
	}
	if stripV1BetaPrefixExact("/v1beta") != "" {
		t.Fatal("stripV1BetaPrefixExact(/v1beta) 应为空串")
	}
	if stripV1BetaPrefixExact("/v1betax") != "/v1betax" {
		t.Fatal("非版本前缀应保留")
	}
}

// 归一化路径：无前导斜杠补齐、剥 v1beta。
func TestWANormalizedRequestPath(t *testing.T) {
	request := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "v1beta/interactions/abc"}}
	if got := ResourceIDFromRequest(request); got != "abc" {
		t.Fatalf("无前导斜杠路径 ID = %q", got)
	}
}

// 流事件错误字段提取分支（response.error / type=error 平铺 / mcp 跳过）。
func TestWAOpenAIStreamEventErrorFields(t *testing.T) {
	cases := []struct {
		name, data, code, message string
	}{
		{"response.error 嵌套", `{"response":{"error":{"code":"e1","message":"m1"}}}`, "e1", "m1"},
		{"error 子对象", `{"error":{"code":"e2","message":"m2"}}`, "e2", "m2"},
		{"type=error 平铺", `{"type":"error","code":"e3","message":"m3"}`, "e3", "m3"},
		{"type=error 无字段", `{"type":"error"}`, "", ""},
		{"mcp 失败跳过", `{"type":"response.mcp_call.failed","error":{"code":"x"}}`, "", ""},
		{"无错误", `{"candidates":[]}`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := ParseSSEEventData(tc.data, "", "", 0)
			if event.ErrorCode != tc.code || event.ErrorMessage != tc.message {
				t.Fatalf("错误字段 = %q/%q，期望 %q/%q", event.ErrorCode, event.ErrorMessage, tc.code, tc.message)
			}
		})
	}
}

// 内存回退模式的 Delete 走进程内缓存删除分支。
func TestWAInteractionAffinityMemoryDelete(t *testing.T) {
	ctx := context.Background()
	affinity := NewInteractionAffinity(nil).WithNowFunc(func() time.Time { return waFixedNow })
	scope := AffinityScope{SystemAccountID: "sys", APIKeyID: "key", GroupID: "grp"}
	if _, err := affinity.Remember(ctx, "ix_del", waGeminiAccount(), scope); err != nil {
		t.Fatalf("Remember error: %v", err)
	}
	result, err := affinity.Delete(ctx, "ix_del", scope)
	if err != nil || result.Action != AffinityActionDeleted {
		t.Fatalf("内存回退 Delete = %+v/%v", result, err)
	}
	request := httptest.NewRequest("GET", "/v1beta/interactions/ix_del", nil)
	if _, found, _ := affinity.Resolve(ctx, request, scope); found {
		t.Fatal("删除后不应命中")
	}
}
