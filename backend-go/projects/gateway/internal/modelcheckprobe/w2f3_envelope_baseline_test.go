package modelcheckprobe

// W2 F3 接线验收基线（计划 W2 行、设计 §9.4）：Execute 上游请求构造迁入
// upstreamenvelope.Build 前后，同输入请求必须结构等价——URL/method、头
// （显式含 accept/openai-beta/content-type/认证头）与结构化 body。身份值
// 参数化：不比具体 UUID，比存在性与互相派生关系（X-Client-Request-Id ==
// Session-Id、thread-id == session-id、window-id == thread-id+":0"、turn
// metadata JSON 镜像身份头与固定键）。本文件先于接线改动落基线，迁移后同一
// 断言保持。响应处理（流式解析/评分/重试/结算）不属于本步比较范围。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// w2f3Capture 捕获 Execute 实际发出的请求与 body，并应答 200 JSON。
type w2f3Capture struct {
	request *http.Request
	body    []byte
}

func (c *w2f3Capture) transport() roundTripFunc {
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		c.request = request
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		c.body = body
		request.Body = io.NopCloser(strings.NewReader(string(body)))
		responseBody := `{"model":"w2f3-model","output_text":"OK-MODEL-CHECK"}`
		responseHeader := "application/json"
		if request.Header.Get("Accept") == "text/event-stream" {
			responseHeader = "text/event-stream"
			responseBody = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"w2f3-model\",\"output_text\":\"OK-MODEL-CHECK\"}}\n\ndata: [DONE]\n"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{responseHeader}},
			Body:       io.NopCloser(strings.NewReader(responseBody)),
			Request:    request,
		}, nil
	})
}

func w2f3OAuthHeaders(extra map[string]string) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer w2f3-access-token")
	headers.Set("chatgpt-account-id", "w2f3-acct")
	for name, value := range extra {
		headers.Set(name, value)
	}
	return headers
}

