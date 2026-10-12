package upstreamenvelope

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// fixedCodexIdentity 返回 golden 测试的固定身份输入（§9.3 身份单源：生产中
// 由调用方每次请求生成，测试以固定值参数化对比 A/B 形态的 body 注入与身份头）。
func fixedCodexIdentity() CodexIdentity {
	return CodexIdentity{
		InstallationID:   "install-golden",
		SessionID:        "session-golden",
		ThreadID:         "thread-golden",
		TurnID:           "turn-golden",
		WindowID:         "thread-golden:0",
		TurnMetadataJSON: `{"installation_id":"install-golden","request_kind":"turn","session_id":"session-golden","thread_id":"thread-golden","turn_id":"turn-golden","turn_started_at_unix_ms":1760000000000}`,
	}
}

// codexFormIdentityHeaders 是 Codex 形态（A/B 同集合，§9.1）Build 统一写入
// 的身份头期望：八项身份头 + openai-beta 键（A 形态缺省补齐值，B 形态由调用
// 方删除该键）。User-Agent 断言对内置常量：单测无版本覆盖，运行时生效值与
// CodexExecUserAgent 逐字一致。
func codexFormIdentityHeaders() map[string]string {
	return map[string]string{
		"Originator":            "codex_exec",
		"User-Agent":            upstreamidentity.CodexExecUserAgent,
		"Session-Id":            "session-golden",
		"Thread-Id":             "thread-golden",
		"X-Client-Request-Id":   "session-golden",
		"X-Codex-Beta-Features": "remote_compaction_v2",
		"X-Codex-Window-Id":     "thread-golden:0",
		"X-Codex-Turn-Metadata": fixedCodexIdentity().TurnMetadataJSON,
		"Openai-Beta":           "responses=experimental",
	}
}

