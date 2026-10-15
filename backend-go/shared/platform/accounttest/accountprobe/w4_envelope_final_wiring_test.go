package accountprobe

// W4 F2 接线矩阵（计划 W4 行、信封统一设计 §9.1–§9.5）：executeAttempt /
// buildTestRequest 走 upstreamenvelope.Build 后各形态出站形状与评分跟随。
// 本文件吸收原 m4_envelope_migration_test.go 的迁移锚点（预算单一来源、URL
// 归一化、Accept 口径、认证策略映射）——M4 组合式接缝随最终接线退出，改由
// 本矩阵对最终请求直接断言。
//
// 迁移前基线（W3 同法：基线单独跑绿后按 §9.1 显式 diff 重写断言；本步无法
// git 分段提交，diff 记录于此）：
//   - A 形态（OAuth Codex）：迁移前 URL=固定上游 /responses，responses_json
//     发 JSON/stream:false + max_output_tokens:1024 + store:false + Accept
//     application/json，无身份头/Lite 头；迁移后按 §9.1-A 恒 SSE（Accept
//     text/event-stream + stream:true）、BudgetNone（删 max_output_tokens）、
//     八身份头与 §9.5 Lite 头由 Build 写入。
//   - B 形态：迁移前 headers 段（原 executeAttempt）与 body 段
//     （applyCodexCompatibilityPayload）各生成一套独立 UUID（双身份缺陷，
//     headers metadata 缺 turn_started_at_unix_ms）；迁移后 §9.3 单源恒等
//     （TestW4CodexIdentitySingleSource 是该缺陷的回归钉子）。
//   - C 形态各协议：body 字段集不变（JSON 键序归信封编码，语义等价）；
//     interactions SSE 的 Accept 由固定 text/event-stream 收敛为按认证策略
//     口径（§9.1-C）；GLM 档案寻址按 §9.2-5 剥请求 /v1（裁决的寻址修正）；
//     api_key 形态 gemini SSE 双 accept 与原 :476 信封口径一致。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// w4CaptureTransport 记录出站请求 URL 并返回罐装 body（评分跟随测试）。
// bodyFunc 非空时按请求惰性构造 body（假上游需读挑战题作答的场景）。
type w4CaptureTransport struct {
	body       string
	bodyFunc   func(request *http.Request) string
	capturedOn bool
	url        string
}

