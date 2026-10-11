package exactkeyprobe

// W3 F4 接线基线（计划 W3 行、设计 §9.4）：buildProbeRequest 各形态出站形状
// 基线。身份值参数化：不比具体 UUID，比存在性与互相派生关系（X-Client-Request-Id
// == Session-Id、Thread-Id == Session-Id、Window-Id == Session-Id+":0"、body
// client_metadata 与身份头镜像同一身份）。B/C/Code Assist 臂为迁移前后不变式；
// A 形态臂在迁移前钉住现 JSON/stream:false 行为，迁移后改为 §9.1 声明的恒
// SSE 形状（显式 diff，见该测试内注释）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// w3BaselineRequest 构造 buildProbeRequest 的出站请求并读取 body。
func w3BaselineRequest(t *testing.T, baseURL string, mutate func(*Input)) (*http.Request, []byte) {
	t.Helper()
	base, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{
		AccountID:        "w3-acc",
		InputVersion:     1,
		ConfigRevision:   1,
		DispatchRevision: 1,
		Provider:         "openai",
		Type:             "api_key",
		EndpointMode:     "chat_json",
		HealthModel:      "w3-model",
		BaseURL:          baseURL,
		IssuedAt:         time.Now(),
		ExpiresAt:        time.Now().Add(time.Hour),
	}
	if mutate != nil {
		mutate(&input)
	}
	request, err := buildProbeRequest(context.Background(), base, input, "w3-token")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	return request, body
}

func w3BaselineBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("出站 body 非法: %v", err)
	}
	return payload
}

