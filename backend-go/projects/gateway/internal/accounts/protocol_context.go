package accounts

// 账户级协议上下文推导（供应商与上游协议面重构设计 §1.1/§2.1/§9.3）。旧
// requireEnabledProviderProtocolProfile 从 provider_protocol_profiles 行读取
// (protocol_code, protocol_version, profile id)——该表已随目标 schema 退役
// （§9.3），写链不再查库取协议身份。语义替代：connection_preset_code 是 §1.1
// 编译期 registry 注册值，与旧 profile ID 一一对应（maintenance
// providerconnectionmigration legacyProfileTargets 同表），registry surface
// 即 (protocol_code, protocol_version)。
//
//   - 创建链（携 connections 载荷）：deriveConnectionProtocolContext 取载荷序
//     第一个非纯媒体连接的 registry surface 组装账户级上下文（过渡期
//     accounts.protocol_code / protocol_version 列与 legacy 写侧词表机器的
//     喂参；多连接跨协议面账户以首个文本连接为准，连接行自身仍逐行携带
//     registry surface，协议事实归属连接行）。
//   - 草稿/探针链（无 connections 载荷，model-catalog/refresh、balance/test-
//     draft）：载荷显式携带 providerProtocolProfileId 时按入参 ID 反查协议
//     上下文（draftProtocolContext，#B4 ⑩——gemini openai_chat 存量账户曾
//     被 vendor 默认 native 归一、openai 族端点模式落词表外 400）；无入参
//     或入参 ID 词表外回落 vendorDefaultProtocolContext（vendor 语义默认
//     档案，与退役前各 vendor 的默认 profile 行一致）。
//
// 本推导是 protocol 列退役（§9.11 后续切片）前的过渡投影；generic（openai/
// hybrid）与纯媒体集合无 pin，profileID 留空——openai-compatible / anthropic
// driver 路由只依赖 protocol 对。

