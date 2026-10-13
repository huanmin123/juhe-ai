package accountscore

import (
	"fmt"
	"sort"
	"strings"

	capabilitydomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// AI 账户能力域拆分 M2（PLAN-20261011T141004000Z）：服务端统一保存规则的
// 允许集计算（计划 §服务端统一校验 + §关键决策与理由 D6 三集合模型）。
//
//   - 允许能力集（allowed）＝ 域 lane 集 × 显式供应商矩阵门（
//     capabilityDomainMatrix，计划《域 × 供应商合法矩阵》的常量实现）×
//     既有写侧 per-provider×type 词表。词表只复用 endpointmodes.go 的既有
//     集合与门面 driver 路由语义（resolveCapabilityWriteContext），不定义
//     任何新模式值（禁止新造词表）。
//   - 默认能力集（default）＝ 新建账户 modes 缺省时按域落默认集；chat 域与
//     现行归一化默认逐项一致（I3），由测试钉住。
//   - 实际声明集（declared）的域外遗留集合以服务端数据库现值为准（I7），
//     编辑按「遗留集合只缩不扩」校验（AssertCapabilityLegacyShrinkOnly，M3
//     放开缩小分支；清理动作单独审计）。

// 域 lane 集（计划 §域模型设计 + §对话域纯粹化 v8-A）：chat = 平台包
// 「对话 modes」13 值锚集（v8-A 后不再追加 images_json——图像 lane 移出对话
// 域，存量共享、新增隔离，遗留值由 D6 三集合机制承载）；video = 视频任务
// 四值；audio = 语音合成 + 长音频任务四值 + 短转写 + 实时会话（v8-A 自对话
// 域移入）；image = images_json。
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
// images_json，图像 lane 新增声明只走图像域，对话域存量声明成为域外遗留值）。
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
	capabilityMatrixHybrid           = "hybrid"               // 「hybrid」行
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
	capabilityMatrixHybrid:           {CapabilityDomainChat: true, CapabilityDomainVideo: true, CapabilityDomainAudio: true, CapabilityDomainImage: true},
	capabilityMatrixMinimax:          {CapabilityDomainChat: false, CapabilityDomainVideo: true, CapabilityDomainAudio: true, CapabilityDomainImage: false},
	capabilityMatrixVolcengine:       {CapabilityDomainChat: true, CapabilityDomainVideo: true, CapabilityDomainAudio: true, CapabilityDomainImage: false},
	capabilityMatrixQwen:             {CapabilityDomainChat: true, CapabilityDomainVideo: true, CapabilityDomainAudio: true, CapabilityDomainImage: false},
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
	switch {
	case provider == OpenAICompatibleProviderCode && openaiProtocol:
		return capabilityMatrixOpenAI, openAIFamilyVocabulary(accountType)
	case provider == GeminiProviderCode && profileID == GeminiOpenAIChatV1BetaProfileID && openaiProtocol:
		return capabilityMatrixGeminiOpenAIChat, openAIFamilyVocabulary(accountType)
	case provider == GptVendorCode && openaiProtocol:
		return capabilityMatrixOpenAI, openAIFamilyVocabulary(accountType)
	case (accountType == "api_key" || accountType == "oauth") &&
		provider == XaiProviderCode && openaiProtocol && profileID == XaiOpenAIV1ProfileID:
		return capabilityMatrixXai, openAIFamilyVocabulary(accountType)
	case IsDeepSeekProviderCodeToken(providerCode) &&
		(anthropicProtocol || profileID == DeepSeekAnthropicV1ProfileID):
		// deepseek anthropic 档案 pin：normalizeDeepSeekEndpointModesForWrite
		// 收敛为 Messages 对；矩阵按「anthropic / deepseek-anthropic」行收紧。
		return capabilityMatrixAnthropic, append([]string{}, AnthropicMessagesEndpointModeValues...)
	case IsDeepSeekProviderCodeToken(providerCode) && openaiProtocol && profileID == DeepSeekOpenAIV1ProfileID:
		return capabilityMatrixDeepSeekOpenAI, openAIFamilyVocabulary(accountType)
	case provider == AnthropicProviderCode && anthropicProtocol:
		return capabilityMatrixAnthropic, append([]string{}, AnthropicEndpointModeValues...)
	case provider == GeminiProviderCode && IsGeminiProtocolProfileOf(profile):
		return capabilityMatrixGeminiNative, append([]string{}, GeminiEndpointModeValues...)
	case IsGlmProviderCodeToken(providerCode) &&
		(anthropicProtocol || profileID == GlmCodingAnthropicV1ProfileID):
		return capabilityMatrixGlmAnthropic, append([]string{}, AnthropicMessagesEndpointModeValues...)
	case IsGlmProviderCodeToken(providerCode) && openaiProtocol &&
		(profileID == GlmGeneralOpenAIV1ProfileID || profileID == GlmCodingOpenAIV1ProfileID):
		return capabilityMatrixGlmOpenAI, glmOpenAIVocabulary()
	case IsMinimaxProviderCodeToken(providerCode) && openaiProtocol && profileID == MinimaxOpenAIV1ProfileID:
		return capabilityMatrixMinimax, append([]string{}, MinimaxMediaEndpointModeValues...)
	case IsVolcengineProviderCodeToken(providerCode) && openaiProtocol && profileID == VolcengineOpenAIV1ProfileID:
		return capabilityMatrixVolcengine, append([]string{}, VolcengineEndpointModeValues...)
	case IsQwenProviderCodeToken(providerCode) && openaiProtocol && profileID == QwenOpenAIV1ProfileID:
		return capabilityMatrixQwen, append([]string{}, QwenEndpointModeValues...)
	case IsHybridProviderCodeToken(providerCode):
		return capabilityMatrixHybrid, append([]string{}, HybridEndpointModeValues...)
	}
	return "", nil
}