// TestW3F4BaselinePlainAPIKeyArms 钉 C 形态（普通 API Key）出站形状：
// URL/Accept/认证头/预算字段/body 语义。迁移前后不变式。
func TestW3F4BaselinePlainAPIKeyArms(t *testing.T) {
	t.Run("chat_json", func(t *testing.T) {
		request, body := w3BaselineRequest(t, "https://w3-up.test", nil)
		if request.URL.String() != "https://w3-up.test/v1/chat/completions" || request.Method != "POST" {
			t.Fatalf("URL=%s method=%s", request.URL, request.Method)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer w3-token" {
			t.Fatalf("authorization=%q", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("accept=%q", got)
		}
		payload := w3BaselineBody(t, body)
		if payload["model"] != "w3-model" || payload["stream"] != false || payload["max_tokens"] != float64(probeOutputTokenLimit) {
			t.Fatalf("body=%#v", payload)
		}
	})
	t.Run("responses_json", func(t *testing.T) {
		request, body := w3BaselineRequest(t, "https://w3-up.test", func(input *Input) {
			input.ProtocolProfileID = "profile_openai_openai_v1"
			input.EndpointMode = "responses_json"
		})
		if request.URL.String() != "https://w3-up.test/v1/responses" {
			t.Fatalf("URL=%s", request.URL)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("accept=%q", got)
		}
		payload := w3BaselineBody(t, body)
		if payload["stream"] != false || payload["max_output_tokens"] != float64(probeOutputTokenLimit) {
			t.Fatalf("body=%#v", payload)
		}
		if _, ok := payload["input"].([]any); !ok {
			t.Fatalf("input=%#v", payload["input"])
		}
		if _, ok := payload["instructions"].(string); !ok {
			t.Fatalf("instructions=%#v", payload["instructions"])
		}
	})
	t.Run("responses_sse 双 accept", func(t *testing.T) {
		request, body := w3BaselineRequest(t, "https://w3-up.test", func(input *Input) {
			input.EndpointMode = "responses_sse"
		})
		if got := request.Header.Get("Accept"); got != "application/json, text/event-stream" {
			t.Fatalf("accept=%q", got)
		}
		payload := w3BaselineBody(t, body)
		if payload["stream"] != true || payload["max_output_tokens"] != float64(probeOutputTokenLimit) {
			t.Fatalf("body=%#v", payload)
		}
	})
	t.Run("images_json 无预算字段", func(t *testing.T) {
		request, body := w3BaselineRequest(t, "https://w3-up.test", func(input *Input) {
			input.EndpointMode = "images_json"
		})
		if request.URL.String() != "https://w3-up.test/v1/images/generations" {
			t.Fatalf("URL=%s", request.URL)
		}
		payload := w3BaselineBody(t, body)
		for _, banned := range []string{"max_tokens", "max_output_tokens"} {
			if _, exists := payload[banned]; exists {
				t.Fatalf("images 契约无预算字段，不得伪造 %s: %#v", banned, payload)
			}
		}
	})
	t.Run("messages_json anthropic", func(t *testing.T) {
		request, body := w3BaselineRequest(t, "https://w3-up.test", func(input *Input) {
			input.Provider = "anthropic"
			input.ProtocolProfileID = "profile_anthropic_anthropic_v1"
			input.ProtocolCode = "anthropic"
			input.EndpointMode = "messages_json"
		})
		if request.URL.String() != "https://w3-up.test/v1/messages" {
			t.Fatalf("URL=%s", request.URL)
		}
		if got := request.Header.Get("X-Api-Key"); got != "w3-token" {
			t.Fatalf("x-api-key=%q", got)
		}
		if got := request.Header.Get("Anthropic-Version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version=%q", got)
		}
		payload := w3BaselineBody(t, body)
		if payload["max_tokens"] != float64(probeOutputTokenLimit) {
			t.Fatalf("body=%#v", payload)
		}
	})
	t.Run("generate_content_json google_oauth 非 Code Assist", func(t *testing.T) {
		// W4 跨包统一（§4.1 口径，与 F2 同规）：google_oauth 非 Code Assist
		// 按 AuthOAuth 解析——认证头 Bearer + 配额项目不变；SSE Accept 从
		// api-key 双 accept 收敛为纯流式（W3 曾按 AuthBearer 留双 accept），
		// 见同文件 google_oauth sse 纯流式臂。
		request, body := w3BaselineRequest(t, "https://w3-up.test", func(input *Input) {
			input.Provider = "gemini"
			input.ProtocolProfileID = "profile_gemini_native_v1beta"
			input.ProtocolCode = "gemini"
			input.ProtocolVersion = "v1beta"
			input.EndpointMode = "generate_content_json"
			input.Type = "google_oauth"
			input.OAuthQuotaProjectID = "w3-quota"
		})
		if request.URL.String() != "https://w3-up.test/v1beta/models/w3-model:generateContent" {
			t.Fatalf("URL=%s", request.URL)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer w3-token" {
			t.Fatalf("authorization=%q", got)
		}
		if got := request.Header.Get("X-Goog-User-Project"); got != "w3-quota" {
			t.Fatalf("x-goog-user-project=%q", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("accept=%q（json 形态无策略差异）", got)
		}
		payload := w3BaselineBody(t, body)
		config, ok := payload["generationConfig"].(map[string]any)
		if !ok || config["maxOutputTokens"] != float64(probeOutputTokenLimit) {
			t.Fatalf("generationConfig=%#v", payload["generationConfig"])
		}
	})
	t.Run("google_oauth sse 纯流式（W4 §4.1 统一）", func(t *testing.T) {
		request, _ := w3BaselineRequest(t, "https://w3-up.test", func(input *Input) {
			input.Provider = "gemini"
			input.ProtocolProfileID = "profile_gemini_native_v1beta"
			input.ProtocolCode = "gemini"
			input.ProtocolVersion = "v1beta"
			input.EndpointMode = "generate_content_sse"
			input.Type = "google_oauth"
			input.OAuthQuotaProjectID = "w3-quota"
		})
		if got := request.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("accept=%q, want AuthOAuth 纯流式（W4 统一）", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer w3-token" {
			t.Fatalf("authorization=%q", got)
		}
		if got := request.Header.Get("X-Goog-User-Project"); got != "w3-quota" {
			t.Fatalf("x-goog-user-project=%q", got)
		}
	})
	t.Run("interactions_sse api-revision 与无预算", func(t *testing.T) {
		request, body := w3BaselineRequest(t, "https://w3-up.test", func(input *Input) {
			input.Provider = "gemini"
			input.ProtocolProfileID = "profile_gemini_native_v1beta"
			input.ProtocolCode = "gemini"
			input.ProtocolVersion = "v1beta"
			input.EndpointMode = "interactions_sse"
		})
		if got := request.Header.Get("Api-Revision"); got != "2026-05-20" {
			t.Fatalf("api-revision=%q", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json, text/event-stream" {
			t.Fatalf("accept=%q", got)
		}
		payload := w3BaselineBody(t, body)
		if payload["model"] != "w3-model" || payload["stream"] != true {
			t.Fatalf("body=%#v", payload)
		}
	})
	t.Run("GLM 官方根剥请求 /v1", func(t *testing.T) {
		request, _ := w3BaselineRequest(t, "https://open.bigmodel.cn/api/paas/v4", func(input *Input) {
			input.Provider = "glm"
			input.ProtocolProfileID = "profile_glm_general_openai_v1"
		})
		if request.URL.String() != "https://open.bigmodel.cn/api/paas/v4/chat/completions" {
			t.Fatalf("URL=%s", request.URL)
		}
	})
}

// TestW3F4BaselineCodexCompatibilityShape 钉 B 形态（codex_responses 兼容画像）
// 出站形状：身份头/身份 body 单源派生、Lite 头 §9.5 模型条件、预算保留、
// Accept 双 accept、不写 OpenAI-Beta。迁移前后不变式。
func TestW3F4BaselineCodexCompatibilityShape(t *testing.T) {
	for _, tc := range []struct {
		model    string
		wantLite bool
	}{
		{model: "gpt-5.6-sol", wantLite: true},
		{model: "gpt-5.6-terra", wantLite: true},
		{model: "gpt-5.6-preview", wantLite: false},
		{model: "w3-plain-model", wantLite: false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			request, body := w3BaselineRequest(t, "https://w3-up.test", func(input *Input) {
				input.EndpointMode = "responses_sse"
				input.ClientCompatibility = "codex_responses"
				input.HealthModel = tc.model
			})
			if request.URL.String() != "https://w3-up.test/v1/responses" {
				t.Fatalf("URL=%s", request.URL)
			}
			header := request.Header
			if got := header.Get("Accept"); got != "application/json, text/event-stream" {
				t.Fatalf("accept=%q", got)
			}
			if got := header.Get("Openai-Beta"); got != "" {
				t.Fatalf("B 形态不得补 openai-beta=%q", got)
			}
			if got := header.Get("Originator"); got != "codex_exec" {
				t.Fatalf("originator=%q", got)
			}
			if got := header.Get("User-Agent"); got != upstreamidentity.EffectiveCodexUserAgent() {
				t.Fatalf("user-agent=%q", got)
			}
			session := header.Get("Session-Id")
			if session == "" {
				t.Fatalf("session-id 缺失: %v", header)
			}
			if got := header.Get("Thread-Id"); got != session {
				t.Fatalf("thread-id=%q want == session", got)
			}
			if got := header.Get("X-Client-Request-Id"); got != session {
				t.Fatalf("x-client-request-id=%q want == session", got)
			}
			if got := header.Get("X-Codex-Window-Id"); got != session+":0" {
				t.Fatalf("x-codex-window-id=%q", got)
			}
			if got := header.Get("X-Codex-Beta-Features"); got != "remote_compaction_v2" {
				t.Fatalf("x-codex-beta-features=%q", got)
			}
			if got := header.Get("X-OpenAI-Internal-Codex-Responses-Lite"); tc.wantLite && got != "true" {
				t.Fatalf("Lite 模型 %s 必须发送 Lite 头: %q", tc.model, got)
			} else if !tc.wantLite && got != "" {
				t.Fatalf("非 Lite 模型 %s 不得发送 Lite 头: %q", tc.model, got)
			}
			payload := w3BaselineBody(t, body)
			if payload["store"] != false || payload["stream"] != true || payload["parallel_tool_calls"] != false {
				t.Fatalf("body=%#v", payload)
			}
			if payload["prompt_cache_key"] != session {
				t.Fatalf("prompt_cache_key=%v want == session", payload["prompt_cache_key"])
			}
			if payload["max_output_tokens"] != float64(probeOutputTokenLimit) {
				t.Fatalf("B 形态保留预算字段: %#v", payload)
			}
			include, ok := payload["include"].([]any)
			if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
				t.Fatalf("include=%#v", payload["include"])
			}
			reasoning, ok := payload["reasoning"].(map[string]any)
			if !ok || reasoning["context"] != "all_turns" {
				t.Fatalf("reasoning=%#v", payload["reasoning"])
			}
			metadata, ok := payload["client_metadata"].(map[string]any)
			if !ok || metadata["session_id"] != session || metadata["x-codex-window-id"] != session+":0" {
				t.Fatalf("client_metadata=%#v", payload["client_metadata"])
			}
			if got := metadata["x-codex-turn-metadata"]; got != header.Get("X-Codex-Turn-Metadata") {
				t.Fatalf("身份单源：body metadata 与身份头不一致 %v vs %q", got, header.Get("X-Codex-Turn-Metadata"))
			}
			var turnMetadata map[string]any
			if err := json.Unmarshal([]byte(header.Get("X-Codex-Turn-Metadata")), &turnMetadata); err != nil {
				t.Fatalf("x-codex-turn-metadata 非法 JSON: %v", err)
			}
			if turnMetadata["session_id"] != session || turnMetadata["thread_id"] != session {
				t.Fatalf("turn metadata=%#v", turnMetadata)
			}
			if _, ok := turnMetadata["turn_started_at_unix_ms"].(float64); !ok {
				t.Fatalf("turn metadata 缺 turn_started_at_unix_ms: %#v", turnMetadata)
			}
			if turnID, _ := turnMetadata["turn_id"].(string); turnID == "" || turnID == session {
				// F4 现值：turn_id 为独立 UUID（≠ session），接线不得丢失。
				t.Fatalf("turn metadata turn_id 必须为独立 UUID: %#v", turnMetadata)
			}
		})
	}
}

