// Package oauthmgmt owns the M17 vertical slice: the four-provider OAuth
// management route family ported from backend/src/modules/{openai,anthropic,
// gemini,grok}-oauth (dual-mounted as /__aisys__/api/{provider}-oauth for
// admins and /__aisys__/api/my-{provider}-oauth under forceSelfAccessScope).
// The slice covers authorize-URL sessions (state/nonce/PKCE per provider),
// create-from-code and create-from-refresh-token account creation on the M08
// accounts store, manual access-token refresh and reauthorization through the
// oauth-credential-rotation CAS (config_revision + 1), the grok SSO device
// flow, the gemini capabilities document and the operation-log emission for
// every mutation. Upstream token endpoints are reached only through the
// injected TokenExchanger; the grok device flow through SSODeviceRequester, so
// tests run with httptest/mock transports and never touch the network.
package oauthmgmt

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
)

// ConflictError maps to the route family 409 paths (owner-scoped duplicate
// account names surface through the accounts slice with the same shape).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ValidationError maps to the 400 mutation paths (provider/profile checks,
// revision validation, missing refresh tokens).
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// RevisionConflictError maps to AccountConfigRevisionConflictError: 409 with
// the route-specific copy.
type RevisionConflictError struct{ Message string }

func (e *RevisionConflictError) Error() string { return e.Message }

// Provider codes and protocol profile IDs mirror domain/provider-protocol.ts.
const (
	ProviderGPT       = "gpt"
	ProviderAnthropic = "anthropic"
	ProviderGemini    = "gemini"
	ProviderXAI       = "xai"

	ProfileGPTOpenAIV1        = "profile_gpt_openai_v1"
	ProfileXAIOpenAIV1        = "profile_xai_openai_v1"
	ProfileAnthropicV1        = "profile_anthropic_anthropic_v1"
	ProfileGeminiNativeV1Beta = "profile_gemini_native_v1beta"
)

// Store is the dual-mode OAuth account persistence. secret is the Node
// runtimeConfig.secret material: accounts.credentials_encrypted rows written
// by Node stay decryptable. exchanger may be nil only together with an
// explicitly injected transport at the route layer; NewStore rejects nil.
type Store struct {
	db           *sql.DB
	pg           bool
	secret       string
	now          func() time.Time
	newI         func(prefix string) string
	sessions     *sessionStore
	Accounts     *accounts.Store
	exchanger    TokenExchanger
	ssoRequester SSODeviceRequester
	ssoSleep     func(ctx context.Context, delay time.Duration) error
	// invalidator / revisionAdvancer are the rotation post-commit
	// invalidation + in-transaction circuit fence ports (invalidation.go).
	// Nil until the With* options wire them; a nil port keeps the rotation
	// self-contained (out-of-package fixtures).
	invalidator      CacheInvalidator
	revisionAdvancer DispatchRevisionAdvancer
	// proxyRequired 强制 requiresProxy 供应商（openai/anthropic/gemini/xai）
	// 的建户/刷新/重授权绑定代理：未绑定直接 400。生产默认开启；测试可通过
	// WithProxyRequired(false) 关闭以保持既有无代理夹具可用。
	proxyRequired bool
}

// Option configures optional collaborators.
type Option func(*Store)

// WithSSODeviceTransport injects the grok device-flow transport; tests supply
// scripted requesters so the SSO import never leaves the process.
func WithSSODeviceTransport(requester SSODeviceRequester) Option {
	return func(s *Store) {
		if requester != nil {
			s.ssoRequester = requester
		}
	}
}

// WithSSOSleep injects the device-flow poll sleep (tests pass a no-op).
func WithSSOSleep(sleep func(ctx context.Context, delay time.Duration) error) Option {
	return func(s *Store) {
		if sleep != nil {
			s.ssoSleep = sleep
		}
	}
}

// WithProxyRequired toggles the requiresProxy enforcement（默认开启）；测试
// 夹具大量依赖无代理建户/刷新路径，显式关闭以保持其可用。
func WithProxyRequired(enabled bool) Option {
	return func(s *Store) {
		s.proxyRequired = enabled
	}
}

