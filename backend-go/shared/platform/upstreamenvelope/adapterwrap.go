package upstreamenvelope

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// adapterwrap.go 是适配器包装钩子的实现（设计文档 §6 M5）。钩子在 Build 的
// body 编码与预算字段注入之后应用，只负责最终 path 与 body 包装；请求头
// 写入规则在 Build：Codex 形态（A/B）身份头、A 形态强制 SSE 与 openai-beta
// 缺省补齐由 Build 按 Spec 形态统一写入（§9.1/§9.3/§9.5），Code Assist 的
// x-goog-user-project 等账户/凭据语义头仍由调用方经 Spec.Auth /
// Spec.ExtraHeaders 注入（§4.1：适配器只提供声明式端点、认证及能力描述）。

// codexDeletedFields 是 Codex 归一化删除的字段清单（取证：modelcheckprobe
// normalizeOpenAIOAuthCodexRequest——这些 tuning / 状态字段不被 Codex 上游
// 接受，含 max_output_tokens，与 §4.3 Codex 例外一致）。
var codexDeletedFields = []string{
	"background", "conversation", "context_management",
	"frequency_penalty", "max_completion_tokens", "max_output_tokens",
	"metadata", "presence_penalty", "prompt_cache_retention",
	"safety_identifier", "stream_options", "temperature", "top_p",
	"truncation", "user",
}

// wrapCodexResponses 是 AdapterOpenAIOAuthCodex 的包装钩子：对已编码的
// Responses body 应用 Codex 归一化，path 固定为 /responses（固定上游由
// adapterDef.fixedBaseURL 处理）。形状承接 W2 前各调用方自有归一化的共同
// 契约（F3 normalizeOpenAIOAuthCodexRequest、accountprobe
// applyCodexCompatibilityPayload 均已随接线删除，分别见信封接线计划 W2/W4）：
//   - 字符串 input 包装为 user 消息数组；结构化 input 保留；
//   - instructions 缺省补空字符串，存在时必须是字符串；
//   - 删除 codexDeletedFields 全部字段；store=false、stream=true；
//   - gpt-5.6-* 模型追加 reasoning.context=all_turns（合并既有 reasoning
//     对象）与 parallel_tool_calls=false。
//
// Codex 归一化不携带预算字段（§4.3 Codex 例外，ResolveBudgetPolicy 对本
// 适配器恒返回 BudgetNone）；非 none 策略在此显式失败，避免注入的预算字段
// 被归一化静默删除造成请求形状与调用方意图分叉。
func wrapCodexResponses(spec Spec, _ string, body []byte) (string, []byte, error) {
	if spec.EndpointMode != ModeResponsesJSON && spec.EndpointMode != ModeResponsesSSE {
		return "", nil, fmt.Errorf("upstreamenvelope: 适配器 %s 仅支持 responses_json/responses_sse 端点，收到 %s", AdapterOpenAIOAuthCodex, spec.EndpointMode)
	}
	if !spec.Budget.IsNone() {
		return "", nil, fmt.Errorf("upstreamenvelope: Codex 归一化不携带预算字段（设计文档 §4.3 Codex 例外），预算策略 %s 必须为 none", spec.Budget.Name())
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", nil, fmt.Errorf("upstreamenvelope: Codex 请求 body 无效（必须为 JSON 对象）：%w", err)
	}
	if parsed == nil {
		return "", nil, errors.New("upstreamenvelope: Codex 请求 body 无效（必须为 JSON 对象）")
	}
	model, ok := parsed["model"].(string)
	if !ok || strings.TrimSpace(model) == "" {
		return "", nil, errors.New("upstreamenvelope: Codex 请求 model 缺失或非法")
	}
	if input, ok := parsed["input"].(string); ok {
		parsed["input"] = []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": input,
			}},
		}}
	} else if _, ok := parsed["input"].([]any); !ok {
		return "", nil, errors.New("upstreamenvelope: Codex 请求 input 非法（必须是字符串或消息数组）")
	}
	if instructions, present := parsed["instructions"]; present {
		if _, ok := instructions.(string); !ok {
			return "", nil, errors.New("upstreamenvelope: Codex 请求 instructions 必须是字符串")
		}
	} else {
		parsed["instructions"] = ""
	}
	for _, field := range codexDeletedFields {
		delete(parsed, field)
	}
	parsed["store"] = false
	parsed["stream"] = true
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-5.6-") {
		reasoning, _ := parsed["reasoning"].(map[string]any)
		if reasoning == nil {
			reasoning = map[string]any{}
		}
		reasoning["context"] = "all_turns"
		parsed["reasoning"] = reasoning
		parsed["parallel_tool_calls"] = false
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return "", nil, fmt.Errorf("upstreamenvelope: 编码 Codex 归一化 body 失败：%w", err)
	}
	return "/responses", encoded, nil
}

