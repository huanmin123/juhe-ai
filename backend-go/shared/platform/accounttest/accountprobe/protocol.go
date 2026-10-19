// Package accountprobe 移植 Node 诊断探针栈中被后台探针任务消费的窄路径：
//   - backend/src/modules/accounts/account-test-request.ts 的协议请求构造
//     （OpenAI chat/completions、OpenAI Responses、Anthropic Messages、
//     Gemini generateContent / interactions、Images）；
//   - backend/src/modules/accounts/account-test-response-diagnostics.ts 与
//     account-test-success-evidence.ts 的响应分类；
//   - backend/src/modules/accounts/account-diagnostic-retry-policy.ts 的
//     分级超时（Node 10s/20s/30s，2026-10-07 上调为 20s/30s/40s）与
//     automatic-account-probe-outcome 的传输证据。
//
// W4 起（信封统一设计 §9，计划 W4 行）最终请求构造收口 upstreamenvelope.Build：
// 本包只保留探针语义 body 装配（BodyFields / Multipart）、账户形态 → 认证/预算
// 策略映射与身份生成（§9.3 单源）；method、URL、标准/认证/身份头、body 编码与
// 适配器包装全部由 Build 承担，本包不再拼接最终 HTTP 请求。
package accountprobe

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
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
	// M1 同步音频探针形态（音频设计 §11.9）：audio_speech = POST
	// /v1/audio/speech（2xx + audio/* 判活）；audio_transcription_json =
	// multipart POST /v1/audio/transcriptions（2xx + JSON text 判活）。
	ModeAudioSpeech            EndpointMode = "audio_speech"
	ModeAudioTranscriptionJSON EndpointMode = "audio_transcription_json"
)

func (m EndpointMode) streaming() bool {
	return m == ModeChatSSE || m == ModeResponsesSSE || m == ModeMessagesSSE ||
		m == ModeGenerateContentSSE || m == ModeInteractionsSSE
}

