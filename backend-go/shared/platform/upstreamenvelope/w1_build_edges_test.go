package upstreamenvelope

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestBuildEdgeContracts(t *testing.T) {
	validChat := func() Spec {
		return Spec{
			BaseURL:      testOpenAIBase,
			EndpointMode: ModeChatJSON,
			Auth:         Auth{Strategy: AuthBearer, Token: testBearerToken},
			Budget:       BudgetNone,
			BodyFields: []BodyField{
				{Key: "model", Value: "gpt-probe"},
				{Key: "messages", Value: []map[string]string{{"role": "user", "content": "ok"}}},
			},
		}
	}

	t.Run("AuthNone 生成无认证头请求", func(t *testing.T) {
		spec := validChat()
		spec.Auth = Auth{Strategy: AuthNone}
		request, err := Build(context.Background(), spec)
		if err != nil {
			t.Fatalf("Build(AuthNone) err=%v", err)
		}
		if got := request.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q，AuthNone 应保持为空", got)
		}
		if got := request.Header.Get("X-Api-Key"); got != "" {
			t.Fatalf("X-Api-Key = %q，AuthNone 应保持为空", got)
		}
	})

	t.Run("JSON 端点拒绝空 BodyFields", func(t *testing.T) {
		spec := validChat()
		spec.BodyFields = nil
		assertBuildError(t, context.Background(), spec, "需要 BodyFields")
	})

	t.Run("不可序列化字段返回原始 marshal 错误", func(t *testing.T) {
		spec := validChat()
		spec.BodyFields = []BodyField{{Key: "unsupported", Value: make(chan int)}}
		assertBuildError(t, context.Background(), spec, "编码 body 字段 \"unsupported\" 失败")
	})

	t.Run("generationConfig 不可序列化时显式失败", func(t *testing.T) {
		spec := Spec{
			BaseURL:      testGeminiBase,
			EndpointMode: ModeGenerateContentJSON,
			Model:        testGeminiModel,
			Auth:         Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken},
			Budget:       BudgetBounded(DefaultProbeOutputTokens),
			BodyFields:   []BodyField{{Key: "generationConfig", Value: make(chan int)}},
		}
		assertBuildError(t, context.Background(), spec, "编码 generationConfig 失败")
	})

	t.Run("base URL 解析失败保留错误", func(t *testing.T) {
		spec := validChat()
		spec.BaseURL = "https://[::1"
		assertBuildError(t, context.Background(), spec, "base_url 无效")
	})

	t.Run("nil context 返回构造请求错误", func(t *testing.T) {
		assertBuildError(t, nil, validChat(), "构造请求失败")
	})
}

func TestBuildMultipartValidationEdges(t *testing.T) {
	base := Spec{
		BaseURL:      testOpenAIBase,
		EndpointMode: ModeAudioTranscriptionJSON,
		Auth:         Auth{Strategy: AuthBearer, Token: testBearerToken},
		Budget:       BudgetNone,
	}
	cases := []struct {
		name      string
		content   MultipartContent
		wantError string
	}{
		{
			name:      "空文件内容",
			content:   MultipartContent{File: MultipartFile{FieldName: "file", Filename: "probe.wav"}},
			wantError: "必须携带非空 file 段",
		},
		{
			name:      "空文件字段名",
			content:   MultipartContent{File: MultipartFile{FieldName: "  ", Filename: "probe.wav", Data: []byte("wav")}},
			wantError: "缺少字段名",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			spec := base
			spec.Multipart = &item.content
			assertBuildError(t, context.Background(), spec, item.wantError)
		})
	}

	t.Run("特殊字符字段名和文件名可往返", func(t *testing.T) {
		file := MultipartFile{
			FieldName: "audio input",
			Filename:  "probe (draft).wav",
			Data:      []byte("wav"),
		}
		spec := base
		spec.Multipart = &MultipartContent{
			Fields: []MultipartField{{Name: "model name", Value: "stt-probe"}},
			File:   file,
		}
		request, err := Build(context.Background(), spec)
		if err != nil {
			t.Fatalf("Build err=%v", err)
		}
		verifyMultipartBody(t, request, map[string]string{"model name": "stt-probe"}, file)
	})
}

func assertBuildError(t *testing.T, ctx context.Context, spec Spec, want string) {
	t.Helper()
	request, err := Build(ctx, spec)
	if err == nil {
		t.Fatalf("Build 应失败，实际请求=%+v", request)
	}
	if request != nil {
		t.Fatalf("Build 失败时请求应为空，实际=%+v", request)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("错误 %q 应包含 %q", err, want)
	}
}

