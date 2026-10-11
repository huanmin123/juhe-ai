package upstreamenvelope

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// TestBuildCodexAdapterWrapGolden 是 AdapterOpenAIOAuthCodex 包装钩子的
// golden 契约（§6 M5 验证：path/header/body golden，确认不会携带被归一化
// 删除的字段）。期望形状逐字段取证自 modelcheckprobe
// normalizeOpenAIOAuthCodexRequest 的 body 改写；请求头增强属调用方职责，
// 这里只断言 URL（固定上游 + /responses）与 body 形状。
func TestBuildCodexAdapterWrapGolden(t *testing.T) {
	// 归一化删除的字段清单与实现同源（adapterwrap.go codexDeletedFields），
	// 外加 gpt-5.6 之外模型不携带的 reasoning / parallel_tool_calls。
	codexAbsent := append(append([]string(nil), codexDeletedFields...), "reasoning", "parallel_tool_calls")
	codexSpec := func(fields []BodyField, mode EndpointMode, budget BudgetPolicy) Spec {
		return Spec{
			BaseURL:      "https://account.example.com/v1",
			EndpointMode: mode,
			Adapter:      AdapterOpenAIOAuthCodex,
			Auth:         Auth{Strategy: AuthOAuth, Token: testBearerToken},
			Budget:       budget,
			BodyFields:   fields,
		}
	}
	cases := []struct {
		name           string
		spec           Spec
		wantURL        string
		wantBody       map[string]any
		wantAbsentBody []string
	}{
		{
			name: "responses_json 字符串 input 归一化为 user 消息数组",
			spec: codexSpec([]BodyField{
				{Key: "model", Value: "gpt-5.4"},
				{Key: "input", Value: "接口连通性自动测试"},
				{Key: "stream", Value: false},
			}, ModeResponsesJSON, BudgetNone),
			wantURL: CodexBaseURL + "/responses",
			wantBody: map[string]any{
				"model":        "gpt-5.4",
				"instructions": "",
				"store":        false,
				// 调用方传 stream=false 也被归一化为 true（取证现状：Codex 探针恒流式）。
				"stream": true,
				"input": []any{map[string]any{
					"type": "message",
					"role": "user",
					"content": []any{map[string]any{
						"type": "input_text",
						"text": "接口连通性自动测试",
					}},
				}},
			},
			wantAbsentBody: codexAbsent,
		},
		{
			name: "responses_sse 结构化 input 与 instructions 原样保留",
			spec: codexSpec([]BodyField{
				{Key: "model", Value: "gpt-5.4"},
				{Key: "input", Value: []any{map[string]any{
					"type": "message",
					"role": "user",
					"content": []any{map[string]any{
						"type": "input_text",
						"text": "ok",
					}},
				}}},
				{Key: "instructions", Value: "保持简短"},
				{Key: "stream", Value: true},
			}, ModeResponsesSSE, BudgetNone),
			wantURL: CodexBaseURL + "/responses",
			wantBody: map[string]any{
				"model": "gpt-5.4",
				"input": []any{map[string]any{
					"type": "message",
					"role": "user",
					"content": []any{map[string]any{
						"type": "input_text",
						"text": "ok",
					}},
				}},
				"instructions": "保持简短",
				"store":        false,
				"stream":       true,
			},
			wantAbsentBody: codexAbsent,
		},
		{
			name: "gpt-5.6 模型追加 reasoning.context 并合并既有键",
			spec: codexSpec([]BodyField{
				{Key: "model", Value: "gpt-5.6-codex"},
				{Key: "input", Value: "ok"},
				{Key: "reasoning", Value: map[string]any{"effort": "low"}},
			}, ModeResponsesJSON, BudgetNone),
			wantURL: CodexBaseURL + "/responses",
			wantBody: map[string]any{
				"model":               "gpt-5.6-codex",
				"instructions":        "",
				"store":               false,
				"stream":              true,
				"reasoning":           map[string]any{"effort": "low", "context": "all_turns"},
				"parallel_tool_calls": false,
			},
			wantAbsentBody: codexDeletedFields,
		},
		{
			name: "tuning 与状态字段全部被归一化删除",
			spec: codexSpec([]BodyField{
				{Key: "model", Value: "gpt-5.4"},
				{Key: "input", Value: "ok"},
				{Key: "background", Value: true},
				{Key: "conversation", Value: "conv-1"},
				{Key: "context_management", Value: "auto"},
				{Key: "frequency_penalty", Value: 0.2},
				{Key: "max_completion_tokens", Value: 9},
				{Key: "max_output_tokens", Value: 512},
				{Key: "metadata", Value: map[string]any{"k": "v"}},
				{Key: "presence_penalty", Value: 0.1},
				{Key: "prompt_cache_retention", Value: "24h"},
				{Key: "safety_identifier", Value: "s-1"},
				{Key: "stream_options", Value: map[string]any{"include_usage": true}},
				{Key: "temperature", Value: 0.7},
				{Key: "top_p", Value: 0.9},
				{Key: "truncation", Value: "auto"},
				{Key: "user", Value: "u-1"},
			}, ModeResponsesJSON, BudgetNone),
			wantURL: CodexBaseURL + "/responses",
			wantBody: map[string]any{
				"model":        "gpt-5.4",
				"instructions": "",
				"store":        false,
				"stream":       true,
				"input": []any{map[string]any{
					"type": "message",
					"role": "user",
					"content": []any{map[string]any{
						"type": "input_text",
						"text": "ok",
					}},
				}},
			},
			wantAbsentBody: codexDeletedFields,
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
			body := decodeJSONBody(t, request)
			for key, want := range item.wantBody {
				if got, ok := body[key]; !ok || !reflect.DeepEqual(got, want) {
					t.Fatalf("body[%q] = %#v（存在 %v），期望 %#v", key, got, ok, want)
				}
			}
			for _, key := range item.wantAbsentBody {
				if _, ok := body[key]; ok {
					t.Fatalf("Codex 归一化后不应携带字段 %q", key)
				}
			}
		})
	}

	errorCases := []struct {
		name string
		spec Spec
		want string
	}{
		{
			name: "非 responses 端点显式失败",
			spec: codexSpec([]BodyField{{Key: "model", Value: "gpt-probe"}, {Key: "messages", Value: []map[string]any{{"role": "user", "content": "ok"}}}}, ModeChatJSON, BudgetNone),
			want: "仅支持 responses_json/responses_sse",
		},
		{
			name: "有界预算被拒绝（Codex 恒 none）",
			spec: codexSpec([]BodyField{{Key: "model", Value: "gpt-probe"}, {Key: "input", Value: "ok"}}, ModeResponsesJSON, BudgetBounded(DefaultProbeOutputTokens)),
			want: "预算",
		},
		{
			name: "input 类型非法显式失败",
			spec: codexSpec([]BodyField{{Key: "model", Value: "gpt-probe"}, {Key: "input", Value: 42}}, ModeResponsesJSON, BudgetNone),
			want: "input",
		},
		{
			name: "instructions 非字符串显式失败",
			spec: codexSpec([]BodyField{{Key: "model", Value: "gpt-probe"}, {Key: "input", Value: "ok"}, {Key: "instructions", Value: 7}}, ModeResponsesJSON, BudgetNone),
			want: "instructions",
		},
		{
			name: "model 缺失显式失败",
			spec: codexSpec([]BodyField{{Key: "input", Value: "ok"}}, ModeResponsesJSON, BudgetNone),
			want: "model",
		},
	}
	for _, item := range errorCases {
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
}

