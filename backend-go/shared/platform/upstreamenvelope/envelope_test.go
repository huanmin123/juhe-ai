package upstreamenvelope

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const (
	testOpenAIBase     = "https://probe.example.com"
	testAnthropicBase  = "https://anthropic.example.com"
	testGeminiBase     = "https://gemini.example.com"
	testBearerToken    = "tok-probe"
	testAnthropicToken = "sk-ant-probe"
	testGeminiToken    = "gm-probe"
	testGeminiModel    = "gemini-probe"
)

// TestBuildEndpointContract 对 catalog 每个登记条目断言 method、最终 URL、
// 认证头、Content-Type/Accept、body 编码与协议必填字段、预算字段按策略
// 出现/省略（§7.1 共享构造契约）。
func TestBuildEndpointContract(t *testing.T) {
	bounded := BudgetBounded(DefaultProbeOutputTokens)
	cases := []struct {
		name             string
		spec             Spec
		wantURL          string
		wantAccept       string
		wantContentType  string
		wantAuthName     string
		wantAuthValue    string
		wantExtraHeaders map[string]string
		wantBody         map[string]any
		wantAbsentBody   []string
		// wantMultipartFields / wantMultipartFile 仅 multipart 条目使用。
		wantMultipartFields map[string]string
		wantMultipartFile   *MultipartFile
	}{
		{
			name: "chat_json",
			spec: Spec{
				BaseURL: testOpenAIBase, EndpointMode: ModeChatJSON,
				Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: bounded,
				BodyFields: []BodyField{
					{Key: "model", Value: "gpt-probe"},
					{Key: "messages", Value: []map[string]any{{"role": "user", "content": "接口连通性自动测试"}}},
					{Key: "stream", Value: false},
				},
			},
			wantURL:         testOpenAIBase + "/v1/chat/completions",
			wantAccept:      "application/json",
			wantContentType: "application/json",
			wantAuthName:    "Authorization", wantAuthValue: "Bearer " + testBearerToken,
			wantBody: map[string]any{
				"model":      "gpt-probe",
				"max_tokens": float64(DefaultProbeOutputTokens),
				"stream":     false,
			},
		},
		{
			name: "chat_sse",
			spec: Spec{
				BaseURL: testOpenAIBase, EndpointMode: ModeChatSSE,
				Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: bounded,
				BodyFields: []BodyField{
					{Key: "model", Value: "gpt-probe"},
					{Key: "messages", Value: []map[string]any{{"role": "user", "content": "接口连通性自动测试"}}},
					{Key: "stream", Value: true},
				},
			},
			wantURL:         testOpenAIBase + "/v1/chat/completions",
			wantAccept:      "application/json, text/event-stream",
			wantContentType: "application/json",
			wantAuthName:    "Authorization", wantAuthValue: "Bearer " + testBearerToken,
			wantBody: map[string]any{
				"model":      "gpt-probe",
				"max_tokens": float64(DefaultProbeOutputTokens),
				"stream":     true,
			},
		},
		{
			name: "responses_json",
			spec: Spec{
				BaseURL: testOpenAIBase, EndpointMode: ModeResponsesJSON,
				Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: bounded,
				BodyFields: []BodyField{
					{Key: "model", Value: "gpt-probe"},
					{Key: "input", Value: "接口连通性自动测试"},
					{Key: "stream", Value: false},
				},
			},
			wantURL:         testOpenAIBase + "/v1/responses",
			wantAccept:      "application/json",
			wantContentType: "application/json",
			wantAuthName:    "Authorization", wantAuthValue: "Bearer " + testBearerToken,
			wantBody: map[string]any{
				"model":             "gpt-probe",
				"max_output_tokens": float64(DefaultProbeOutputTokens),
				"stream":            false,
			},
			wantAbsentBody: []string{"max_tokens"},
		},
		{
			name: "responses_sse",
			spec: Spec{
				BaseURL: testOpenAIBase, EndpointMode: ModeResponsesSSE,
				Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: bounded,
				BodyFields: []BodyField{
					{Key: "model", Value: "gpt-probe"},
					{Key: "input", Value: "接口连通性自动测试"},
					{Key: "stream", Value: true},
				},
			},
			wantURL:         testOpenAIBase + "/v1/responses",
			wantAccept:      "application/json, text/event-stream",
			wantContentType: "application/json",
			wantAuthName:    "Authorization", wantAuthValue: "Bearer " + testBearerToken,
			wantBody: map[string]any{
				"model":             "gpt-probe",
				"max_output_tokens": float64(DefaultProbeOutputTokens),
				"stream":            true,
			},
		},
		{
			name: "messages_json",
			spec: Spec{
				BaseURL: testAnthropicBase, EndpointMode: ModeMessagesJSON,
				Auth: Auth{Strategy: AuthAnthropicAPIKey, Token: testAnthropicToken}, Budget: bounded,
				BodyFields: []BodyField{
					{Key: "model", Value: "claude-probe"},
					{Key: "messages", Value: []map[string]any{{"role": "user", "content": "接口连通性自动测试"}}},
					{Key: "stream", Value: false},
				},
			},
			wantURL:         testAnthropicBase + "/v1/messages",
			wantAccept:      "application/json",
			wantContentType: "application/json",
			wantAuthName:    "X-Api-Key", wantAuthValue: testAnthropicToken,
			wantExtraHeaders: map[string]string{"Anthropic-Version": "2023-06-01"},
			wantBody: map[string]any{
				"model":      "claude-probe",
				"max_tokens": float64(DefaultProbeOutputTokens),
				"stream":     false,
			},
			wantAbsentBody: []string{"max_output_tokens"},
		},
		{
			name: "messages_sse",
			spec: Spec{
				BaseURL: testAnthropicBase, EndpointMode: ModeMessagesSSE,
				Auth: Auth{Strategy: AuthAnthropicAPIKey, Token: testAnthropicToken}, Budget: bounded,
				BodyFields: []BodyField{
					{Key: "model", Value: "claude-probe"},
					{Key: "messages", Value: []map[string]any{{"role": "user", "content": "接口连通性自动测试"}}},
					{Key: "stream", Value: true},
				},
			},
			wantURL:         testAnthropicBase + "/v1/messages",
			wantAccept:      "application/json, text/event-stream",
			wantContentType: "application/json",
			wantAuthName:    "X-Api-Key", wantAuthValue: testAnthropicToken,
			wantExtraHeaders: map[string]string{"Anthropic-Version": "2023-06-01"},
			wantBody: map[string]any{
				"model":      "claude-probe",
				"max_tokens": float64(DefaultProbeOutputTokens),
				"stream":     true,
			},
		},
		{
			name: "generate_content_json",
			spec: Spec{
				BaseURL: testGeminiBase, EndpointMode: ModeGenerateContentJSON, Model: testGeminiModel,
				Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: bounded,
				BodyFields: []BodyField{
					{Key: "contents", Value: []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "接口连通性自动测试"}}}}},
				},
			},
			wantURL:         testGeminiBase + "/v1beta/models/" + testGeminiModel + ":generateContent",
			wantAccept:      "application/json",
			wantContentType: "application/json",
			wantAuthName:    "X-Goog-Api-Key", wantAuthValue: testGeminiToken,
			wantBody: map[string]any{
				"generationConfig": map[string]any{"maxOutputTokens": float64(DefaultProbeOutputTokens)},
			},
			wantAbsentBody: []string{"max_tokens", "max_output_tokens"},
		},
		{
			name: "generate_content_sse",
			spec: Spec{
				BaseURL: testGeminiBase, EndpointMode: ModeGenerateContentSSE, Model: testGeminiModel,
				Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: bounded,
				BodyFields: []BodyField{
					{Key: "contents", Value: []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "接口连通性自动测试"}}}}},
				},
			},
			wantURL:         testGeminiBase + "/v1beta/models/" + testGeminiModel + ":streamGenerateContent?alt=sse",
			wantAccept:      "application/json, text/event-stream",
			wantContentType: "application/json",
			wantAuthName:    "X-Goog-Api-Key", wantAuthValue: testGeminiToken,
			wantBody: map[string]any{
				"generationConfig": map[string]any{"maxOutputTokens": float64(DefaultProbeOutputTokens)},
			},
		},
		{
			name: "interactions_json",
			spec: Spec{
				BaseURL: testGeminiBase, EndpointMode: ModeInteractionsJSON,
				Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: BudgetNone,
				BodyFields: []BodyField{
					{Key: "model", Value: testGeminiModel},
					{Key: "input", Value: "接口连通性自动测试"},
					{Key: "stream", Value: false},
				},
			},
			wantURL:         testGeminiBase + "/v1beta/interactions",
			wantAccept:      "application/json",
			wantContentType: "application/json",
			wantAuthName:    "X-Goog-Api-Key", wantAuthValue: testGeminiToken,
			wantExtraHeaders: map[string]string{"Api-Revision": "2026-05-20"},
			wantBody: map[string]any{
				"model": testGeminiModel,
				"input": "接口连通性自动测试",
			},
			wantAbsentBody: []string{"max_tokens", "max_output_tokens", "generationConfig"},
		},
		{
			name: "interactions_sse",
			spec: Spec{
				BaseURL: testGeminiBase, EndpointMode: ModeInteractionsSSE,
				Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: BudgetNone,
				BodyFields: []BodyField{
					{Key: "model", Value: testGeminiModel},
					{Key: "input", Value: "接口连通性自动测试"},
					{Key: "stream", Value: true},
				},
			},
			wantURL:         testGeminiBase + "/v1beta/interactions",
			wantAccept:      "application/json, text/event-stream",
			wantContentType: "application/json",
			wantAuthName:    "X-Goog-Api-Key", wantAuthValue: testGeminiToken,
			wantExtraHeaders: map[string]string{"Api-Revision": "2026-05-20"},
			wantBody: map[string]any{
				"stream": true,
			},
			wantAbsentBody: []string{"generationConfig"},
		},
		{
			name: "images_json",
			spec: Spec{
				BaseURL: testOpenAIBase, EndpointMode: ModeImagesJSON,
				Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone,
				BodyFields: []BodyField{
					{Key: "model", Value: "image-probe"},
					{Key: "prompt", Value: "Solid black. 123"},
					{Key: "n", Value: 1},
				},
			},
			wantURL:         testOpenAIBase + "/v1/images/generations",
			wantAccept:      "application/json",
			wantContentType: "application/json",
			wantAuthName:    "Authorization", wantAuthValue: "Bearer " + testBearerToken,
			wantBody:       map[string]any{"model": "image-probe", "n": float64(1)},
			wantAbsentBody: []string{"max_tokens", "max_output_tokens", "generationConfig"},
		},
		{
			name: "audio_speech",
			spec: Spec{
				BaseURL: testOpenAIBase, EndpointMode: ModeAudioSpeech,
				Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone,
				BodyFields: []BodyField{
					{Key: "model", Value: "tts-probe"},
					{Key: "input", Value: "ok"},
					{Key: "voice", Value: "alloy"},
					{Key: "response_format", Value: "wav"},
				},
			},
			wantURL:         testOpenAIBase + "/v1/audio/speech",
			wantAccept:      "application/json",
			wantContentType: "application/json",
			wantAuthName:    "Authorization", wantAuthValue: "Bearer " + testBearerToken,
			wantBody:       map[string]any{"model": "tts-probe", "voice": "alloy", "response_format": "wav"},
			wantAbsentBody: []string{"max_tokens", "max_output_tokens", "generationConfig"},
		},
		{
			name: "audio_transcription_json",
			spec: Spec{
				BaseURL: testOpenAIBase, EndpointMode: ModeAudioTranscriptionJSON,
				Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone,
				Multipart: &MultipartContent{
					Fields: []MultipartField{{Name: "model", Value: "stt-probe"}},
					File:   MultipartFile{FieldName: "file", Filename: "probe.wav", ContentType: "audio/wav", Data: []byte("RIFFwav-bytes")},
				},
			},
			wantURL:         testOpenAIBase + "/v1/audio/transcriptions",
			wantAccept:      "application/json",
			wantContentType: "multipart/form-data",
			wantAuthName:    "Authorization", wantAuthValue: "Bearer " + testBearerToken,
			wantMultipartFields: map[string]string{"model": "stt-probe"},
			wantMultipartFile:   &MultipartFile{FieldName: "file", Filename: "probe.wav", ContentType: "audio/wav", Data: []byte("RIFFwav-bytes")},
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			request, err := Build(context.Background(), item.spec)
			if err != nil {
				t.Fatalf("Build 失败: %v", err)
			}
			if request.Method != http.MethodPost {
				t.Fatalf("method = %q，期望 POST", request.Method)
			}
			if got := request.URL.String(); got != item.wantURL {
				t.Fatalf("URL = %q，期望 %q", got, item.wantURL)
			}
			if got := request.Header.Get("Accept"); got != item.wantAccept {
				t.Fatalf("Accept = %q，期望 %q", got, item.wantAccept)
			}
			if !strings.HasPrefix(request.Header.Get("Content-Type"), item.wantContentType) {
				t.Fatalf("Content-Type = %q，期望前缀 %q", request.Header.Get("Content-Type"), item.wantContentType)
			}
			if got := request.Header.Get(item.wantAuthName); got != item.wantAuthValue {
				t.Fatalf("认证头 %s = %q，期望 %q", item.wantAuthName, got, item.wantAuthValue)
			}
			for name, want := range item.wantExtraHeaders {
				if got := request.Header.Get(name); got != want {
					t.Fatalf("特殊头 %s = %q，期望 %q", name, got, want)
				}
			}
			if item.wantMultipartFile != nil {
				verifyMultipartBody(t, request, item.wantMultipartFields, *item.wantMultipartFile)
				return
			}
			body := decodeJSONBody(t, request)
			for key, want := range item.wantBody {
				if got, ok := body[key]; !ok || !reflect.DeepEqual(got, want) {
					t.Fatalf("body[%q] = %#v（存在 %v），期望 %#v", key, got, ok, want)
				}
			}
			for _, key := range item.wantAbsentBody {
				if _, ok := body[key]; ok {
					t.Fatalf("body 不应携带字段 %q，原始 body: %s", key, readRequestBody(t, request))
				}
			}
		})
	}
}