// TestW3F4BaselineOAuthCodexOutboundShape 断言 A 形态（OAuth Codex）接线后
// 出站形状（§9.1-A 裁决，与 F3 W2 接线同构）。迁移前基线已单独跑绿并留档
// （URL=账户 base 拼 /responses、responses_json 发 JSON/stream:false +
// max_output_tokens + Accept application/json、无身份头、responses_sse 不注
// 入 store）。以下 §9.1 声明的行为变化不再按旧形状断言：
//   - 恒 SSE：Accept 恒 text/event-stream、body 恒 stream:true（responses_json
//     模式名仅为账户配置，评分跟随请求编码形态按 SSE 解析）；
//   - BudgetNone：删 max_output_tokens；store=false 两种模式统一注入；
//   - 身份头集合（originator/UA/session-id/…）由 Build 按 §9.3 单源写入；
//   - URL 固定为信封 CodexBaseURL（§9.1-A 固定上游，账户 base 不再参与）。
func TestW3F4BaselineOAuthCodexOutboundShape(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		model     string
		wantLite  bool
		want56Fix bool
	}{
		{mode: "responses_json", model: "gpt-test"},
		{mode: "responses_sse", model: "gpt-test"},
		{mode: "responses_json", model: "gpt-5.6-sol", wantLite: true, want56Fix: true},
		{mode: "responses_json", model: "gpt-5.6-preview", wantLite: false, want56Fix: true},
	} {
		t.Run(tc.mode+"/"+tc.model, func(t *testing.T) {
			request, body := w3BaselineRequest(t, "https://probe-a.test/backend-api/codex", func(input *Input) {
				input.Type = "oauth"
				input.ProtocolProfileID = "profile_gpt_openai_v1"
				input.ProtocolCode = "openai"
				input.EndpointMode = tc.mode
				input.HealthModel = tc.model
				input.OAuthAccountID = "w3-account"
			})
			// §9.1-A 固定上游：账户 base 不再参与 A 形态寻址。
			if request.URL.String() != upstreamenvelope.CodexBaseURL+"/responses" {
				t.Fatalf("URL=%s, want %s/responses", request.URL, upstreamenvelope.CodexBaseURL)
			}
			if got := request.Header.Get("Accept"); got != "text/event-stream" {
				t.Fatalf("accept=%q, want 恒 SSE", got)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer w3-token" {
				t.Fatalf("authorization=%q", got)
			}
			if got := request.Header.Get("Openai-Beta"); got != "responses=experimental" {
				t.Fatalf("openai-beta=%q", got)
			}
			if got := request.Header.Get("Chatgpt-Account-Id"); got != "w3-account" {
				t.Fatalf("chatgpt-account-id=%q", got)
			}
			// 身份头集合与派生关系（§9.3 单源，调用方生成后经 Build 写入）。
			if got := request.Header.Get("Originator"); got != "codex_exec" {
				t.Fatalf("originator=%q", got)
			}
			if got := request.Header.Get("User-Agent"); got != upstreamidentity.EffectiveCodexUserAgent() {
				t.Fatalf("user-agent=%q", got)
			}
			session := request.Header.Get("Session-Id")
			if session == "" {
				t.Fatalf("session-id 缺失: %v", request.Header)
			}
			if got := request.Header.Get("Thread-Id"); got != session {
				t.Fatalf("thread-id=%q want == session", got)
			}
			if got := request.Header.Get("X-Client-Request-Id"); got != session {
				t.Fatalf("x-client-request-id=%q want == session", got)
			}
			if got := request.Header.Get("X-Codex-Window-Id"); got != session+":0" {
				t.Fatalf("x-codex-window-id=%q", got)
			}
			if got := request.Header.Get("X-Codex-Beta-Features"); got != "remote_compaction_v2" {
				t.Fatalf("x-codex-beta-features=%q", got)
			}
			if got := request.Header.Get("X-OpenAI-Internal-Codex-Responses-Lite"); tc.wantLite && got != "true" {
				t.Fatalf("Lite 模型 %s 必须发送 Lite 头: %q", tc.model, got)
			} else if !tc.wantLite && got != "" {
				t.Fatalf("非 Lite 模型 %s 不得发送 Lite 头: %q", tc.model, got)
			}
			payload := w3BaselineBody(t, body)
			if payload["store"] != false || payload["stream"] != true {
				t.Fatalf("A 形态恒 store=false/stream=true: %#v", payload)
			}
			if payload["model"] != tc.model {
				t.Fatalf("model=%v", payload["model"])
			}
			if _, exists := payload["max_output_tokens"]; exists {
				t.Fatalf("A 形态 BudgetNone 不得携带 max_output_tokens: %#v", payload)
			}
			inputArr, ok := payload["input"].([]any)
			if !ok || len(inputArr) != 1 {
				t.Fatalf("input=%#v", payload["input"])
			}
			if _, ok := payload["instructions"].(string); !ok {
				t.Fatalf("instructions=%#v", payload["instructions"])
			}
			reasoning, hasReasoning := payload["reasoning"].(map[string]any)
			if tc.want56Fix {
				if !hasReasoning || reasoning["context"] != "all_turns" || payload["parallel_tool_calls"] != false {
					t.Fatalf("gpt-5.6-* 必须带 reasoning.context + parallel_tool_calls=false: %#v", payload)
				}
			} else if hasReasoning {
				t.Fatalf("非 gpt-5.6-* 不应带 reasoning: %#v", payload)
			}
		})
	}
	t.Run("oauth+codex_responses 优先按 A 形态", func(t *testing.T) {
		// §9.1 矩阵按账户形态分列：oauth 账户不落入 B 的兼容画像注入。
		request, body := w3BaselineRequest(t, "https://probe-a.test/backend-api/codex", func(input *Input) {
			input.Type = "oauth"
			input.ProtocolProfileID = "profile_gpt_openai_v1"
			input.ProtocolCode = "openai"
			input.EndpointMode = "responses_sse"
			input.ClientCompatibility = "codex_responses"
		})
		if request.URL.String() != upstreamenvelope.CodexBaseURL+"/responses" {
			t.Fatalf("URL=%s", request.URL)
		}
		if got := request.Header.Get("Session-Id"); got == "" {
			t.Fatalf("A 形态必须带身份头")
		}
		payload := w3BaselineBody(t, body)
		if _, exists := payload["client_metadata"]; exists {
			t.Fatalf("A 形态不得注入兼容画像 client_metadata: %#v", payload)
		}
	})
}

