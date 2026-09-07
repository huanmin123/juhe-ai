// Package accountprobe 移植 Node 诊断探针栈中被后台探针任务消费的窄路径：
//   - backend/src/modules/accounts/account-test-request.ts 的协议请求构造
//     （OpenAI chat/completions、OpenAI Responses、Anthropic Messages、
//     Gemini generateContent / interactions、Images）；
//   - backend/src/modules/accounts/account-test-response-diagnostics.ts 与
//     account-test-success-evidence.ts 的响应分类；
//   - backend/src/modules/accounts/account-diagnostic-retry-policy.ts 的
//     分级超时（10s/20s/30s）与 automatic-account-probe-outcome 的传输证据。
//
// Node 侧探针经进程内网关（handleOpenAIGatewayRequest）发起；Go jobs 不复刻
// 整个网关，只复刻后台探针路径的可观察行为：单候选账户、disableSessionAffinity、
// disableAccountStateMutation、上游 Bearer 认证（gateway/upstream/request.ts
// buildUpstreamHeaders 是唯一认证头写入点）与 limited 诊断脱敏。
package accountprobe

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// EndpointMode 与 Node AccountSupportedEndpointMode 的探针子集一致。
type EndpointMode string

const (
	ModeChatJSON            EndpointMode = "chat_json"
	ModeChatSSE             EndpointMode = "chat_sse"
	ModeResponsesJSON       EndpointMode = "responses_json"
	ModeResponsesSSE        EndpointMode = "responses_sse"
	ModeMessagesJSON        EndpointMode = "messages_json"
	ModeMessagesSSE         EndpointMode = "messages_sse"
	ModeGenerateContentJSON EndpointMode = "generate_content_json"
	ModeGenerateContentSSE  EndpointMode = "generate_content_sse"
	ModeInteractionsJSON    EndpointMode = "interactions_json"
	ModeInteractionsSSE     EndpointMode = "interactions_sse"
	ModeImagesJSON          EndpointMode = "images_json"
)

func (m EndpointMode) streaming() bool {
	return m == ModeChatSSE || m == ModeResponsesSSE || m == ModeMessagesSSE ||
		m == ModeGenerateContentSSE || m == ModeInteractionsSSE
}

func (m EndpointMode) anthropic() bool {
	return m == ModeMessagesJSON || m == ModeMessagesSSE
}

func (m EndpointMode) gemini() bool {
	return m == ModeGenerateContentJSON || m == ModeGenerateContentSSE ||
		m == ModeInteractionsJSON || m == ModeInteractionsSSE
}

// DiagnosticProtocol 与 Node AccountTestDiagnosticProtocol 一致。
type DiagnosticProtocol string

const (
	ProtocolOpenAI    DiagnosticProtocol = "openai"
	ProtocolAnthropic DiagnosticProtocol = "anthropic"
	ProtocolGemini    DiagnosticProtocol = "gemini"
)

// 账户探针常量（Node account-test-request.ts）。
const (
	outputChallengeExpected = "juhe"
	outputChallengePrompt   = "只能回复：" + outputChallengeExpected
	outputTokenLimit        = 256
	defaultInstructions     = "You are ChatGPT, a helpful assistant."
	anthropicVersion        = "2.1.201"
	anthropicBuildID        = "eb7"
	anthropicDeviceID       = "7cfe24060ed291eb6ea9b7a6edf6947d14da82a0068470a6fc9cf8c147b252dc"
	clientProfileHeader     = "x-juhe-client-profile"
	imageTestPrompt         = "Solid black."
)

// OutputChallenge 等价 Node AccountTestOutputChallenge。
type OutputChallenge struct {
	ExpectedOutput string
	Prompt         string
}

// CreateOutputChallenge 等价 createAccountTestOutputChallenge。
func CreateOutputChallenge() OutputChallenge {
	return OutputChallenge{ExpectedOutput: outputChallengeExpected, Prompt: outputChallengePrompt}
}

// testRequest 是一次探针请求的协议形态（path/body/headers）。
type testRequest struct {
	path    string
	body    []byte
	headers map[string]string
	model   string
}

// defaultEndpointMode 等价 resolveAccountTestEndpointMode：无显式 mode 时取
// 支持列表首个；列表为空报 Node 同文案配置错误。
func defaultEndpointMode(supported []EndpointMode) (EndpointMode, error) {
	if len(supported) == 0 {
		return "", fmt.Errorf("账户上游接口能力中没有可用于连接测试的请求形态")
	}
	return supported[0], nil
}

// manualTestEndpointModes 等价 accountManualTestEndpointModes：healthCheck
// 模式优先，其后按协议的固定顺序去重。credentials.supported_endpoint_modes
// 的归一化集合由调用方传入（normalizedModes）。
func manualTestEndpointModes(defaultMode EndpointMode, normalizedModes map[EndpointMode]bool, order []EndpointMode) []EndpointMode {
	out := make([]EndpointMode, 0, len(order)+1)
	if defaultMode != "" && normalizedModes[defaultMode] {
		out = append(out, defaultMode)
	}
	for _, mode := range order {
		if normalizedModes[mode] {
			added := false
			for _, existing := range out {
				if existing == mode {
					added = true
					break
				}
			}
			if !added {
				out = append(out, mode)
			}
		}
	}
	return out
}

