// 本文件是 jobs internal/accounthealth「精确 Key+模型诊断探针」执行器闭包
// 的平台层下沉副本（成对关系见 probe.go 包注释）：jobs 侧同包同名机制与
// gateway 恢复驱动共用本实现。
package exactkeyprobe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
)

// DirectInput is the typed result expected from the PostgreSQL read-only
// adapter. Keeping business evidence here prevents a SQL row with an omitted
// authorization/source/proxy condition from silently becoming probeable.
type DirectInput struct {
	Account       DirectAccount
	Authorization *DirectAuthorization
	Source        *DirectSource
	Binding       DirectBinding
	Proxy         *DirectProxy
	InputVersion  int64
	IssuedAt      time.Time
	ExpiresAt     time.Time
	TLSPolicy     string
	Schedule      Schedule
}

type DirectAccount struct {
	ID                                         string
	ConfigRevision                             int64
	DispatchRevision                           int64
	Provider                                   string
	ProtocolProfileID                          string
	ProtocolCode                               string
	ProtocolVersion                            string
	Type                                       string
	ClientCompatibility                        string
	Status                                     string
	Schedulable                                bool
	EndpointMode                               string
	HealthModel                                string
	MappedUpstreamModel                        string
	MappedUpstreamEndpointFamily               string
	CredentialsEncrypted                       string
	AccountExpiresAt                           *time.Time
	CooldownUntil                              *time.Time
	Cooldown                                   *CooldownFence
	TemporaryUnavailableContinuousProbeEnabled bool
	// 供应商解耦（设计 §2.1）：Provider 读取侧由 vendor_code 投影，
	// ProtocolProfileID 由健康检查连接的 connection_preset_code 经
	// ConnectionPresetProfileID 派生；OAuthType 是 accounts.oauth_type 列
	// （OAuth 分流的唯一运行时来源，凭据 JSON 不再携带）；探针上游地址是
	// 健康检查连接行的 base_url（凭据 JSON 不再携带 base_url）。
	OAuthType            string
	ConnectionPresetCode string
	ConnectionBaseURL    string
}

type DirectSource struct {
	ID                   string
	ConfigRevision       int64
	Provider             string
	ProtocolProfileID    string
	ProtocolCode         string
	ProtocolVersion      string
	Type                 string
	ClientCompatibility  string
	Status               string
	Schedulable          bool
	AccountExpiresAt     *time.Time
	CooldownUntil        *time.Time
	LastErrorCode        string
	CredentialsEncrypted string
	// 与 DirectAccount 同构的新身份/连接投影（授权实例的资源事实按来源账户读）。
	OAuthType            string
	ConnectionPresetCode string
	ConnectionBaseURL    string
}

type DirectAuthorization struct {
	ID            string
	Status        string
	ExpiresAt     *time.Time
	QuotaEligible bool
}

type DirectBinding struct {
	GroupID                string
	Enabled                bool
	AuthorizationBindingID string
}

type DirectProxy struct {
	ID                string
	Enabled           bool
	Type              string
	Host              string
	Port              int
	Username          string
	PasswordEncrypted string
}

