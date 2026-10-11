package upstreamenvelope

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// adapterwrap.go 是适配器包装钩子的实现（设计文档 §6 M5）。钩子在 Build 的
// body 编码与预算字段注入之后应用，只负责最终 path 与 body 包装；请求头增强
// （Codex 的 originator / session-id / x-codex-* / openai-beta 等，Code
// Assist 的 x-goog-user-project 等）属于账户/凭据语义，仍由调用方经
// Spec.Auth / Spec.ExtraHeaders 注入（§4.1：适配器只提供声明式端点、认证及
// 能力描述）。

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
// adapterDef.fixedBaseURL 处理）。形状逐行为对齐 modelcheckprobe
// normalizeOpenAIOAuthCodexRequest 的 body 改写（accountprobe
// applyCodexCompatibilityPayload 是另一形状，不经本钩子）：
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