func (t *w4CaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.url = request.URL.String()
	t.capturedOn = true
	body := t.body
	if t.bodyFunc != nil {
		body = t.bodyFunc(request)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

// w4Challenge 是参数化的探针挑战（出站形状断言不依赖随机性）。
var w4Challenge = OutputChallenge{ExpectedOutput: "46", Prompt: "接口连通性自动测试：请计算 12+34，只回复数字"}

// w4BuildRequest 构造单次出站请求并读出 body（身份值参数化：不比具体 UUID，
// 比存在性与互相派生关系）。凭据取 view.SelectedAPIKey（生产同源），未设置时
// 回落 w4-key。
func w4BuildRequest(t *testing.T, view *View, mode EndpointMode, model string) (*http.Request, []byte) {
	t.Helper()
	request, err := w4BuildRequestE(view, mode, model)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	return request, body
}

// w4BuildRequestE 是 w4BuildRequest 的错误透传变体（负路径断言用）。
func w4BuildRequestE(view *View, mode EndpointMode, model string) (*http.Request, error) {
	if view.Credentials == nil {
		view.Credentials = map[string]any{}
	}
	apiKey := view.SelectedAPIKey
	if strings.TrimSpace(apiKey) == "" {
		apiKey = "w4-key"
	}
	return buildTestRequest(context.Background(), view, mode, model, w4Challenge, apiKey)
}

func w4Body(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("出站 body 非法: %v", err)
	}
	return payload
}

// w4AssertIdentityHeaders 断言 A/B 形态共享的身份头集合与派生关系（§9.3 单源：
// session=UUID、thread=session、x-client-request-id=session、window=thread+":0"）。
func w4AssertIdentityHeaders(t *testing.T, header http.Header) string {
	t.Helper()
	if got := header.Get("Originator"); got != "codex_exec" {
		t.Fatalf("originator=%q", got)
	}
	if got := header.Get("User-Agent"); got != upstreamidentity.EffectiveCodexUserAgent() {
		t.Fatalf("user-agent=%q, want EffectiveCodexUserAgent", got)
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
	return session
}

// TestW4CodexIdentitySingleSource 是双身份缺陷的回归钉子（计划 W4 验收列点
// 名）：B 形态请求的 headers 身份与 body client_metadata 必须由同一份身份
// 派生（§9.3 单源）。迁移前 headers 段与 applyCodexCompatibilityPayload 各自
// 随机生成，session_id 头与 client_metadata.session_id 是两个不同 UUID，且
// headers metadata 缺 turn_started_at_unix_ms——两条都必须保持修复态。
func TestW4CodexIdentitySingleSource(t *testing.T) {
	for _, mode := range []EndpointMode{ModeResponsesJSON, ModeResponsesSSE} {
		t.Run(string(mode), func(t *testing.T) {
			view := probeView("https://w4-upstream.test")
			view.ClientCompatibility = "codex_responses"
			request, body := w4BuildRequest(t, view, mode, "gpt-test")
			header := request.Header
			payload := w4Body(t, body)
			session := w4AssertIdentityHeaders(t, header)

			// 双身份恒等断言（缺陷钉子）。
			metadata, ok := payload["client_metadata"].(map[string]any)
			if !ok {
				t.Fatalf("B 形态必须注入 client_metadata: %#v", payload)
			}
			if got := metadata["session_id"]; got != session {
				t.Fatalf("headers 身份与 body client_metadata.session_id 必须恒等: body=%v header=%s", got, session)
			}
			if got := metadata["thread_id"]; got != header.Get("Thread-Id") {
				t.Fatalf("thread 身份不同源: body=%v header=%q", got, header.Get("Thread-Id"))
			}
			if got := metadata["x-codex-window-id"]; got != header.Get("X-Codex-Window-Id") {
				t.Fatalf("window 身份不同源: body=%v header=%q", got, header.Get("X-Codex-Window-Id"))
			}
			if got := metadata["x-codex-turn-metadata"]; got != header.Get("X-Codex-Turn-Metadata") {
				t.Fatalf("turn metadata 身份不同源: body=%v header=%q", got, header.Get("X-Codex-Turn-Metadata"))
			}
			if got := metadata["x-codex-installation-id"]; got == "" {
				t.Fatalf("client_metadata 缺 installation: %#v", metadata)
			}
			if got := payload["prompt_cache_key"]; got != session {
				t.Fatalf("prompt_cache_key=%v want == session", got)
			}

			// turn metadata 九键（§9.3）：全身份值 + request_kind/thread_source/
			// sandbox/turn_started_at_unix_ms（F2 原缺失键）。
			var turnMetadata map[string]any
			if err := json.Unmarshal([]byte(header.Get("X-Codex-Turn-Metadata")), &turnMetadata); err != nil {
				t.Fatalf("x-codex-turn-metadata 非法 JSON: %v", err)
			}
			for _, key := range []string{"installation_id", "session_id", "thread_id", "turn_id", "window_id", "request_kind", "thread_source", "sandbox", "turn_started_at_unix_ms"} {
				if _, exists := turnMetadata[key]; !exists {
					t.Fatalf("turn metadata 缺 %s: %#v", key, turnMetadata)
				}
			}
			if turnMetadata["session_id"] != session || turnMetadata["thread_id"] != session || turnMetadata["window_id"] != session+":0" {
				t.Fatalf("turn metadata 身份派生断裂: %#v", turnMetadata)
			}
			if turnID, _ := turnMetadata["turn_id"].(string); turnID == "" || turnID == session {
				t.Fatalf("turn_id 必须是独立 UUID（F2/F4 现值）: %#v", turnMetadata)
			}

			// B 形态注入与预算保留（§9.1-B）：不写 openai-beta，Accept 按模式。
			if got := header.Get("Openai-Beta"); got != "" {
				t.Fatalf("B 形态不得补 openai-beta=%q", got)
			}
			if payload["store"] != false || payload["parallel_tool_calls"] != false {
				t.Fatalf("body=%#v", payload)
			}
			if payload["max_output_tokens"] != float64(upstreamenvelope.DefaultProbeOutputTokens) {
				t.Fatalf("B 形态保留预算字段: %#v", payload)
			}
			include, ok := payload["include"].([]any)
			if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
				t.Fatalf("include=%#v", payload["include"])
			}
			if reasoning, ok := payload["reasoning"].(map[string]any); !ok || reasoning["context"] != "all_turns" {
				t.Fatalf("reasoning=%#v", payload["reasoning"])
			}
			wantAccept := "application/json"
			if mode == ModeResponsesSSE {
				wantAccept = "application/json, text/event-stream"
			}
			if got := header.Get("Accept"); got != wantAccept {
				t.Fatalf("accept=%q want %q", got, wantAccept)
			}
			if got := header.Get("Authorization"); got != "Bearer sk-test" {
				t.Fatalf("authorization=%q", got)
			}
		})
	}
}

// TestW4FormAOAuthCodexOutboundShape 断言 A 形态（OAuth Codex）接线后出站
// 形状（§9.1-A；显式 diff 见文件头注释）。
func TestW4FormAOAuthCodexOutboundShape(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      EndpointMode
		model     string
		wantLite  bool
		want56Fix bool
	}{
		{name: "responses_json", mode: ModeResponsesJSON, model: "gpt-test"},
		{name: "responses_sse", mode: ModeResponsesSSE, model: "gpt-test"},
		{name: "lite-hit", mode: ModeResponsesJSON, model: "gpt-5.6-sol", wantLite: true, want56Fix: true},
		{name: "lite-miss", mode: ModeResponsesJSON, model: "gpt-5.6-preview", want56Fix: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := probeView("https://ignored.example.com")
			view.Type = "oauth"
			view.ProviderProtocolProfileID = "profile_gpt_openai_v1"
			view.Credentials = map[string]any{"account_id": "w4-account"}
			request, body := w4BuildRequest(t, view, tc.mode, tc.model)
			header := request.Header
			payload := w4Body(t, body)

			if request.URL.String() != upstreamenvelope.CodexBaseURL+"/responses" {
				t.Fatalf("URL=%s, want 固定上游 /responses", request.URL)
			}
			if got := header.Get("Accept"); got != "text/event-stream" {
				t.Fatalf("A 形态 Accept 恒 SSE: %q", got)
			}
			if got := header.Get("Authorization"); got != "Bearer sk-test" {
				t.Fatalf("authorization=%q", got)
			}
			if got := header.Get("Openai-Beta"); got != "responses=experimental" {
				t.Fatalf("openai-beta=%q", got)
			}
			if got := header.Get("Chatgpt-Account-Id"); got != "w4-account" {
				t.Fatalf("chatgpt-account-id=%q", got)
			}
			w4AssertIdentityHeaders(t, header)
			if got := header.Get("X-OpenAI-Internal-Codex-Responses-Lite"); tc.wantLite && got != "true" {
				t.Fatalf("Lite 模型 %s 必须发送 Lite 头: %q", tc.model, got)
			} else if !tc.wantLite && got != "" {
				t.Fatalf("非 Lite 模型 %s 不得发送 Lite 头: %q", tc.model, got)
			}
			// A 形态 body：恒 stream=true/store=false、BudgetNone、无兼容画像注入。
			if payload["store"] != false || payload["stream"] != true {
				t.Fatalf("A 形态恒 store=false/stream=true: %#v", payload)
			}
			if _, exists := payload["max_output_tokens"]; exists {
				t.Fatalf("A 形态 BudgetNone 不得携带 max_output_tokens: %#v", payload)
			}
			if payload["model"] != tc.model {
				t.Fatalf("model=%v", payload["model"])
			}
			input, ok := payload["input"].([]any)
			if !ok || len(input) != 1 {
				t.Fatalf("input=%#v", payload["input"])
			}
			if _, ok := payload["instructions"].(string); !ok {
				t.Fatalf("instructions 必须为字符串: %#v", payload["instructions"])
			}
			if _, exists := payload["client_metadata"]; exists {
				t.Fatalf("A 形态不得注入 client_metadata: %#v", payload)
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
	// oauth 账户即使携带 codex_responses 也按 A 处理（§9.1 矩阵无第四形态）。
	t.Run("oauth+codex_responses 优先 A 形态", func(t *testing.T) {
		view := probeView("https://ignored.example.com")
		view.Type = "oauth"
		view.ProviderProtocolProfileID = "profile_gpt_openai_v1"
		view.ClientCompatibility = "codex_responses"
		request, body := w4BuildRequest(t, view, ModeResponsesSSE, "gpt-test")
		if request.URL.String() != upstreamenvelope.CodexBaseURL+"/responses" {
			t.Fatalf("URL=%s", request.URL)
		}
		if request.Header.Get("Session-Id") == "" {
			t.Fatal("A 形态必须带身份头")
		}
		payload := w4Body(t, body)
		if _, exists := payload["client_metadata"]; exists {
			t.Fatalf("A 形态不得注入 client_metadata: %#v", payload)
		}
		if _, exists := payload["prompt_cache_key"]; exists {
			t.Fatalf("A 形态不得注入 prompt_cache_key: %#v", payload)
		}
	})
}

// TestW4FormCOpenAIArms 断言 C 形态（普通 API Key / 非 A 的 OAuth）OpenAI 系
// 出站形状：URL/Accept/认证头/预算字段/body 语义（§9.1-C，迁移前后不变式，
// 键序差异除外）。
func TestW4FormCOpenAIArms(t *testing.T) {
	t.Run("chat_json", func(t *testing.T) {
		request, body := w4BuildRequest(t, probeView("https://w4-upstream.test"), ModeChatJSON, "w4-model")
		if request.URL.String() != "https://w4-upstream.test/v1/chat/completions" {
			t.Fatalf("URL=%s", request.URL)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("accept=%q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("authorization=%q", got)
		}
		payload := w4Body(t, body)
		if payload["model"] != "w4-model" || payload["stream"] != false || payload["max_tokens"] != float64(upstreamenvelope.DefaultProbeOutputTokens) {
			t.Fatalf("body=%#v", payload)
		}
		if _, ok := payload["messages"].([]any); !ok {
			t.Fatalf("messages=%#v", payload["messages"])
		}
	})
	t.Run("chat_sse 双 accept", func(t *testing.T) {
		request, body := w4BuildRequest(t, probeView("https://w4-upstream.test"), ModeChatSSE, "w4-model")
		if got := request.Header.Get("Accept"); got != "application/json, text/event-stream" {
			t.Fatalf("accept=%q", got)
		}
		if payload := w4Body(t, body); payload["stream"] != true {
			t.Fatalf("stream=%v", payload["stream"])
		}
	})
	t.Run("responses_json", func(t *testing.T) {
		view := probeView("https://w4-upstream.test")
		view.ProviderProtocolProfileID = "profile_openai_openai_v1"
		request, body := w4BuildRequest(t, view, ModeResponsesJSON, "w4-model")
		if request.URL.String() != "https://w4-upstream.test/v1/responses" {
			t.Fatalf("URL=%s", request.URL)
		}
		payload := w4Body(t, body)
		if payload["stream"] != false || payload["max_output_tokens"] != float64(upstreamenvelope.DefaultProbeOutputTokens) {
			t.Fatalf("body=%#v", payload)
		}
		if _, ok := payload["input"].([]any); !ok {
			t.Fatalf("input=%#v", payload["input"])
		}
		if _, ok := payload["instructions"].(string); !ok {
			t.Fatalf("instructions=%#v", payload["instructions"])
		}
		if _, exists := payload["store"]; exists {
			t.Fatalf("api_key responses 不得携带 store: %#v", payload)
		}
	})
	t.Run("oauth 空 profile chat 保持账户 base 与伴随头", func(t *testing.T) {
		view := probeView("https://w4-upstream.test")
		view.Type = "oauth"
		view.ProviderProtocolProfileID = ""
		view.Credentials = map[string]any{"account_id": "w4-acct"}
		view.SelectedAPIKey = "oauth-token"
		request, body := w4BuildRequest(t, view, ModeChatJSON, "w4-model")
		if request.URL.String() != "https://w4-upstream.test/v1/chat/completions" {
			t.Fatalf("非 gpt 档案不得改道固定上游: %s", request.URL)
		}
		if got := request.Header.Get("Chatgpt-Account-Id"); got != "w4-acct" {
			t.Fatalf("chatgpt-account-id=%q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer oauth-token" {
			t.Fatalf("authorization=%q", got)
		}
		if payload := w4Body(t, body); payload["max_tokens"] != float64(upstreamenvelope.DefaultProbeOutputTokens) {
			t.Fatalf("body=%#v", payload)
		}
	})
	t.Run("oauth 非 gpt 档案 responses 保留 store=false", func(t *testing.T) {
		view := probeView("https://w4-upstream.test")
		view.Type = "oauth"
		view.ProviderProtocolProfileID = "profile_openai_other_v1"
		view.SelectedAPIKey = "oauth-token"
		request, body := w4BuildRequest(t, view, ModeResponsesJSON, "w4-model")
		if request.URL.String() != "https://w4-upstream.test/v1/responses" {
			t.Fatalf("URL=%s", request.URL)
		}
		payload := w4Body(t, body)
		if payload["store"] != false {
			t.Fatalf("oauth responses 语义 store=false 丢失: %#v", payload)
		}
		if request.Header.Get("Openai-Beta") != "" {
			t.Fatalf("C 形态不补 openai-beta: %q", request.Header.Get("Openai-Beta"))
		}
	})
}

// TestW4MultipartTranscription 断言 STT multipart 出站形状（§9.2-4）：字段、
// file bytes/MIME 与 Content-Type boundary-body 匹配；不比较随机 boundary
// 字面值（标准库 writer 的 boundary 与原手写编码不同属预期收敛）。
func TestW4MultipartTranscription(t *testing.T) {
	request, body := w4BuildRequest(t, probeView("https://w4-upstream.test"), ModeAudioTranscriptionJSON, "whisper-1")
	if request.URL.String() != "https://w4-upstream.test/v1/audio/transcriptions" {
		t.Fatalf("URL=%s", request.URL)
	}
	if got := request.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("accept=%q", got)
	}
	contentType := request.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || strings.TrimSpace(params["boundary"]) == "" {
		t.Fatalf("content-type=%q err=%v", contentType, err)
	}
	form, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("解析 multipart 失败: %v", err)
	}
	if got := form.Value["model"]; len(got) != 1 || got[0] != "whisper-1" {
		t.Fatalf("model 字段=%v", form.Value["model"])
	}
	fileHeaders, ok := form.File["file"]
	if !ok || len(fileHeaders) != 1 {
		t.Fatalf("file 段缺失: %v", form.File)
	}
	fileHeader := fileHeaders[0]
	file, err := fileHeader.Open()
	if err != nil {
		t.Fatalf("打开 file 段失败: %v", err)
	}
	defer file.Close()
	if fileHeader.Filename != "probe.wav" {
		t.Fatalf("filename=%q", fileHeader.Filename)
	}
	if got := fileHeader.Header.Get("Content-Type"); got != "audio/wav" {
		t.Fatalf("file MIME=%q", got)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, probeWAVPayload()) {
		t.Fatalf("file bytes 与内置 WAV 样本不一致: %d bytes", len(data))
	}
}

// TestW4AudioSpeechArms 断言两类 audio_speech 出站形状（音频设计 §11.9/§5.1）。
func TestW4AudioSpeechArms(t *testing.T) {
	t.Run("openai", func(t *testing.T) {
		request, body := w4BuildRequest(t, probeView("https://w4-upstream.test"), ModeAudioSpeech, "tts-1")
		if request.URL.String() != "https://w4-upstream.test/v1/audio/speech" {
			t.Fatalf("URL=%s", request.URL)
		}
		payload := w4Body(t, body)
		if payload["model"] != "tts-1" || payload["input"] != "ok" || payload["voice"] != probeSpeechDefaultVoice || payload["response_format"] != "wav" {
			t.Fatalf("body=%#v", payload)
		}
		if _, exists := payload["max_tokens"]; exists {
			t.Fatalf("audio 契约无预算字段: %#v", payload)
		}
	})
	t.Run("gemini generateContent 承载且无预算", func(t *testing.T) {
		view := probeView("https://gem.test/v1beta")
		view.ProtocolCode = "gemini"
		request, body := w4BuildRequest(t, view, ModeAudioSpeech, "gemini-2.5-flash-preview-tts")
		if request.URL.String() != "https://gem.test/v1beta/models/gemini-2.5-flash-preview-tts:generateContent" {
			t.Fatalf("URL=%s", request.URL)
		}
		payload := w4Body(t, body)
		config, ok := payload["generationConfig"].(map[string]any)
		if !ok {
			t.Fatalf("generationConfig=%#v", payload["generationConfig"])
		}
		modalities, _ := config["responseModalities"].([]any)
		if len(modalities) != 1 || modalities[0] != "AUDIO" {
			t.Fatalf("responseModalities=%v", config["responseModalities"])
		}
		prebuilt, _ := config["speechConfig"].(map[string]any)["prebuiltVoiceConfig"].(map[string]any)
		if prebuilt["voiceName"] != "Kore" {
			t.Fatalf("voiceName=%v", prebuilt["voiceName"])
		}
		if _, exists := config["maxOutputTokens"]; exists {
			t.Fatalf("gemini audio_speech 探针族无输出预算: %#v", config)
		}
		contents, _ := payload["contents"].([]any)
		if len(contents) != 1 {
			t.Fatalf("contents=%v", payload["contents"])
		}
	})
}

// TestW4ImagesJSONArm 断言图片生成出站形状（§1：不是图片输入；不套用文本
// 输出预算）。
func TestW4ImagesJSONArm(t *testing.T) {
	request, body := w4BuildRequest(t, probeView("https://w4-upstream.test"), ModeImagesJSON, "img-1")
	if request.URL.String() != "https://w4-upstream.test/v1/images/generations" {
		t.Fatalf("URL=%s", request.URL)
	}
	payload := w4Body(t, body)
	if payload["model"] != "img-1" || payload["n"] != float64(1) || payload["size"] != "1024x1024" || payload["quality"] != "low" || payload["output_format"] != "webp" || payload["output_compression"] != float64(100) {
		t.Fatalf("body=%#v", payload)
	}
	prompt, _ := payload["prompt"].(string)
	if !strings.HasPrefix(prompt, "Solid black. ") {
		t.Fatalf("prompt=%q", prompt)
	}
	for _, banned := range []string{"max_tokens", "max_output_tokens"} {
		if _, exists := payload[banned]; exists {
			t.Fatalf("images 契约无预算字段: %#v", payload)
		}
	}
}

// TestW4AnthropicMessagesArms 断言 Anthropic Messages 出站形状：catalog 固定
// 头、x-api-key、session 恒等（x-claude-code-session-id 头与 body
// metadata.user_id.session_id 同源）与预算必填注入。
func TestW4AnthropicMessagesArms(t *testing.T) {
	for _, mode := range []EndpointMode{ModeMessagesJSON, ModeMessagesSSE} {
		t.Run(string(mode), func(t *testing.T) {
			view := probeView("https://claude.test")
			view.ProtocolCode = "anthropic"
			request, body := w4BuildRequest(t, view, mode, "claude-1")
			if request.URL.String() != "https://claude.test/v1/messages" {
				t.Fatalf("URL=%s", request.URL)
			}
			header := request.Header
			if got := header.Get("Anthropic-Version"); got != "2023-06-01" {
				t.Fatalf("anthropic-version=%q", got)
			}
			if got := header.Get("X-Api-Key"); got != "sk-test" {
				t.Fatalf("x-api-key=%q", got)
			}
			payload := w4Body(t, body)
			if payload["model"] != "claude-1" || payload["max_tokens"] != float64(upstreamenvelope.DefaultProbeOutputTokens) {
				t.Fatalf("body=%#v", payload)
			}
			if payload["stream"] != (mode == ModeMessagesSSE) {
				t.Fatalf("stream=%v", payload["stream"])
			}
			metadata, _ := payload["metadata"].(map[string]any)
			userID, _ := metadata["user_id"].(map[string]any)
			session := header.Get("X-Claude-Code-Session-Id")
			if session == "" {
				t.Fatalf("x-claude-code-session-id 缺失: %v", header)
			}
			if userID["session_id"] != session {
				t.Fatalf("anthropic session 身份不同源: body=%v header=%s", userID["session_id"], session)
			}
			system, _ := payload["system"].([]any)
			if len(system) == 0 {
				t.Fatalf("system=%#v", payload["system"])
			}
			wantAccept := "application/json"
			if mode == ModeMessagesSSE {
				wantAccept = "application/json, text/event-stream"
			}
			if got := header.Get("Accept"); got != wantAccept {
				t.Fatalf("accept=%q want %q", got, wantAccept)
			}
		})
	}
}

// TestW4InteractionsArms 断言 Interactions 出站形状：api-revision catalog 头
// 到达线上请求（§9.5 验证项，F2 原缺失属缺陷）、无预算字段、Accept 按认证
// 策略口径（api-key 双 accept / google_oauth 纯流式；§9.1-C 收敛 diff）。
func TestW4InteractionsArms(t *testing.T) {
	t.Run("api_key json", func(t *testing.T) {
		view := probeView("https://gem.test")
		view.ProtocolCode = "gemini"
		request, body := w4BuildRequest(t, view, ModeInteractionsJSON, "gemini-2")
		if request.URL.String() != "https://gem.test/v1beta/interactions" {
			t.Fatalf("URL=%s", request.URL)
		}
		if got := request.Header.Get("Api-Revision"); got != "2026-05-20" {
			t.Fatalf("api-revision=%q, want catalog 固定头 2026-05-20", got)
		}
		if got := request.Header.Get("X-Goog-Api-Key"); got != "sk-test" {
			t.Fatalf("x-goog-api-key=%q", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("accept=%q", got)
		}
		payload := w4Body(t, body)
		if payload["model"] != "gemini-2" || payload["stream"] != false {
			t.Fatalf("body=%#v", payload)
		}
		if payload["input"] != w4Challenge.Prompt {
			t.Fatalf("input=%v", payload["input"])
		}
		if _, has := payload["maxOutputTokens"]; has {
			t.Fatalf("interactions 为 BudgetFieldNone 端点，不得伪造预算字段: %#v", payload)
		}
	})
	t.Run("api_key sse 双 accept", func(t *testing.T) {
		view := probeView("https://gem.test")
		view.ProtocolCode = "gemini"
		request, body := w4BuildRequest(t, view, ModeInteractionsSSE, "gemini-2")
		if got := request.Header.Get("Accept"); got != "application/json, text/event-stream" {
			t.Fatalf("accept=%q（§9.1-C 口径）", got)
		}
		if payload := w4Body(t, body); payload["stream"] != true {
			t.Fatalf("stream=%v", payload["stream"])
		}
	})
	t.Run("google_oauth sse 纯流式", func(t *testing.T) {
		view := probeView("https://gem.test")
		view.ProtocolCode = "gemini"
		view.Type = "google_oauth"
		view.Credentials = map[string]any{"quota_project_id": "w4-quota"}
		view.SelectedAPIKey = "google-token"
		request, body := w4BuildRequest(t, view, ModeInteractionsSSE, "gemini-2")
		if got := request.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("accept=%q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer google-token" {
			t.Fatalf("authorization=%q", got)
		}
		if got := request.Header.Get("X-Goog-User-Project"); got != "w4-quota" {
			t.Fatalf("x-goog-user-project=%q", got)
		}
		_ = body
	})
}

// TestW4GLMThreeBaseForms 断言 GLM 恢复消费 profile 后的三种 base 形态
// （§9.2-5 裁决的寻址修正：官方版本根剥请求 /v1；代理 /v1 根与空根保留）。
// 原 F2 屏蔽 GLM 档案导致官方根被拼成 /api/paas/v4/v1/chat/completions。
func TestW4GLMThreeBaseForms(t *testing.T) {
	for _, tc := range []struct {
		name    string
		base    string
		wantURL string
	}{
		{name: "官方版本根剥请求 /v1", base: "https://open.bigmodel.cn/api/paas/v4", wantURL: "https://open.bigmodel.cn/api/paas/v4/chat/completions"},
		{name: "代理 /v1 根保留请求 /v1", base: "https://glm-proxy.test/v1", wantURL: "https://glm-proxy.test/v1/chat/completions"},
		{name: "空根补 /v1", base: "https://glm-proxy.test", wantURL: "https://glm-proxy.test/v1/chat/completions"},
	} {
		for _, profile := range []string{"profile_glm_general_openai_v1", "profile_glm_coding_openai_v1"} {
			t.Run(tc.name+"/"+profile, func(t *testing.T) {
				view := probeView(tc.base)
				view.ProviderCode = "glm"
				view.ProviderProtocolProfileID = profile
				request, _ := w4BuildRequest(t, view, ModeChatJSON, "glm-4.6")
				if request.URL.String() != tc.wantURL {
					t.Fatalf("URL=%s, want %s", request.URL, tc.wantURL)
				}
			})
		}
	}
	// GLM anthropic 档案不受 OpenAI 剥 /v1 规约影响（anthropic 分支补 /v1）。
	t.Run("glm anthropic 档案", func(t *testing.T) {
		view := probeView("https://open.bigmodel.cn/api/anthropic")
		view.ProtocolCode = "anthropic"
		view.ProviderProtocolProfileID = "profile_glm_coding_anthropic_v1"
		request, _ := w4BuildRequest(t, view, ModeMessagesJSON, "glm-4.6")
		if request.URL.String() != "https://open.bigmodel.cn/api/anthropic/v1/messages" {
			t.Fatalf("URL=%s", request.URL)
		}
	})
}

// TestW4CodeAssistShape 断言 Code Assist / Google One 出站形状（§9.1）：改道
// 内部端点、{model, project, request} 三字段包装、恒 SSE、Bearer + 配额项目；
// 账户配置模式名不改变线真相。
func TestW4CodeAssistShape(t *testing.T) {
	for _, mode := range []EndpointMode{ModeGenerateContentJSON, ModeGenerateContentSSE} {
		t.Run(string(mode), func(t *testing.T) {
			view := probeView("https://cloudcode-pa.googleapis.com")
			view.ProtocolCode = "gemini"
			view.Type = "google_oauth"
			view.Credentials = map[string]any{"oauth_type": "code_assist", "project_id": "w4-project", "quota_project_id": "w4-quota"}
			view.SelectedAPIKey = "google-token"
			request, body := w4BuildRequest(t, view, mode, "gemini-2")
			if request.URL.String() != "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse" {
				t.Fatalf("URL=%s", request.URL)
			}
			if got := request.Header.Get("Accept"); got != "text/event-stream" {
				t.Fatalf("accept=%q（Code Assist 恒流式）", got)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer google-token" {
				t.Fatalf("authorization=%q", got)
			}
			if got := request.Header.Get("X-Goog-User-Project"); got != "w4-quota" {
				t.Fatalf("x-goog-user-project=%q", got)
			}
			payload := w4Body(t, body)
			if payload["model"] != "gemini-2" || payload["project"] != "w4-project" {
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
			if !ok || config["maxOutputTokens"] != float64(upstreamenvelope.DefaultProbeOutputTokens) {
				t.Fatalf("request.generationConfig=%#v", inner["generationConfig"])
			}
		})
	}
	t.Run("缺 project_id 显式失败", func(t *testing.T) {
		view := probeView("https://cloudcode-pa.googleapis.com")
		view.ProtocolCode = "gemini"
		view.Type = "google_oauth"
		view.Credentials = map[string]any{"oauth_type": "code_assist"}
		if _, err := w4BuildRequestE(view, ModeGenerateContentJSON, "gemini-2"); err == nil || !strings.Contains(err.Error(), "project_id") {
			t.Fatalf("missing project: %v", err)
		}
	})
}

// TestW4ClassifyModeFollowsWireForm 断言评分跟随请求编码形态（§9.4 2026-10-12
// W3 裁决）：A 形态与 Code Assist 恒 SSE 后，responses_json /
// generate_content_json 账户配置模式名的响应按 SSE 解析，SSE 成功证据照常
// 判活，无"先试 JSON 再试 SSE"的 fallback。
func TestW4ClassifyModeFollowsWireForm(t *testing.T) {
	t.Run("A 形态 responses_json 按 SSE 评分", func(t *testing.T) {
		transport := &w4CaptureTransport{
			bodyFunc: func(request *http.Request) string {
				body, _ := io.ReadAll(request.Body)
				return "event: response.output_text.delta\n" +
					`data: {"type":"response.output_text.delta","delta":"` + challengeAnswerFromBody(body) + `"}` + "\n\n" +
					"event: response.completed\n" +
					`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n"
			},
		}
		view := probeView("https://ignored.example.com")
		view.Type = "oauth"
		view.ProviderProtocolProfileID = "profile_gpt_openai_v1"
		view.Credentials = map[string]any{"account_id": "w4-acct"}
		view.HealthCheckEndpointMode = string(ModeResponsesJSON)
		view.NormalizeEndpointModes = map[EndpointMode]bool{ModeResponsesJSON: true}
		service, err := NewService(Options{Source: &fakeSource{view: view}, Secret: "s", Client: &http.Client{Transport: transport}})
		if err != nil {
			t.Fatal(err)
		}
		observation, err := service.Probe(context.Background(), accountquality.ProbeRequest{AccountID: "acc-1", Full: true})
		if err != nil || observation == nil || !observation.Result.Success {
			t.Fatalf("A 形态 SSE 评分承接失败: %+v %v", observation, err)
		}
		if transport.url != upstreamenvelope.CodexBaseURL+"/responses" {
			t.Fatalf("A 形态请求未到固定上游: %s", transport.url)
		}
	})
	t.Run("Code Assist generate_content_json 按 SSE 评分", func(t *testing.T) {
		transport := &w4CaptureTransport{
			bodyFunc: func(request *http.Request) string {
				body, _ := io.ReadAll(request.Body)
				return "data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"" + challengeAnswerFromBody(body) + "\"}]}}]}\n\n"
			},
		}
		view := probeView("https://cloudcode-pa.googleapis.com")
		view.ProtocolCode = "gemini"
		view.Type = "google_oauth"
		view.Credentials = map[string]any{"oauth_type": "code_assist", "project_id": "w4-project"}
		view.HealthCheckEndpointMode = string(ModeGenerateContentJSON)
		view.NormalizeEndpointModes = map[EndpointMode]bool{ModeGenerateContentJSON: true}
		service, err := NewService(Options{Source: &fakeSource{view: view}, Secret: "s", Client: &http.Client{Transport: transport}})
		if err != nil {
			t.Fatal(err)
		}
		observation, err := service.Probe(context.Background(), accountquality.ProbeRequest{AccountID: "acc-1", Full: true})
		if err != nil || observation == nil || !observation.Result.Success {
			t.Fatalf("Code Assist SSE 评分承接失败: %+v %v", observation, err)
		}
		if transport.url != "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse" {
			t.Fatalf("Code Assist 请求未改道内部端点: %s", transport.url)
		}
	})
}