func (d DirectInput) ToInput(secret string, now time.Time) (Input, error) {
	if strings.TrimSpace(secret) == "" {
		return Input{}, fmt.Errorf("PG direct input 缺少业务凭据 secret")
	}
	if err := validateDirectAccount(d.Account, now); err != nil {
		return Input{}, err
	}
	if !d.Binding.Enabled || strings.TrimSpace(d.Binding.GroupID) == "" {
		return Input{}, fmt.Errorf("PG direct input 缺少启用的 group binding")
	}
	if d.InputVersion < 1 || d.IssuedAt.IsZero() || d.ExpiresAt.IsZero() || !d.ExpiresAt.After(now) {
		return Input{}, fmt.Errorf("PG direct input 的 epoch 或有效期无效")
	}
	if strings.TrimSpace(d.TLSPolicy) == "" {
		return Input{}, fmt.Errorf("PG direct input 缺少 TLS policy version")
	}
	effective := d.Account
	sourceRevision := (*int64)(nil)
	if d.Authorization != nil || d.Source != nil {
		if err := validateDirectAuthorization(d, now); err != nil {
			return Input{}, err
		}
		effective.Provider = d.Source.Provider
		effective.ProtocolProfileID = d.Source.ProtocolProfileID
		effective.ProtocolCode = d.Source.ProtocolCode
		effective.ProtocolVersion = d.Source.ProtocolVersion
		effective.Type = d.Source.Type
		effective.ClientCompatibility = d.Source.ClientCompatibility
		effective.CredentialsEncrypted = d.Source.CredentialsEncrypted
		// 新身份/连接面事实归来源资源账户（设计 §2.1 资源事实投影语义）。
		effective.OAuthType = d.Source.OAuthType
		effective.ConnectionPresetCode = d.Source.ConnectionPresetCode
		effective.ConnectionBaseURL = d.Source.ConnectionBaseURL
		sourceRevision = int64Pointer(d.Source.ConfigRevision)
	}
	if !isSupportedDirectProfile(effective.ProtocolProfileID, effective.Provider, effective.Type, d.Account.EndpointMode) || effective.Type != d.Account.Type {
		return Input{}, fmt.Errorf("PG direct input 的有效来源 provider/type 不受 J1 支持")
	}
	if err := validateDirectProtocolMetadata(effective.ProtocolProfileID, effective.ProtocolCode, effective.ProtocolVersion); err != nil {
		return Input{}, err
	}
	probeAccount := d.Account
	probeAccount.Provider = effective.Provider
	probeAccount.ProtocolProfileID = effective.ProtocolProfileID
	probeAccount.ProtocolCode = effective.ProtocolCode
	probeAccount.ProtocolVersion = effective.ProtocolVersion
	probeAccount.Type = effective.Type
	probeMode, probeModel, err := directProbeTarget(probeAccount)
	if err != nil {
		return Input{}, err
	}
	protocol := directProbeProtocolForMode(effective.ProtocolProfileID, effective.Provider, probeMode)
	if protocol == "" {
		return Input{}, fmt.Errorf("PG direct input 的有效来源协议 profile 不受 J1 支持")
	}
	result := Input{
		AccountID:           d.Account.ID,
		InputVersion:        d.InputVersion,
		ConfigRevision:      d.Account.ConfigRevision,
		DispatchRevision:    d.Account.DispatchRevision,
		Provider:            protocol,
		ProtocolProfileID:   effective.ProtocolProfileID,
		ProtocolCode:        effective.ProtocolCode,
		ProtocolVersion:     effective.ProtocolVersion,
		Type:                d.Account.Type,
		ClientCompatibility: effective.ClientCompatibility,
		EndpointMode:        probeMode,
		HealthModel:         probeModel,
		IssuedAt:            d.IssuedAt.UTC(),
		ExpiresAt:           d.ExpiresAt.UTC(),
		TLSPolicyVersion:    d.TLSPolicy,
		Eligibility:         Eligibility{AccountStatus: d.Account.Status, Schedulable: d.Account.Schedulable, BoundGroup: true, AuthorizationEligible: true, SourceConfigRevision: sourceRevision, CooldownUntil: cloneTime(d.Account.CooldownUntil), TemporaryUnavailableContinuousProbeEnabled: boolPointer(d.Account.TemporaryUnavailableContinuousProbeEnabled)},
		Cooldown:            d.Account.Cooldown,
		Schedule:            d.Schedule,
	}
	if (d.Account.Status == "temporary_unavailable" || d.Account.Status == "rate_limited") && !validCooldownFence(d.Account.Cooldown, result) {
		return Input{}, fmt.Errorf("PG direct input 的冷却账户缺少完整 fence")
	}
	credentials, err := decryptJSONObject(secret, effective.CredentialsEncrypted, "账户凭据")
	if err != nil {
		return Input{}, err
	}
	result.BaseURL, err = directBaseURL(effective)
	if err != nil {
		return Input{}, err
	}
	if effective.Type == "google_oauth" {
		// oauth_type 的运行时唯一来源是 accounts.oauth_type 列（设计 §2.1：
		// 凭据重加密事务已删除 oauth_type 键，凭据 JSON 不再承载）。
		oauthType := effective.OAuthType
		if (oauthType == "code_assist" || oauthType == "google_one") && (probeMode != "generate_content_json" && probeMode != "generate_content_sse") {
			return Input{}, fmt.Errorf("J1 Gemini Code Assist / Google One 只支持 GenerateContent 探活")
		}
	}
	if d.Proxy != nil {
		proxy, err := directProxyEnvelope(secret, *d.Proxy)
		if err != nil {
			return Input{}, err
		}
		result.Proxy = &proxy
	}
	if result.Type == "api_key" {
		keys := directAPIKeys(credentials)
		if len(keys) == 0 {
			return Input{}, fmt.Errorf("PG direct input 的 API Key pool 为空")
		}
		for index, key := range keys {
			ciphertext, err := EncryptV1Envelope(secret, []byte(`{"api_key":`+mustJSON(key)+`}`))
			if err != nil {
				return Input{}, err
			}
			fingerprint := directKeyFingerprint(secret, key)
			result.APIKeys = append(result.APIKeys, APIKeyInput{Index: index, Fingerprint: fingerprint, Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: ciphertext}})
		}
		result.KeySetFingerprint = directKeySetFingerprint(result.APIKeys)
		return result, nil
	}
	accessToken, ok := directString(credentials, "access_token")
	if !ok {
		return Input{}, fmt.Errorf("PG direct input 的 OAuth access token 缺失")
	}
	expiresAt, ok := directTime(credentials, "expires_at")
	if !ok || !expiresAt.After(now.Add(time.Minute)) {
		return Input{}, fmt.Errorf("PG direct input 的 OAuth access token 已到期或接近到期")
	}
	ciphertext, err := EncryptV1Envelope(secret, []byte(`{"access_token":`+mustJSON(accessToken)+`}`))
	if err != nil {
		return Input{}, err
	}
	result.OAuthAccess = &CredentialEnvelope{Kind: "oauth_access", Ciphertext: ciphertext}
	result.OAuthExpiresAt = &expiresAt
	if accountID, found := directString(credentials, "account_id"); found {
		result.OAuthAccountID = accountID
	} else if accountID, found := directString(credentials, "chatgpt_user_id"); found {
		result.OAuthAccountID = accountID
	}
	if quotaProject, found := directString(credentials, "quota_project_id"); found {
		result.OAuthQuotaProjectID = quotaProject
	}
	if oauthType := effective.OAuthType; oauthType != "" {
		result.OAuthType = oauthType
	}
	if projectID, found := directString(credentials, "project_id"); found {
		result.OAuthProjectID = projectID
	}
	return result, nil
}