// NewStore builds the store. accountsStore is the M08 accounts slice store the
// create routes delegate to; exchanger is the injected upstream transport.
func NewStore(db *sql.DB, postgres bool, secret string, accountsStore *accounts.Store, exchanger TokenExchanger, now func() time.Time, newID func(string) string, opts ...Option) (*Store, error) {
	if db == nil {
		return nil, errors.New("oauthmgmt store requires a database")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("oauthmgmt store requires the runtime secret")
	}
	if accountsStore == nil {
		return nil, errors.New("oauthmgmt store requires the accounts store")
	}
	if exchanger == nil {
		return nil, errors.New("oauthmgmt store requires a token exchanger")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return randomID(prefix) }
	}
	store := &Store{
		db: db, pg: postgres, secret: secret, now: now, newI: newID,
		sessions: newSessionStore(now), Accounts: accountsStore, exchanger: exchanger,
		proxyRequired: true,
	}
	for _, opt := range opts {
		opt(store)
	}
	return store, nil
}

// ssoDeps renders the device-flow collaborators: the injected (or default)
// transport, the injected (or default) sleep and the store clock.
func (s *Store) ssoDeps() SSODeviceDeps {
	requester := s.ssoRequester
	if requester == nil {
		requester = defaultSSODeviceRequester()
	}
	return SSODeviceDeps{Request: requester, Sleep: s.ssoSleep, Now: s.now}
}

