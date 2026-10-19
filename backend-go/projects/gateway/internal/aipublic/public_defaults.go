package aipublic

// 公开接口（/__aipublic__）的连接维度派生（供应商与上游协议面重构设计 §2/§9）：
// 目标 schema 删除 provider_protocol_profiles 后，公开 API 仍以 vendor +
// providerProtocolProfileId 表达账户身份。档案身份与连接预设由编译期 registry
// 确定性派生：
//   - 档案 ID 按 §2 组合表每个 vendor 唯一（gpt/xai/anthropic/gemini 四个 OAuth
//     供应商有历史档案 ID；其余真实厂商在公开 API 中同样按 vendor 派生同形 ID）；
//   - 连接预设取 registry 中该 (driver, api_key, domain=chat) 的 OpenAI 协议面
//     预设（pub key 推送语义即 OpenAI 兼容 wire），无 OpenAI 面时取首个允许项；
//   - endpointModes 取 AllowedModes 全集，健康检查形态优先 JSON（非流式）。
//
// 公开 API 的外部契约不变：调用方仍提交 providerProtocolProfileId；不提交
// connections 时由这里派生，显式提交时按原样进入写链校验。

import (
	"strings"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// PublicConnectionDefaults 是公开账户推送的连接维度缺省载荷。
type PublicConnectionDefaults struct {
	Connections                     []accountscore.ConnectionInput
	HealthCheckConnectionPresetCode string
	HealthCheckEndpointMode         string
}

// publicProfileIDForVendor 返回公开 API 对 vendor 使用的档案身份。与
// oauthmgmt.profileIDForProvider 同一词表；其余真实厂商沿用
// "profile_<vendor>_<preset>" 同形命名，保证档案身份非空且稳定。
func publicProfileIDForVendor(vendorCode string) string {
	switch vendorCode {
	case contracts.VendorGPT:
		return accountscore.GptOpenAIV1ProfileID
	case contracts.VendorXAI:
		return accountscore.XaiOpenAIV1ProfileID
	case contracts.VendorAnthropic:
		return "profile_anthropic_anthropic_v1"
	case contracts.VendorGemini:
		return "profile_gemini_native_v1beta"
	case contracts.VendorDeepSeek:
		return accountscore.DeepSeekOpenAIV1ProfileID
	case contracts.VendorGLM:
		return accountscore.GlmGeneralOpenAIV1ProfileID
	case contracts.VendorMiniMax:
		return accountscore.MinimaxOpenAIV1ProfileID
	case contracts.VendorVolcengine:
		return accountscore.VolcengineOpenAIV1ProfileID
	case contracts.VendorQwen:
		return accountscore.QwenOpenAIV1ProfileID
	default:
		// generic_http（公开 API 的历史 "openai" 词根）没有 vendors 行，
		// 由调用方在 loadProvider 阶段拒绝；此处返回空表示不派生。
		return ""
	}
}

// publicConnectionPresetForVendor 选取公开推送（api_key + chat）的连接预设：
// 优先 OpenAI 协议面（公开推送即 OpenAI 兼容 wire），否则取 registry 首个
// 允许项。未知组合返回空串，由写链按 connection_capability_unsupported 拒绝。
func publicConnectionPresetForVendor(vendorCode string) string {
	driver := publicDriverForVendor(vendorCode)
	if driver == "" {
		return ""
	}
	fallback := ""
	for _, combo := range contracts.AllowedConnectorCombos() {
		if combo.Driver != driver || combo.AccountType != contracts.AccountTypeAPIKey || combo.OAuthType != "" {
			continue
		}
		if combo.Domain != contracts.DomainChat {
			continue
		}
		if combo.Surface == contracts.SurfaceOpenAIV1 {
			return combo.Preset
		}
		if fallback == "" {
			fallback = combo.Preset
		}
	}
	return fallback
}

// publicDriverForVendor 映射 vendor → connection_driver_code（registry 身份表，
// 设计 §1）。真实厂商的 driver 与 vendor 同名；public API 的 generic 词根在目标
// schema 中不是 vendors 行，因此不在此返回 generic_http。
func publicDriverForVendor(vendorCode string) string {
	switch vendorCode {
	case contracts.VendorGPT:
		return contracts.DriverGPT
	case contracts.VendorXAI:
		return contracts.DriverXAI
	case contracts.VendorDeepSeek:
		return contracts.DriverDeepSeek
	case contracts.VendorAnthropic:
		return contracts.DriverAnthropic
	case contracts.VendorGemini:
		return contracts.DriverGemini
	case contracts.VendorGLM:
		return contracts.DriverGLM
	case contracts.VendorMiniMax:
		return contracts.DriverMiniMax
	case contracts.VendorVolcengine:
		return contracts.DriverVolcengine
	case contracts.VendorQwen:
		return contracts.DriverQwen
	default:
		return ""
	}
}

// DefaultPublicConnections 派生公开推送账户的连接集合与健康检查连接维度。
// baseURL 为空时回落 registry 的 preset 默认地址（generic 无默认，调用方必须
// 提交 baseUrl）。派生结果以常规创建载荷形态进入 Store.Create 全量校验。
func DefaultPublicConnections(vendorCode, baseURL, healthCheckModel string) (*PublicConnectionDefaults, bool) {
	preset := publicConnectionPresetForVendor(vendorCode)
	if preset == "" {
		return nil, false
	}
	modes := contracts.AllowedModes(contracts.DomainChat, preset, contracts.AccountTypeAPIKey, "")
	if len(modes) == 0 {
		return nil, false
	}
	resolvedBaseURL := strings.TrimSpace(baseURL)
	if resolvedBaseURL == "" {
		resolvedBaseURL = contracts.PresetDefaultBaseURL(preset)
	}
	if resolvedBaseURL == "" {
		return nil, false
	}
	return &PublicConnectionDefaults{
		Connections: []accountscore.ConnectionInput{{
			ConnectionPresetCode: preset,
			BaseURL:              resolvedBaseURL,
			EndpointModes:        modes,
			Enabled:              true,
		}},
		HealthCheckConnectionPresetCode: preset,
		HealthCheckEndpointMode:         defaultHealthCheckMode(modes),
	}, true
}

// defaultHealthCheckMode 在连接 mode 集合内取缺省检查形态：优先 JSON（非流式）。
func defaultHealthCheckMode(modes []string) string {
	for _, mode := range modes {
		if strings.HasSuffix(mode, "_json") {
			return mode
		}
	}
	if len(modes) > 0 {
		return modes[0]
	}
	return ""
}