func directProbeTarget(account DirectAccount) (string, string, error) {
	mode, model, _, err := accountprobe.ResolveHybridProbeTarget(
		account.ProtocolProfileID,
		accountprobe.EndpointMode(account.EndpointMode),
		account.HealthModel,
		account.MappedUpstreamModel,
		account.MappedUpstreamEndpointFamily,
	)
	if err != nil {
		return "", "", fmt.Errorf("PG direct input 的 %w", err)
	}
	return string(mode), model, nil
}

func validateDirectAccount(account DirectAccount, now time.Time) error {
	if strings.TrimSpace(account.ID) == "" || account.ConfigRevision < 1 || account.DispatchRevision < 1 {
		return fmt.Errorf("PG direct input 的账户或 revision 无效")
	}
	if account.Type != "api_key" && account.Type != "oauth" && account.Type != "google_oauth" {
		return fmt.Errorf("PG direct input 的 provider/type 不受 J1 支持")
	}
	// 供应商解耦：profile/provider 协议元数据校验移到 ToInput 的 effective
	// （授权实例按来源账户投影）之后统一执行；实例行在新 schema 不再冗余
	// 身份列（vendor_code/preset 均为 NULL），此处只校验账户行自身事实。
	if account.Status != "active" && account.Status != "pending_test" && account.Status != "temporary_unavailable" && account.Status != "rate_limited" {
		return fmt.Errorf("PG direct input 的账户状态不可探活")
	}
	if account.Status != "pending_test" && account.Status != "temporary_unavailable" && account.Status != "rate_limited" && !account.Schedulable {
		return fmt.Errorf("PG direct input 的 active 账户不可调度")
	}
	if account.AccountExpiresAt != nil && !account.AccountExpiresAt.After(now) {
		return fmt.Errorf("PG direct input 的账户已到期")
	}
	if account.CooldownUntil != nil && account.CooldownUntil.After(now) {
		return fmt.Errorf("PG direct input 的账户仍在冷却")
	}
	if strings.TrimSpace(account.HealthModel) == "" || strings.TrimSpace(account.CredentialsEncrypted) == "" {
		return fmt.Errorf("PG direct input 缺少健康模型或凭据")
	}
	return nil
}