// EndpointModeOrderOpenAI 是 accountTestEndpointModeOrder 的 api_key 分支。
func EndpointModeOrderOpenAI() []EndpointMode {
	return []EndpointMode{ModeChatSSE, ModeResponsesSSE, ModeChatJSON, ModeResponsesJSON}
}

// EndpointModeOrderAnthropic / Gemini / OAuth 对齐 Node 其余分支。
func EndpointModeOrderAnthropic() []EndpointMode {
	return []EndpointMode{ModeMessagesJSON, ModeMessagesSSE}
}

func EndpointModeOrderGemini() []EndpointMode {
	return []EndpointMode{ModeInteractionsJSON, ModeInteractionsSSE, ModeGenerateContentJSON, ModeGenerateContentSSE}
}

func EndpointModeOrderOAuth() []EndpointMode {
	return []EndpointMode{ModeResponsesJSON, ModeResponsesSSE}
}

// protocolFamilyForMode 等价 accountTestEndpointModeSourceFamily（模型映射
// 的 source_endpoint_family 匹配键）。
func protocolFamilyForMode(mode EndpointMode) string {
	switch mode {
	case ModeChatJSON, ModeChatSSE:
		return "openai_chat_completions"
	case ModeResponsesJSON, ModeResponsesSSE:
		return "openai_responses"
	case ModeMessagesJSON, ModeMessagesSSE:
		return "anthropic_messages"
	case ModeGenerateContentSSE:
		return "gemini_stream_generate_content"
	default:
		return "gemini_generate_content"
	}
}

func orderedJSON(fields []orderedField) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, field := range fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(field.key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		value, err := field.marshal()
		if err != nil {
			return nil, err
		}
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

type orderedField struct {
	key     string
	marshal func() ([]byte, error)
}

func rawField(key string, raw json.RawMessage) orderedField {
	return orderedField{key: key, marshal: func() ([]byte, error) { return raw, nil }}
}

func marshalValue(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("accountprobe: 探针载荷序列化失败: %v", err))
	}
	return encoded
}

func rawText(value string) json.RawMessage { return marshalValue(value) }

func rawBool(value bool) json.RawMessage { return marshalValue(value) }

func rawInt(value int) json.RawMessage { return marshalValue(value) }

// buildOpenAIResponsesPayload 等价 createOpenAIResponsesTestPayload
// （对象键顺序与 Node 字面量一致；codex_responses 流式 lite 归一化仅在
// OAuth + codex 兼容时出现，jobs 探针路径未复刻，见包注释）。
func buildOpenAIResponsesPayload(model, prompt string, isOAuth bool, stream bool) ([]byte, error) {
	input := []map[string]any{
		{
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": prompt},
			},
		},
	}
	fields := []orderedField{
		{key: "model", marshal: func() ([]byte, error) { return rawText(model), nil }},
		rawField("input", marshalValue(input)),
		{key: "instructions", marshal: func() ([]byte, error) { return rawText(defaultInstructions), nil }},
		{key: "stream", marshal: func() ([]byte, error) { return rawBool(stream), nil }},
		{key: "max_output_tokens", marshal: func() ([]byte, error) { return rawInt(outputTokenLimit), nil }},
	}
	if isOAuth {
		fields = append(fields, orderedField{key: "store", marshal: func() ([]byte, error) { return rawBool(false), nil }})
	}
	return orderedJSON(fields)
}

// buildOpenAIChatCompletionsPayload 等价 createOpenAIChatCompletionsTestPayload。
func buildOpenAIChatCompletionsPayload(model, prompt string, stream bool) ([]byte, error) {
	messages := []map[string]any{{"role": "user", "content": prompt}}
	return orderedJSON([]orderedField{
		{key: "model", marshal: func() ([]byte, error) { return rawText(model), nil }},
		rawField("messages", marshalValue(messages)),
		{key: "max_tokens", marshal: func() ([]byte, error) { return rawInt(outputTokenLimit), nil }},
		{key: "stream", marshal: func() ([]byte, error) { return rawBool(stream), nil }},
	})
}

// buildGeminiGenerateContentPayload 等价 createGeminiGenerateContentTestPayload。
func buildGeminiGenerateContentPayload(prompt string) ([]byte, error) {
	contents := []map[string]any{
		{
			"role":  "user",
			"parts": []map[string]any{{"text": prompt}},
		},
	}
	return orderedJSON([]orderedField{
		rawField("contents", marshalValue(contents)),
		rawField("generationConfig", marshalValue(map[string]any{"maxOutputTokens": outputTokenLimit})),
	})
}