func TestPublicURLHelpers(t *testing.T) {
	t.Run("UpstreamURL 生成 OpenAI 路径并保留 query", func(t *testing.T) {
		got, err := UpstreamURL(testOpenAIBase, ModeChatJSON, "", AdapterNone, "/v1/chat/completions?probe=1")
		if err != nil {
			t.Fatalf("UpstreamURL err=%v", err)
		}
		if want := testOpenAIBase + "/v1/chat/completions?probe=1"; got != want {
			t.Fatalf("UpstreamURL = %q，期望 %q", got, want)
		}
	})

	t.Run("UpstreamURL 规范化不带前导斜杠和根路径", func(t *testing.T) {
		cases := []struct {
			name string
			path string
			want string
		}{
			{name: "相对形态", path: "v1/chat/completions", want: testOpenAIBase + "/v1/chat/completions"},
			{name: "仅版本根", path: "/v1", want: testOpenAIBase + "/v1"},
			{name: "根路径", path: "/", want: testOpenAIBase + "/v1"},
		}
		for _, item := range cases {
			t.Run(item.name, func(t *testing.T) {
				got, err := UpstreamURL(testOpenAIBase, ModeChatJSON, "", AdapterNone, item.path)
				if err != nil {
					t.Fatalf("UpstreamURL err=%v", err)
				}
				if got != item.want {
					t.Fatalf("UpstreamURL = %q，期望 %q", got, item.want)
				}
			})
		}
	})

	t.Run("UpstreamURL 保留 Gemini 路径和 query", func(t *testing.T) {
		got, err := UpstreamURL(testGeminiBase, ModeGenerateContentJSON, "", AdapterNone, "/v1beta/models/gemini-pro:generateContent?key=probe")
		if err != nil {
			t.Fatalf("UpstreamURL err=%v", err)
		}
		if want := testGeminiBase + "/v1beta/models/gemini-pro:generateContent?key=probe"; got != want {
			t.Fatalf("UpstreamURL = %q，期望 %q", got, want)
		}
	})

	t.Run("UpstreamURL Codex adapter 使用固定上游", func(t *testing.T) {
		got, err := UpstreamURL("https://ignored.example.com", ModeResponsesJSON, "", AdapterOpenAIOAuthCodex, "/v1/responses")
		if err != nil {
			t.Fatalf("UpstreamURL err=%v", err)
		}
		if want := CodexBaseURL + "/responses"; got != want {
			t.Fatalf("UpstreamURL = %q，期望 %q", got, want)
		}
	})

	t.Run("Gemini v1beta base 保留无版本前缀的 suffix", func(t *testing.T) {
		got, err := UpstreamURL(testGeminiBase+"/v1beta", ModeGenerateContentJSON, "", AdapterNone, "/models/gemini-pro:generateContent")
		if err != nil {
			t.Fatalf("UpstreamURL err=%v", err)
		}
		if want := testGeminiBase + "/v1beta/models/gemini-pro:generateContent"; got != want {
			t.Fatalf("UpstreamURL = %q，期望 %q", got, want)
		}
	})

	t.Run("Gemini UpstreamURL 空 path 和仅 query 保留 base path", func(t *testing.T) {
		base := testGeminiBase + "/v1beta"
		cases := []struct {
			name string
			path string
			want string
		}{
			{name: "空 suffix", path: "", want: base},
			{name: "仅 query", path: "?alt=sse", want: base + "?alt=sse"},
		}
		for _, item := range cases {
			t.Run(item.name, func(t *testing.T) {
				got, err := UpstreamURL(base, ModeGenerateContentJSON, "", AdapterNone, item.path)
				if err != nil {
					t.Fatalf("UpstreamURL err=%v", err)
				}
				if got != item.want {
					t.Fatalf("UpstreamURL = %q，期望 %q", got, item.want)
				}
			})
		}
	})

	t.Run("UpstreamURL 未知 mode 明确失败", func(t *testing.T) {
		if _, err := UpstreamURL(testOpenAIBase, "robot_json", "", AdapterNone, "/v1/chat/completions"); err == nil || !strings.Contains(err.Error(), "robot_json") {
			t.Fatalf("未知 mode 应显式报错，实际: %v", err)
		}
	})

	t.Run("UpstreamURL 未知 adapter 明确失败", func(t *testing.T) {
		if _, err := UpstreamURL(testOpenAIBase, ModeChatJSON, "", Adapter("unknown_adapter"), "/v1/chat/completions"); err == nil || !strings.Contains(err.Error(), "unknown_adapter") {
			t.Fatalf("未知 adapter 应显式报错，实际: %v", err)
		}
	})

	t.Run("AcceptHeader 按 encoding 和 OAuth 策略选择", func(t *testing.T) {
		cases := []struct {
			name     string
			mode     EndpointMode
			strategy AuthStrategy
			want     string
		}{
			{name: "JSON", mode: ModeChatJSON, strategy: AuthBearer, want: "application/json"},
			{name: "SSE OAuth", mode: ModeChatSSE, strategy: AuthOAuth, want: "text/event-stream"},
			{name: "SSE API key", mode: ModeChatSSE, strategy: AuthBearer, want: "application/json, text/event-stream"},
		}
		for _, item := range cases {
			t.Run(item.name, func(t *testing.T) {
				got, err := AcceptHeader(item.mode, item.strategy)
				if err != nil {
					t.Fatalf("AcceptHeader err=%v", err)
				}
				if got != item.want {
					t.Fatalf("AcceptHeader = %q，期望 %q", got, item.want)
				}
			})
		}
	})

	t.Run("AcceptHeader 未知 mode 明确失败", func(t *testing.T) {
		if _, err := AcceptHeader("robot_json", AuthBearer); err == nil || !strings.Contains(err.Error(), "robot_json") {
			t.Fatalf("未知 mode 应显式报错，实际: %v", err)
		}
	})
}