// randomID mirrors Node newId(prefix): "{prefix}_{millis}_{8 hex}".
func randomID(prefix string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return prefix + "_" + itoa64(time.Now().UnixMilli()) + "_" + hex.EncodeToString(buf)[:8]
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_business." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if !s.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

// isoMillis mirrors Node nowIso() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// nowISO renders the store clock.
func (s *Store) nowISO() string { return isoMillis(s.now()) }

// stripURLQueryForLog 返回去掉 query、fragment 与 userinfo 的日志用 URL。
// token/设备流端点的 query 可能携带凭据类参数（如 verification_uri_complete
// 的 user_code），本包不跨域引用 gatewayusage 的 SanitizeURLForLog（用量快照
// 域），按日志治理规范直接砍掉 query，保留 scheme/host/path 供定位上游。
func stripURLQueryForLog(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String()
}

// exchange performs one upstream token call through the injected exchanger.
// 所有供应商（openai/anthropic/gemini/grok）的 token 出站都经过这里：统一落
// 日志（URL/状态码/耗时/响应体长度），失败时附截断后的响应体，避免上游侧
// 问题需要反复发版才能定位。日志不打印响应体原文（含 access_token）。
func (s *Store) exchange(ctx context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
	started := time.Now()
	response, err := s.exchanger.Do(ensureContext(ctx), request)
	elapsed := time.Since(started)
	if err != nil {
		slog.Error("OAuth token 请求失败",
			"event", "oauth_token_request_failed",
			"url", stripURLQueryForLog(request.URL),
			"elapsedMs", elapsed.Milliseconds(), "error", err)
		return response, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body := response.Body
		if len(body) > 300 {
			body = body[:300] + "…"
		}
		slog.Error("OAuth token 请求被上游拒绝",
			"event", "oauth_token_request_rejected",
			"url", stripURLQueryForLog(request.URL),
			"status", response.StatusCode, "elapsedMs", elapsed.Milliseconds(),
			"bodyLen", len(response.Body), "body", body)
		return response, nil
	}
	slog.Info("OAuth token 请求完成",
		"event", "oauth_token_request_completed",
		"url", stripURLQueryForLog(request.URL),
		"status", response.StatusCode, "elapsedMs", elapsed.Milliseconds(),
		"bodyLen", len(response.Body))
	return response, nil
}

// unmarshalSession decodes a stored session envelope.
func unmarshalSession(raw json.RawMessage, target any) error {
	return json.Unmarshal(raw, target)
}

// canonicalRFC3339 mirrors canonicalizeRfc3339Instant.
func canonicalRFC3339(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return isoMillis(parsed), true
}

// AccessScope mirrors storage/access-scope.ts for this slice: admins operate
// across owners unless a systemAccountId filter narrows the view; users are
// pinned to their own rows (forceSelfAccessScope).
type AccessScope struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

// manageableID mirrors manageableSystemAccountId.
func (a AccessScope) manageableID() string {
	if a.IsAdmin {
		return a.FilterID
	}
	return a.ViewerID
}

func (a AccessScope) canAccessAll() bool { return a.IsAdmin }

// accountsScope renders the M08 access scope for delegated account creation.
func (a AccessScope) accountsScope() accounts.AccessScope {
	return accounts.AccessScope{ViewerID: a.ViewerID, IsAdmin: a.IsAdmin, FilterID: a.FilterID}
}

// providerProfile mirrors the resolved OAuth connection identity: the
// registry-derived profile id plus the vendor default supported models.
// 目标 schema（§9.3）删除 provider_protocol_profiles：协议码/版本与账户类型
// 允许集由编译期 registry（§1.1/§2）确定性派生；vendor 行只提供 enabled 与
// default_supported_models_json。档案缺省健康检查模型随旧表删除，健康检查
// 模型缺省改为已解析支持模型集内首个声明该检查形态的模型（用户显式提交
// 优先）。
type providerProfile struct {
	ID                     string
	ProviderCode           string
	Name                   string
	ProtocolCode           string
	ProtocolVersion        string
	DefaultSupportedModels []string
}

// profileIDForProvider 返回 §2 组合表按 vendor 绑定的唯一 profile 身份
// （与前端表单常量同一词表；grok 的 pin 即 xai 行）。未知 vendor 返回空。
func profileIDForProvider(providerCode string) string {
	switch providerCode {
	case ProviderGPT:
		return ProfileGPTOpenAIV1
	case ProviderXAI:
		return ProfileXAIOpenAIV1
	case ProviderAnthropic:
		return ProfileAnthropicV1
	case ProviderGemini:
		return ProfileGeminiNativeV1Beta
	default:
		return ""
	}
}

// profileAccountTypesForProvider 是 §1.1 registry 的账户类型允许集。
func profileAccountTypesForProvider(providerCode string) []string {
	if providerCode == ProviderGemini {
		return []string{"api_key", "google_oauth"}
	}
	return []string{"api_key", "oauth"}
}

// resolveProviderProfile mirrors resolveOpenAI/Anthropic/Gemini/GrokOAuthProviderProfile:
// vendor lookup + enabled check, profile identity match, account-type membership
// and (grok) the exact profile id pin. 旧 providers/provider_protocol_profiles
// 联表读取已拆除（T30/B4 ⑧）：目标 schema 无档案表，协议与允许集改由编译期
// registry 派生，错误文案保持既有对外契约。
func (s *Store) resolveProviderProfile(ctx context.Context, providerCode, profileID, accountType, requiredProfileID string) (*providerProfile, error) {
	var row struct {
		enabled                sql.NullInt64
		defaultSupportedModels sql.NullString
	}
	err := s.db.QueryRowContext(ensureContext(ctx), s.bind(`SELECT enabled, default_supported_models_json
		FROM `+s.table("vendors")+`
		WHERE code = ?
		LIMIT 1`), providerCode).Scan(&row.enabled, &row.defaultSupportedModels)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ValidationError{Message: "不支持的供应商：" + providerCode}
	}
	if err != nil {
		return nil, err
	}
	if !row.enabled.Valid || row.enabled.Int64 != 1 {
		return nil, &ValidationError{Message: "供应商已停用：" + providerCode}
	}
	profileID = strings.TrimSpace(profileID)
	expectedProfileID := profileIDForProvider(providerCode)
	if expectedProfileID == "" || profileID != expectedProfileID {
		return nil, &ValidationError{Message: "供应商协议档案无效：" + profileID}
	}
	if requiredProfileID != "" && profileID != requiredProfileID {
		return nil, &ValidationError{Message: "供应商协议档案 " + profileID + " 不支持 " + oauthLabelForProvider(providerCode) + " OAuth"}
	}
	accountTypeSupported := false
	for _, candidate := range profileAccountTypesForProvider(providerCode) {
		if candidate == accountType {
			accountTypeSupported = true
			break
		}
	}
	if !accountTypeSupported {
		return nil, &ValidationError{Message: "供应商协议档案 " + profileID + " 不支持 " + oauthLabelForProvider(providerCode) + " OAuth"}
	}
	profile := &providerProfile{
		ID: profileID, ProviderCode: providerCode, Name: profileID,
		ProtocolCode:           protocolCodeForProvider(providerCode),
		ProtocolVersion:        protocolVersionForProvider(providerCode),
		DefaultSupportedModels: []string{},
	}
	if row.defaultSupportedModels.Valid && strings.TrimSpace(row.defaultSupportedModels.String) != "" {
		_ = json.Unmarshal([]byte(row.defaultSupportedModels.String), &profile.DefaultSupportedModels)
	}
	return profile, nil
}

