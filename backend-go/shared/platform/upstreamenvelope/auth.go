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
)

// Auth 是调用方解析后的凭据与策略。Token 为空属于构造错误——探针凭据缺失
// 必须显式失败，不得静默发出无认证请求。
type Auth struct {
	Strategy AuthStrategy
	Token    string
	// ExtraHeaders 是认证伴随的调用方附加头映射（oauth 策略的组成部件）。
	ExtraHeaders map[string]string
}

// Apply 把认证头与附加头映射写入请求 header（§4.1 L1 导出入口）：供迁移中
// 的组合式调用方把私有认证头选择迁到共享规则——exactkeyprobe
// buildProbeRequest（M3）；头部规则与 Build 完全一致，URL 与 body 编码仍由
// Build 承担。Token 为空属于构造错误——探针凭据缺失必须显式失败，不得静默
// 发出无认证请求。
func (a Auth) Apply(header http.Header) error {
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