// audio 判定：M1 同步音频探针形态（非流式、非文本对话协议）。
func (m EndpointMode) audio() bool {
	return m == ModeAudioSpeech || m == ModeAudioTranscriptionJSON
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

// 账户探针常量（Node account-test-request.ts；挑战报文为有意偏离，见注释）。
const (
	// outputChallengePromptFormat 是探针挑战报文格式串（两个十进制操作数）。
	// 2026-10-15 停用历史固定令牌形态（"接口连通性自动测试：请原样输出
	// juhe<随机数字>"）：上游 api.shenwenai.com 等内容审核把「请原样输出
	// 令牌」判为可疑请求并整段返回 HTTP 403（生产实测 14 次中 13 次被拒，
	// 探针失败把正常账户打成临时不可调用）；随机算术题实测 19/19 通过，
	// 且模型必须真实推理才能答对。
	outputChallengePromptFormat = "接口连通性自动测试：请计算 %d+%d，只回复数字"
	anthropicBuildID            = "eb7"
	anthropicDeviceID           = "7cfe24060ed291eb6ea9b7a6edf6947d14da82a0068470a6fc9cf8c147b252dc"
	clientProfileHeader         = "x-juhe-client-profile"
)

// probeInstructionsPool 是 responses 形态 system 指令的轮换池，用于消除固
// 定指纹（2026-09 上游风控开始按固定报文指纹封号）；池首保留历史原值
// （Node account-test-request.ts 的固定值），其余为 OpenAI 生态常见的无害
// system 指令变体。
var probeInstructionsPool = []string{
	"You are ChatGPT, a helpful assistant.",
	"You are ChatGPT, a large language model trained by OpenAI.",
	"You are a helpful assistant.",
	"You are a helpful AI assistant.",
	"You are an AI assistant.",
}

// OutputChallenge 等价 Node AccountTestOutputChallenge。
type OutputChallenge struct {
	ExpectedOutput string
	Prompt         string
}

// CreateOutputChallenge 等价 createAccountTestOutputChallenge，但有意偏离
// Node 的固定报文：2026-09 起上游风控按固定报文指纹封号，请求侧在固定前缀后
// 追加随机成分；2026-10-15 起固定前缀改为随机算术题（操作数 CSPRNG 随机，
// 期望输出为两数之和），兼取打散固定指纹与真实推理证据——模型必须实际计算
// 才能给出期望数字。验证侧保持宽松：可见输出包含该数字（数字边界匹配）即通过。
func CreateOutputChallenge() OutputChallenge {
	left, right := challengeOperands()
	return OutputChallenge{
		ExpectedOutput: strconv.Itoa(left + right),
		Prompt:         fmt.Sprintf(outputChallengePromptFormat, left, right),
	}
}

// challengeOperands 生成两个 10~89 的十进制操作数：按 256 均匀映射到 80 个
// 取值，无模偏差（同 challengeDigits 的 256%4=0 先例）。
func challengeOperands() (int, int) {
	buf := make([]byte, 2)
	randomBytes(buf)
	return 10 + int(buf[0])*80/256, 10 + int(buf[1])*80/256
}

// challengeAnswerMatched 判定可见输出是否包含期望的十进制答案：命中数字的
// 两侧都不得是数字，避免匹配到更长数字内部的片段（如期望 405 命中 "17940576"）。
func challengeAnswerMatched(visible, expected string) bool {
	if expected == "" {
		return false
	}
	for searchFrom := 0; searchFrom+len(expected) <= len(visible); {
		index := strings.Index(visible[searchFrom:], expected)
		if index < 0 {
			return false
		}
		begin := searchFrom + index
		end := begin + len(expected)
		leftOK := begin == 0 || !isASCIIDigit(visible[begin-1])
		rightOK := end == len(visible) || !isASCIIDigit(visible[end])
		if leftOK && rightOK {
			return true
		}
		searchFrom = begin + 1
	}
	return false
}

func isASCIIDigit(char byte) bool {
	return char >= '0' && char <= '9'
}

// randomBytes 用 crypto/rand 填充 buf；读取失败时按 idgen.RandomHex 先例回
// 退时间熵。降级仅影响随机性，不影响验证语义：输出挑战验证是宽松包含
// ExpectedOutput，不依赖请求侧随机成分。
func randomBytes(buf []byte) {
	if _, err := rand.Read(buf); err != nil {
		now := uint64(time.Now().UnixNano())
		for i := range buf {
			buf[i] = byte(now >> (uint(i%8) * 8))
		}
	}
}

// challengeDigits 生成 3~6 位随机数字：长度 = 3 + (1 字节 & 3)（256%4=0，
// 无模偏差），每位 = '0' + (字节 % 10)。
func challengeDigits() string {
	buf := make([]byte, 7)
	randomBytes(buf)
	length := 3 + int(buf[0]&0x03)
	out := make([]byte, length)
	for i := 0; i < length; i++ {
		out[i] = '0' + buf[1+i]%10
	}
	return string(out)
}

// probeInstructions 从轮换池随机选取 responses 形态的 system 指令；池首为
// 历史原值（见 probeInstructionsPool）。
func probeInstructions() string {
	buf := make([]byte, 1)
	randomBytes(buf)
	return probeInstructionsPool[int(buf[0])%len(probeInstructionsPool)]
}

// buildImageTestPrompt 返回图片探针报文：历史固定值 Solid black. 加随机数
// 字后缀以消除固定指纹；图片验证只看 HTTP envelope，不校验 prompt 文本，
// 后缀不影响结果。
func buildImageTestPrompt() string {
	return "Solid black. " + challengeDigits()
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
// 各协议族形态顺序统一流式优先（AI账户检查模型与人工测试设计 §5 2026-10-19
// 定案）：真实流量以流式为主，_sse 恒先于 _json，与
// accounts/accountstest.AccountTestEndpointModeOrder 同序。
func EndpointModeOrderOpenAI() []EndpointMode {
	return []EndpointMode{ModeChatSSE, ModeResponsesSSE, ModeChatJSON, ModeResponsesJSON, ModeAudioSpeech, ModeAudioTranscriptionJSON}
}

// EndpointModeOrderAnthropic / Gemini / OAuth 对齐 Node 其余分支（流式优先）。
func EndpointModeOrderAnthropic() []EndpointMode {
	return []EndpointMode{ModeMessagesSSE, ModeMessagesJSON}
}

func EndpointModeOrderGemini() []EndpointMode {
	// M1：gemini 族的 audio_speech 探针形态在尾部（TTS adapter 承载；
	// gemini 无 /v1/audio/transcriptions 直连形态，不列 transcription）。
	return []EndpointMode{ModeInteractionsSSE, ModeGenerateContentSSE, ModeInteractionsJSON, ModeGenerateContentJSON, ModeAudioSpeech}
}

func EndpointModeOrderOAuth() []EndpointMode {
	return []EndpointMode{ModeResponsesSSE, ModeResponsesJSON}
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

// anthropicMessagesBodyFields 装配 Anthropic Messages 探针语义 body（等价原
// createAnthropicClaudeCodeAccountTestPayload 的字段集；W4 起不再私有编码，
// max_tokens 由 Build 按 catalog 必填字段注入——BUG-0201 下 1024、无
// thinking/effort 的轻量探针契约不变，见信封统一设计 §4.3）。
func anthropicMessagesBodyFields(model, prompt string, stream bool, sessionID string) []upstreamenvelope.BodyField {
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
		{"type": "text", "text": fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=sdk-cli;", upstreamidentity.EffectiveClaudeCodeVersion(), anthropicBuildID)},
		{"type": "text", "text": "You are a Claude agent, built on Anthropic's Claude Agent SDK.", "cache_control": map[string]any{"type": "ephemeral"}},
		{"type": "text", "text": fmt.Sprintf("CWD: %s\nDate: %s", cwdOrEmpty(), time.Now().UTC().Format("2006-01-02"))},
	}
	metadataUser := map[string]any{
		"device_id":    anthropicDeviceID,
		"account_uuid": "",
		"session_id":   sessionID,
	}
	return []upstreamenvelope.BodyField{
		{Key: "model", Value: model},
		{Key: "messages", Value: messages},
		{Key: "system", Value: system},
		{Key: "tools", Value: []any{}},
		// 探测请求必须轻量（max_tokens 1024、不带 thinking/effort）：非流式
		// 探测要等完整响应，推理增强会让首字节超过 Cloudflare 类代理的 100s
		// 上限（上游回 524），也远超手动测试 60s 诊断预算（BUG-0201）。
		{Key: "metadata", Value: map[string]any{"user_id": metadataUser}},
		{Key: "stream", Value: stream},
	}
}

// probeSpeechDefaultVoice 是目录 voices 未承载时的探针音色缺省（任务报告
// 标注 voices 清单缺口；上游对不支持音色回 400，探针按失败证据呈现）。
const probeSpeechDefaultVoice = "alloy"

// probeWAVPayload 是 STT 探针的内置小 WAV 样本：合法 RIFF/WAVE 头（PCM
// 24kHz/16bit/mono）+ 极短静音帧。合成思路与 shared/platform/mockupstream
// payload_audio.go 的 wavPayload 同构，但 accountprobe 不 import mockupstream
// （测试基建互引），在本包内内置等价最小字节。
func probeWAVPayload() []byte {
	const (
		sampleRate = 24000
		channels   = 1
		bits       = 16
		frames     = 240 // 10ms 静音（24000Hz * 0.01s）
	)
	data := make([]byte, frames*channels*(bits/8))
	blockAlign := channels * bits / 8
	b := make([]byte, 0, 44+len(data))
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(36+len(data)))
	b = append(b, "WAVE"...)
	b = append(b, "fmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1) // PCM
	b = binary.LittleEndian.AppendUint16(b, channels)
	b = binary.LittleEndian.AppendUint32(b, sampleRate)
	b = binary.LittleEndian.AppendUint32(b, sampleRate*uint32(blockAlign))
	b = binary.LittleEndian.AppendUint16(b, uint16(blockAlign))
	b = binary.LittleEndian.AppendUint16(b, bits)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	b = append(b, data...)
	return b
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
