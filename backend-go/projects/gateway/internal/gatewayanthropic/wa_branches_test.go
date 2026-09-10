package gatewayanthropic

import (
	"net/http"
	"net/url"
	"testing"
)

// 流事件输出文本提取（inspector 分类用）：tool_use 起始块 JSON 化、delta
// 文本类型分支与空值回退。
func TestWAOutputTextFromStreamEvent(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		data      string
		want      string
	}{
		{"tool_use 起始块", "content_block_start", `{"content_block":{"type":"tool_use","id":"t1"}}`, `{"id":"t1","type":"tool_use"}`}, // map 序列化按键排序
		{"非 tool_use 起始块", "content_block_start", `{"content_block":{"type":"text","text":"x"}}`, ""},
		{"text_delta", "content_block_delta", `{"delta":{"type":"text_delta","text":"hi"}}`, "hi"},
		{"input_json_delta", "content_block_delta", `{"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`, `{"a":`},
		{"thinking_delta", "content_block_delta", `{"delta":{"type":"thinking_delta","thinking":"推理"}}`, "推理"},
		{"delta 类型未知", "content_block_delta", `{"delta":{"type":"???"}}`, ""},
		{"delta 缺失", "content_block_delta", `{}`, ""},
		{"其他事件", "message_stop", `{"type":"message_stop"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := outputTextFromStreamEvent(tc.eventType, waParseJSONObject(t, tc.data))
			if got != tc.want {
				t.Fatalf("outputTextFromStreamEvent = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// 错误/usage 路径在无嵌套对象时的空路径回退。
func TestWAErrorAndUsageRawPathsEmpty(t *testing.T) {
	if got := errorRawPaths(waParseJSONObject(t, `{"type":"error","code":"x"}`)); len(got) != 0 {
		t.Fatalf("errorRawPaths = %v，期望空", got)
	}
	if got := usageRawPaths(waParseJSONObject(t, `{}`)); len(got) != 0 {
		t.Fatalf("usageRawPaths = %v，期望空", got)
	}
}

// RequestPathAndQuery：仅 query 的 URL 路径为空时补 "/"。
func TestWARequestPathAndQueryEmptyPath(t *testing.T) {
	request := &http.Request{URL: &url.URL{RawQuery: "a=1"}}
	if got := RequestPathAndQuery(request); got != "/?a=1" {
		t.Fatalf("RequestPathAndQuery = %q，期望 /?a=1", got)
	}
}

// query 排序：无序参数按插入排序交换（确定性输出）。
func TestWAWithQueryParamSortedOutput(t *testing.T) {
	got := withQueryParamIfMissing("/v1/messages?zeta=1&alpha=2", "beta", "true")
	if got != "/v1/messages?alpha=2&beta=true&zeta=1" {
		t.Fatalf("排序输出 = %q", got)
	}
}

// 跳过后的 PushParsedEvent / Finish 直接返回快照。
func TestWAStreamInspectorSkippedShortCircuit(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("data: " + repeatA(256*1024+1) + "\n\n")
	if !inspector.Snapshot().Skipped {
		t.Fatal("前置条件：应已跳过")
	}
	snapshotBefore := inspector.Snapshot()
	if snapshot := inspector.PushParsedEvent(ParseSSEEventData(`{"type":"message_stop"}`, "", "", 0), 0); snapshot.EventCount != snapshotBefore.EventCount {
		t.Fatal("跳过后 PushParsedEvent 不应分类事件")
	}
	if snapshot := inspector.Finish(); snapshot.EventCount != snapshotBefore.EventCount {
		t.Fatal("跳过后 Finish 不应再冲刷事件")
	}
}

func repeatA(count int) string {
	out := make([]byte, count)
	for index := range out {
		out[index] = 'a'
	}
	return string(out)
}

// 短原始字节的输入估算下限为 1 token。
func TestWAEstimateRequestInputTokensShortRaw(t *testing.T) {
	tokens, ok := EstimateRequestInputTokens(RequestFacts{RawBody: []byte("abc")})
	if !ok || tokens != 1 {
		t.Fatalf("3 字节估算 = %d/%v，期望 1/true", tokens, ok)
	}
}