// legacyDefaultEndpointModes 返回上下文的现行（chat 时代）写侧默认集——
// 即 modes 键缺省时各 driver 归一化落下的集合。路由与集合全部复用既有
// 默认解析（DefaultOpenAI/Anthropic/GeminiEndpointModes + 门面
// minimax/volcengine/qwen/hybrid 的固定默认），逐项不改（I3）。
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
		(provider == GeminiProviderCode && profileID == GeminiOpenAIChatV1BetaProfileID)) && openaiProtocol:
		return openAIDefaults()
	case provider == GptVendorCode && openaiProtocol:
		return openAIDefaults()
	case (accountType == "api_key" || accountType == "oauth") &&
		provider == XaiProviderCode && openaiProtocol && profileID == XaiOpenAIV1ProfileID:
		// xai driver pin：normalizeOpenAIEndpointModesForWrite 按 xai 档案取
		// 默认（DefaultOpenAIEndpointModes 只看 provider+type，xai 落四值）。
		return DefaultOpenAIEndpointModes(ModeDefaultContext{ProviderCode: XaiProviderCode, AccountType: accountType})
	case IsDeepSeekProviderCodeToken(providerCode) &&
		(anthropicProtocol || profileID == DeepSeekAnthropicV1ProfileID):
		return DefaultAnthropicEndpointModes(ModeDefaultContext{ProviderProtocolProfileID: DeepSeekAnthropicV1ProfileID})
	case IsDeepSeekProviderCodeToken(providerCode) && openaiProtocol && profileID == DeepSeekOpenAIV1ProfileID:
		return DefaultOpenAIEndpointModes(ModeDefaultContext{ProviderCode: DeepSeekProviderCode, AccountType: accountType})
	case provider == AnthropicProviderCode && anthropicProtocol:
		return DefaultAnthropicEndpointModes(ModeDefaultContext{ProviderProtocolProfileID: profileID})
	case provider == GeminiProviderCode && IsGeminiProtocolProfileOf(profile):
		return DefaultGeminiEndpointModes(ModeDefaultContext{})
	case IsGlmProviderCodeToken(providerCode) &&
		(anthropicProtocol || profileID == GlmCodingAnthropicV1ProfileID):
		return DefaultAnthropicEndpointModes(ModeDefaultContext{ProviderProtocolProfileID: GlmCodingAnthropicV1ProfileID})
	case IsGlmProviderCodeToken(providerCode) && openaiProtocol &&
		(profileID == GlmGeneralOpenAIV1ProfileID || profileID == GlmCodingOpenAIV1ProfileID):
		return DefaultOpenAIEndpointModes(ModeDefaultContext{ProviderCode: GlmProviderCode, AccountType: accountType})
	case IsMinimaxProviderCodeToken(providerCode) && openaiProtocol && profileID == MinimaxOpenAIV1ProfileID:
		return []string{"audio_speech"}
	case IsVolcengineProviderCodeToken(providerCode) && openaiProtocol && profileID == VolcengineOpenAIV1ProfileID:
		return []string{"video_get"}
	case IsQwenProviderCodeToken(providerCode) && openaiProtocol && profileID == QwenOpenAIV1ProfileID:
		return []string{"video_get"}
	case IsHybridProviderCodeToken(providerCode):
		return append([]string{}, HybridEndpointModeValues...)
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
// chat 域=现行默认（逐项不变，I3）；video=四值组；audio=audio_speech
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
		return intersectLaneModes(legacyDefaultEndpointModes(providerCode, accountType, profile), vocabulary)
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
// chat=chat_json（现行硬编码）、image=images_json、audio=audio_speech、
// video=video_create。仅入参未显式提供时使用。
func DefaultHealthCheckEndpointModeForDomain(domain string) string {
	switch domain {
	case CapabilityDomainVideo:
		return "video_create"
	case CapabilityDomainAudio:
		return "audio_speech"
	case CapabilityDomainImage:
		return "images_json"
	}
	return "chat_json"
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

// CapabilityDomainLegacyDriftError 渲染编辑遗留集合变化的 400（D6：域外
// 能力不可变更，显式清理是 M3 提示动作）。
func CapabilityDomainLegacyDriftError() error {
	return &ValidationError{Message: "域外能力不可变更（仅可通过显式清理缩小）"}
}

// AssertCapabilityLegacyShrinkOnly 执行 D6 保存规则的 M3 放开分支（计划
// §D6 显式清理）：newDeclared−allowed ⊆ oldDeclared−allowed 时放行并返回被
// 清理的遗留值（oldLegacy−newLegacy，排序去重；空切片 = 无清理）；扩大或
// 换元（减一加一）一律拒绝。allowed 为 nil 时按空集处理。调用方必须以
// 「请求显式携带 supported_endpoint_modes 数组」为放行前提（清理动作的提交
// 形态）；删键（null）路径走归一化默认集重置，默认集不含遗留值会借归一化
// 静默删除遗留能力（I7），必须改走 AssertCapabilityLegacySetsEqual 逐项相等
// 拦截。清理动作由调用方落单独审计（操作日志「清理域外能力」）。
func AssertCapabilityLegacyShrinkOnly(oldDeclared, newDeclared, allowed []string) (removed []string, err error) {
	oldLegacy := sortedDifference(oldDeclared, allowed)
	newLegacy := sortedDifference(newDeclared, allowed)
	newSet := StringSet(newLegacy)
	oldSet := StringSet(oldLegacy)
	for _, mode := range oldLegacy {
		if !newSet[mode] {
			removed = append(removed, mode)
		}
	}
	for _, mode := range newLegacy {
		if !oldSet[mode] {
			return nil, CapabilityDomainLegacyDriftError()
		}
	}
	return removed, nil
}

// AssertCapabilityLegacySetsEqual 执行 D6 普通编辑保存规则（M2 基线，M3 仍
// 约束非显式提交路径）：newDeclared−allowed 与 oldDeclared−allowed 两个遗留
// 集合排序后逐项相等，不等即拒绝（既不扩大也不缩小）。allowed 为 nil 时按
// 空集处理。
func AssertCapabilityLegacySetsEqual(oldDeclared, newDeclared, allowed []string) error {
	oldLegacy := sortedDifference(oldDeclared, allowed)
	newLegacy := sortedDifference(newDeclared, allowed)
	if len(oldLegacy) != len(newLegacy) {
		return CapabilityDomainLegacyDriftError()
	}
	for index := range oldLegacy {
		if oldLegacy[index] != newLegacy[index] {
			return CapabilityDomainLegacyDriftError()
		}
	}
	return nil
}

// sortedDifference 返回 values − allowed 的排序去重结果。
func sortedDifference(values, allowed []string) []string {
	permitted := StringSet(allowed)
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		if permitted[value] || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
