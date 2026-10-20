package accounts

// OAuth 管理式建户（oauthmgmt 四家 OAuth 路由）的连接维度缺省派生（供应商与
// 上游协议面重构设计 §2/§2.2/§8）：OAuth 建户路由不携带 connections 与健康
// 检查连接维度载荷，由 vendor/账户类型按 §1.1 注册表确定性派生——
//   - preset 取 §2 组合表该 OAuth 组合绑定的唯一连接预设；
//   - baseUrl 取 preset 注册的 default_base_url（contracts.PresetDefaultBaseURL，
//     与 seed / UI 初始值同一口径，不新造第二套缺省表）；
//   - endpoint modes 取 AllowedModes(chat, preset, account_type, oauth_type)
//     全集（§2.2 收窄——含 gpt.oauth / xai.oauth 的 responses 收窄——由
//     registry 承载，此处不维护第二份词表）。
//
// 写面 resolveConnectionSet / 健康检查三件套的 §8 必填校验不放宽：派生结果以
// 常规创建载荷形态进入 Store.Create 全量校验。

import (
	"fmt"
	"strings"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// OAuthManagedConnectionDefaults 是一次 OAuth 管理式建户的连接维度缺省：
// 单行连接集合 + 健康检查连接维度（preset 即该连接；mode 取连接的 JSON
// 检查形态，探针优先非流式）。
type OAuthManagedConnectionDefaults struct {
	Connections                     []accountscore.ConnectionInput
	HealthCheckConnectionPresetCode string
	HealthCheckEndpointMode         string
}

// DefaultConnectionsForOAuthVendor 按 vendor/账户类型派生 OAuth 管理式建户的
// 连接维度缺省。§2 组合表四类 OAuth 各绑定唯一 preset：gpt.oauth →
// gpt_openai_v1、xai.oauth → xai_openai_v1、anthropic.oauth → anthropic_v1、
// gemini.google_oauth → gemini_native_v1beta（oauth_type 按 §1.1 从 credentials
// 推导，∈ {code_assist, google_one, ai_studio}）。未注册组合显式拒绝，不拼串
// 推断能力。
func DefaultConnectionsForOAuthVendor(providerCode, accountType string, credentials Credentials) (*OAuthManagedConnectionDefaults, error) {
	oauthType := accountOAuthTypeForRegistry(accountType, "", credentials)
	var preset string
	switch {
	case accountType == contracts.AccountTypeOAuth && providerCode == accountscore.GptVendorCode:
		preset = contracts.PresetGPTOpenAIV1
	case accountType == contracts.AccountTypeOAuth && providerCode == accountscore.XaiProviderCode:
		preset = contracts.PresetXAIOpenAIV1
	case accountType == contracts.AccountTypeOAuth && providerCode == accountscore.AnthropicProviderCode:
		preset = contracts.PresetAnthropicV1
	case accountType == contracts.AccountTypeGoogleOAuth && providerCode == accountscore.GeminiProviderCode:
		preset = contracts.PresetGeminiNativeV1Beta
	default:
		return nil, &ValidationError{Message: fmt.Sprintf("connection_capability_unsupported: OAuth 管理式建户不支持的供应商组合（provider=%s, accountType=%s）", providerCode, accountType)}
	}
	if accountType == contracts.AccountTypeGoogleOAuth && oauthType == "" {
		return nil, &ValidationError{Message: "connection_capability_unsupported: gemini google_oauth 建户凭据缺少 oauth_type（code_assist/google_one/ai_studio）"}
	}
	// §2 组合表：四类 OAuth 组合在 chat 域各恰有一个允许 preset。
	domain := accountscore.CapabilityDomainChat
	_, driver := accountVendorIdentity(providerCode)
	if !contracts.IsAllowedConnectorCombo(driver, accountType, oauthType, preset, domain) {
		return nil, &ValidationError{Message: fmt.Sprintf("connection_capability_unsupported: 连接预设 %s 不允许用于 OAuth 组合（provider=%s, accountType=%s, oauthType=%s, domain=%s）", preset, providerCode, accountType, oauthType, domain)}
	}
	modes := contracts.AllowedModes(domain, preset, accountType, oauthType)
	if len(modes) == 0 {
		return nil, &ValidationError{Message: fmt.Sprintf("connection_capability_unsupported: 连接预设 %s 在 %s 域没有可用 endpoint mode", preset, domain)}
	}
	return &OAuthManagedConnectionDefaults{
		Connections: []accountscore.ConnectionInput{{
			ConnectionPresetCode: preset,
			BaseURL:              contracts.PresetDefaultBaseURL(preset),
			EndpointModes:        modes,
			Enabled:              true,
		}},
		HealthCheckConnectionPresetCode: preset,
		HealthCheckEndpointMode:         defaultHealthCheckModeForConnectionModes(modes),
	}, nil
}

// defaultHealthCheckModeForConnectionModes 在派生连接的启用 mode 集合内取缺省
// 检查形态：流式优先（AI账户检查模型与人工测试设计 2026-10-19 定案）——取
// ASCII 序首个 _sse 形态，无 _sse 时才回落 _json，再无则取首个 mode
// （contracts.AllowedModes 已按 ASCII 升序返回）。
func defaultHealthCheckModeForConnectionModes(modes []string) string {
	for _, mode := range modes {
		if strings.HasSuffix(mode, "_sse") {
			return mode
		}
	}
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