// assertW2F3OAuthCodexShape 断言 OAuth Codex（A 形态）出站请求形状：
// 固定上游 /responses、强制 SSE、openai-beta 缺省值、身份头集合与派生关系、
// Codex 归一化 body。身份值全部经派生关系断言，不固定具体 UUID。
func assertW2F3OAuthCodexShape(t *testing.T, capture *w2f3Capture, model string) {
	t.Helper()
	if capture.request == nil {
		t.Fatal("未捕获到出站请求")
	}
	if got := capture.request.URL.String(); got != OpenAIOAuthCodexBaseURL+"/responses" {
		t.Fatalf("OAuth Codex URL=%q, want %q", got, OpenAIOAuthCodexBaseURL+"/responses")
	}
	if capture.request.Method != http.MethodPost {
		t.Fatalf("method=%q", capture.request.Method)
	}
	header := capture.request.Header
	if got := header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("accept=%q, want text/event-stream（A 形态强制 SSE）", got)
	}
	if got := header.Get("Openai-Beta"); got != "responses=experimental" {
		t.Fatalf("openai-beta=%q, want responses=experimental", got)
	}
	if got := header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type=%q", got)
	}
	if got := header.Get("Authorization"); got != "Bearer w2f3-access-token" {
		t.Fatalf("authorization=%q（调用方头逐字保留）", got)
	}
	if got := header.Get("Chatgpt-Account-Id"); got != "w2f3-acct" {
		t.Fatalf("chatgpt-account-id=%q（调用方头逐字保留）", got)
	}
	// 八身份头 + F3 现行 x-codex-installation-id：存在性与互相派生关系。
	session := header.Get("Session-Id")
	if session == "" {
		t.Fatalf("session-id 缺失: %v", header)
	}
	if got := header.Get("Originator"); got != "codex_exec" {
		t.Fatalf("originator=%q", got)
	}
	if got := header.Get("User-Agent"); got != upstreamidentity.EffectiveCodexUserAgent() {
		t.Fatalf("user-agent=%q, want %q", got, upstreamidentity.EffectiveCodexUserAgent())
	}
	if got := header.Get("Thread-Id"); got != session {
		t.Fatalf("thread-id=%q, want == session-id %q", got, session)
	}
	if got := header.Get("X-Client-Request-Id"); got != session {
		t.Fatalf("x-client-request-id=%q, want == session-id %q", got, session)
	}
	if got := header.Get("X-Codex-Beta-Features"); got != "remote_compaction_v2" {
		t.Fatalf("x-codex-beta-features=%q", got)
	}
	if got := header.Get("X-Codex-Window-Id"); got != session+":0" {
		t.Fatalf("x-codex-window-id=%q, want == session-id+\":0\"", got)
	}
	installation := header.Get("X-Codex-Installation-Id")
	if installation == "" {
		t.Fatalf("x-codex-installation-id 缺失: %v", header)
	}
	// §9.5 Lite 头模型条件：最终上游模型精确命中 gpt-5.6-sol/terra/luna 时
	// 发送 true，其余模型一律不发送（W2 接线后由信封按 Spec.Model 判定）。
	wantLite := false
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		wantLite = true
	}
	if got := header.Get("X-OpenAI-Internal-Codex-Responses-Lite"); wantLite && got != "true" {
		t.Fatalf("Lite 模型 %s 必须发送 X-OpenAI-Internal-Codex-Responses-Lite: true, got %q", model, got)
	} else if !wantLite && got != "" {
		t.Fatalf("非 Lite 模型 %s 不得发送 Lite 头, got %q", model, got)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(header.Get("X-Codex-Turn-Metadata")), &metadata); err != nil {
		t.Fatalf("x-codex-turn-metadata 非法 JSON: %v (%q)", err, header.Get("X-Codex-Turn-Metadata"))
	}
	metadataAssertions := []struct {
		key  string
		want any
	}{
		{"installation_id", installation},
		{"session_id", session},
		{"thread_id", session},
		{"turn_id", session},
		{"window_id", session + ":0"},
		{"request_kind", "turn"},
		{"thread_source", "user"},
		{"sandbox", "none"},
	}
	for _, item := range metadataAssertions {
		if metadata[item.key] != item.want {
			t.Fatalf("turn metadata[%s]=%v, want %v", item.key, metadata[item.key], item.want)
		}
	}
	startedAt, ok := metadata["turn_started_at_unix_ms"].(float64)
	if !ok || startedAt <= 0 {
		t.Fatalf("turn metadata turn_started_at_unix_ms=%v", metadata["turn_started_at_unix_ms"])
	}
	// Codex 归一化 body。
	var payload map[string]any
	if err := json.Unmarshal(capture.body, &payload); err != nil {
		t.Fatalf("body 非法: %v", err)
	}
	if payload["store"] != false || payload["stream"] != true {
		t.Fatalf("store/stream=%v/%v, want false/true", payload["store"], payload["stream"])
	}
	input, ok := payload["input"].([]any)
	if !ok || len(input) == 0 {
		t.Fatalf("input 未包装为数组: %#v", payload["input"])
	}
	message, _ := input[0].(map[string]any)
	if message["role"] != "user" {
		t.Fatalf("input[0].role=%v", message["role"])
	}
	if payload["instructions"] != "You are a model capability checker. Follow the requested output exactly." {
		t.Fatalf("instructions=%v（语义字段保留）", payload["instructions"])
	}
	if payload["model"] != model {
		t.Fatalf("model=%v, want %q", payload["model"], model)
	}
	for _, banned := range []string{"max_output_tokens", "temperature", "client_metadata"} {
		if _, exists := payload[banned]; exists {
			t.Fatalf("body 不应携带 %s: %#v", banned, payload)
		}
	}
	reasoningExpected := strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-5.6-")
	reasoning, hasReasoning := payload["reasoning"].(map[string]any)
	if reasoningExpected {
		if !hasReasoning || reasoning["context"] != "all_turns" || payload["parallel_tool_calls"] != false {
			t.Fatalf("gpt-5.6-* 模型必须带 reasoning.context=all_turns + parallel_tool_calls=false: %#v", payload)
		}
	} else if hasReasoning {
		t.Fatalf("非 gpt-5.6-* 模型不应带 reasoning: %#v", payload)
	}
}