// TestW3F4BaselineCodeAssistShape 钉 Code Assist 出站形状：改道内部端点、
// 三字段包装、GeminiCLI 系统身份、Bearer + 配额项目、Accept 纯流式（线真相
// 恒 SSE，与账户配置模式名无关）。迁移前后不变式。
func TestW3F4BaselineCodeAssistShape(t *testing.T) {
	for _, mode := range []string{"generate_content_json", "generate_content_sse"} {
		t.Run(mode, func(t *testing.T) {
			request, body := w3BaselineRequest(t, "https://cloudcode-pa.googleapis.com", func(input *Input) {
				input.Provider = "gemini"
				input.ProtocolProfileID = "profile_gemini_native_v1beta"
				input.ProtocolCode = "gemini"
				input.ProtocolVersion = "v1beta"
				input.EndpointMode = mode
				input.Type = "google_oauth"
				input.OAuthType = "code_assist"
				input.OAuthProjectID = "w3-project"
				input.OAuthQuotaProjectID = "w3-quota"
			})
			if request.URL.String() != "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse" {
				t.Fatalf("URL=%s", request.URL)
			}
			if got := request.Header.Get("Accept"); got != "text/event-stream" {
				t.Fatalf("accept=%q（Code Assist 恒流式口径）", got)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer w3-token" {
				t.Fatalf("authorization=%q", got)
			}
			if got := request.Header.Get("X-Goog-User-Project"); got != "w3-quota" {
				t.Fatalf("x-goog-user-project=%q", got)
			}
			if got := request.Header.Get("User-Agent"); got != upstreamidentity.EffectiveGeminiCLIUserAgent() {
				t.Fatalf("user-agent=%q", got)
			}
			payload := w3BaselineBody(t, body)
			if payload["model"] != "w3-model" || payload["project"] != "w3-project" {
				t.Fatalf("body=%#v", payload)
			}
			inner, ok := payload["request"].(map[string]any)
			if !ok {
				t.Fatalf("request 包装缺失: %#v", payload)
			}
			if _, ok := inner["contents"].([]any); !ok {
				t.Fatalf("request.contents=%#v", inner["contents"])
			}
			config, ok := inner["generationConfig"].(map[string]any)
			if !ok || config["maxOutputTokens"] != float64(probeOutputTokenLimit) {
				t.Fatalf("request.generationConfig=%#v", inner["generationConfig"])
			}
		})
	}
}