// firstSupportedModelDeclaringMode 在已解析支持模型集内按顺序取首个声明
// healthCheckEndpointMode 的模型。目标 schema 已删除档案 default_health_check_model
// （§9.3），健康检查模型缺省改由真实能力声明决定；无模型声明时回落首个
// 支持模型，保持写面既有 health_check_connection_conflict 错误面。
func (s *Store) firstSupportedModelDeclaringMode(ctx context.Context, providerCode, mode string, supportedModels []string) (string, error) {
	if len(supportedModels) == 0 {
		return "", nil
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return supportedModels[0], nil
	}
	placeholders := make([]string, 0, len(supportedModels))
	args := make([]any, 0, len(supportedModels)+1)
	args = append(args, providerCode)
	for _, model := range supportedModels {
		placeholders = append(placeholders, "?")
		args = append(args, model)
	}
	rows, err := s.db.QueryContext(ensureContext(ctx), s.bind(`SELECT upstream_model, supported_endpoint_modes_json
		FROM `+s.table("models")+`
		WHERE vendor_code = ? AND upstream_model IN (`+strings.Join(placeholders, ", ")+`)`), args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	declared := map[string]bool{}
	for rows.Next() {
		var model, modesJSON string
		if err := rows.Scan(&model, &modesJSON); err != nil {
			return "", err
		}
		modes := []string{}
		if strings.TrimSpace(modesJSON) != "" {
			_ = json.Unmarshal([]byte(modesJSON), &modes)
		}
		for _, candidate := range modes {
			if candidate == mode {
				declared[model] = true
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	for _, model := range supportedModels {
		if declared[model] {
			return model, nil
		}
	}
	return supportedModels[0], nil
}

// protocolCodeForProvider mirrors the per-provider protocol expectations.
func protocolCodeForProvider(providerCode string) string {
	if providerCode == ProviderGemini {
		return "gemini"
	}
	if providerCode == ProviderAnthropic {
		return "anthropic"
	}
	return "openai"
}

func protocolVersionForProvider(providerCode string) string {
	if providerCode == ProviderGemini {
		return "v1beta"
	}
	return "v1"
}

// oauthLabelForProvider mirrors the route copy labels ("OpenAI", "Anthropic",
// "Gemini", "Grok").
func oauthLabelForProvider(providerCode string) string {
	switch providerCode {
	case ProviderGPT:
		return "OpenAI"
	case ProviderAnthropic:
		return "Anthropic"
	case ProviderGemini:
		return "Gemini"
	case ProviderXAI:
		return "Grok"
	}
	return providerCode
}

// findEnabledGroupInScope 校验建户目标分组（供应商与上游协议面重构设计 §8：
// groups.provider_code 已删除，分组与 provider 无关，普通分组可含任意厂商账
// 户）：分组在访问 scope 内存在且 enabled 即通过。admin（manageableID 为空）
// 跨 owner 校验存在性；非 admin 钉自己的 owner。与 accounts 写面
// groupOwnerReference 的归属口径同向（admin 显式分组时写面把账户 owner 切到
// 分组 owner），路由面只提前 400 不改语义。
func (s *Store) findEnabledGroupInScope(ctx context.Context, groupID string, access AccessScope) (bool, error) {
	where := ""
	args := []any{groupID}
	if scoped := access.manageableID(); scoped != "" {
		where = " AND system_account_id = ?"
		args = append(args, scoped)
	}
	var id string
	err := s.db.QueryRowContext(ensureContext(ctx), s.bind(`SELECT id FROM `+s.table("groups")+`
		WHERE id = ? AND enabled = 1`+where+` LIMIT 1`), args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// CreateAccountInput bundles everything the OAuth create routes hand to the
// M08 accounts store.
type CreateAccountInput struct {
	ProviderCode              string
	ProviderProtocolProfileID string
	Name                      string
	AccountType               string
	Credentials               map[string]any
	Status                    string
	ConcurrencyLimit          *int
	Priority                  *int
	SuperPriorityEnabled      *bool
	FallbackEnabled           *bool
	SupportedModels           []string
	HealthCheckModel          *string
	HealthCheckEndpointMode   *string
	ModelMappings             []accounts.ModelMapping
	Tags                      []string
	ProxyProfileID            *string
	GroupID                   *string
	AccountExpiresAt          *string
	AvailabilitySchedule      any
	Notes                     *string
}

// CreateOAuthAccount mirrors createAccountAsync on the M08 slice: profile
// defaults for supported models, accountCreationStatusInput semantics and the
// delegated owner-scoped insert with group binding. 连接维度（供应商与上游协
// 议面重构设计 §8）：OAuth 建户路由不携带 connections 与健康检查连接维度，
// 按 vendor/账户类型经 accounts.DefaultConnectionsForOAuthVendor 派生默认连
// 接集合与健康检查连接维度（显式入参优先）；派生载荷进入 Store.Create 后仍
// 受写面 §8 全量校验，必填门不放宽。
func (s *Store) CreateOAuthAccount(ctx context.Context, input CreateAccountInput, access AccessScope) (*accounts.CreateResult, error) {
	defaults, err := accounts.DefaultConnectionsForOAuthVendor(input.ProviderCode, input.AccountType, accounts.Credentials(input.Credentials))
	if err != nil {
		return nil, err
	}
	profile, err := s.resolveProviderProfile(ctx, input.ProviderCode, input.ProviderProtocolProfileID, input.AccountType, requiredProfileForProvider(input.ProviderCode))
	if err != nil {
		return nil, err
	}
	supportedModels := input.SupportedModels
	if len(supportedModels) == 0 {
		// OAuth 建户无法即时枚举上游模型：默认取模型目录里该供应商最新的
		// 5 个对话模型（目录带 release_date，随目录刷新自动跟进）；目录不可
		// 用（表缺失/无数据）时回落供应商档案的默认支持模型。
		if latest, latestErr := s.latestCatalogModels(ctx, input.ProviderCode, 5); latestErr == nil && len(latest) > 0 {
			supportedModels = latest
		} else {
			supportedModels = profile.DefaultSupportedModels
		}
	}
	// 健康检查三件套缺省：preset/mode 取派生连接维度；model 取支持模型集内
	// 首个声明该检查形态的模型（目标 schema 已删除档案 default_health_check_model；
	// 用户显式提交 healthCheckModel 时优先）。
	healthCheckModel := input.HealthCheckModel
	if healthCheckModel == nil && len(supportedModels) > 0 {
		configured, selectErr := s.firstSupportedModelDeclaringMode(ctx, input.ProviderCode, defaults.HealthCheckEndpointMode, supportedModels)
		if selectErr != nil {
			return nil, selectErr
		}
		if configured != "" {
			healthCheckModel = &configured
		}
	}
	healthCheckEndpointMode := input.HealthCheckEndpointMode
	if healthCheckEndpointMode == nil {
		mode := defaults.HealthCheckEndpointMode
		healthCheckEndpointMode = &mode
	}
	creationStatus := accounts.AccountCreationStatusInput(input.Status)
	healthCheckPreset := defaults.HealthCheckConnectionPresetCode
	result, err := s.Accounts.Create(ensureContext(ctx), accounts.CreateInput{
		ProviderCode:                    input.ProviderCode,
		ProviderProtocolProfileID:       input.ProviderProtocolProfileID,
		Name:                            input.Name,
		AccountType:                     input.AccountType,
		Credentials:                     accounts.Credentials(input.Credentials),
		SupportedModels:                 supportedModels,
		Connections:                     defaults.Connections,
		HealthCheckConnectionPresetCode: &healthCheckPreset,
		HealthCheckModel:                healthCheckModel,
		HealthCheckEndpointMode:         healthCheckEndpointMode,
		ModelMappings:                   input.ModelMappings,
		Tags:                            input.Tags,
		Status:                          creationStatus,
		ConcurrencyLimit:                input.ConcurrencyLimit,
		Priority:                        input.Priority,
		SuperPriorityEnabled:            input.SuperPriorityEnabled,
		FallbackEnabled:                 input.FallbackEnabled,
		ProxyProfileID:                  input.ProxyProfileID,
		GroupID:                         input.GroupID,
		AccountExpiresAt:                input.AccountExpiresAt,
		AvailabilitySchedule:            input.AvailabilitySchedule,
		Notes:                           input.Notes,
	}, access.accountsScope())
	if err != nil {
		var conflict *accounts.ConflictError
		if errors.As(err, &conflict) {
			return nil, &ConflictError{Message: conflict.Message}
		}
		var validation *accounts.ValidationError
		if errors.As(err, &validation) {
			return nil, &ValidationError{Message: validation.Message}
		}
		return nil, err
	}
	return result, nil
}

// requiredProfileForProvider mirrors the grok profile pin (XAI_OPENAI_V1 only).
func requiredProfileForProvider(providerCode string) string {
	if providerCode == ProviderXAI {
		return ProfileXAIOpenAIV1
	}
	return ""
}

// containsString 报告列表是否含精确相等的项（健康检查缺省模型归属判断）。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// latestCatalogModels returns the provider's newest conversational models from
// the builtin arm of the models 单表（供应商与上游协议面重构设计 §3：
// provider_model_catalog 已并入 models，旧目录行 scope 固定迁为 builtin，
// provider_code 值域迁为 vendor_code）。谓词与写入面 resolveEntryModelID 同键
// 同口径：status='active' + builtin 窗口（catalog_visible 不参与成员资格，
// §4；shutdown_date 同样不是成员资格维度），默认值因此全部能过建户写面断言。
// 排除图像/向量：旧 mode 谓词（mode != 'image_generation' AND mode !=
// 'embedding'）经 §2.2 mode 词表逆映射等价改写为 supported_endpoint_modes_json
// 不含 images_json 与 embedding 族三 mode；空/NULL modes 数组不过滤，等价旧
// `mode IS NULL OR mode = ”` 保留臂。返回 upstream_model（写入面身份键）。
// release_date 为空的行排最后；CASE 排序写法对 SQLite 与 PG 同时成立（PG 的
// DESC 默认 NULLS FIRST，必须显式压后）。
func (s *Store) latestCatalogModels(ctx context.Context, providerCode string, limit int) ([]string, error) {
	// json_each / jsonb_array_elements_text 分别是 SQLite 与 PG 的数组展开读
	// 法（同 providers.endpointModesAnyPredicate 方案，本包不跨包引用私有方
	// 法，方言分支就地生成；元素列名两者同为 value）。
	modesSource := "json_each(supported_endpoint_modes_json)"
	if s.pg {
		modesSource = "jsonb_array_elements_text(supported_endpoint_modes_json::jsonb)"
	}
	rows, err := s.db.QueryContext(ensureContext(ctx), s.bind(`SELECT upstream_model FROM `+s.table("models")+`
		WHERE scope = 'builtin' AND vendor_code = ? AND status = 'active'
			AND NOT EXISTS (
				SELECT 1 FROM `+modesSource+` AS endpoint_mode
				WHERE endpoint_mode.value IN (?, ?, ?, ?)
			)
		ORDER BY CASE WHEN release_date IS NULL OR trim(release_date) = '' THEN 1 ELSE 0 END,
			release_date DESC, upstream_model ASC
		LIMIT `+itoa(limit)), providerCode,
		"images_json", "embed_content", "count_tokens", "message_token_counting")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := []string{}
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		if strings.TrimSpace(model) != "" {
			models = append(models, model)
		}
	}
	return models, rows.Err()
}

// rotationAccount mirrors OAuthCredentialRotationAccount. ProviderCode 承载
// 目标 schema 的 vendor_code 身份值（§2.1：真实厂商 vendor_code == provider_code）。
type rotationAccount struct {
	ID              string
	SystemAccountID string
	ProviderCode    string
	ProtocolCode    string
	ProtocolVersion string
	// OAuthType 是目标 schema 的 accounts.oauth_type 判定列（§2.1；仅
	// google_oauth 资源账户非空）。判定列优先、凭据键回落服务迁移窗口存量行
	//（#B4 ⑨：refresh 删键前必须完成本读面切换）。
	OAuthType             string
	Name                  string
	Type                  string
	Status                string
	LastErrorCode         string
	ProxyProfileID        string
	Credentials           map[string]any
	CredentialFingerprint string
	AccountExpiresAt      string
	ConfigRevision        int64
	UpdatedAt             string
}

// CredentialsUnavailableError marks a scope-matched account whose sealed
// credentials are absent or undecryptable (Node decryptJson throw path).
type CredentialsUnavailableError struct{}

func (e *CredentialsUnavailableError) Error() string { return "OAuth 凭据读取失败" }

// findRotationAccount mirrors findOAuthCredentialRotationAccountAsync.
func (s *Store) findRotationAccount(ctx context.Context, accountID string, access AccessScope) (*rotationAccount, error) {
	where := ""
	args := []any{accountID}
	if scoped := access.manageableID(); scoped != "" {
		where = " AND system_account_id = ?"
		args = append(args, scoped)
	}
	var row struct {
		id               string
		systemAccountID  string
		providerCode     string
		protocolCode     string
		protocolVersion  string
		oauthType        string
		name             string
		accountType      string
		status           string
		lastErrorCode    sql.NullString
		proxyProfileID   sql.NullString
		encrypted        string
		fingerprint      sql.NullString
		accountExpiresAt sql.NullString
		configRevision   int64
		updatedAt        string
	}
	// 身份键按目标 schema（供应商与上游协议面重构设计 §2.1/§9.11）：真实厂商
	// vendor_code == provider_code，行读以 COALESCE(vendor_code,'') 承载既有
	// ProviderCode 语义；provider_protocol_profile_id 列已随目标列集删除。
	err := s.db.QueryRowContext(ensureContext(ctx), s.bind(`SELECT id, system_account_id, COALESCE(vendor_code, ''),
			protocol_code, protocol_version, COALESCE(oauth_type, ''), name, type, status,
			last_error_code, proxy_profile_id, credentials_encrypted, credential_fingerprint,
			account_expires_at, config_revision, updated_at
		FROM `+s.table("accounts")+`
		WHERE id = ?
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL`+where+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.systemAccountID, &row.providerCode,
		&row.protocolCode, &row.protocolVersion, &row.oauthType, &row.name, &row.accountType,
		&row.status, &row.lastErrorCode, &row.proxyProfileID, &row.encrypted, &row.fingerprint,
		&row.accountExpiresAt, &row.configRevision, &row.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	credentials := map[string]any{}
	if err := decryptJSON(s.secret, row.encrypted, &credentials); err != nil {
		return nil, &CredentialsUnavailableError{}
	}
	if credentials == nil {
		credentials = map[string]any{}
	}
	if row.configRevision < 1 {
		row.configRevision = 1
	}
	return &rotationAccount{
		ID: row.id, SystemAccountID: row.systemAccountID,
		ProviderCode: row.providerCode,
		ProtocolCode: row.protocolCode, ProtocolVersion: row.protocolVersion,
		OAuthType: row.oauthType,
		Name:      row.name, Type: row.accountType, Status: row.status,
		LastErrorCode: row.lastErrorCode.String, ProxyProfileID: row.proxyProfileID.String,
		Credentials: credentials, CredentialFingerprint: row.fingerprint.String,
		AccountExpiresAt: row.accountExpiresAt.String,
		ConfigRevision:   row.configRevision, UpdatedAt: row.updatedAt,
	}, nil
}

// RotationResult mirrors OAuthCredentialRotationResult.
type RotationResult struct {
	ID             string
	ConfigRevision int64
	UpdatedAt      string
	Changed        bool
	Credentials    map[string]any
}

// RotateCredentialsInput mirrors rotateOAuthCredentialsAsync input. 身份 CAS
// 键 = vendor_code（§2.1 行为键，真实厂商 == provider_code）+ account type +
// config revision；profile 维度已随目标列集删除（§9.11）。
type RotateCredentialsInput struct {
	AccountID              string
	ExpectedConfigRevision int64
	ExpectedProviderCode   string
	ExpectedAccountType    string
	Credentials            map[string]any
	Access                 AccessScope
}

// RotateCredentials mirrors rotateOAuthCredentialsAsync (the recovery-state
// branch is an M17 deferral): revision CAS, credential column swap,
// config_revision + 1 and the unchanged no-op receipt.
func (s *Store) RotateCredentials(ctx context.Context, input RotateCredentialsInput) (*RotationResult, error) {
	ctx = ensureContext(ctx)
	if input.ExpectedConfigRevision < 1 {
		return nil, &ValidationError{Message: "账户配置版本无效"}
	}
	if expiresAt, exists := input.Credentials["expires_at"]; exists {
		if _, ok := canonicalRFC3339(normalizeText(expiresAt)); !ok {
			return nil, &ValidationError{Message: "OAuth 凭据 expires_at 必须是带 Z 或数值 offset 的 RFC3339 时间"}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	where := ""
	args := []any{input.AccountID}
	if scoped := input.Access.manageableID(); scoped != "" {
		where = " AND system_account_id = ?"
		args = append(args, scoped)
	}
	var row struct {
		id              string
		systemAccountID string
		providerCode    string
		accountType     string
		encrypted       string
		configRevision  int64
	}
	// 身份读键 = vendor_code（§2.1 行为键，真实厂商 == provider_code；
	// §9.11 目标列集无 provider_code / provider_protocol_profile_id）。
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, COALESCE(vendor_code, ''),
			type, credentials_encrypted, config_revision
		FROM `+s.table("accounts")+`
		WHERE id = ?
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL`+where+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.systemAccountID, &row.providerCode, &row.accountType,
		&row.encrypted, &row.configRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.providerCode != input.ExpectedProviderCode ||
		row.accountType != input.ExpectedAccountType {
		return nil, nil
	}
	if row.configRevision != input.ExpectedConfigRevision {
		return nil, &RevisionConflictError{Message: "账户配置版本冲突"}
	}
	// 覆盖字段值域落库校验（C3）：service_tier_override / reasoning_effort_override
	// 与账户写入面凭据归一化同规则（token 形状 + 协议族值域，单一真源
	// shared/platform requestoverride，见 requestoverride_validation.go）。轮换
	// （refreshToken）与重授权（reauthorizeFromCode / reauthorizeFromRefreshToken）
	// 共用本入口；非法值在事务内先拒绝、不写库——否则畸形值落库后会在请求时被
	// 运行时 gatewayoauthcodex.ReadGptAccountRequestOverrides 打成硬失败。存量
	// 畸形值在下次轮换/重授权保存时同样被拒（覆盖设计 §6.4：非法存量值在下次
	// 保存时被后端拒绝），故校验先于凭据未变化的 no-op 分支。
	if err := validateAccountRequestOverrideCredentials(row.providerCode, input.Credentials); err != nil {
		return nil, err
	}
	current := map[string]any{}
	if err := decryptJSON(s.secret, row.encrypted, &current); err != nil {
		return nil, &CredentialsUnavailableError{}
	}
	if credentialsEqual(current, input.Credentials) {
		return &RotationResult{
			ID: row.id, ConfigRevision: row.configRevision,
			UpdatedAt: s.nowISO(), Changed: false, Credentials: input.Credentials,
		}, nil
	}
	source, err := requiredCredentialSource(input.ExpectedAccountType, input.Credentials)
	if err != nil {
		return nil, err
	}
	sealed, err := encryptJSON(s.secret, map[string]any(input.Credentials))
	if err != nil {
		return nil, err
	}
	expiresAt := sql.NullString{}
	if value, exists := input.Credentials["expires_at"]; exists {
		if canonical, ok := canonicalRFC3339(normalizeText(value)); ok {
			expiresAt = sql.NullString{String: canonical, Valid: true}
		}
	}
	refreshPresent := 0
	if text := normalizeText(input.Credentials["refresh_token"]); text != "" {
		refreshPresent = 1
	}
	updatedAt := s.nowISO()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
			oauth_access_token_expires_at = ?, oauth_refresh_token_present = ?,
			config_revision = config_revision + 1, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND COALESCE(vendor_code, '') = ?
			AND type = ? AND config_revision = ?
			AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`),
		sealed, hashSecret(source), maskSecret(source), expiresAt, refreshPresent, updatedAt,
		row.id, row.systemAccountID, row.providerCode, row.accountType, row.configRevision)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: "账户配置版本冲突"}
	}
	// In-transaction circuit fence (T2 audit; Node
	// oauth-credential-rotation.repository.ts:202-214): when the upstream
	// connection identity changed, advance the dispatch revision family so an
	// OPEN transport circuit cannot survive a credential rotation. The family
	// advance must run inside the rotation transaction — a failure rolls the
	// whole rotation back.
	if s.revisionAdvancer != nil && circuitCredentialIdentityChanged(current, input.Credentials) {
		if err := s.revisionAdvancer.AdvanceDispatchRevisionFamily(ctx, tx, row.id, s.newI("dispatch"), s.now().UnixMilli()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	rotation := &RotationResult{
		ID: row.id, ConfigRevision: row.configRevision + 1,
		UpdatedAt: updatedAt, Changed: true, Credentials: input.Credentials,
	}
	// Post-commit double notification (T2 audit; Node
	// oauth-credential-rotation.repository.ts:223-226).
	s.finishRotationSideEffects(rotation)
	return rotation, nil
}

// credentialsEqual mirrors isDeepStrictEqual over the JSON-normalized
// credential maps (canonical re-encode comparison).
func credentialsEqual(current, next map[string]any) bool {
	encode := func(value map[string]any) (string, bool) {
		raw, err := json.Marshal(value)
		return string(raw), err == nil
	}
	left, leftOK := encode(current)
	right, rightOK := encode(next)
	return leftOK && rightOK && left == right
}

// stringCredential mirrors stringCredential: trimmed string credential or "".
func stringCredential(credentials map[string]any, key string) string {
	return normalizeText(credentials[key])
}

// requiredCredentialSource mirrors requiredAccountCredentialSource
// (account-credentials-normalization.ts): the secret field that backs the
// fingerprint/mask columns per account type. Kept package-local because the
// accounts slice keeps its own copy private.
func requiredCredentialSource(accountType string, credentials map[string]any) (string, error) {
	pick := func(keys ...string) string {
		for _, key := range keys {
			if value := stringCredential(credentials, key); value != "" {
				return value
			}
		}
		return ""
	}
	switch accountType {
	case "oauth":
		if value := pick("refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "OAuth 凭据不能为空"}
	case "api_key":
		if value := stringCredential(credentials, "api_key"); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "API Key 不能为空"}
	case "google_oauth":
		if value := pick("refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "Google OAuth 凭据不能为空"}
	default:
		if value := pick("api_key", "refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "账户凭据不能为空"}
	}
}