// TestBuildGeminiCodeAssistAdapterWrapGolden 是 AdapterGeminiCodeAssist 包装
// 钩子的 golden 契约：path 固定 /v1internal:streamGenerateContent?alt=sse
// （JSON 与 SSE 模式一致），body 包装为 {model, project, request} 三字段。
// 形状取证自 accountprobe buildTestRequest 与 exactkeyprobe buildProbeRequest；
// project 经 Spec.Project 传入（凭据语义），x-goog-user-project 等请求头仍由
// 调用方注入。
func TestBuildGeminiCodeAssistAdapterWrapGolden(t *testing.T) {
	contents := []any{map[string]any{
		"role":  "user",
		"parts": []any{map[string]any{"text": "接口连通性自动测试"}},
	}}
	cases := []struct {
		name             string
		spec             Spec
		wantURL          string
		wantModel        string
		wantProject      string
		wantRequest      map[string]any
		wantExtraHeaders map[string]string
	}{
		{
			name: "generate_content_sse 三字段包装并注入预算",
			spec: Spec{
				BaseURL:      "https://cloudcode-pa.example.com",
				EndpointMode: ModeGenerateContentSSE,
				Adapter:      AdapterGeminiCodeAssist,
				Model:        testGeminiModel,
				Project:      "proj-probe",
				Auth:         Auth{Strategy: AuthOAuth, Token: testBearerToken, ExtraHeaders: map[string]string{"x-goog-user-project": "proj-probe"}},
				Budget:       BudgetBounded(DefaultProbeOutputTokens),
				BodyFields:   []BodyField{{Key: "contents", Value: contents}},
			},
			wantURL:     "https://cloudcode-pa.example.com/v1internal:streamGenerateContent?alt=sse",
			wantModel:   testGeminiModel,
			wantProject: "proj-probe",
			wantRequest: map[string]any{
				"contents":         contents,
				"generationConfig": map[string]any{"maxOutputTokens": float64(DefaultProbeOutputTokens)},
			},
			wantExtraHeaders: map[string]string{"X-Goog-User-Project": "proj-probe"},
		},
		{
			name: "generate_content_json 同样改道 SSE 内部端点且 BudgetNone 不注入",
			spec: Spec{
				BaseURL:      "https://cloudcode-pa.example.com",
				EndpointMode: ModeGenerateContentJSON,
				Adapter:      AdapterGeminiCodeAssist,
				Model:        testGeminiModel,
				Project:      "proj-probe",
				Auth:         Auth{Strategy: AuthOAuth, Token: testBearerToken},
				Budget:       BudgetNone,
				BodyFields:   []BodyField{{Key: "contents", Value: contents}},
			},
			wantURL:     "https://cloudcode-pa.example.com/v1internal:streamGenerateContent?alt=sse",
			wantModel:   testGeminiModel,
			wantProject: "proj-probe",
			wantRequest: map[string]any{
				"contents": contents,
			},
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
			body := decodeJSONBody(t, request)
			if len(body) != 3 {
				t.Fatalf("包装 body 应恰为 {model, project, request} 三字段，实际键集: %v", body)
			}
			if got := body["model"]; got != item.wantModel {
				t.Fatalf("body.model = %#v，期望 %q", got, item.wantModel)
			}
			if got := body["project"]; got != item.wantProject {
				t.Fatalf("body.project = %#v，期望 %q", got, item.wantProject)
			}
			wrapped, ok := body["request"].(map[string]any)
			if !ok {
				t.Fatalf("body.request 缺失或非对象: %#v", body["request"])
			}
			if !reflect.DeepEqual(wrapped, item.wantRequest) {
				t.Fatalf("body.request = %#v，期望 %#v", wrapped, item.wantRequest)
			}
			for name, want := range item.wantExtraHeaders {
				if got := request.Header.Get(name); got != want {
					t.Fatalf("请求头 %s = %q，期望 %q（头语义仍由调用方注入）", name, got, want)
				}
			}
		})
	}

	errorCases := []struct {
		name string
		spec Spec
		want string
	}{
		{
			name: "缺少 Spec.Project 显式失败",
			spec: Spec{
				BaseURL: "https://cloudcode-pa.example.com", EndpointMode: ModeGenerateContentSSE,
				Adapter: AdapterGeminiCodeAssist, Model: testGeminiModel,
				Auth: Auth{Strategy: AuthOAuth, Token: testBearerToken}, Budget: BudgetNone,
				BodyFields: []BodyField{{Key: "contents", Value: contents}},
			},
			want: "Spec.Project",
		},
		{
			name: "非 generateContent 端点显式失败",
			spec: Spec{
				BaseURL: "https://cloudcode-pa.example.com", EndpointMode: ModeInteractionsSSE,
				Adapter: AdapterGeminiCodeAssist, Model: testGeminiModel, Project: "proj-probe",
				Auth: Auth{Strategy: AuthOAuth, Token: testBearerToken}, Budget: BudgetNone,
				BodyFields: []BodyField{{Key: "model", Value: testGeminiModel}, {Key: "input", Value: "ok"}},
			},
			want: "仅支持 generate_content_json/generate_content_sse",
		},
	}
	for _, item := range errorCases {
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
}