// directProfileProtocolMetadata 是探针协议元数据投影：profile → (protocol,
// version)。供应商解耦后读侧身份改为 (vendor_code, 健康检查连接 preset)，由
// ConnectionPresetProfileID 派生出同一批 profile_* ID（下游取值形状不变），
// 本表与 isSupportedDirectProfile 继续按 profile 做最终 fail-closed 门禁。
// generic_* 三档对应新 generic_http 连接（旧 openai-compatible/hybrid 泛化
// 身份）；profile_hybrid_* 为 Node 遗留形态，种子未含但保留兼容）。
var directProfileProtocolMetadata = map[string][2]string{
	"profile_openai_openai_v1":             {"openai", "v1"},
	"profile_gpt_openai_v1":                {"openai", "v1"},
	"profile_xai_openai_v1":                {"openai", "v1"},
	"profile_deepseek_openai_v1":           {"openai", "v1"},
	"profile_deepseek_anthropic_v1":        {"anthropic", "v1"},
	"profile_anthropic_anthropic_v1":       {"anthropic", "v1"},
	"profile_glm_general_openai_v1":        {"openai", "v1"},
	"profile_glm_coding_openai_v1":         {"openai", "v1"},
	"profile_glm_coding_anthropic_v1":      {"anthropic", "v1"},
	"profile_gemini_openai_chat_v1beta":    {"openai", "v1"},
	"profile_gemini_native_v1beta":         {"gemini", "v1beta"},
	"profile_generic_openai_v1":            {"openai", "v1"},
	"profile_generic_anthropic_v1":         {"anthropic", "v1"},
	"profile_generic_gemini_v1beta":        {"gemini", "v1beta"},
	"profile_hybrid_openai_chat_v1":        {"openai", "v1"},
	"profile_hybrid_anthropic_messages_v1": {"anthropic", "v1"},
	"profile_hybrid_gemini_native_v1beta":  {"gemini", "v1beta"},
}