// TestBuildURLBaseNormalization 覆盖 base 归一化的三种形态（OpenAI 系补
// /v1、Anthropic 保留全路径、Gemini /v1beta 剥前缀），全部经 Build 走真实
// 构造路径。
func TestBuildURLBaseNormalization(t *testing.T) {
	bounded := BudgetBounded(DefaultProbeOutputTokens)
	chatBody := func(stream bool) []BodyField {
		return []BodyField{
			{Key: "model", Value: "gpt-probe"},
			{Key: "messages", Value: []map[string]any{{"role": "user", "content": "ok"}}},
			{Key: "stream", Value: stream},
		}
	}
	cases := []struct {
		name    string
		spec    Spec
		wantURL string
	}{
		// OpenAI 系三种 base 形态。
		{name: "openai 无版本根", spec: Spec{BaseURL: "https://up.example.com", EndpointMode: ModeChatJSON, Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://up.example.com/v1/chat/completions"},
		{name: "openai 带 /v1 根", spec: Spec{BaseURL: "https://up.example.com/v1", EndpointMode: ModeChatJSON, Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://up.example.com/v1/chat/completions"},
		{name: "openai 带 /v1/ 尾斜杠", spec: Spec{BaseURL: "https://up.example.com/v1/", EndpointMode: ModeChatJSON, Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://up.example.com/v1/chat/completions"},
		{name: "openai 带路径前缀", spec: Spec{BaseURL: "https://up.example.com/gw", EndpointMode: ModeResponsesJSON, Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://up.example.com/gw/v1/responses"},
		// Anthropic base 原样保留全路径。
		{name: "anthropic 裸根", spec: Spec{BaseURL: "https://a.example.com", EndpointMode: ModeMessagesJSON, Auth: Auth{Strategy: AuthAnthropicAPIKey, Token: testAnthropicToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://a.example.com/v1/messages"},
		{name: "anthropic 带路径", spec: Spec{BaseURL: "https://a.example.com/anthropic", EndpointMode: ModeMessagesJSON, Auth: Auth{Strategy: AuthAnthropicAPIKey, Token: testAnthropicToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://a.example.com/anthropic/v1/messages"},
		{name: "anthropic 带 /v1 根", spec: Spec{BaseURL: "https://a.example.com/anthropic/v1", EndpointMode: ModeMessagesJSON, Auth: Auth{Strategy: AuthAnthropicAPIKey, Token: testAnthropicToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://a.example.com/anthropic/v1/messages"},
		{name: "anthropic 带 /v1/ 尾斜杠", spec: Spec{BaseURL: "https://a.example.com/anthropic/v1/", EndpointMode: ModeMessagesJSON, Auth: Auth{Strategy: AuthAnthropicAPIKey, Token: testAnthropicToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://a.example.com/anthropic/v1/messages"},
		// Gemini base pathname 拼接。
		{name: "gemini 裸根", spec: Spec{BaseURL: "https://g.example.com", EndpointMode: ModeGenerateContentJSON, Model: testGeminiModel, Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://g.example.com/v1beta/models/" + testGeminiModel + ":generateContent"},
		{name: "gemini 带 /v1beta 根", spec: Spec{BaseURL: "https://g.example.com/v1beta", EndpointMode: ModeGenerateContentJSON, Model: testGeminiModel, Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://g.example.com/v1beta/models/" + testGeminiModel + ":generateContent"},
		{name: "gemini 带 /v1beta/ 尾斜杠", spec: Spec{BaseURL: "https://g.example.com/v1beta/", EndpointMode: ModeGenerateContentJSON, Model: testGeminiModel, Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://g.example.com/v1beta/models/" + testGeminiModel + ":generateContent"},
		{name: "gemini 带路径前缀", spec: Spec{BaseURL: "https://g.example.com/gv", EndpointMode: ModeGenerateContentSSE, Model: testGeminiModel, Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://g.example.com/gv/v1beta/models/" + testGeminiModel + ":streamGenerateContent?alt=sse"},
		{name: "gemini models/ 前缀模型名归一", spec: Spec{BaseURL: "https://g.example.com/v1beta", EndpointMode: ModeGenerateContentJSON, Model: "Models/" + testGeminiModel, Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: bounded, BodyFields: chatBody(false)}, wantURL: "https://g.example.com/v1beta/models/" + testGeminiModel + ":generateContent"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			request, err := Build(context.Background(), item.spec)
			if err != nil {
				t.Fatalf("Build 失败: %v", err)
			}
			if got := request.URL.String(); got != item.wantURL {
				t.Fatalf("URL = %q，期望 %q", got, item.wantURL)
			}
		})
	}
}

// TestBuildURLVariantPathRules 覆盖 GLM / gemini-openai 路径规约与 Codex
// 固定上游（适配器包装 M5 才迁移，URL 规约先经本函数直接钉住）。
func TestBuildURLVariantPathRules(t *testing.T) {
	cases := []struct {
		name         string
		baseURL      string
		profileID    string
		adapter      Adapter
		protocol     Protocol
		pathAndQuery string
		wantURL      string
	}{
		{
			name:    "gemini-openai base 自带 /v1beta/openai 时剥请求 /v1",
			baseURL: "https://g.example.com/v1beta/openai", profileID: profileGeminiOpenAIChat,
			protocol: ProtocolOpenAI, pathAndQuery: "/v1/chat/completions",
			wantURL: "https://g.example.com/v1beta/openai/chat/completions",
		},
		{
			name:    "gemini-openai 常规 base 走默认归一化",
			baseURL: "https://g.example.com", profileID: profileGeminiOpenAIChat,
			protocol: ProtocolOpenAI, pathAndQuery: "/v1/chat/completions",
			wantURL: "https://g.example.com/v1/chat/completions",
		},
		{
			name:    "GLM 官方版本根剥请求 /v1",
			baseURL: "https://open.bigmodel.example/api/paas/v4", profileID: profileGLMGeneralOpenAI,
			protocol: ProtocolOpenAI, pathAndQuery: "/v1/chat/completions",
			wantURL: "https://open.bigmodel.example/api/paas/v4/chat/completions",
		},
		{
			name:    "GLM coding 官方版本根剥请求 /v1",
			baseURL: "https://open.bigmodel.example/api/coding/paas/v4", profileID: profileGLMCodingOpenAI,
			protocol: ProtocolOpenAI, pathAndQuery: "/v1/chat/completions",
			wantURL: "https://open.bigmodel.example/api/coding/paas/v4/chat/completions",
		},
		{
			name:    "GLM 代理 /v1 根保留请求 /v1",
			baseURL: "https://proxy.example.com/v1", profileID: profileGLMCodingOpenAI,
			protocol: ProtocolOpenAI, pathAndQuery: "/v1/chat/completions",
			wantURL: "https://proxy.example.com/v1/chat/completions",
		},
		{
			name:    "GLM 空路径 base 保留请求 /v1",
			baseURL: "https://proxy.example.com", profileID: profileGLMGeneralOpenAI,
			protocol: ProtocolOpenAI, pathAndQuery: "/v1/chat/completions",
			wantURL: "https://proxy.example.com/v1/chat/completions",
		},
		{
			// accountprobe 现状：codex 分支先校验并解析账户 base，再整体替换为
			// 固定上游，因此 base 仍必填。
			name:    "Codex 适配器固定上游 + 剥 /v1",
			baseURL: "https://account.example.com/v1", adapter: AdapterOpenAIOAuthCodex,
			protocol: ProtocolOpenAI, pathAndQuery: "/responses",
			wantURL: CodexBaseURL + "/responses",
		},
		{
			name:    "非 GLM 档案不受路径规约影响",
			baseURL: "https://open.bigmodel.example/api/paas/v4", profileID: "",
			protocol: ProtocolOpenAI, pathAndQuery: "/v1/chat/completions",
			wantURL: "https://open.bigmodel.example/api/paas/v4/v1/chat/completions",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			def := adapterDef{}
			if item.adapter != AdapterNone {
				registered, ok := adapters[item.adapter]
				if !ok {
					t.Fatalf("适配器 %s 未登记", item.adapter)
				}
				def = registered
			}
			got, err := buildUpstreamURL(Spec{BaseURL: item.baseURL, ProtocolProfileID: item.profileID}, Endpoint{Protocol: item.protocol}, def, item.pathAndQuery)
			if err != nil {
				t.Fatalf("buildUpstreamURL 失败: %v", err)
			}
			if got != item.wantURL {
				t.Fatalf("URL = %q，期望 %q", got, item.wantURL)
			}
		})
	}

	if _, err := buildUpstreamURL(Spec{}, Endpoint{Protocol: ProtocolOpenAI}, adapterDef{}, "/v1/chat/completions"); err == nil {
		t.Fatal("缺少 base_url 必须返回错误")
	}
}

// TestBuildAuthStrategies 断言四类认证策略的头形态与错误路径；凭据值由
// 调用方传入，包不持有。
func TestBuildAuthStrategies(t *testing.T) {
	build := func(auth Auth) *http.Request {
		t.Helper()
		request, err := Build(context.Background(), Spec{
			BaseURL: testOpenAIBase, EndpointMode: ModeChatJSON, Auth: auth,
			Budget: BudgetNone,
			BodyFields: []BodyField{
				{Key: "model", Value: "gpt-probe"},
				{Key: "messages", Value: []map[string]any{{"role": "user", "content": "ok"}}},
				{Key: "stream", Value: false},
			},
		})
		if err != nil {
			t.Fatalf("Build 失败: %v", err)
		}
		return request
	}
	cases := []struct {
		name   string
		auth   Auth
		header string
		value  string
		extra  map[string]string
	}{
		{name: "bearer", auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, header: "Authorization", value: "Bearer " + testBearerToken},
		{name: "anthropic_api_key", auth: Auth{Strategy: AuthAnthropicAPIKey, Token: testAnthropicToken}, header: "X-Api-Key", value: testAnthropicToken},
		{name: "gemini_api_key", auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, header: "X-Goog-Api-Key", value: testGeminiToken},
		{
			name: "oauth Bearer + 附加头映射", auth: Auth{
				Strategy: AuthOAuth, Token: testBearerToken,
				ExtraHeaders: map[string]string{"chatgpt-account-id": "acct-1", "openai-beta": "responses=experimental"},
			},
			header: "Authorization", value: "Bearer " + testBearerToken,
			extra: map[string]string{"Chatgpt-Account-Id": "acct-1", "Openai-Beta": "responses=experimental"},
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			request := build(item.auth)
			if got := request.Header.Get(item.header); got != item.value {
				t.Fatalf("%s = %q，期望 %q", item.header, got, item.value)
			}
			for name, want := range item.extra {
				if got := request.Header.Get(name); got != want {
					t.Fatalf("附加头 %s = %q，期望 %q", name, got, want)
				}
			}
		})
	}

	t.Run("空凭据显式失败", func(t *testing.T) {
		request, err := Build(context.Background(), Spec{BaseURL: testOpenAIBase, EndpointMode: ModeChatJSON, Auth: Auth{Strategy: AuthBearer}, Budget: BudgetNone, BodyFields: []BodyField{{Key: "model", Value: "gpt-probe"}}})
		if err == nil {
			t.Fatalf("空凭据必须返回错误，实际得到请求 %+v", request)
		}
	})
	t.Run("未知认证策略显式失败", func(t *testing.T) {
		_, err := Build(context.Background(), Spec{BaseURL: testOpenAIBase, EndpointMode: ModeChatJSON, Auth: Auth{Strategy: "hmac", Token: testBearerToken}, Budget: BudgetNone, BodyFields: []BodyField{{Key: "model", Value: "gpt-probe"}}})
		if err == nil || !strings.Contains(err.Error(), "hmac") {
			t.Fatalf("未知认证策略必须返回明确错误，实际: %v", err)
		}
	})
}

// TestBuildBudgetGuards 断言预算策略与端点契约的冲突全部显式失败。
func TestBuildBudgetGuards(t *testing.T) {
	responsesBody := []BodyField{{Key: "model", Value: "gpt-probe"}, {Key: "input", Value: "ok"}, {Key: "stream", Value: false}}
	imagesBody := []BodyField{{Key: "model", Value: "image-probe"}, {Key: "prompt", Value: "ok"}}
	messagesBody := []BodyField{{Key: "model", Value: "claude-probe"}, {Key: "messages", Value: []map[string]any{{"role": "user", "content": "ok"}}}, {Key: "stream", Value: false}}
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{
			name: "未选择预算策略",
			spec: Spec{BaseURL: testOpenAIBase, EndpointMode: ModeResponsesJSON, Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, BodyFields: responsesBody},
			want: "缺少预算策略",
		},
		{
			name: "预算上限非法",
			spec: Spec{BaseURL: testOpenAIBase, EndpointMode: ModeResponsesJSON, Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetBounded(0), BodyFields: responsesBody},
			want: "上限非法",
		},
		{
			name: "无预算字段端点注入有界预算",
			spec: Spec{BaseURL: testOpenAIBase, EndpointMode: ModeImagesJSON, Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetBounded(1024), BodyFields: imagesBody},
			want: "不得注入",
		},
		{
			name: "Anthropic 必填预算字段不接受 none",
			spec: Spec{BaseURL: testAnthropicBase, EndpointMode: ModeMessagesJSON, Auth: Auth{Strategy: AuthAnthropicAPIKey, Token: testAnthropicToken}, Budget: BudgetNone, BodyFields: messagesBody},
			want: "协议必填",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			request, err := Build(context.Background(), item.spec)
			if err == nil {
				t.Fatalf("必须返回错误，实际得到请求 %+v", request)
			}
			if !strings.Contains(err.Error(), item.want) {
				t.Fatalf("错误 %q 应包含 %q", err, item.want)
			}
		})
	}

	t.Run("Responses 接受 none 策略（Codex 类语义）", func(t *testing.T) {
		request, err := Build(context.Background(), Spec{
			BaseURL: testOpenAIBase, EndpointMode: ModeResponsesJSON,
			Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone,
			BodyFields: responsesBody,
		})
		if err != nil {
			t.Fatalf("Responses + BudgetNone 不应报错: %v", err)
		}
		body := decodeJSONBody(t, request)
		if _, ok := body["max_output_tokens"]; ok {
			t.Fatal("BudgetNone 时 max_output_tokens 必须省略，不得伪造")
		}
	})
}

// TestBuildUnknownModeAndAdapter 断言未知形态与未迁移适配器全部明确失败，
// 不静默兜底（§4.2）。
func TestBuildUnknownModeAndAdapter(t *testing.T) {
	body := []BodyField{{Key: "model", Value: "gpt-probe"}}
	if _, err := Build(context.Background(), Spec{BaseURL: testOpenAIBase, EndpointMode: "robot_json", Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone, BodyFields: body}); err == nil || !strings.Contains(err.Error(), "robot_json") {
		t.Fatalf("未知 endpointMode 必须明确报错，实际: %v", err)
	}
	if _, err := Build(context.Background(), Spec{BaseURL: testOpenAIBase, EndpointMode: ModeChatJSON, Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone, BodyFields: body, Adapter: Adapter("unknown_adapter")}); err == nil || !strings.Contains(err.Error(), "unknown_adapter") {
		t.Fatalf("未知适配器必须明确报错，实际: %v", err)
	}
	for _, adapter := range []Adapter{AdapterOpenAIOAuthCodex, AdapterGeminiCodeAssist} {
		_, err := Build(context.Background(), Spec{BaseURL: testOpenAIBase, EndpointMode: ModeResponsesJSON, Auth: Auth{Strategy: AuthOAuth, Token: testBearerToken}, Budget: BudgetNone, BodyFields: body, Adapter: adapter})
		if err == nil || !strings.Contains(err.Error(), "M5") {
			t.Fatalf("适配器 %s 的包装尚未迁移，必须报 M5 错误，实际: %v", adapter, err)
		}
	}
	if _, err := Build(context.Background(), Spec{BaseURL: testOpenAIBase, EndpointMode: ModeResponsesJSON, Protocol: ProtocolGemini, Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone, BodyFields: body}); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("协议与端点不一致必须明确报错，实际: %v", err)
	}
}

// TestBuildMultipartTranscriptionBody 断言 multipart 边界编码：文本字段与
// 二进制 file 段（§1 转录形态）。
func TestBuildMultipartTranscriptionBody(t *testing.T) {
	file := MultipartFile{FieldName: "file", Filename: "probe.wav", ContentType: "audio/wav", Data: []byte("RIFF....WAVEfmt")}
	request, err := Build(context.Background(), Spec{
		BaseURL: testOpenAIBase, EndpointMode: ModeAudioTranscriptionJSON,
		Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone,
		Multipart: &MultipartContent{
			Fields: []MultipartField{{Name: "model", Value: "stt-probe"}},
			File:   file,
		},
	})
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	contentType := request.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("解析 Content-Type %q 失败: %v", contentType, err)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("media type = %q，期望 multipart/form-data", mediaType)
	}
	boundary := params["boundary"]
	if boundary == "" {
		t.Fatalf("Content-Type 缺少 boundary: %q", contentType)
	}
	if !strings.Contains(contentType, "boundary="+boundary) {
		t.Fatalf("Content-Type 应包含 boundary 参数: %q", contentType)
	}
	verifyMultipartBody(t, request, map[string]string{"model": "stt-probe"}, file)

	t.Run("缺失 file 段显式失败", func(t *testing.T) {
		_, err := Build(context.Background(), Spec{
			BaseURL: testOpenAIBase, EndpointMode: ModeAudioTranscriptionJSON,
			Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone,
			Multipart: &MultipartContent{Fields: []MultipartField{{Name: "model", Value: "stt-probe"}}},
		})
		if err == nil || !strings.Contains(err.Error(), "file 段") {
			t.Fatalf("multipart 缺 file 段必须明确报错，实际: %v", err)
		}
	})
}

// TestGeminiGenerationConfigMerge 断言调用方自带 generationConfig 时信封只
// 注入 maxOutputTokens 键（音频 responseModalities 场景）。
func TestGeminiGenerationConfigMerge(t *testing.T) {
	request, err := Build(context.Background(), Spec{
		BaseURL: testGeminiBase, EndpointMode: ModeGenerateContentJSON, Model: testGeminiModel,
		Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: BudgetBounded(DefaultProbeOutputTokens),
		BodyFields: []BodyField{
			{Key: "contents", Value: []map[string]any{{"parts": []map[string]any{{"text": "ok"}}}}},
			{Key: "generationConfig", Value: map[string]any{"responseModalities": []string{"AUDIO"}}},
		},
	})
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	body := decodeJSONBody(t, request)
	config, ok := body["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig 缺失或非对象: %#v", body["generationConfig"])
	}
	if got := config["maxOutputTokens"]; got != float64(DefaultProbeOutputTokens) {
		t.Fatalf("maxOutputTokens = %#v，期望 %d", got, DefaultProbeOutputTokens)
	}
	if got := config["responseModalities"]; !reflect.DeepEqual(got, []any{"AUDIO"}) {
		t.Fatalf("调用方自带键不得丢失: %#v", got)
	}

	t.Run("generationConfig 非对象显式失败", func(t *testing.T) {
		_, err := Build(context.Background(), Spec{
			BaseURL: testGeminiBase, EndpointMode: ModeGenerateContentJSON, Model: testGeminiModel,
			Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: BudgetBounded(DefaultProbeOutputTokens),
			BodyFields: []BodyField{{Key: "generationConfig", Value: "bad"}},
		})
		if err == nil || !strings.Contains(err.Error(), "JSON 对象") {
			t.Fatalf("generationConfig 非对象必须明确报错，实际: %v", err)
		}
	})
}

// TestResponsesBudgetIncidentRegression 是驱动事故的回归 Mock（设计文档
// §2/§7.2）：Mock 上游对输出预算 <100 的 Responses 请求返回与事故同类 400，
// 对 1024 与字段省略（Codex 类无预算语义）返回稳定成功。断言
// BudgetBounded(1024) 携带 max_output_tokens=1024（≥100）、BudgetNone 时
// 字段省略、迁移前的预算 16 被同类 400 拒绝。Mock 可回放、结果稳定，不
// 依赖真实供应商或其内部转换实现。
func TestResponsesBudgetIncidentRegression(t *testing.T) {
	var (
		seenPath  string
		sawField  bool
		seenValue float64
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取 mock 上游请求体失败: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("mock 上游收到非 JSON body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		sawField = false
		seenValue = 0
		if value, ok := body["max_output_tokens"]; ok {
			number, isNumber := value.(float64)
			if !isNumber {
				t.Errorf("max_output_tokens 必须是数字，实际: %#v", value)
			}
			sawField = true
			seenValue = number
		}
		// 事故语义：输出预算低于上游最低值 100 → 400。
		if sawField && seenValue < 100 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"max_tokens=16 is below the minimum the model provider accepts (100); raise it to at least 100"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_mock","status":"completed","output":[]}`))
	}))
	defer srv.Close()

	build := func(t *testing.T, budget BudgetPolicy) *http.Request {
		t.Helper()
		request, err := Build(context.Background(), Spec{
			BaseURL:      srv.URL,
			EndpointMode: ModeResponsesJSON,
			Auth:         Auth{Strategy: AuthBearer, Token: testBearerToken},
			Budget:       budget,
			BodyFields: []BodyField{
				{Key: "model", Value: "gpt-probe"},
				{Key: "input", Value: "接口连通性自动测试"},
				{Key: "stream", Value: false},
			},
		})
		if err != nil {
			t.Fatalf("Build 失败: %v", err)
		}
		return request
	}
	do := func(t *testing.T, request *http.Request) int {
		t.Helper()
		response, err := srv.Client().Do(request)
		if err != nil {
			t.Fatalf("发送 mock 上游请求失败: %v", err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}

	t.Run("BudgetBounded(1024) 携带 max_output_tokens=1024 且被接受", func(t *testing.T) {
		if code := do(t, build(t, BudgetBounded(DefaultProbeOutputTokens))); code != http.StatusOK {
			t.Fatalf("1024 预算请求应被接受，实际状态码 %d", code)
		}
		if seenPath != "/v1/responses" {
			t.Fatalf("mock 上游收到的路径 = %q，期望 /v1/responses", seenPath)
		}
		if !sawField || seenValue != float64(DefaultProbeOutputTokens) {
			t.Fatalf("上游应收到 max_output_tokens=1024，实际 sawField=%v value=%v", sawField, seenValue)
		}
	})

	t.Run("BudgetNone 时字段省略且被接受", func(t *testing.T) {
		if code := do(t, build(t, BudgetNone)); code != http.StatusOK {
			t.Fatalf("无预算字段请求应被接受，实际状态码 %d", code)
		}
		if sawField {
			t.Fatalf("BudgetNone 不得携带 max_output_tokens，上游收到 %v", seenValue)
		}
	})

	t.Run("迁移前预算 16 复现同类 400", func(t *testing.T) {
		if code := do(t, build(t, BudgetBounded(16))); code != http.StatusBadRequest {
			t.Fatalf("预算 16 应复现事故同类 400，实际状态码 %d", code)
		}
	})
}

// TestBuildBodyMismatch 断言 body 内容类型与端点编码不匹配时显式失败。
func TestBuildBodyMismatch(t *testing.T) {
	body := []BodyField{{Key: "model", Value: "gpt-probe"}}
	if _, err := Build(context.Background(), Spec{
		BaseURL: testOpenAIBase, EndpointMode: ModeChatJSON,
		Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone,
		BodyFields: body,
		Multipart:  &MultipartContent{File: MultipartFile{FieldName: "file", Filename: "x", Data: []byte("x")}},
	}); err == nil || !strings.Contains(err.Error(), "不接受 Multipart") {
		t.Fatalf("JSON 端点拒绝 Multipart 内容，实际: %v", err)
	}
	if _, err := Build(context.Background(), Spec{
		BaseURL: testOpenAIBase, EndpointMode: ModeAudioTranscriptionJSON,
		Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone,
		BodyFields: body,
	}); err == nil || !strings.Contains(err.Error(), "multipart") {
		t.Fatalf("multipart 端点拒绝 BodyFields，实际: %v", err)
	}
	if _, err := Build(context.Background(), Spec{
		BaseURL: testOpenAIBase, EndpointMode: ModeAudioTranscriptionJSON,
		Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone,
	}); err == nil || !strings.Contains(err.Error(), "必须提供 Multipart") {
		t.Fatalf("multipart 端点缺少内容必须明确报错，实际: %v", err)
	}
	if _, err := Build(context.Background(), Spec{
		BaseURL: testGeminiBase, EndpointMode: ModeGenerateContentJSON, Model: " ",
		Auth: Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken}, Budget: BudgetBounded(DefaultProbeOutputTokens),
		BodyFields: []BodyField{{Key: "contents", Value: "ok"}},
	}); err == nil || !strings.Contains(err.Error(), "Spec.Model") {
		t.Fatalf("gemini 路径模板缺 model 必须明确报错，实际: %v", err)
	}
}

// verifyMultipartBody 解析请求的 multipart body，断言文本字段与 file 段的
// name/filename/Content-Type/数据。
func verifyMultipartBody(t *testing.T, request *http.Request, wantFields map[string]string, wantFile MultipartFile) {
	t.Helper()
	_, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("解析 multipart Content-Type 失败: %v", err)
	}
	reader := multipart.NewReader(bytes.NewReader(readRequestBody(t, request)), params["boundary"])
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("读取 multipart 段失败: %v", err)
		}
		formName := part.FormName()
		if formName == wantFile.FieldName {
			data, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("读取 file 段数据失败: %v", err)
			}
			if !bytes.Equal(data, wantFile.Data) {
				t.Fatalf("file 段数据不一致: got %q want %q", data, wantFile.Data)
			}
			if got := part.FileName(); got != wantFile.Filename {
				t.Fatalf("file 段 filename = %q，期望 %q", got, wantFile.Filename)
			}
			if got := part.Header.Get("Content-Type"); got != wantFile.ContentType {
				t.Fatalf("file 段 Content-Type = %q，期望 %q", got, wantFile.ContentType)
			}
			seen["file"] = true
			continue
		}
		value, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("读取字段 %s 失败: %v", formName, err)
		}
		want, ok := wantFields[formName]
		if !ok {
			t.Fatalf("出现未预期的 multipart 字段 %q", formName)
		}
		if string(value) != want {
			t.Fatalf("字段 %s = %q，期望 %q", formName, value, want)
		}
		seen[formName] = true
	}
	for name := range wantFields {
		if !seen[name] {
			t.Fatalf("multipart 缺少字段 %q", name)
		}
	}
	if !seen["file"] {
		t.Fatalf("multipart 缺少 file 段（边界编码必须含 file 段）")
	}
}

func readRequestBody(t *testing.T, request *http.Request) []byte {
	t.Helper()
	if request.GetBody == nil {
		t.Fatal("请求缺少 GetBody（应基于 bytes.Reader 构造）")
	}
	body, err := request.GetBody()
	if err != nil {
		t.Fatalf("读取请求体失败: %v", err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("读取请求体失败: %v", err)
	}
	return raw
}

func decodeJSONBody(t *testing.T, request *http.Request) map[string]any {
	t.Helper()
	raw := readRequestBody(t, request)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("解码 body 失败: %v\n原始 body: %s", err, raw)
	}
	return body
}