func TestW2F3BaselineOAuthCodexOutboundShape(t *testing.T) {
	resetProbeClientVersionOverrides(t)
	// gpt-5.6-sol 命中 §9.5 Lite 清单与 gpt-5.6-* reasoning 条件双臂；
	// gpt-5.6-preview 仅命中 reasoning 前缀；gpt-5.2 两者皆不命中。
	for _, model := range []string{"gpt-5.2", "gpt-5.6-preview", "gpt-5.6-sol"} {
		t.Run(model, func(t *testing.T) {
			request, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIResponses, model, "hello", modelcheckprofile.EndpointModeResponsesJSON)
			if err != nil {
				t.Fatal(err)
			}
			capture := &w2f3Capture{}
			result, err := Execute(context.Background(), request, Options{
				Adapter:  AdapterOpenAIOAuthCodex,
				Endpoint: "https://custom.example/v1",
				Headers:  w2f3OAuthHeaders(nil),
				Client:   &http.Client{Transport: capture.transport()},
				Timeout:  time.Second,
			})
			if err != nil || !result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			assertW2F3OAuthCodexShape(t, capture, model)
		})
	}
	t.Run("openai-beta 显式值保留", func(t *testing.T) {
		request, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.2", "hello", modelcheckprofile.EndpointModeResponsesJSON)
		if err != nil {
			t.Fatal(err)
		}
		capture := &w2f3Capture{}
		_, err = Execute(context.Background(), request, Options{
			Adapter:  AdapterOpenAIOAuthCodex,
			Endpoint: "https://custom.example",
			Headers:  w2f3OAuthHeaders(map[string]string{"openai-beta": "responses=custom"}),
			Client:   &http.Client{Transport: capture.transport()},
			Timeout:  time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := capture.request.Header.Get("Openai-Beta"); got != "responses=custom" {
			t.Fatalf("openai-beta=%q, want 客户端显式值保留", got)
		}
	})
}

// TestW2F3BaselineAPIKeyOutboundShape 断言普通 API Key（C 形态）出站请求：
// URL 与 Accept 按 endpointMode（信封口径：JSON application/json、SSE
// api-key 双 accept）、Content-Type application/json、认证头逐字保留、
// body 结构等价于请求载荷。
func TestW2F3BaselineAPIKeyOutboundShape(t *testing.T) {
	cases := []struct {
		name       string
		protocol   modelcheckprofile.Protocol
		model      string
		stream     bool
		endpoint   string
		headers    func() http.Header
		wantURL    string
		wantAccept string
	}{
		{
			name: "responses_json", protocol: modelcheckprofile.ProtocolOpenAIResponses, model: "gpt-5.2",
			endpoint: "https://w2f3-up.example", headers: func() http.Header {
				headers := http.Header{}
				headers.Set("Authorization", "Bearer w2f3-key")
				return headers
			},
			wantURL: "https://w2f3-up.example/v1/responses", wantAccept: "application/json",
		},
		{
			name: "responses_sse", protocol: modelcheckprofile.ProtocolOpenAIResponses, model: "gpt-5.2", stream: true,
			endpoint: "https://w2f3-up.example", headers: func() http.Header {
				headers := http.Header{}
				headers.Set("Authorization", "Bearer w2f3-key")
				return headers
			},
			wantURL: "https://w2f3-up.example/v1/responses", wantAccept: "application/json, text/event-stream",
		},
		{
			name: "chat_json", protocol: modelcheckprofile.ProtocolOpenAIChat, model: "chat-model",
			endpoint: "https://w2f3-up.example", headers: func() http.Header {
				headers := http.Header{}
				headers.Set("Authorization", "Bearer w2f3-key")
				return headers
			},
			wantURL: "https://w2f3-up.example/v1/chat/completions", wantAccept: "application/json",
		},
		{
			name: "messages_json", protocol: modelcheckprofile.ProtocolAnthropic, model: "claude-model",
			endpoint: "https://w2f3-up.example", headers: func() http.Header {
				headers := http.Header{}
				headers.Set("x-api-key", "w2f3-anthropic-key")
				headers.Set("anthropic-version", "2023-06-01")
				return headers
			},
			wantURL: "https://w2f3-up.example/v1/messages", wantAccept: "application/json",
		},
		{
			name: "generate_content_sse", protocol: modelcheckprofile.ProtocolGeminiNative, model: "gem-model", stream: true,
			endpoint: "https://w2f3-up.example", headers: func() http.Header {
				headers := http.Header{}
				headers.Set("x-goog-api-key", "w2f3-gemini-key")
				return headers
			},
			wantURL: "https://w2f3-up.example/v1beta/models/gem-model:streamGenerateContent?alt=sse",
			// OAuth/Code Assist 纯流式；api-key 形态双 accept（现行信封口径）。
			wantAccept: "application/json, text/event-stream",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request, err := BuildBasic(test.protocol, test.model, "hello", test.stream)
			if err != nil {
				t.Fatal(err)
			}
			capture := &w2f3Capture{}
			result, err := Execute(context.Background(), request, Options{
				Endpoint: test.endpoint,
				Headers:  test.headers(),
				Client:   &http.Client{Transport: capture.transport()},
				Timeout:  time.Second,
			})
			if err != nil || !result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if capture.request == nil {
				t.Fatal("未捕获到出站请求")
			}
			if got := capture.request.URL.String(); got != test.wantURL {
				t.Fatalf("URL=%q, want %q", got, test.wantURL)
			}
			if capture.request.Method != http.MethodPost {
				t.Fatalf("method=%q", capture.request.Method)
			}
			if got := capture.request.Header.Get("Accept"); got != test.wantAccept {
				t.Fatalf("accept=%q, want %q", got, test.wantAccept)
			}
			if got := capture.request.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("content-type=%q", got)
			}
			for name, values := range test.headers() {
				if got := capture.request.Header.Get(name); got != values[0] {
					t.Fatalf("头 %s=%q, want 调用方值 %q 逐字保留", name, got, values[0])
				}
			}
			var sent, wanted map[string]any
			if err := json.Unmarshal(capture.body, &sent); err != nil {
				t.Fatalf("出站 body 非法: %v", err)
			}
			if err := json.Unmarshal(request.Body, &wanted); err != nil {
				t.Fatalf("请求 body 非法: %v", err)
			}
			if !reflect.DeepEqual(sent, wanted) {
				t.Fatalf("body 结构不等价:\n sent=%#v\n want=%#v", sent, wanted)
			}
		})
	}
	t.Run("调用方 Accept 覆盖保留", func(t *testing.T) {
		request, err := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.2", "hello", false)
		if err != nil {
			t.Fatal(err)
		}
		capture := &w2f3Capture{}
		headers := http.Header{}
		headers.Set("Authorization", "Bearer w2f3-key")
		headers.Set("Accept", "application/vnd.w2f3.custom")
		if _, err := Execute(context.Background(), request, Options{Endpoint: "https://w2f3-up.example", Headers: headers, Client: &http.Client{Transport: capture.transport()}, Timeout: time.Second}); err != nil {
			t.Fatal(err)
		}
		if got := capture.request.Header.Get("Accept"); got != "application/vnd.w2f3.custom" {
			t.Fatalf("accept=%q, want 调用方显式值保留", got)
		}
	})
	t.Run("catalog 端点头缺省填充", func(t *testing.T) {
		// 信封 Build 无条件写 catalog 条目固定头（messages 的
		// anthropic-version；Interactions 的 api-revision 同规）：调用方未
		// 携带时由信封缺省填充，携带时逐字保留（上一用例已断言）。
		request, err := BuildBasic(modelcheckprofile.ProtocolAnthropic, "claude-model", "hello", false)
		if err != nil {
			t.Fatal(err)
		}
		capture := &w2f3Capture{}
		headers := http.Header{}
		headers.Set("x-api-key", "w2f3-anthropic-key")
		if _, err := Execute(context.Background(), request, Options{Endpoint: "https://w2f3-up.example", Headers: headers, Client: &http.Client{Transport: capture.transport()}, Timeout: time.Second}); err != nil {
			t.Fatal(err)
		}
		if got := capture.request.Header.Get("Anthropic-Version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version=%q, want catalog 缺省 2023-06-01", got)
		}
	})
	t.Run("无凭据头显式无认证（AuthNone）", func(t *testing.T) {
		// C 形态现行语义：调用方未携带任何凭据头时请求照发且不写认证头
		//（Execute 显式选择信封 AuthNone，不是静默兜底）。
		request, err := BuildBasic(modelcheckprofile.ProtocolOpenAIChat, "chat-model", "hello", false)
		if err != nil {
			t.Fatal(err)
		}
		capture := &w2f3Capture{}
		if _, err := Execute(context.Background(), request, Options{Endpoint: "https://w2f3-up.example", Client: &http.Client{Transport: capture.transport()}, Timeout: time.Second}); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"} {
			if got := capture.request.Header.Get(name); got != "" {
				t.Fatalf("无凭据请求不得写认证头 %s=%q", name, got)
			}
		}
	})
}