// ConnectionPresetProfileID 把 account_connections.connection_preset_code 确定性地
// 派生为探针 profile ID（供应商解耦 §8 去供应商化：旧 accounts.provider_code +
// provider_protocol_profile_id 身份列退役，探针协议目录本身未变）。映射来源：
// shared/contracts/connector_registry.go 的 preset 词表逐条比对旧 profile 词表；
// anthropic 目录 ID（profile_anthropic_anthropic_v1）与 preset（anthropic_v1）
// 字面不同，按字面映射，不做字符串拼推导。不在表内的 preset（minimax_media、
// volcengine_*、qwen_dashscope 等媒体/新增厂商）没有已注册的 J1 探针协议目录，
// 返回 false 并由上层 fail closed。
// connectionPresetProfiles 是 connection_preset_code ↔ 探针 profile ID 的唯一
// 映射表（双向查询共用；改表即两侧同步）。
var connectionPresetProfiles = []struct {
	preset  string
	profile string
}{
	{"gpt_openai_v1", "profile_gpt_openai_v1"},
	{"xai_openai_v1", "profile_xai_openai_v1"},
	{"deepseek_openai_v1", "profile_deepseek_openai_v1"},
	{"deepseek_anthropic_v1", "profile_deepseek_anthropic_v1"},
	{"anthropic_v1", "profile_anthropic_anthropic_v1"},
	{"glm_general_openai_v1", "profile_glm_general_openai_v1"},
	{"glm_coding_openai_v1", "profile_glm_coding_openai_v1"},
	{"glm_coding_anthropic_v1", "profile_glm_coding_anthropic_v1"},
	{"gemini_openai_chat_v1beta", "profile_gemini_openai_chat_v1beta"},
	{"gemini_native_v1beta", "profile_gemini_native_v1beta"},
	{"generic_openai_v1", "profile_generic_openai_v1"},
	{"generic_anthropic_v1", "profile_generic_anthropic_v1"},
	{"generic_gemini_v1beta", "profile_generic_gemini_v1beta"},
}

func ConnectionPresetProfileID(preset string) (string, bool) {
	trimmed := strings.TrimSpace(preset)
	for _, item := range connectionPresetProfiles {
		if item.preset == trimmed {
			return item.profile, true
		}
	}
	return "", false
}

// ConnectionPresetForProfileID 是 ConnectionPresetProfileID 的逆向查询：把旧
// profile ID 还原为账户健康检查连接 preset，供读面按“档案 = 健康检查连接
// preset”做筛选。媒体/新增厂商 preset 无 J1 探针目录，不在表中，返回 false。
func ConnectionPresetForProfileID(profileID string) (string, bool) {
	trimmed := strings.TrimSpace(profileID)
	for _, item := range connectionPresetProfiles {
		if item.profile == trimmed {
			return item.preset, true
		}
	}
	return "", false
}

func validateDirectProtocolMetadata(profile, code, version string) error {
	trimmed := strings.TrimSpace(profile)
	if trimmed == "" {
		return nil
	}
	expected, known := directProfileProtocolMetadata[trimmed]
	if !known {
		return fmt.Errorf("PG direct input 的协议 profile 未注册协议元数据")
	}
	if strings.TrimSpace(code) != "" && code != expected[0] {
		return fmt.Errorf("PG direct input 的 protocol_code 与 profile 不一致")
	}
	if strings.TrimSpace(version) != "" && version != expected[1] {
		return fmt.Errorf("PG direct input 的 protocol_version 与 profile 不一致")
	}
	return nil
}

func validateDirectAuthorization(input DirectInput, now time.Time) error {
	if input.Authorization == nil || input.Source == nil {
		return fmt.Errorf("PG direct input 的授权账户缺少 authorization/source")
	}
	authorization := input.Authorization
	source := input.Source
	if strings.TrimSpace(authorization.ID) == "" || authorization.Status != "active" || !authorization.QuotaEligible || (authorization.ExpiresAt != nil && !authorization.ExpiresAt.After(now)) {
		return fmt.Errorf("PG direct input 的授权不可用")
	}
	if input.Binding.AuthorizationBindingID != authorization.ID {
		return fmt.Errorf("PG direct input 的授权 group binding 不匹配")
	}
	if strings.TrimSpace(source.ID) == "" || source.ConfigRevision < 1 || !isSupportedDirectProfile(source.ProtocolProfileID, source.Provider, source.Type, input.Account.EndpointMode) || source.Type != input.Account.Type || source.Status != "active" || !source.Schedulable || source.LastErrorCode == "account_expired" || strings.TrimSpace(source.CredentialsEncrypted) == "" {
		return fmt.Errorf("PG direct input 的物理来源账户不可用")
	}
	if source.AccountExpiresAt != nil && !source.AccountExpiresAt.After(now) {
		return fmt.Errorf("PG direct input 的物理来源账户已到期")
	}
	if source.CooldownUntil != nil && source.CooldownUntil.After(now) {
		return fmt.Errorf("PG direct input 的物理来源账户仍在冷却")
	}
	return nil
}