// wrapCodexCompatibilityProfile 是 AdapterOpenAICodexCompatibility 的包装
// 钩子（B 形态：codex_responses 兼容画像，§9.1-B）：对已编码 Responses body
// 注入兼容画像字段，结构逐键等价 exactkeyprobe
// applyCodexResponsesCompatibility（身份值参数化，经 Spec.CodexIdentity
// 传入，§9.3 单源）：
//   - store=false、include=["reasoning.encrypted_content"]；
//   - reasoning={"context":"all_turns"}（整体覆盖写，不合并既有键，与 F4
//     现状一致）；
//   - parallel_tool_calls=false；
//   - prompt_cache_key=身份 SessionID；
//   - client_metadata 六键（x-codex-window-id/turn_id/session_id/
//     x-codex-turn-metadata/x-codex-installation-id/thread_id），与身份
//     请求头共用同一份身份。
//
// B 形态保留预算字段与调用方 tuning 字段（§9.1：B 不继承 A 的 BudgetNone
// 例外，不做归一化删除）。path 原样返回：URL 由账户 BaseURL +
// buildUpstreamURL 档案路径规约决定（适配器无 fixedBaseURL）。body 解码/
// 编码失败与身份缺失显式报错，不静默降级。
func wrapCodexCompatibilityProfile(spec Spec, path string, body []byte) (string, []byte, error) {
	if spec.EndpointMode != ModeResponsesJSON && spec.EndpointMode != ModeResponsesSSE {
		return "", nil, fmt.Errorf("upstreamenvelope: 适配器 %s 仅支持 responses_json/responses_sse 端点，收到 %s", AdapterOpenAICodexCompatibility, spec.EndpointMode)
	}
	if spec.CodexIdentity == (CodexIdentity{}) {
		return "", nil, fmt.Errorf("upstreamenvelope: 适配器 %s 缺少 Spec.CodexIdentity（设计文档 §9.3 身份单源：身份由调用方每次请求生成后传入）", AdapterOpenAICodexCompatibility)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", nil, fmt.Errorf("upstreamenvelope: 解码 Responses body 失败：%w", err)
	}
	if parsed == nil {
		return "", nil, errors.New("upstreamenvelope: Responses body 无效（必须为 JSON 对象）")
	}
	identity := spec.CodexIdentity
	parsed["store"] = false
	parsed["include"] = []string{"reasoning.encrypted_content"}
	parsed["reasoning"] = map[string]any{"context": "all_turns"}
	parsed["parallel_tool_calls"] = false
	parsed["client_metadata"] = map[string]any{
		"x-codex-window-id":       identity.WindowID,
		"turn_id":                 identity.TurnID,
		"session_id":              identity.SessionID,
		"x-codex-turn-metadata":   identity.TurnMetadataJSON,
		"x-codex-installation-id": identity.InstallationID,
		"thread_id":               identity.ThreadID,
	}
	parsed["prompt_cache_key"] = identity.SessionID
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return "", nil, fmt.Errorf("upstreamenvelope: 编码 Codex 兼容画像 body 失败：%w", err)
	}
	return path, encoded, nil
}

// wrapGeminiCodeAssist 是 AdapterGeminiCodeAssist 的包装钩子：把
// generateContent 请求改道 Code Assist 内部端点并把 body 包装为
// {model, project, request} 三字段（取证：accountprobe buildTestRequest 与
// exactkeyprobe buildProbeRequest——JSON 与 SSE 模式统一走
// /v1internal:streamGenerateContent?alt=sse；interactions 端点不包装）。
// project 取账户凭据的 project_id，经 Spec.Project 传入：它是包装 body 的
// 协议字段而非 HTTP 头，不复用 ExtraHeaders（那会真实写入请求头）；缺省
// 显式失败（accountprobe：“缺少 project_id”）。内部 request 对象原样保留，
// 预算字段仍由信封按 generateContent 条目注入。
func wrapGeminiCodeAssist(spec Spec, _ string, body []byte) (string, []byte, error) {
	if spec.EndpointMode != ModeGenerateContentJSON && spec.EndpointMode != ModeGenerateContentSSE {
		return "", nil, fmt.Errorf("upstreamenvelope: 适配器 %s 仅支持 generate_content_json/generate_content_sse 端点，收到 %s", AdapterGeminiCodeAssist, spec.EndpointMode)
	}
	project := strings.TrimSpace(spec.Project)
	if project == "" {
		return "", nil, errors.New("upstreamenvelope: Gemini Code Assist / Google One 缺少 Spec.Project（凭据 project_id）")
	}
	var request any
	if err := json.Unmarshal(body, &request); err != nil {
		return "", nil, fmt.Errorf("upstreamenvelope: 解码 generateContent body 失败：%w", err)
	}
	encoded, err := json.Marshal(map[string]any{"model": spec.Model, "project": project, "request": request})
	if err != nil {
		return "", nil, fmt.Errorf("upstreamenvelope: 编码 Code Assist 包装 body 失败：%w", err)
	}
	return "/v1internal:streamGenerateContent?alt=sse", encoded, nil
}
