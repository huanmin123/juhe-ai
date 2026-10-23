package accountscore

import (
	"fmt"
	"sort"
	"strings"

	capabilitydomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// AI 账户能力域拆分（PLAN-20261011T141004000Z）：服务端统一保存规则的
// 允许集计算（计划 §服务端统一校验）。
//
//   - 允许能力集（allowed）＝ 域 lane 集 × 显式供应商矩阵门（
//     capabilityDomainMatrix，计划《域 × 供应商合法矩阵》的常量实现）×
//     既有写侧 per-provider×type 词表。词表只复用 endpointmodes.go 的既有
//     集合与门面 driver 路由语义（resolveCapabilityWriteContext），不定义
//     任何新模式值（禁止新造词表）。
//   - 默认能力集（default）＝ 新建账户 modes 缺省时按域落默认集；chat 域缺省
//     ⊆ 域允许集（BUG-0323 根治口径）：现行默认本就域内的供应商（openai 族/
//     gemini/anthropic/glm）逐项不变，媒体优先供应商（volcengine/qwen 缺省
//     video_get）落词表内基础对话对，由测试钉住。
//   - 严格不变式：账户声明集（credentials JSON 的 supported_endpoint_modes）
//     必须 ⊆ 所在域允许集，全链路强制——新建/导入/克隆/编辑/批量同一规则，
//     越界一律拒绝（CapabilityDomainOutlierError），不存在任何跨域保留
//     状态；声明集裁剪由 maintenance 迁移命令一次性完成。

// 域 lane 集（计划 §域模型设计 + §对话域纯粹化 v8-A）：chat = 平台包
// 「对话 modes」13 值锚集（v8-A 后不追加 images_json——图像 lane 移出对话
// 域，新增声明一律隔离在各域之内）；video = 视频任务四值；audio = 语音合成
// + 长音频任务四值 + 短转写 + 实时会话（v8-A 自对话域移入）；image =
// images_json。
var (
	videoDomainLaneModes = []string{"video_create", "video_get", "video_content", "video_cancel"}
	audioJobLaneModes    = []string{"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel"}
	audioSpeechLaneModes = []string{"audio_speech"}
	// v8-A 对话域纯粹化：短转写与实时会话归音频域 lane 集。
	audioTranscriptionLaneModes = []string{"audio_transcription_json"}
	realtimeSessionLaneModes    = []string{"realtime_session"}
	imageDomainLaneModes        = []string{"images_json"}
)

// CapabilityDomainLabels 是四个域的用户可见名称（错误文案用）。
var CapabilityDomainLabels = map[string]string{
	CapabilityDomainChat:  "对话",
	CapabilityDomainVideo: "视频",
	CapabilityDomainAudio: "音频",
	CapabilityDomainImage: "图像",
}

// CapabilityDomainLabel 返回域的用户可见名称；未知域原样返回。
func CapabilityDomainLabel(domain string) string {
	if label, ok := CapabilityDomainLabels[domain]; ok {
		return label
	}
	return domain
}

// chatDomainLaneModes 返回对话域 lane 集（平台包 13 值锚集；v8-A 后不追加
// images_json，图像 lane 声明只走图像域）。
func chatDomainLaneModes() []string {
	return capabilitydomain.ChatModes()
}

// domainLaneModes 返回域 lane 集；未知/空域返回 nil。
func domainLaneModes(domain string) []string {
	switch domain {
	case CapabilityDomainChat:
		return chatDomainLaneModes()
	case CapabilityDomainVideo:
		return append([]string{}, videoDomainLaneModes...)
	case CapabilityDomainAudio:
		return append(append(append(append([]string{},
			audioSpeechLaneModes...), audioJobLaneModes...),
			audioTranscriptionLaneModes...), realtimeSessionLaneModes...)
	case CapabilityDomainImage:
		return append([]string{}, imageDomainLaneModes...)
	}
	return nil
}

// openAIFamilyVocabulary 是 openai 族上下文（openai-compatible/gpt/xai/
// deepseek-openai）的写侧词表：oauth 账户按既有
// assertOpenAIEndpointModesCompatible 收窄为 Responses 对，其余为全词表。
func openAIFamilyVocabulary(accountType string) []string {
	if accountType == "oauth" {
		return append([]string{}, OpenAIResponsesEndpointModes...)
	}
	return append([]string{}, OpenAIEndpointModeValues...)
}

// glmOpenAIVocabulary 是 glm OpenAI 协议档案的写侧词表（chat 对 + M3 视频
// opt-in + M6 语音透传 opt-in；与门面 normalizeGLMEndpointModesForWrite 的
// 收敛集同源）。
func glmOpenAIVocabulary() []string {
	return append(append(append([]string{}, OpenAIChatEndpointModes...), GlmVideoEndpointModeValues...), GlmSpeechEndpointModeValues...)
}

// 矩阵行标识：与计划《域 × 供应商合法矩阵》的行一一对应（capabilityDomainMatrix
// 注释给出逐行对照）。路由语义与门面 providerAccountCredentialDrivers 一致。
const (
	capabilityMatrixOpenAI           = "openai"               // 「openai / gpt」行（openai-compatible 与 gpt driver，含 oauth）
	capabilityMatrixXai              = "xai"                  // 「xai」行（audio 待契约回填后随矩阵更新开通）
	capabilityMatrixDeepSeekOpenAI   = "deepseek-openai"      // 「deepseek（openai 档案）」行
	capabilityMatrixAnthropic        = "anthropic"            // 「anthropic / deepseek-anthropic」行（anthropic provider 与 deepseek anthropic pin 档案）
	capabilityMatrixGlmOpenAI        = "glm-openai"           // 「glm（openai 档案）」行（general + coding）
	capabilityMatrixGlmAnthropic     = "glm-coding-anthropic" // 「glm-coding-anthropic」行（glm anthropic 路由整体）
	capabilityMatrixGeminiNative     = "gemini-native"        // 「gemini native」行
	capabilityMatrixGeminiOpenAIChat = "gemini-openai-chat"   // 「gemini（OpenAI Chat 档案）」行
	capabilityMatrixMinimax          = "minimax"              // 「minimax」行
	capabilityMatrixVolcengine       = "volcengine"           // 「volcengine」行
	capabilityMatrixQwen             = "qwen"                 // 「qwen」行
)

// capabilityDomainMatrix 是域 × 供应商合法矩阵的显式常量表（计划《域 × 供应
// 商合法矩阵》，2026-10-12 主代理裁决：矩阵是产品事实，开通判据 = 已实施
// adapter + 写侧校验 + 凭据要求齐备；标「—」的格一律拒绝，不允许词表求交的
// 自然结果，避免造出必然失败的问题账户）。矩阵门与词表求交叠加，单收敛点：
// AllowedEndpointModes 与 DefaultEndpointModesForDomain 共用
// resolveCapabilityWriteContext 的行判定，导入/批量经同一对函数自动覆盖。
// xai 行 audio 位待上游契约回填后随计划矩阵更新开通。
var capabilityDomainMatrix = map[string]map[string]bool{
	capabilityMatrixOpenAI:           {CapabilityDomainChat: true, CapabilityDomainVideo: true, CapabilityDomainAudio: true, CapabilityDomainImage: true},
	capabilityMatrixXai:              {CapabilityDomainChat: true, CapabilityDomainVideo: true, CapabilityDomainAudio: false, CapabilityDomainImage: false},
	capabilityMatrixDeepSeekOpenAI:   {CapabilityDomainChat: true, CapabilityDomainVideo: false, CapabilityDomainAudio: false, CapabilityDomainImage: false},
	capabilityMatrixAnthropic:        {CapabilityDomainChat: true, CapabilityDomainVideo: false, CapabilityDomainAudio: false, CapabilityDomainImage: false},
	capabilityMatrixGlmOpenAI:        {CapabilityDomainChat: true, CapabilityDomainVideo: true, CapabilityDomainAudio: true, CapabilityDomainImage: false},
	capabilityMatrixGlmAnthropic:     {CapabilityDomainChat: true, CapabilityDomainVideo: false, CapabilityDomainAudio: false, CapabilityDomainImage: false},
	capabilityMatrixGeminiNative:     {CapabilityDomainChat: true, CapabilityDomainVideo: true, CapabilityDomainAudio: true, CapabilityDomainImage: false},
	capabilityMatrixGeminiOpenAIChat: {CapabilityDomainChat: true, CapabilityDomainVideo: false, CapabilityDomainAudio: false, CapabilityDomainImage: false},
	capabilityMatrixMinimax:          {CapabilityDomainChat: false, CapabilityDomainVideo: true, CapabilityDomainAudio: true, CapabilityDomainImage: false},
	// volcengine 行图像格 2026-10-17 开通（真实上游取证：ark 官方 OpenAI
	// 兼容图像端点 /api/v3/images/generations 实测出图成功
	//（doubao-seedream-5-0-flash-260915，OpenAI images 形态响应 data[].url +
	// usage）；Agent Plan 专属端点 /api/plan/v3/images/generations 实测可用，
	// 出站 URL 由链上 chainVolcengineImagesUpstreamURL 服务根归一承接）。
	capabilityMatrixVolcengine: {CapabilityDomainChat: true, CapabilityDomainVideo: true, CapabilityDomainAudio: true, CapabilityDomainImage: true},
	capabilityMatrixQwen:       {CapabilityDomainChat: true, CapabilityDomainVideo: true, CapabilityDomainAudio: true, CapabilityDomainImage: false},
}

// capabilityDomainMatrixAllows 查矩阵门：行未列出（未知上下文）默认拒绝，
// 与计划「矩阵未列出的组合默认拒绝」一致。
func capabilityDomainMatrixAllows(row, domain string) bool {
	allowed, ok := capabilityDomainMatrix[row]
	if !ok {
		return false
	}
	return allowed[domain]
}

// profileMatches 精确匹配协议档案 ID（设计 §2.1：
// provider_protocol_profile_id 列退役后，档案由连接 preset 派生，可能为空）。
func profileMatches(profileID string, expected ...string) bool {
	for _, candidate := range expected {
		if profileID == candidate {
			return true
		}
	}
	return false
}

// profileUnpinnedMatches 供「一族只有一个档案」的分支使用：档案为空时由
// vendor + 协议面唯一确定该分支，按通配放行；有多档案的 vendor（deepseek /
// glm）必须先用协议面区分族，不能走这里。
func profileUnpinnedMatches(profileID string, expected ...string) bool {
	return profileID == "" || profileMatches(profileID, expected...)
}

// resolveCapabilityWriteContext 解析上下文（provider×type×协议档案）对应的
// 矩阵行与既有写侧词表——词表即当前归一化路径（providerAccountCredentialDrivers
// 逐个 driver 的 normalizeForWrite 值域校验 + 类型断言）可接受的全部模式值，
// 矩阵行即 capabilityDomainMatrix 的键。路由顺序镜像门面 driver 注册表；
// 未注册上下文返回空行与 nil 词表（允许集为空 = 该上下文不支持任何域的
// 账户声明）。
func resolveCapabilityWriteContext(providerCode, accountType string, profile ProtocolPredicate) (string, []string) {
	provider := NormalizeProviderToken(providerCode)
	protocol := NormalizeProviderToken(profile.ProtocolCode)
	if provider == "" && protocol == "" {
		// 门面 providerAccountCredentialDriverForContext：双空 token 回退
		// openai-compatible driver。
		return capabilityMatrixOpenAI, openAIFamilyVocabulary(accountType)
	}
	profileID := profile.ProviderProtocolProfileID
	openaiProtocol := IsOpenAIProtocolProfileOf(profile)
	anthropicProtocol := IsAnthropicProtocolProfileOf(profile)
	// 供应商解耦（设计 §2.1/§9.11）：generic_http 账户 vendor 为空，接口能力
	// 矩阵行由协议面决定（openai/anthropic/gemini 三个 generic preset）。
	if provider == "" {
		switch {
		case openaiProtocol:
			return capabilityMatrixOpenAI, openAIFamilyVocabulary(accountType)
		case anthropicProtocol:
			return capabilityMatrixAnthropic, append([]string{}, AnthropicEndpointModeValues...)
		case IsGeminiProtocolProfileOf(profile):
			return capabilityMatrixGeminiNative, append([]string{}, GeminiEndpointModeValues...)
		}
	}
	switch {
	case provider == OpenAICompatibleProviderCode && openaiProtocol:
		return capabilityMatrixOpenAI, openAIFamilyVocabulary(accountType)
	case provider == GeminiProviderCode && profileUnpinnedMatches(profileID, GeminiOpenAIChatV1BetaProfileID) && openaiProtocol:
		return capabilityMatrixGeminiOpenAIChat, openAIFamilyVocabulary(accountType)
	case provider == GptVendorCode && openaiProtocol:
		return capabilityMatrixOpenAI, openAIFamilyVocabulary(accountType)
	case (accountType == "api_key" || accountType == "oauth") &&
		provider == XaiProviderCode && openaiProtocol && profileUnpinnedMatches(profileID, XaiOpenAIV1ProfileID):
		return capabilityMatrixXai, openAIFamilyVocabulary(accountType)
	case IsDeepSeekProviderCodeToken(providerCode) &&
		(anthropicProtocol || (protocol == "" && profileMatches(profileID, DeepSeekAnthropicV1ProfileID))):
		// deepseek anthropic 档案 pin：normalizeDeepSeekEndpointModesForWrite
		// 收敛为 Messages 对；矩阵按「anthropic / deepseek-anthropic」行收紧。
		return capabilityMatrixAnthropic, append([]string{}, AnthropicMessagesEndpointModeValues...)
	case IsDeepSeekProviderCodeToken(providerCode) && openaiProtocol && profileUnpinnedMatches(profileID, DeepSeekOpenAIV1ProfileID):
		return capabilityMatrixDeepSeekOpenAI, openAIFamilyVocabulary(accountType)
	case provider == AnthropicProviderCode && anthropicProtocol:
		return capabilityMatrixAnthropic, append([]string{}, AnthropicEndpointModeValues...)
	case provider == GeminiProviderCode && IsGeminiProtocolProfileOf(profile):
		return capabilityMatrixGeminiNative, append([]string{}, GeminiEndpointModeValues...)
	case IsGlmProviderCodeToken(providerCode) &&
		(anthropicProtocol || (protocol == "" && profileMatches(profileID, GlmCodingAnthropicV1ProfileID))):
		return capabilityMatrixGlmAnthropic, append([]string{}, AnthropicMessagesEndpointModeValues...)
	case IsGlmProviderCodeToken(providerCode) && openaiProtocol &&
		profileUnpinnedMatches(profileID, GlmGeneralOpenAIV1ProfileID, GlmCodingOpenAIV1ProfileID):
		return capabilityMatrixGlmOpenAI, glmOpenAIVocabulary()
	case IsMinimaxProviderCodeToken(providerCode) && openaiProtocol && profileUnpinnedMatches(profileID, MinimaxOpenAIV1ProfileID):
		return capabilityMatrixMinimax, append([]string{}, MinimaxMediaEndpointModeValues...)
	case IsVolcengineProviderCodeToken(providerCode) && openaiProtocol && profileUnpinnedMatches(profileID, VolcengineOpenAIV1ProfileID):
		return capabilityMatrixVolcengine, append([]string{}, VolcengineEndpointModeValues...)
	case IsQwenProviderCodeToken(providerCode) && openaiProtocol && profileUnpinnedMatches(profileID, QwenOpenAIV1ProfileID):
		return capabilityMatrixQwen, append([]string{}, QwenEndpointModeValues...)
	}
	return "", nil
}

// legacyDefaultEndpointModes 返回上下文的现行（chat 时代）写侧默认集——
// 即 modes 键缺省时各 driver 归一化落下的集合。路由与集合全部复用既有
// 默认解析（DefaultOpenAI/Anthropic/GeminiEndpointModes + 门面
// minimax/volcengine/qwen 的固定默认），逐项不改（I3）。
func legacyDefaultEndpointModes(providerCode, accountType string, profile ProtocolPredicate) []string {
	provider := NormalizeProviderToken(providerCode)
	protocol := NormalizeProviderToken(profile.ProtocolCode)
	if provider == "" && protocol == "" {
		return DefaultOpenAIEndpointModes(ModeDefaultContext{AccountType: accountType})
	}
	profileID := profile.ProviderProtocolProfileID
	openaiProtocol := IsOpenAIProtocolProfileOf(profile)
	anthropicProtocol := IsAnthropicProtocolProfileOf(profile)
	openAIDefaults := func() []string {
		return DefaultOpenAIEndpointModes(ModeDefaultContext{ProviderCode: provider, AccountType: accountType})
	}
	switch {
	case (provider == OpenAICompatibleProviderCode ||
		(provider == GeminiProviderCode && profileUnpinnedMatches(profileID, GeminiOpenAIChatV1BetaProfileID))) && openaiProtocol:
		return openAIDefaults()
	case provider == GptVendorCode && openaiProtocol:
		return openAIDefaults()
	case (accountType == "api_key" || accountType == "oauth") &&
		provider == XaiProviderCode && openaiProtocol && profileUnpinnedMatches(profileID, XaiOpenAIV1ProfileID):
		// xai driver pin：normalizeOpenAIEndpointModesForWrite 按 xai 档案取
		// 默认（DefaultOpenAIEndpointModes 只看 provider+type，xai 落四值）。
		return DefaultOpenAIEndpointModes(ModeDefaultContext{ProviderCode: XaiProviderCode, AccountType: accountType})
	case IsDeepSeekProviderCodeToken(providerCode) &&
		(anthropicProtocol || (protocol == "" && profileMatches(profileID, DeepSeekAnthropicV1ProfileID))):
		return DefaultAnthropicEndpointModes(ModeDefaultContext{ProviderProtocolProfileID: DeepSeekAnthropicV1ProfileID})
	case IsDeepSeekProviderCodeToken(providerCode) && openaiProtocol && profileUnpinnedMatches(profileID, DeepSeekOpenAIV1ProfileID):
		return DefaultOpenAIEndpointModes(ModeDefaultContext{ProviderCode: DeepSeekProviderCode, AccountType: accountType})
	case provider == AnthropicProviderCode && anthropicProtocol:
		return DefaultAnthropicEndpointModes(ModeDefaultContext{ProviderProtocolProfileID: profileID})
	case provider == GeminiProviderCode && IsGeminiProtocolProfileOf(profile):
		return DefaultGeminiEndpointModes(ModeDefaultContext{})
	case IsGlmProviderCodeToken(providerCode) &&
		(anthropicProtocol || (protocol == "" && profileMatches(profileID, GlmCodingAnthropicV1ProfileID))):
		return DefaultAnthropicEndpointModes(ModeDefaultContext{ProviderProtocolProfileID: GlmCodingAnthropicV1ProfileID})
	case IsGlmProviderCodeToken(providerCode) && openaiProtocol &&
		profileUnpinnedMatches(profileID, GlmGeneralOpenAIV1ProfileID, GlmCodingOpenAIV1ProfileID):
		return DefaultOpenAIEndpointModes(ModeDefaultContext{ProviderCode: GlmProviderCode, AccountType: accountType})
	case IsMinimaxProviderCodeToken(providerCode) && openaiProtocol && profileUnpinnedMatches(profileID, MinimaxOpenAIV1ProfileID):
		return []string{"audio_speech"}
	case IsVolcengineProviderCodeToken(providerCode) && openaiProtocol && profileUnpinnedMatches(profileID, VolcengineOpenAIV1ProfileID):
		return []string{"video_get"}
	case IsQwenProviderCodeToken(providerCode) && openaiProtocol && profileUnpinnedMatches(profileID, QwenOpenAIV1ProfileID):
		return []string{"video_get"}
	}
	return nil
}

// AllowedEndpointModes 返回上下文的允许能力集：显式供应商矩阵门 × 域 lane
// 集 × 既有写侧词表。矩阵标「—」的格（如 anthropic+video、xai+audio、
// deepseek+video、gemini OpenAI Chat 档案+video、glm+image、minimax+chat）
// 与词表交集为空的组合（gpt oauth+video、qwen audio 的 audio_speech）、
// 域非法与未注册上下文一律返回空集——调用方把空集视为「该供应商不支持该域」。
func AllowedEndpointModes(domain, providerCode, accountType string, profile ProtocolPredicate) []string {
	row, vocabulary := resolveCapabilityWriteContext(providerCode, accountType, profile)
	if len(vocabulary) == 0 {
		return []string{}
	}
	if !capabilityDomainMatrixAllows(row, domain) {
		return []string{}
	}
	lanes := domainLaneModes(domain)
	if lanes == nil {
		return []string{}
	}
	return intersectLaneModes(lanes, vocabulary)
}

// DefaultEndpointModesForDomain 返回新建账户 modes 缺省时按域落下的默认集：
// chat 域=现行默认与域 lane 集、词表求交（BUG-0323 根治：缺省恒 ⊆ 域允许集；
// 现行默认本就域内的供应商逐项不变，媒体优先供应商缺省不含对话值时落词表内
// 基础对话对）；video=四值组；audio=audio_speech
// （无 audio_speech 词表的供应商退为长音频四值，如 qwen）；image=
// images_json——经同一矩阵门与写侧词表求交（与 AllowedEndpointModes 单收敛
// 点），空集视为该供应商不支持该域。v8-A：音频域默认集维持现状语义，不含
// 新移入的 audio_transcription_json / realtime_session（默认集不收，显式
// 勾选承担，契约 §媒体域表单完整性升级 v8-B 联动明文），纯转写账户显式选
// audio_transcription_json。
func DefaultEndpointModesForDomain(domain, providerCode, accountType string, profile ProtocolPredicate) []string {
	row, vocabulary := resolveCapabilityWriteContext(providerCode, accountType, profile)
	if len(vocabulary) == 0 {
		return []string{}
	}
	if !capabilityDomainMatrixAllows(row, domain) {
		return []string{}
	}
	switch domain {
	case CapabilityDomainChat:
		inDomain := intersectLaneModes(intersectLaneModes(legacyDefaultEndpointModes(providerCode, accountType, profile), vocabulary), chatDomainLaneModes())
		if len(inDomain) > 0 {
			return inDomain
		}
		// 现行默认不含对话值（媒体优先供应商，如 volcengine/qwen 缺省
		// video_get）：落词表内基础对话对，保证 chat 域缺省恒 ⊆ 允许集
		// （BUG-0323 根治）。
		return intersectLaneModes([]string{"chat_json", "chat_sse"}, vocabulary)
	case CapabilityDomainVideo:
		return intersectLaneModes(videoDomainLaneModes, vocabulary)
	case CapabilityDomainAudio:
		if speech := intersectLaneModes(audioSpeechLaneModes, vocabulary); len(speech) > 0 {
			return speech
		}
		return intersectLaneModes(audioJobLaneModes, vocabulary)
	case CapabilityDomainImage:
		return intersectLaneModes(imageDomainLaneModes, vocabulary)
	}
	return []string{}
}

// DefaultHealthCheckEndpointModeForDomain 是新建账户
// health_check_endpoint_mode 的按域默认落值（媒体账户生命周期定案表）：
// chat=chat_sse（流式优先，AI账户检查模型与人工测试设计 2026-10-19 定案；
// 原 chat_json）、image=images_json、audio=audio_speech、video=video_create。
// 仅入参未显式提供时使用。
func DefaultHealthCheckEndpointModeForDomain(domain string) string {
	switch domain {
	case CapabilityDomainVideo:
		return "video_create"
	case CapabilityDomainAudio:
		return "audio_speech"
	case CapabilityDomainImage:
		return "images_json"
	}
	return "chat_sse"
}

// intersectLaneModes 按 lanes 顺序保留 vocab 内的成员（去重由 lanes 本身
// 无重保证；空交集返回空切片）。
func intersectLaneModes(lanes, vocabulary []string) []string {
	set := StringSet(vocabulary)
	out := []string{}
	for _, mode := range lanes {
		if set[mode] {
			out = append(out, mode)
		}
	}
	return out
}

// OutOfDomainModes 返回声明集中域允许集之外的值（排序去重），供错误文案
// 列出越界值。
func OutOfDomainModes(declared, allowed []string) []string {
	permitted := StringSet(allowed)
	outliers := []string{}
	seen := map[string]bool{}
	for _, mode := range declared {
		if permitted[mode] || seen[mode] {
			continue
		}
		seen[mode] = true
		outliers = append(outliers, mode)
	}
	sort.Strings(outliers)
	return outliers
}

// CapabilityDomainUnsupportedError 渲染「{供应商} 不支持{域}账户」400。
func CapabilityDomainUnsupportedError(providerCode, domain string) error {
	return &ValidationError{Message: fmt.Sprintf("%s 不支持%s账户", providerCode, CapabilityDomainLabel(domain))}
}

// CapabilityDomainOutlierError 渲染新建声明越界的 400（列出越界值）。
func CapabilityDomainOutlierError(domain string, outliers []string) error {
	return &ValidationError{Message: fmt.Sprintf("账户能力域%s不支持上游接口能力：%s",
		CapabilityDomainLabel(domain), strings.Join(outliers, "、"))}
}