func isDirectOpenAIProvider(value string) bool {
	return value == "gpt" || value == "openai"
}

func isSupportedDirectProfile(profile, provider, accountType, mode string) bool {
	if profile == "" {
		if !isDirectOpenAIProvider(provider) || (accountType != "api_key" && accountType != "oauth") {
			return false
		}
		if accountType == "oauth" {
			return mode == "responses_json" || mode == "responses_sse"
		}
		return mode == "chat_json" || mode == "chat_sse" || mode == "responses_json" || mode == "responses_sse" || mode == "images_json"
	}
	switch profile {
	case "profile_gpt_openai_v1":
		if provider != "gpt" {
			return false
		}
		return openAIProfileMode(accountType, mode, true)
	case "profile_openai_openai_v1":
		if provider != "openai" || accountType != "api_key" {
			return false
		}
		return openAIChatOrResponseMode(mode, true)
	case "profile_xai_openai_v1":
		if provider != "xai" {
			return false
		}
		return openAIProfileMode(accountType, mode, true)
	case "profile_deepseek_openai_v1":
		return provider == "deepseek" && accountType == "api_key" && openAIChatOrResponseMode(mode, true)
	case "profile_glm_general_openai_v1", "profile_glm_coding_openai_v1":
		return provider == "glm" && accountType == "api_key" && (mode == "chat_json" || mode == "chat_sse")
	case "profile_gemini_openai_chat_v1beta":
		return provider == "gemini" && accountType == "api_key" && (mode == "chat_json" || mode == "chat_sse")
	case "profile_hybrid_openai_chat_v1":
		return provider == "hybrid" && accountType == "api_key" && hybridProfileMode(mode)
	case "profile_hybrid_anthropic_messages_v1":
		return provider == "hybrid" && accountType == "api_key" && (mode == "messages_json" || mode == "messages_sse")
	case "profile_hybrid_gemini_native_v1beta":
		return provider == "hybrid" && accountType == "api_key" && geminiProfileMode(mode)
	case "profile_anthropic_anthropic_v1":
		return provider == "anthropic" && (accountType == "api_key" || accountType == "oauth") && (mode == "messages_json" || mode == "messages_sse")
	case "profile_deepseek_anthropic_v1":
		return provider == "deepseek" && accountType == "api_key" && (mode == "messages_json" || mode == "messages_sse")
	case "profile_glm_coding_anthropic_v1":
		return provider == "glm" && accountType == "api_key" && (mode == "messages_json" || mode == "messages_sse")
	case "profile_gemini_native_v1beta":
		return provider == "gemini" && (accountType == "api_key" || accountType == "google_oauth") && geminiProfileMode(mode)
	// generic_http 连接（vendor_code NULL）：厂商未知，不做 provider 交叉校验；
	// 协议面由 profile 本身固定（与旧 openai-compatible/hybrid 泛化身份等价）。
	case "profile_generic_openai_v1":
		return accountType == "api_key" && openAIChatOrResponseMode(mode, true)
	case "profile_generic_anthropic_v1":
		return accountType == "api_key" && (mode == "messages_json" || mode == "messages_sse")
	case "profile_generic_gemini_v1beta":
		return accountType == "api_key" && geminiProfileMode(mode)
	default:
		return false
	}
}

func openAIProfileMode(accountType, mode string, allowImages bool) bool {
	if accountType != "api_key" && accountType != "oauth" {
		return false
	}
	if accountType == "oauth" {
		return mode == "responses_json" || mode == "responses_sse"
	}
	return openAIChatOrResponseMode(mode, allowImages)
}

func openAIChatOrResponseMode(mode string, allowImages bool) bool {
	if mode == "chat_json" || mode == "chat_sse" || mode == "responses_json" || mode == "responses_sse" {
		return true
	}
	return allowImages && mode == "images_json"
}