// TestBuildCodexAdapterWrapGolden 是 AdapterOpenAIOAuthCodex（A 形态）包装钩子
// 的 golden 契约（§6 M5 / §9.1-A 验证：path/header/body golden，确认不会携带
// 被归一化删除的字段）。body 期望形状逐字段取证自 modelcheckprobe
// normalizeOpenAIOAuthCodexRequest；强制 SSE、openai-beta 缺省补齐与八项身份
// 头自 W1 起由 Build 统一写入，这里一并断言。
func TestBuildCodexAdapterWrapGolden(t *testing.T) {
	// 归一化删除的字段清单与实现同源（adapterwrap.go codexDeletedFields），
	// 外加 gpt-5.6 之外模型不携带的 reasoning / parallel_tool_calls。
	codexAbsent := append(append([]string(nil), codexDeletedFields...), "reasoning", "parallel_tool_calls")
	codexSpec := func(fields []BodyField, mode EndpointMode, budget BudgetPolicy) Spec {
		return Spec{
			BaseURL:       "https://account.example.com/v1",
			EndpointMode:  mode,
			Adapter:       AdapterOpenAIOAuthCodex,
			CodexIdentity: fixedCodexIdentity(),
			Auth:          Auth{Strategy: AuthOAuth, Token: testBearerToken},
			Budget:        budget,
			BodyFields:    fields,
		}
	}
	cases := []struct {
		name              string
		spec              Spec
		wantURL           string
		wantBody          map[string]any
		wantAbsentBody    []string
		wantAccept        string
		wantHeaders       map[string]string
		wantAbsentHeaders []string
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
			wantAccept:     "text/event-stream",
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
			wantAccept:     "text/event-stream",
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
			wantAccept:     "text/event-stream",
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
			wantAccept:     "text/event-stream",
			wantAbsentBody: codexDeletedFields,
		},
		{
			// A 形态 openai-beta 缺省补齐（§9.5）；此处验证 ExtraHeaders 显式值
			// 保留、不被覆盖。身份头集合由循环内的 codexFormIdentityHeaders 基线
			// 断言，此处只覆盖差异键。
			name: "显式 openai-beta 经 ExtraHeaders 保留",
			spec: func() Spec {
				spec := codexSpec([]BodyField{
					{Key: "model", Value: "gpt-5.4"},
					{Key: "input", Value: "ok"},
				}, ModeResponsesSSE, BudgetNone)
				spec.ExtraHeaders = map[string]string{"openai-beta": "responses=turbo_smoke", "Accept": "application/json"}
				return spec
			}(),
			wantURL:        CodexBaseURL + "/responses",
			wantAccept:     "text/event-stream",
			wantHeaders:    map[string]string{"Openai-Beta": "responses=turbo_smoke"},
			wantBody:       nil,
			wantAbsentBody: codexAbsent,
		},
		{
			// §9.5 Lite 头命中侧：Spec.Model 为最终上游模型，小写去空白后精确
			// 匹配（大小写混合 + 空白环绕仍命中）。
			name: "gpt-5.6-Terra 命中 Lite 头",
			spec: func() Spec {
				spec := codexSpec([]BodyField{
					{Key: "model", Value: "gpt-5.6-Terra"},
					{Key: "input", Value: "ok"},
				}, ModeResponsesJSON, BudgetNone)
				spec.Model = " gpt-5.6-Terra "
				return spec
			}(),
			wantURL:        CodexBaseURL + "/responses",
			wantAccept:     "text/event-stream",
			wantHeaders:    map[string]string{"X-Openai-Internal-Codex-Responses-Lite": "true"},
			wantAbsentBody: codexDeletedFields,
			wantBody: map[string]any{
				"model":               "gpt-5.6-Terra",
				"instructions":        "",
				"store":               false,
				"stream":              true,
				"reasoning":           map[string]any{"context": "all_turns"},
				"parallel_tool_calls": false,
			},
		},
		{
			// §9.5 Lite 头不命中侧：非 Lite 清单模型一律不发送。
			name: "gpt-6-sol 不发送 Lite 头",
			spec: func() Spec {
				spec := codexSpec([]BodyField{
					{Key: "model", Value: "gpt-5.4"},
					{Key: "input", Value: "ok"},
				}, ModeResponsesJSON, BudgetNone)
				spec.Model = "gpt-6-sol"
				return spec
			}(),
			wantURL:           CodexBaseURL + "/responses",
			wantAccept:        "text/event-stream",
			wantAbsentHeaders: []string{"X-Openai-Internal-Codex-Responses-Lite"},
			wantAbsentBody:    codexAbsent,
			wantBody: map[string]any{
				"model":        "gpt-5.4",
				"instructions": "",
				"store":        false,
				"stream":       true,
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
			// A 形态强制 SSE + 八项身份头 + openai-beta 缺省补齐由 Build 统一
			// 写入（§9.1/§9.3/§9.5）；case wantHeaders 仅覆盖差异键。
			if got := request.Header.Get("Accept"); got != item.wantAccept {
				t.Fatalf("Accept = %q，期望 %q", got, item.wantAccept)
			}
			wantHeaders := codexFormIdentityHeaders()
			for name, want := range item.wantHeaders {
				wantHeaders[name] = want
			}
			for name, want := range wantHeaders {
				if got := request.Header.Get(name); got != want {
					t.Fatalf("请求头 %s = %q，期望 %q", name, got, want)
				}
			}
			for _, name := range item.wantAbsentHeaders {
				if got := request.Header.Get(name); got != "" {
					t.Fatalf("不应携带请求头 %s，实际 %q", name, got)
				}
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
		{
			name: "缺 Spec.CodexIdentity 显式失败（A 形态防静默丢身份）",
			spec: func() Spec {
				spec := codexSpec([]BodyField{{Key: "model", Value: "gpt-probe"}, {Key: "input", Value: "ok"}}, ModeResponsesJSON, BudgetNone)
				spec.CodexIdentity = CodexIdentity{}
				return spec
			}(),
			want: "CodexIdentity",
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

// TestBuildCodexCompatibilityAdapterWrapGolden 是 AdapterOpenAICodex
// Compatibility（B 形态：codex_responses 兼容画像，§9.1-B）的 golden 契约。
// body 注入结构逐键等价 exactkeyprobe applyCodexResponsesCompatibility（身份
// 值参数化，经 Spec.CodexIdentity 单源传入）；预算字段与调用方 tuning 字段
// 保留（B 不继承 A 的 BudgetNone / 归一化删除）；URL 走账户 Spec.BaseURL
// （不吞进固定上游）；Accept 按 entry+strategy 现行口径不强制 SSE；身份头与
// A 同集合由 Build 统一写入，openai-beta 不补，Lite 头按 §9.5 模型条件。
func TestBuildCodexCompatibilityAdapterWrapGolden(t *testing.T) {
	identity := fixedCodexIdentity()
	clientMetadata := map[string]any{
		"x-codex-window-id":       identity.WindowID,
		"turn_id":                 identity.TurnID,
		"session_id":              identity.SessionID,
		"x-codex-turn-metadata":   identity.TurnMetadataJSON,
		"x-codex-installation-id": identity.InstallationID,
		"thread_id":               identity.ThreadID,
	}
	// B 形态身份头期望：与 A 同集合，但 openai-beta 不由 Build 补齐。
	bFormIdentityHeaders := func() map[string]string {
		headers := codexFormIdentityHeaders()
		delete(headers, "Openai-Beta")
		return headers
	}
	bSpec := func(fields []BodyField, mode EndpointMode, budget BudgetPolicy) Spec {
		return Spec{
			BaseURL:       "https://account.example.com/v1",
			EndpointMode:  mode,
			Adapter:       AdapterOpenAICodexCompatibility,
			CodexIdentity: identity,
			Auth:          Auth{Strategy: AuthBearer, Token: testBearerToken},
			Budget:        budget,
			BodyFields:    fields,
		}
	}
	cases := []struct {
		name              string
		spec              Spec
		wantURL           string
		wantAccept        string
		wantHeaders       map[string]string
		wantAbsentHeaders []string
		wantBody          map[string]any
	}{
		{
			name: "responses_sse 兼容注入且预算与 tuning 字段保留",
			spec: bSpec([]BodyField{
				{Key: "model", Value: "gpt-5.4"},
				{Key: "input", Value: "接口连通性自动测试"},
				{Key: "stream", Value: true},
				{Key: "reasoning", Value: map[string]any{"effort": "low"}},
				{Key: "temperature", Value: 0.7},
			}, ModeResponsesSSE, BudgetBounded(DefaultProbeOutputTokens)),
			wantURL:    "https://account.example.com/v1/responses",
			wantAccept: "application/json, text/event-stream",
			wantAbsentHeaders: []string{
				"Openai-Beta",
				"X-Openai-Internal-Codex-Responses-Lite",
			},
			wantBody: map[string]any{
				"model":  "gpt-5.4",
				"input":  "接口连通性自动测试",
				"stream": true,
				// reasoning 整体覆盖写，调用方 effort 键不保留（F4 现状）。
				"reasoning":           map[string]any{"context": "all_turns"},
				"store":               false,
				"include":             []any{"reasoning.encrypted_content"},
				"parallel_tool_calls": false,
				"prompt_cache_key":    identity.SessionID,
				"client_metadata":     clientMetadata,
				// 预算字段保留（§9.1-B），tuning 字段不被删除。
				"max_output_tokens": float64(DefaultProbeOutputTokens),
				"temperature":       0.7,
			},
		},
		{
			// §9.5 Lite 头命中侧（B 形态）：Spec.Model 为最终上游模型，大小写
			// 混合 + 空白环绕仍精确命中。
			name: "responses_json gpt-5.6-Terra 命中 Lite 头",
			spec: func() Spec {
				spec := bSpec([]BodyField{
					{Key: "model", Value: "gpt-5.6-Terra"},
					{Key: "input", Value: "ok"},
					{Key: "stream", Value: false},
				}, ModeResponsesJSON, BudgetBounded(DefaultProbeOutputTokens))
				spec.Model = " gpt-5.6-Terra "
				return spec
			}(),
			wantURL:     "https://account.example.com/v1/responses",
			wantAccept:  "application/json",
			wantHeaders: map[string]string{"X-Openai-Internal-Codex-Responses-Lite": "true"},
			wantBody: map[string]any{
				"model":             "gpt-5.6-Terra",
				"store":             false,
				"include":           []any{"reasoning.encrypted_content"},
				"reasoning":         map[string]any{"context": "all_turns"},
				"prompt_cache_key":  identity.SessionID,
				"client_metadata":   clientMetadata,
				"max_output_tokens": float64(DefaultProbeOutputTokens),
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
				t.Fatalf("URL = %q，期望 %q（B 形态必须走账户 Spec.BaseURL）", got, item.wantURL)
			}
			if got := request.Header.Get("Accept"); got != item.wantAccept {
				t.Fatalf("Accept = %q，期望 %q（B 形态不强制 SSE）", got, item.wantAccept)
			}
			wantHeaders := bFormIdentityHeaders()
			for name, want := range item.wantHeaders {
				wantHeaders[name] = want
			}
			for name, want := range wantHeaders {
				if got := request.Header.Get(name); got != want {
					t.Fatalf("请求头 %s = %q，期望 %q", name, got, want)
				}
			}
			for _, name := range item.wantAbsentHeaders {
				if got := request.Header.Get(name); got != "" {
					t.Fatalf("不应携带请求头 %s，实际 %q", name, got)
				}
			}
			body := decodeJSONBody(t, request)
			for key, want := range item.wantBody {
				if got, ok := body[key]; !ok || !reflect.DeepEqual(got, want) {
					t.Fatalf("body[%q] = %#v（存在 %v），期望 %#v", key, got, ok, want)
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
			spec: bSpec([]BodyField{{Key: "model", Value: "gpt-probe"}, {Key: "messages", Value: []map[string]any{{"role": "user", "content": "ok"}}}}, ModeChatJSON, BudgetBounded(DefaultProbeOutputTokens)),
			want: "仅支持 responses_json/responses_sse",
		},
		{
			name: "缺 Spec.CodexIdentity 显式失败（B 形态防静默丢身份）",
			spec: func() Spec {
				spec := bSpec([]BodyField{{Key: "model", Value: "gpt-probe"}, {Key: "input", Value: "ok"}}, ModeResponsesJSON, BudgetBounded(DefaultProbeOutputTokens))
				spec.CodexIdentity = CodexIdentity{}
				return spec
			}(),
			want: "CodexIdentity",
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
