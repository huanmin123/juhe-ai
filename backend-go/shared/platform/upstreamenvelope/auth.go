package upstreamenvelope

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// AuthStrategy 是认证策略名（§1：认证字段和附加头随适配器变化；信封统一
// 应用认证策略，凭据值由调用方解析后传入，本包不持有凭据）。
type AuthStrategy string

const (
	// AuthBearer：OpenAI 系 Bearer（API Key 与 OpenAI OAuth 均为此形态）。
	AuthBearer AuthStrategy = "bearer"
	// AuthAnthropicAPIKey：Anthropic x-api-key。OpenAI 式 oauth 账户与
	// GLM anthropic 档案（profile_glm_coding_anthropic_v1）改用 AuthBearer
	// （accountprobe executeAttempt / exactkeyprobe buildProbeRequest 现状）。
	AuthAnthropicAPIKey AuthStrategy = "anthropic_api_key"
	// AuthGeminiAPIKey：Gemini x-goog-api-key。google_oauth 账户改用
	// AuthOAuth。
	AuthGeminiAPIKey AuthStrategy = "gemini_api_key"
	// AuthOAuth：Bearer + 调用方附加头映射（chatgpt-account-id、
	// x-goog-user-project 等供应商专用头经 Auth.ExtraHeaders 注入）。
	AuthOAuth AuthStrategy = "oauth"
	// AuthNone 是显式无认证策略，供已解析为"本请求不携带凭据"的调用方
	// 显式选择（现行取证：F3 modelcheckprobe C 形态 Options.Headers 直通，
	// 凭据头缺失时请求照发且不写认证头；W2 接线裁决 2026-10-12）。它不是
	// 缺省/兜底：零值策略仍属未选择，Build 照常显式失败；其余策略空
	// token 仍无条件 fail-closed。Apply 对本策略不写任何头。
	AuthNone AuthStrategy = "none"
)

// Auth 是调用方解析后的凭据与策略。Token 为空属于构造错误（显式 AuthNone
// 策略除外：无认证请求由调用方显式选择，见 AuthNone）——探针凭据缺失必须
// 显式失败，不得静默发出无认证请求。
type Auth struct {
	Strategy AuthStrategy
	Token    string
	// ExtraHeaders 是认证伴随的调用方附加头映射（oauth 策略的组成部件）。
	ExtraHeaders map[string]string
}

// Apply 把认证头与附加头映射写入请求 header（§4.1 L1 导出入口）：供迁移中
// 的组合式调用方把私有认证头选择迁到共享规则——exactkeyprobe
// buildProbeRequest（M3）；头部规则与 Build 完全一致，URL 与 body 编码仍由
// Build 承担。Token 为空属于构造错误（AuthNone 除外）——探针凭据缺失必须
// 显式失败，不得静默发出无认证请求。
func (a Auth) Apply(header http.Header) error {
	if a.Strategy == AuthNone {
		// 显式无认证：不写任何头（认证头与附加头映射均不应用）；Token
		// 不参与校验。契约见 AuthNone 常量注释。
		return nil
	}
	if strings.TrimSpace(a.Token) == "" {
		return errors.New("upstreamenvelope: 认证凭据为空（凭据由调用方解析后传入，缺失必须显式失败）")
	}
	switch a.Strategy {
	case AuthBearer, AuthOAuth:
		header.Set("Authorization", "Bearer "+a.Token)
	case AuthAnthropicAPIKey:
		header.Set("X-Api-Key", a.Token)
	case AuthGeminiAPIKey:
		header.Set("X-Goog-Api-Key", a.Token)
	default:
		return fmt.Errorf("upstreamenvelope: 未知的认证策略 %q", a.Strategy)
	}
	for name, value := range a.ExtraHeaders {
		header.Set(name, value)
	}
	return nil
}