func geminiProfileMode(mode string) bool {
	return mode == "generate_content_json" || mode == "generate_content_sse" || mode == "interactions_json" || mode == "interactions_sse"
}

func hybridProfileMode(mode string) bool {
	return openAIChatOrResponseMode(mode, true) || mode == "messages_json" || mode == "messages_sse" || geminiProfileMode(mode)
}

func directProbeProtocolForMode(profile, provider, mode string) string {
	switch profile {
	case "profile_anthropic_anthropic_v1", "profile_deepseek_anthropic_v1", "profile_glm_coding_anthropic_v1", "profile_generic_anthropic_v1":
		return "anthropic"
	case "profile_gemini_native_v1beta", "profile_generic_gemini_v1beta":
		return "gemini"
	case "profile_gpt_openai_v1", "profile_openai_openai_v1", "profile_xai_openai_v1", "profile_deepseek_openai_v1", "profile_glm_general_openai_v1", "profile_glm_coding_openai_v1", "profile_gemini_openai_chat_v1beta", "profile_generic_openai_v1":
		return "openai"
	case "profile_hybrid_openai_chat_v1":
		if mode == "messages_json" || mode == "messages_sse" {
			return "anthropic"
		}
		if geminiProfileMode(mode) {
			return "gemini"
		}
		if openAIChatOrResponseMode(mode, true) {
			return "openai"
		}
		return ""
	case "profile_hybrid_anthropic_messages_v1":
		return "anthropic"
	case "profile_hybrid_gemini_native_v1beta":
		return "gemini"
	case "":
		if isDirectOpenAIProvider(provider) {
			return "openai"
		}
	}
	return ""
}

func probeProfileProvider(profile, protocol string) string {
	switch profile {
	case "profile_gpt_openai_v1":
		return "gpt"
	case "profile_openai_openai_v1":
		return "openai"
	case "profile_xai_openai_v1":
		return "xai"
	case "profile_deepseek_openai_v1", "profile_deepseek_anthropic_v1":
		return "deepseek"
	case "profile_glm_general_openai_v1", "profile_glm_coding_openai_v1", "profile_glm_coding_anthropic_v1":
		return "glm"
	case "profile_anthropic_anthropic_v1":
		return "anthropic"
	case "profile_gemini_native_v1beta", "profile_gemini_openai_chat_v1beta":
		return "gemini"
	case "profile_hybrid_openai_chat_v1", "profile_hybrid_anthropic_messages_v1", "profile_hybrid_gemini_native_v1beta":
		return "hybrid"
	default:
		return protocol
	}
}

// directBaseURL 解析探针上游地址。供应商解耦后单一事实源是健康检查连接行的
// base_url（account_connections.base_url NOT NULL，设计 §2.1：凭据 JSON 不得
// 再保存 base_url，profile 级默认上游表随 provider_protocol_profiles 一并退
// 役）；gpt OAuth 的 Codex 固定上游由请求构造层信封（upstreamenvelope A 形态
// fixedBaseURL）在出站时强制，与本字段解耦。
func directBaseURL(account DirectAccount) (string, error) {
	configured := strings.TrimSpace(account.ConnectionBaseURL)
	if configured == "" {
		return "", fmt.Errorf("PG direct input 的健康检查连接缺少 base_url")
	}
	return strings.TrimRight(configured, "/"), nil
}