func TestEntriesReturnsIndependentCatalogCopy(t *testing.T) {
	want := Entries()
	returned := Entries()
	if len(returned) == 0 {
		t.Fatal("Entries 不应为空")
	}
	returned[0].Mode = "mutated"
	for _, entry := range returned {
		for key := range entry.Headers {
			entry.Headers[key] = "mutated"
		}
	}
	if got := Entries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("修改 Entries 返回值污染了 catalog：\n got  %+v\n want %+v", got, want)
	}
}

func TestBuildGeminiModelPathEdges(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		wantURL string
	}{
		{
			name:    "Models 前缀归一且 Unicode 和保留字符单次转义",
			model:   "Models/中 文/alpha?x",
			wantURL: testGeminiBase + "/v1beta/models/%E4%B8%AD%20%E6%96%87%2Falpha%3Fx:generateContent",
		},
		{
			name:    "仅含 Models 前缀时回落默认模型",
			model:   "Models/",
			wantURL: testGeminiBase + "/v1beta/models/gemini-pro:generateContent",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			request, err := Build(context.Background(), Spec{
				BaseURL:      testGeminiBase,
				EndpointMode: ModeGenerateContentJSON,
				Model:        item.model,
				Auth:         Auth{Strategy: AuthGeminiAPIKey, Token: testGeminiToken},
				Budget:       BudgetBounded(DefaultProbeOutputTokens),
				BodyFields:   []BodyField{{Key: "contents", Value: []map[string]string{{"role": "user", "text": "ok"}}}},
			})
			if err != nil {
				t.Fatalf("Build err=%v", err)
			}
			if got := request.URL.String(); got != item.wantURL {
				t.Fatalf("URL = %q，期望 %q", got, item.wantURL)
			}
		})
	}
}