// buildAnthropicMessagesPayload 等价 createAnthropicClaudeCodeAccountTestPayload。
func buildAnthropicMessagesPayload(model, prompt string, stream bool, sessionID string) ([]byte, error) {
	messages := []map[string]any{
		{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": anthropicSystemReminder()},
				{"type": "text", "text": prompt + "\n", "cache_control": map[string]any{"type": "ephemeral"}},
			},
		},
	}
	system := []map[string]any{
		{"type": "text", "text": fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=sdk-cli;", anthropicVersion, anthropicBuildID)},
		{"type": "text", "text": "You are a Claude agent, built on Anthropic's Claude Agent SDK.", "cache_control": map[string]any{"type": "ephemeral"}},
		{"type": "text", "text": fmt.Sprintf("CWD: %s\nDate: %s", cwdOrEmpty(), time.Now().UTC().Format("2006-01-02"))},
	}
	metadataUser := marshalValue(map[string]any{
		"device_id":    anthropicDeviceID,
		"account_uuid": "",
		"session_id":   sessionID,
	})
	return orderedJSON([]orderedField{
		{key: "model", marshal: func() ([]byte, error) { return rawText(model), nil }},
		rawField("messages", marshalValue(messages)),
		rawField("system", marshalValue(system)),
		{key: "tools", marshal: func() ([]byte, error) { return marshalValue([]any{}), nil }},
		{key: "max_tokens", marshal: func() ([]byte, error) { return rawInt(32000), nil }},
		{key: "thinking", marshal: func() ([]byte, error) { return marshalValue(map[string]any{"type": "adaptive"}), nil }},
		{key: "output_config", marshal: func() ([]byte, error) { return marshalValue(map[string]any{"effort": "high"}), nil }},
		{key: "metadata", marshal: func() ([]byte, error) {
			return orderedJSON([]orderedField{
				{key: "user_id", marshal: func() ([]byte, error) { return metadataUser, nil }},
			})
		}},
		{key: "stream", marshal: func() ([]byte, error) { return rawBool(stream), nil }},
	})
}

// buildImagesPayload 等价 createOpenAIImageGenerationTestRequest 的 body。
func buildImagesPayload(model string) ([]byte, error) {
	return orderedJSON([]orderedField{
		{key: "model", marshal: func() ([]byte, error) { return rawText(model), nil }},
		{key: "prompt", marshal: func() ([]byte, error) { return rawText(imageTestPrompt), nil }},
		{key: "n", marshal: func() ([]byte, error) { return rawInt(1), nil }},
		{key: "size", marshal: func() ([]byte, error) { return rawText("1024x1024"), nil }},
		{key: "quality", marshal: func() ([]byte, error) { return rawText("low"), nil }},
		{key: "output_format", marshal: func() ([]byte, error) { return rawText("webp"), nil }},
		{key: "output_compression", marshal: func() ([]byte, error) { return rawInt(100), nil }},
	})
}

func anthropicSystemReminder() string {
	date := time.Now().UTC().Format("2006-01-02")
	return "<system-reminder>\n" +
		"As you answer the user's questions, you can use the following context:\n" +
		"# currentDate\n" +
		"Today's date is " + date + ".\n" +
		"\n" +
		"      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n" +
		"</system-reminder>\n\n\n"
}

func cwdOrEmpty() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func newUUID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return hex.EncodeToString(buffer)
	}
	// RFC 4122 version 4 形态（Node randomUUID）。
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	value := hex.EncodeToString(buffer)
	return value[0:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:32]
}

// geminiModelPath 等价 geminiModelPath：剥一次不区分大小写的 models/ 前缀，
// 空值回落 gemini-pro。
func geminiModelPath(model string) string {
	normalized := trimSpace(model)
	if len(normalized) >= 7 && equalFoldPrefix(normalized, "models/") {
		normalized = normalized[7:]
	}
	if normalized == "" {
		normalized = "gemini-pro"
	}
	return "models/" + urlPathEscape(normalized)
}

func equalFoldPrefix(value, prefix string) bool {
	if len(value) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a, b := value[i], prefix[i]
		if 'A' <= a && a <= 'Z' {
			a += 'a' - 'A'
		}
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func trimSpace(value string) string {
	start, end := 0, len(value)
	for start < end && isSpaceByte(value[start]) {
		start++
	}
	for end > start && isSpaceByte(value[end-1]) {
		end--
	}
	return value[start:end]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}

func urlPathEscape(value string) string {
	// 与 Node encodeURIComponent 一致的保留字符集。
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	out := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		b := value[i]
		if byteInSet(b, unreserved) {
			out = append(out, b)
			continue
		}
		out = append(out, '%')
		high := b >> 4
		low := b & 0x0f
		out = append(out, hexDigit(high), hexDigit(low))
	}
	return string(out)
}

func hexDigit(v byte) byte {
	if v < 10 {
		return '0' + v
	}
	return 'A' + (v - 10)
}

func byteInSet(b byte, set string) bool {
	for i := 0; i < len(set); i++ {
		if set[i] == b {
			return true
		}
	}
	return false
}