func decryptJSONObject(secret, encrypted, label string) (map[string]json.RawMessage, error) {
	plaintext, err := DecryptV1Envelope(secret, encrypted)
	if err != nil {
		return nil, fmt.Errorf("解封 PG direct input %s失败: %w", label, err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(plaintext, &result); err != nil {
		return nil, fmt.Errorf("解析 PG direct input %s失败", label)
	}
	return result, nil
}

func directAPIKeys(credentials map[string]json.RawMessage) []string {
	values := make([]string, 0)
	if raw, found := credentials["api_keys"]; found {
		_ = json.Unmarshal(raw, &values)
	}
	if len(values) == 0 {
		if key, found := directString(credentials, "api_key"); found {
			values = []string{key}
		}
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

func directProxyEnvelope(secret string, proxy DirectProxy) (CredentialEnvelope, error) {
	if strings.TrimSpace(proxy.ID) == "" || !proxy.Enabled || proxy.Port < 1 || proxy.Port > 65535 || net.ParseIP(proxy.Host) == nil && strings.TrimSpace(proxy.Host) == "" {
		return CredentialEnvelope{}, fmt.Errorf("PG direct input 的 proxy profile 不可用")
	}
	scheme := proxy.Type
	if scheme == "socks5" {
		scheme = "socks5h"
	}
	if scheme != "http" && scheme != "https" && scheme != "socks5h" {
		return CredentialEnvelope{}, fmt.Errorf("PG direct input 的 proxy 协议不受支持")
	}
	endpoint := &url.URL{Scheme: scheme, Host: net.JoinHostPort(proxy.Host, fmt.Sprintf("%d", proxy.Port))}
	if strings.TrimSpace(proxy.Username) != "" {
		if strings.TrimSpace(proxy.PasswordEncrypted) == "" {
			return CredentialEnvelope{}, fmt.Errorf("PG direct input 的 proxy password 缺失")
		}
		password, err := decryptJSONObject(secret, proxy.PasswordEncrypted, "proxy password")
		if err != nil {
			return CredentialEnvelope{}, err
		}
		secretValue, found := directString(password, "password")
		if !found {
			return CredentialEnvelope{}, fmt.Errorf("PG direct input 的 proxy password 缺失")
		}
		endpoint.User = url.UserPassword(proxy.Username, secretValue)
	}
	ciphertext, err := EncryptV1Envelope(secret, []byte(`{"url":`+mustJSON(endpoint.String())+`}`))
	if err != nil {
		return CredentialEnvelope{}, err
	}
	return CredentialEnvelope{Kind: "proxy_url", Ciphertext: ciphertext}, nil
}

func directString(values map[string]json.RawMessage, key string) (string, bool) {
	var value string
	if raw, found := values[key]; !found || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", false
	}
	return strings.TrimSpace(value), true
}

func directTime(values map[string]json.RawMessage, key string) (time.Time, bool) {
	value, found := directString(values, key)
	if !found {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
}

func directKeyFingerprint(secret, key string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

func directKeySetFingerprint(keys []APIKeyInput) string {
	fingerprints := make([]string, 0, len(keys))
	for _, key := range keys {
		fingerprints = append(fingerprints, key.Fingerprint)
	}
	sort.Strings(fingerprints)
	var hash uint32 = 2166136261
	for _, fingerprint := range fingerprints {
		for _, character := range fingerprint {
			hash ^= uint32(character)
			hash *= 16777619
		}
		hash ^= 0
		hash *= 16777619
	}
	return fmt.Sprintf("%08x", hash)
}

func mustJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func int64Pointer(value int64) *int64 { return &value }

func boolPointer(value bool) *bool { return &value }

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}

// validCooldownFence 是 jobs accounthealth scheduler.go 同名函数的成对副本
// （下沉取证发现 ToInput 的冷却 fence 校验依赖该留守函数，而平台层禁止反向
// import jobs，故按「成对复制+注释互指」先例复制）：实现为纯函数，两侧语义
// 必须一致，修改任一侧必须同步另一侧。
func validCooldownFence(fence *CooldownFence, input Input) bool {
	if fence == nil || fence.ObservationStartedAt.IsZero() || strings.TrimSpace(fence.Generation) == "" {
		return false
	}
	if input.Eligibility.SourceConfigRevision == nil {
		return fence.SourceConfigRevision == nil
	}
	return fence.SourceConfigRevision != nil && *fence.SourceConfigRevision == *input.Eligibility.SourceConfigRevision
}