func TestBuildAdapterMalformedBodyEdges(t *testing.T) {
	identity := CodexIdentity{
		InstallationID: "install-probe",
		SessionID:      "session-probe",
		ThreadID:       "thread-probe",
		TurnID:         "turn-probe",
		WindowID:       "window-probe",
	}
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{
			name: "Codex Responses model 必须为非空字符串",
			spec: Spec{
				BaseURL: testOpenAIBase, EndpointMode: ModeResponsesJSON,
				Adapter: AdapterOpenAIOAuthCodex, CodexIdentity: identity,
				Auth: Auth{Strategy: AuthOAuth, Token: testBearerToken}, Budget: BudgetNone,
				BodyFields: []BodyField{{Key: "model", Value: 42}, {Key: "input", Value: "ok"}},
			},
			want: "model 缺失或非法",
		},
		{
			name: "Gemini Code Assist 拒绝非 generateContent 端点",
			spec: Spec{
				BaseURL: testGeminiBase, EndpointMode: ModeInteractionsJSON,
				Adapter: AdapterGeminiCodeAssist, Project: "project-probe",
				Auth: Auth{Strategy: AuthOAuth, Token: testBearerToken}, Budget: BudgetNone,
				BodyFields: []BodyField{{Key: "input", Value: "ok"}},
			},
			want: "仅支持 generate_content_json/generate_content_sse",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			assertBuildError(t, context.Background(), item.spec, item.want)
		})
	}

	t.Run("Codex compatibility 覆盖调用方错误类型的专属字段", func(t *testing.T) {
		request, err := Build(context.Background(), Spec{
			BaseURL: testOpenAIBase, EndpointMode: ModeResponsesJSON,
			Adapter: AdapterOpenAICodexCompatibility, CodexIdentity: identity,
			Auth: Auth{Strategy: AuthBearer, Token: testBearerToken}, Budget: BudgetNone,
			BodyFields: []BodyField{
				{Key: "model", Value: "gpt-probe"},
				{Key: "input", Value: "ok"},
				{Key: "reasoning", Value: "caller-value"},
				{Key: "client_metadata", Value: "caller-value"},
			},
		})
		if err != nil {
			t.Fatalf("Build err=%v", err)
		}
		body := decodeJSONBody(t, request)
		if got, ok := body["reasoning"].(map[string]any); !ok || got["context"] != "all_turns" {
			t.Fatalf("reasoning = %#v，兼容画像应写入对象结构", body["reasoning"])
		}
		if got, ok := body["client_metadata"].(map[string]any); !ok || got["session_id"] != identity.SessionID {
			t.Fatalf("client_metadata = %#v，兼容画像应从 CodexIdentity 生成对象", body["client_metadata"])
		}
	})
}

func TestAdapterWrapHookInputValidation(t *testing.T) {
	identity := CodexIdentity{
		InstallationID: "install-probe",
		SessionID:      "session-probe",
		ThreadID:       "thread-probe",
		TurnID:         "turn-probe",
		WindowID:       "window-probe",
	}
	cases := []struct {
		name string
		call func() (string, []byte, error)
		want string
	}{
		{
			name: "Codex OAuth 坏 JSON",
			call: func() (string, []byte, error) {
				return wrapCodexResponses(Spec{EndpointMode: ModeResponsesJSON, Budget: BudgetNone}, "/responses", []byte("{"))
			},
			want: "Codex 请求 body 无效",
		},
		{
			name: "Codex OAuth JSON null",
			call: func() (string, []byte, error) {
				return wrapCodexResponses(Spec{EndpointMode: ModeResponsesJSON, Budget: BudgetNone}, "/responses", []byte("null"))
			},
			want: "Codex 请求 body 无效",
		},
		{
			name: "Codex compatibility 坏 JSON",
			call: func() (string, []byte, error) {
				return wrapCodexCompatibilityProfile(Spec{EndpointMode: ModeResponsesJSON, CodexIdentity: identity}, "/responses", []byte("{"))
			},
			want: "解码 Responses body 失败",
		},
		{
			name: "Codex compatibility JSON null",
			call: func() (string, []byte, error) {
				return wrapCodexCompatibilityProfile(Spec{EndpointMode: ModeResponsesJSON, CodexIdentity: identity}, "/responses", []byte("null"))
			},
			want: "Responses body 无效",
		},
		{
			name: "Gemini Code Assist 坏 JSON",
			call: func() (string, []byte, error) {
				return wrapGeminiCodeAssist(Spec{EndpointMode: ModeGenerateContentJSON, Model: testGeminiModel, Project: "project-probe"}, "/v1beta/models/gemini-pro:generateContent", []byte("{"))
			},
			want: "解码 generateContent body 失败",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			path, body, err := item.call()
			if err == nil {
				t.Fatalf("hook 应失败，path=%q body=%q", path, body)
			}
			if !strings.Contains(err.Error(), item.want) {
				t.Fatalf("错误 %q 应包含 %q", err, item.want)
			}
			if path != "" || body != nil {
				t.Fatalf("失败时不得返回 partial output，path=%q body=%q", path, body)
			}
		})
	}

	t.Run("Codex compatibility hook 拒绝零值身份", func(t *testing.T) {
		path, body, err := wrapCodexCompatibilityProfile(
			Spec{EndpointMode: ModeResponsesJSON},
			"/responses",
			[]byte(`{"model":"gpt-probe","input":"ok"}`),
		)
		if err == nil || !strings.Contains(err.Error(), "缺少 Spec.CodexIdentity") {
			t.Fatalf("零值身份应明确失败，实际 err=%v", err)
		}
		if path != "" || body != nil {
			t.Fatalf("失败时不得返回 partial output，path=%q body=%q", path, body)
		}
	})
}