import (
	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// protocolContext 是账户级协议上下文（accounts.protocol_code/protocol_version
// 列与 legacy 谓词族的过渡喂参）。
type protocolContext struct {
	protocolCode    string
	protocolVersion string
	profileID       string
}

// predicate 投影为 legacy 谓词入参（providerCode 由调用方按账户语义补齐）。
func (c protocolContext) predicate(providerCode string) protocolPredicateInput {
	return protocolPredicateInput{
		providerCode:              providerCode,
		protocolCode:              c.protocolCode,
		protocolVersion:           c.protocolVersion,
		providerProtocolProfileID: c.profileID,
	}
}

// openAIProtocolContext 是无 pin 的 openai 族上下文（generic / 纯媒体集合）。
func openAIProtocolContext() protocolContext {
	return protocolContext{
		protocolCode:    accountscore.OpenAIProtocolCode,
		protocolVersion: accountscore.OpenAIProtocolVersion,
	}
}

// surfaceProtocolContext 拆分 registry surface → 协议上下文（无 pin）。
func surfaceProtocolContext(surface string) (protocolContext, bool) {
	switch surface {
	case contracts.SurfaceOpenAIV1:
		return protocolContext{
			protocolCode:    accountscore.OpenAIProtocolCode,
			protocolVersion: accountscore.OpenAIProtocolVersion,
		}, true
	case contracts.SurfaceAnthropicV1:
		return protocolContext{
			protocolCode:    accountscore.AnthropicProtocolCode,
			protocolVersion: accountscore.AnthropicProtocolVersion,
		}, true
	case contracts.SurfaceGeminiV1Beta:
		return protocolContext{
			protocolCode:    accountscore.GeminiProtocolCode,
			protocolVersion: accountscore.GeminiProtocolVersion,
		}, true
	}
	return protocolContext{}, false
}

// presetProfileID 是 registry preset → 旧 profile ID pin 的固定对应表（§1.1
// 注册值一一对应；generic_*/anthropic_v1 官方预设无 pin）。
var presetProfileID = map[string]string{
	contracts.PresetGPTOpenAIV1:            accountscore.GptOpenAIV1ProfileID,
	contracts.PresetXAIOpenAIV1:            accountscore.XaiOpenAIV1ProfileID,
	contracts.PresetDeepSeekOpenAIV1:       accountscore.DeepSeekOpenAIV1ProfileID,
	contracts.PresetDeepSeekAnthropicV1:    accountscore.DeepSeekAnthropicV1ProfileID,
	contracts.PresetGLMGeneralOpenAIV1:     accountscore.GlmGeneralOpenAIV1ProfileID,
	contracts.PresetGLMCodingOpenAIV1:      accountscore.GlmCodingOpenAIV1ProfileID,
	contracts.PresetGLMCodingAnthropicV1:   accountscore.GlmCodingAnthropicV1ProfileID,
	contracts.PresetGeminiOpenAIChatV1Beta: accountscore.GeminiOpenAIChatV1BetaProfileID,
	contracts.PresetGeminiNativeV1Beta:     geminiNativeV1BetaProfileID,
	contracts.PresetMiniMaxMedia:           accountscore.MinimaxOpenAIV1ProfileID,
	contracts.PresetVolcengineArk:          accountscore.VolcengineOpenAIV1ProfileID,
	contracts.PresetVolcengineOpenSpeech:   accountscore.VolcengineOpenAIV1ProfileID,
	contracts.PresetQwenDashScope:          accountscore.QwenOpenAIV1ProfileID,
}

// deriveConnectionProtocolContext 从解析后的连接集合推导账户级协议上下文：
// 载荷序第一个「有 pin 预设或非纯媒体 surface」的连接定 (protocol, version,
// pin)。纯媒体预设（minimax_media / volcengine_openspeech，surface 为空）凭
// preset 对应表落 pin，协议对退 openai 族（媒体 adapter 的上游面恒为 OpenAI
// 形态，媒体设计 §9）——丢失 pin 会让 legacy 词表路由（resolveCapabilityWrite
// Context 的 per-vendor 行）解析失败、合法媒体域创建被误拒。无 pin 无 surface
// （generic 纯媒体集合）退 openai 族无 pin 上下文。
func deriveConnectionProtocolContext(resolved []resolvedConnection) protocolContext {
	for _, connection := range resolved {
		pin := presetProfileID[connection.preset]
		if pin == "" && connection.surface == "" {
			continue
		}
		context, ok := surfaceProtocolContext(connection.surface)
		if !ok {
			context = openAIProtocolContext()
		}
		context.profileID = pin
		return context
	}
	return openAIProtocolContext()
}

// draftProtocolContext 解析草稿链（model-catalog/refresh、balance/test-draft
// 的 prepareBalanceDraft）协议上下文：载荷显式携带 providerProtocolProfileId
// 时按入参 ID 反查（修复 #B4 ⑩：gemini openai_chat 存量账户带 openai 族端点
// 模式草稿，曾被 vendor 默认 native 上下文归一进 gemini 词表 → 组合词表外
// 400）；无入参或入参 ID 词表外回落 vendor 默认——无入参路径行为不变，词表
// 外 ID 无协议事实可推导，不新增 400 路径。
func draftProtocolContext(providerCode, profileID string) protocolContext {
	if profileID != "" {
		if context, ok := profileIDProtocolContext[profileID]; ok {
			return context
		}
	}
	return vendorDefaultProtocolContext(providerCode)
}

// profileIDProtocolContext 是草稿链入参 profileId 的反查表（§9.3 后 profile
// 行已退役，入参 ID 只能对 §1.1 编译期 registry 对应表落协议上下文）。键集
// 与 presetProfileID 的值集一一对应，同批维护（protocol_context_test 成对
// 断言钉住）；协议对按各档案的 registry surface 落（gemini openai_chat 为
// openai_v1 surface，native 为 gemini_v1beta；纯媒体预设同
// deriveConnectionProtocolContext 的 pin 保留、协议对退 openai 族）。
var profileIDProtocolContext = map[string]protocolContext{
	accountscore.GptOpenAIV1ProfileID: {
		protocolCode:    accountscore.OpenAIProtocolCode,
		protocolVersion: accountscore.OpenAIProtocolVersion,
		profileID:       accountscore.GptOpenAIV1ProfileID,
	},
	accountscore.XaiOpenAIV1ProfileID: {
		protocolCode:    accountscore.OpenAIProtocolCode,
		protocolVersion: accountscore.OpenAIProtocolVersion,
		profileID:       accountscore.XaiOpenAIV1ProfileID,
	},
	accountscore.DeepSeekOpenAIV1ProfileID: {
		protocolCode:    accountscore.OpenAIProtocolCode,
		protocolVersion: accountscore.OpenAIProtocolVersion,
		profileID:       accountscore.DeepSeekOpenAIV1ProfileID,
	},
	accountscore.DeepSeekAnthropicV1ProfileID: {
		protocolCode:    accountscore.AnthropicProtocolCode,
		protocolVersion: accountscore.AnthropicProtocolVersion,
		profileID:       accountscore.DeepSeekAnthropicV1ProfileID,
	},
	accountscore.GlmGeneralOpenAIV1ProfileID: {
		protocolCode:    accountscore.OpenAIProtocolCode,
		protocolVersion: accountscore.OpenAIProtocolVersion,
		profileID:       accountscore.GlmGeneralOpenAIV1ProfileID,
	},
	accountscore.GlmCodingOpenAIV1ProfileID: {
		protocolCode:    accountscore.OpenAIProtocolCode,
		protocolVersion: accountscore.OpenAIProtocolVersion,
		profileID:       accountscore.GlmCodingOpenAIV1ProfileID,
	},
	accountscore.GlmCodingAnthropicV1ProfileID: {
		protocolCode:    accountscore.AnthropicProtocolCode,
		protocolVersion: accountscore.AnthropicProtocolVersion,
		profileID:       accountscore.GlmCodingAnthropicV1ProfileID,
	},
	accountscore.GeminiOpenAIChatV1BetaProfileID: {
		protocolCode:    accountscore.OpenAIProtocolCode,
		protocolVersion: accountscore.OpenAIProtocolVersion,
		profileID:       accountscore.GeminiOpenAIChatV1BetaProfileID,
	},
	geminiNativeV1BetaProfileID: {
		protocolCode:    accountscore.GeminiProtocolCode,
		protocolVersion: accountscore.GeminiProtocolVersion,
		profileID:       geminiNativeV1BetaProfileID,
	},
	accountscore.MinimaxOpenAIV1ProfileID: {
		protocolCode:    accountscore.OpenAIProtocolCode,
		protocolVersion: accountscore.OpenAIProtocolVersion,
		profileID:       accountscore.MinimaxOpenAIV1ProfileID,
	},
	accountscore.VolcengineOpenAIV1ProfileID: {
		protocolCode:    accountscore.OpenAIProtocolCode,
		protocolVersion: accountscore.OpenAIProtocolVersion,
		profileID:       accountscore.VolcengineOpenAIV1ProfileID,
	},
	accountscore.QwenOpenAIV1ProfileID: {
		protocolCode:    accountscore.OpenAIProtocolCode,
		protocolVersion: accountscore.OpenAIProtocolVersion,
		profileID:       accountscore.QwenOpenAIV1ProfileID,
	},
}

// vendorDefaultProtocolContext 是草稿/探针链（无 connections 载荷）的 vendor
// 语义默认上下文，与退役前各 vendor 的默认 profile 行一致（gemini 默认 native
// 档案、deepseek/glm 默认 openai 档案；openai/hybrid 语义无 vendor 行，落
// generic openai 族无 pin）。
func vendorDefaultProtocolContext(providerCode string) protocolContext {
	switch normalizeProviderToken(providerCode) {
	case accountscore.GptVendorCode:
		return protocolContext{
			protocolCode:    accountscore.OpenAIProtocolCode,
			protocolVersion: accountscore.OpenAIProtocolVersion,
			profileID:       accountscore.GptOpenAIV1ProfileID,
		}
	case accountscore.XaiProviderCode:
		return protocolContext{
			protocolCode:    accountscore.OpenAIProtocolCode,
			protocolVersion: accountscore.OpenAIProtocolVersion,
			profileID:       accountscore.XaiOpenAIV1ProfileID,
		}
	case accountscore.DeepSeekProviderCode:
		return protocolContext{
			protocolCode:    accountscore.OpenAIProtocolCode,
			protocolVersion: accountscore.OpenAIProtocolVersion,
			profileID:       accountscore.DeepSeekOpenAIV1ProfileID,
		}
	case accountscore.AnthropicProviderCode:
		return protocolContext{
			protocolCode:    accountscore.AnthropicProtocolCode,
			protocolVersion: accountscore.AnthropicProtocolVersion,
		}
	case accountscore.GeminiProviderCode:
		return protocolContext{
			protocolCode:    accountscore.GeminiProtocolCode,
			protocolVersion: accountscore.GeminiProtocolVersion,
			profileID:       geminiNativeV1BetaProfileID,
		}
	case accountscore.GlmProviderCode:
		return protocolContext{
			protocolCode:    accountscore.OpenAIProtocolCode,
			protocolVersion: accountscore.OpenAIProtocolVersion,
			profileID:       accountscore.GlmGeneralOpenAIV1ProfileID,
		}
	case accountscore.MinimaxProviderCode:
		return protocolContext{
			protocolCode:    accountscore.OpenAIProtocolCode,
			protocolVersion: accountscore.OpenAIProtocolVersion,
			profileID:       accountscore.MinimaxOpenAIV1ProfileID,
		}
	case accountscore.VolcengineProviderCode:
		return protocolContext{
			protocolCode:    accountscore.OpenAIProtocolCode,
			protocolVersion: accountscore.OpenAIProtocolVersion,
			profileID:       accountscore.VolcengineOpenAIV1ProfileID,
		}
	case accountscore.QwenProviderCode:
		return protocolContext{
			protocolCode:    accountscore.OpenAIProtocolCode,
			protocolVersion: accountscore.OpenAIProtocolVersion,
			profileID:       accountscore.QwenOpenAIV1ProfileID,
		}
	default:
		return openAIProtocolContext()
	}
}

// vendorDefaultPreset 是 vendorDefaultProtocolContext 同一批 vendor 行的 URL
// 维度：各 vendor 默认 connection preset（§1.1 注册表，与上面各 case 的默认
// profile ID 一一对应）；generic 语义（openai/hybrid 等，无 vendor preset）
// 返回空串。两表必须同批维护：protocol_context_test.go 以成对断言钉住。
func vendorDefaultPreset(providerCode string) string {
	switch normalizeProviderToken(providerCode) {
	case accountscore.GptVendorCode:
		return contracts.PresetGPTOpenAIV1
	case accountscore.XaiProviderCode:
		return contracts.PresetXAIOpenAIV1
	case accountscore.DeepSeekProviderCode:
		return contracts.PresetDeepSeekOpenAIV1
	case accountscore.AnthropicProviderCode:
		return contracts.PresetAnthropicV1
	case accountscore.GeminiProviderCode:
		return contracts.PresetGeminiNativeV1Beta
	case accountscore.GlmProviderCode:
		return contracts.PresetGLMGeneralOpenAIV1
	case accountscore.MinimaxProviderCode:
		return contracts.PresetMiniMaxMedia
	case accountscore.VolcengineProviderCode:
		return contracts.PresetVolcengineArk
	case accountscore.QwenProviderCode:
		return contracts.PresetQwenDashScope
	default:
		return ""
	}
}

// vendorDefaultBaseURL 是草稿/探针链 OAuth 草稿缺省 base_url（供应商与上游
// 协议面重构设计 §1.1 default_base_url 表）：按 vendor 默认 preset 查 registry
// 注册根；generic 语义无 vendor preset，返回空串由调用方落 legacy OpenAI
// 官方根。
func vendorDefaultBaseURL(providerCode string) string {
	preset := vendorDefaultPreset(providerCode)
	if preset == "" {
		return ""
	}
	return contracts.PresetDefaultBaseURL(preset)
}
