package accounts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	"github.com/jackc/pgx/v5/pgconn"
)

// ModelMapping mirrors AccountModelMapping (accountModelMappingSchema).
// （REFACTOR-0005 阶段 0 下沉 accountscore，根包保留类型别名。）
type ModelMapping = accountscore.ModelMapping

// Endpoint family enums of accountModelMappingSchema: an unknown value fails
// the create/PATCH schema parse (400) instead of persisting an unmappable row.
var (
	accountSourceEndpointFamilies = map[string]bool{
		"chat_completions": true, "responses": true, "messages": true,
		"generate_content": true, "stream_generate_content": true,
		// M4b 媒体映射族（媒体设计 §9 hybrid 行）：与 schema 端点族 seed
		// 同码（openai_v1_video_generation / openai_v1_tts）。
		"video_generation": true, "tts": true,
	}
	accountUpstreamEndpointFamilies = map[string]bool{
		"chat_completions": true, "responses": true, "messages": true,
		"generate_content": true,
		// M4b：媒体映射是同族模型名改写（上游面恒为 OpenAI 形态媒体端点）；
		// stream_generate_content 无上游形态不进上游枚举——与 chat 族仅作
		// source 的既有先例一致。
		"video_generation": true, "tts": true,
	}
)

// normalizeModelMappingBody mirrors accountModelMappingSchema.parse: the four
// trimmed string fields are required and the endpoint families are strict
// enums. The message matches the route-family 400 copy.
func normalizeModelMappingBody(object map[string]any) (ModelMapping, bool) {
	mapping := ModelMapping{
		SourceModel:                strings.TrimSpace(textString(object["sourceModel"])),
		SourceEndpointFamily:       strings.TrimSpace(textString(object["sourceEndpointFamily"])),
		UpstreamModel:              strings.TrimSpace(textString(object["upstreamModel"])),
		UpstreamEndpointFamily:     strings.TrimSpace(textString(object["upstreamEndpointFamily"])),
		TargetConnectionPresetCode: strings.TrimSpace(textString(object["targetConnectionPresetCode"])),
	}
	if mapping.SourceModel == "" || mapping.UpstreamModel == "" ||
		mapping.SourceEndpointFamily == "" || mapping.UpstreamEndpointFamily == "" {
		return ModelMapping{}, false
	}
	if !accountSourceEndpointFamilies[mapping.SourceEndpointFamily] ||
		!accountUpstreamEndpointFamilies[mapping.UpstreamEndpointFamily] {
		return ModelMapping{}, false
	}
	if enabled, ok := object["enabled"].(bool); ok {
		mapping.Enabled = &enabled
	} else if value, exists := object["enabled"]; exists && value != nil {
		return ModelMapping{}, false
	}
	// strict(): unknown keys fail the parse.
	for key := range object {
		switch key {
		case "sourceModel", "sourceEndpointFamily", "upstreamModel", "upstreamEndpointFamily", "enabled",
			// B4 W2（设计 §5.1/§8）：连接维度自然键入参。
			"targetConnectionPresetCode":
		default:
			return ModelMapping{}, false
		}
	}
	return mapping, true
}

// normalizeConnectionBody mirrors the §8 connections[] element contract: the
// required connectionPresetCode / baseUrl / endpointModes（非空字符串数组）/
// enabled（必须显式布尔，§2.1）plus the optional protocolSurfaceCode echo;
// unknown keys fail the parse (strict()). 解析器实现下沉 accountscore
// （ParseConnectionInput），门面/导入/公开面共用同一份。
func normalizeConnectionBody(object map[string]any) (ConnectionInput, bool) {
	return accountscore.ParseConnectionInput(object)
}

// NormalizeConnectionBody 是 normalizeConnectionBody 的导出门面（aipublic 公开
// 面入参解析复用同一 §8 元素契约，避免第二份解析器）。
func NormalizeConnectionBody(object map[string]any) (ConnectionInput, bool) {
	return normalizeConnectionBody(object)
}

// accountHealthCheckEndpointModes 阶段 C 下沉 accountscore（导入域字段解析器
// 共用），门面保留同名别名.
var accountHealthCheckEndpointModes = accountscore.AccountHealthCheckEndpointModes

// realVendorCodes 是供应商与上游协议面重构设计 §1.1 注册表的 9 个真实厂商值域
// （与历史 provider connection migration 的 realVendorCodes 同词表）。
//
// vendor_code / connection_driver_code 推导规则（写入面唯一来源，创建请求体
// 当前不携带 vendor/driver 字段）：按 provider 既有语义推导，与已退役的
// 历史迁移规则中的 vendorOfProvider / driver 推导同口径，与 chain 面按
// vendor_code 取身份的运行时语义一致——
//   - provider_code 命中 9 厂商：vendor_code = provider_code，
//     connection_driver_code = provider_code（§1.1 注册表 driver 名与 vendor
//     同名；oauth 账户同规则，OpenAI OAuth 账户现以 provider gpt 创建，天然
//     落 vendor gpt）；
//   - 其余（openai / hybrid 等 generic 语义 provider）：vendor_code = NULL、
//     connection_driver_code = 'generic_http'（设计 §2.1：generic_http 账户
//     vendor 恒 NULL，空串由应用层 registry 保证不落库）。
var realVendorCodes = map[string]bool{
	"gpt": true, "xai": true, "deepseek": true, "anthropic": true, "gemini": true,
	"glm": true, "minimax": true, "volcengine": true, "qwen": true,
}

// accountVendorIdentity 把 provider 语义推导为账户行 (vendor_code,
// connection_driver_code) 写入值；vendor 走 NullString 承载 generic 账户的
// SQL NULL。推导规则见 realVendorCodes 注释。
func accountVendorIdentity(providerCode string) (sql.NullString, string) {
	if realVendorCodes[providerCode] {
		return sql.NullString{String: providerCode, Valid: true}, providerCode
	}
	return sql.NullString{}, "generic_http"
}

var accountStatusValues = map[string]bool{
	"active": true, "pending_test": true, "disabled": true, "error": true,
	"rate_limited": true, "temporary_unavailable": true, "quality_isolated": true,
}

// defaultAccountConcurrencyLimit mirrors DEFAULT_ACCOUNT_CONCURRENCY_LIMIT
// (schema default 5000).
const defaultAccountConcurrencyLimit = 5000

// CreationStatus / AccountCreationStatusInput / CreateInput 阶段 C 下沉中立层
// accountscore（accountstransfer 导入执行器的 AccountWriter 端口签名复用同一
// 契约），门面保留别名，write 路径零改动.
type (
	// CreationStatus mirrors accountscore.CreationStatus.
	CreationStatus = accountscore.CreationStatus
	// CreateInput mirrors accountscore.CreateInput.
	CreateInput = accountscore.CreateInput
	// ConnectionInput mirrors accountscore.ConnectionInput（§8 连接行载荷，B1
	// 断点批；门面别名保持 route/import/测试原调用形态）。
	ConnectionInput = accountscore.ConnectionInput
)

// AccountCreationStatusInput mirrors accountCreationStatusInput（accountscore
// 实现，门面转发保持原调用形态）.
func AccountCreationStatusInput(value any) CreationStatus {
	return accountscore.AccountCreationStatusInput(value)
}

// CreateInput is the validated create payload (accountCreateSchema subset the
// store consumes); nil pointers mean the field was absent.
// （阶段 C 起定义为 accountscore.CreateInput 别名，见上方别名块。）

// assertVendorEnabled 是 profile 行删除后的 vendor 归属校验（供应商与上游协
// 议面重构设计 §2.1/§9.1）：vendor_code 只允许 §1.1 九个真实厂商且外键引用
// vendors.code，真实厂商创建必须在 vendors 表存在且 enabled；generic 语义
// provider（openai/hybrid 等，vendor_code=NULL）无 vendors 行，跳过校验。
func (s *Store) assertVendorEnabled(ctx context.Context, q queryer, providerCode string) error {
	if !realVendorCodes[providerCode] {
		return nil
	}
	var enabled int64
	err := q.QueryRowContext(ctx, s.bind(`SELECT enabled FROM `+s.table("vendors")+`
		WHERE code = ?
		LIMIT 1`), providerCode).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Message: "不支持的供应商：" + providerCode}
	}
	if err != nil {
		return err
	}
	if enabled != 1 {
		return &ValidationError{Message: "供应商已停用：" + providerCode}
	}
	return nil
}

// requiredAccountCredentialSource mirrors requiredAccountCredentialSource
// (account-credentials-normalization.ts): the secret field that backs the
// fingerprint/mask columns per account type.
func requiredAccountCredentialSource(accountType string, credentials Credentials) (string, error) {
	text := func(value any) string {
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
		return ""
	}
	pick := func(keys ...string) string {
		for _, key := range keys {
			if value := text(credentials[key]); value != "" {
				return value
			}
		}
		return ""
	}
	apiKeys := func() string {
		if list, ok := credentials["api_keys"].([]any); ok {
			for _, item := range list {
				if value := text(item); value != "" {
					return value
				}
			}
		}
		return text(credentials["api_key"])
	}
	switch accountType {
	case "oauth":
		if value := pick("refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "OAuth 凭据不能为空"}
	case "api_key":
		if value := apiKeys(); value != "" {
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

// accountCredentialFingerprint mirrors account-identity.ts: sha256 hex of the
// trimmed source secret.
func accountCredentialFingerprint(source string) string {
	return HashSecret(strings.TrimSpace(source))
}

// normalizeAccountTagNamesInput mirrors normalizeAccountTagNamesInput.
func normalizeAccountTagNamesInput(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, &ValidationError{Message: "账户标签必须是字符串数组"}
	}
	output := []string{}
	seen := map[string]bool{}
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			return nil, &ValidationError{Message: "账户标签必须是字符串数组"}
		}
		name := strings.Join(strings.Fields(text), " ")
		if name == "" {
			continue
		}
		if len([]rune(name)) > maxTagNameLength {
			return nil, &ValidationError{Message: "账户标签不能超过 40 个字符"}
		}
		if seen[name] {
			continue
		}
		if len(output) >= maxTagsPerAccount {
			return nil, &ValidationError{Message: "单个账户最多配置 24 个标签"}
		}
		seen[name] = true
		output = append(output, name)
	}
	return output, nil
}

// normalizeSupportedModelsInput trims, dedupes and drops blanks (mirror of the
// supported-models normalization subset the slice keeps). A single model is
// bounded to 200 characters and an account to 64 models so a misbehaving
// writer cannot balloon the candidate merge used by model-check options.
func normalizeSupportedModelsInput(value any) ([]string, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, &ValidationError{Message: "账户支持模型必须是字符串数组"}
	}
	seen := map[string]bool{}
	output := []string{}
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			return nil, &ValidationError{Message: "账户支持模型必须是字符串数组"}
		}
		model := strings.TrimSpace(text)
		if model == "" || seen[model] {
			continue
		}
		if len(model) > 200 {
			return nil, &ValidationError{Message: "单个支持模型长度不能超过 200 字符"}
		}
		seen[model] = true
		output = append(output, model)
	}
	if len(output) > 64 {
		return nil, &ValidationError{Message: "单个账户最多配置 64 个支持模型"}
	}
	return output, nil
}

func assertSupportedModelsRequired(models []string) error {
	for _, model := range models {
		if strings.TrimSpace(model) != "" {
			return nil
		}
	}
	return &ValidationError{Message: "账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型"}
}

// normalizedHealthCheckModel mirrors normalizedAccountHealthCheckModelInput.
func normalizedHealthCheckModel(value string, supportedModels []string) (string, error) {
	model := strings.TrimSpace(value)
	if model == "" {
		return "", &ValidationError{Message: "账户检查模型不能为空"}
	}
	for _, candidate := range supportedModels {
		if candidate == model {
			return model, nil
		}
	}
	return "", &ValidationError{Message: "账户检查模型必须属于账户支持模型"}
}

// groupOwnerReference mirrors groupOwnerAndProviderForAccountWriteAsync rows
// （组无 provider 维度，设计 §8）：只解析 owner 归属维度。
type groupReference struct {
	id              string
	systemAccountID string
	name            sql.NullString
}

// nullStringForOAuthType 把空串 oauth_type 判定值落 SQL NULL（§2.1：非
// google_oauth 账户与未携带 oauth_type 的 google_oauth 存量形态均 NULL）。
func nullStringForOAuthType(value string) sql.NullString {
	if strings.TrimSpace(value) == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: strings.TrimSpace(value), Valid: true}
}

// Create mirrors createAccountInClientAsync (owner-mode core): provider
// profile check, group binding resolution, sealed credentials with fingerprint
// and mask, schedule-aware initial status, supported models / mappings / tags
// / name search terms writes, then the in-transaction tail of the archive
// create (repositories.ts:2412-2432): the dispatch revision family advance
// plus the J1 snapshot health-input outbox row for the health-capable
// gpt/openai accounts (BUG-0174 M-8; the direct_input_reader candidate SQL
// hard-joins account_health_jobs_input_versions, so the version row the
// snapshot reserves is what makes a new account visible to probing). The
// committed tail (repositories.ts:2440-2443) lands after the commit through
// finishCreateSideEffects; the initial activation probe stays the committed
// caller below, exactly like the Node route layer.
func (s *Store) Create(ctx context.Context, input CreateInput, access AccessScope) (*CreateResult, error) {
	ctx = ensureCtx(ctx)
	result, err := s.createInTransactionWithRetry(ctx, input, access)
	if err != nil {
		return nil, err
	}
	// Post-commit invalidation tail (repositories.ts:2440-2443): the group
	// stats dirty marker plus the lookup / group-account-ids / gateway
	// runtime flushes, best-effort.
	s.finishCreateSideEffects(ctx, result)
	// Node accounts.routes.ts dispatches the initial probe right after the
	// create transaction succeeds and before the 201 is rendered. A nil
	// effects port keeps the create self-contained (tests); the probe reason
	// mirrors Node's 'activation'.
	if result.InitialHealthCheckRequired {
		if effects := s.runtimeResetEffectsOrNil(); effects != nil {
			effects.DispatchAccountHealthCheck(result.ID, "activation")
		}
	}
	return result, nil
}

// accountCreateTxAttempts 是账户创建事务在 PostgreSQL 死锁/序列化失败下的
// 有界重试次数。创建事务与常驻 jobs（账户健康、可用性投影、分组统计等）
// 并发写 accounts / group_accounts 相关行，PG 检测到锁环时会中止其中一方
// （40P01 deadlock_detected / 40001 serialization_failure）；死锁是瞬态
// 事实，重试整个事务即可恢复，不应把 500 暴露给调用方（2026-10-26 全链路
// 验收实证：jobs 常驻后连续创建第三个账户时命中 40P01）。
const accountCreateTxAttempts = 3

func (s *Store) createInTransactionWithRetry(ctx context.Context, input CreateInput, access AccessScope) (*CreateResult, error) {
	var lastErr error
	for attempt := 1; attempt <= accountCreateTxAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt-1) * 50 * time.Millisecond)
		}
		result, err := s.createInTransaction(ctx, input, access)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryableAccountCreateTxError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (s *Store) createInTransaction(ctx context.Context, input CreateInput, access AccessScope) (*CreateResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := s.now()
	nowISO := isoMillis(now)
	result, err := s.createInTx(ctx, tx, input, access, now, nowISO)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// isRetryableAccountCreateTxError 只识别 PostgreSQL 的瞬态事务中止码；其余
// 错误（校验、约束、连接失败）原样上报，不做重试掩盖。
func isRetryableAccountCreateTxError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40P01" || pgErr.Code == "40001"
}

func (s *Store) createInTx(ctx context.Context, tx *sql.Tx, input CreateInput, access AccessScope, now time.Time, nowISO string) (*CreateResult, error) {
	id := s.newI("acc")
	// providerCode 非空由两个入口前置校验保证（routes.go createBody 与
	// aipublic parseAccountAddBody 的 requiredTrimmedBody），此处不再重复。
	providerCode := strings.TrimSpace(input.ProviderCode)
	// vendor 归属校验（设计 §2.1/§9.1，profile 行删除后的替代门）：真实厂商
	// 必须在 vendors 表存在且 enabled；generic 语义 provider 无 vendors 行，
	// 跳过（vendor_code 落 SQL NULL）。
	if err := s.assertVendorEnabled(ctx, tx, providerCode); err != nil {
		return nil, err
	}
	// accountType 非空由 createBody 保证（routes.go）；aipublic 固定写入
	// "api_key"。此处不再重复空值校验。
	accountType := strings.TrimSpace(input.AccountType)

	// 能力域保存规则（计划 §服务端统一校验）：域由请求显式
	// 携带（camelCase capabilityDomain，缺省 chat，创建后不可更改 D3）。
	// 新建不允许任何域外声明：最终声明集 ⊆ allowed；allowed 为空 = 该供应
	// 商不支持该域。
	domain := strings.TrimSpace(input.CapabilityDomain)
	if domain == "" {
		domain = accountscore.CapabilityDomainChat
	}
	if !accountscore.AccountCapabilityDomainSet[domain] {
		return nil, &ValidationError{Message: "能力域无效"}
	}

	// 克隆综合（§2）：克隆载荷不携带连接/健康检查时由源账户连接行与 health
	// 三字段反解综合（连接按 preset/Base URL/endpoint modes/enabled 原样重建，
	// health 三字段按 preset 重新解析复制）；源无连接行按显式错误拒绝，不做
	// 跨形态兼容层。
	if input.CloneSourceID != nil && strings.TrimSpace(*input.CloneSourceID) != "" {
		cloneConnections, cloneHealthPreset, cloneHealthMode, cloneHealthModel, err := s.loadAccountConnectionFacts(ctx, tx, strings.TrimSpace(*input.CloneSourceID))
		if err != nil {
			return nil, err
		}
		input.Connections = cloneConnections
		if input.HealthCheckConnectionPresetCode == nil || strings.TrimSpace(*input.HealthCheckConnectionPresetCode) == "" {
			preset := cloneHealthPreset
			input.HealthCheckConnectionPresetCode = &preset
		}
		if input.HealthCheckEndpointMode == nil {
			mode := cloneHealthMode
			input.HealthCheckEndpointMode = &mode
		}
		if input.HealthCheckModel == nil {
			model := cloneHealthModel
			input.HealthCheckModel = &model
		}
		if strings.TrimSpace(*input.HealthCheckConnectionPresetCode) == "" {
			return nil, &ValidationError{Message: "克隆源账户健康检查连接缺失，无法克隆：" + strings.TrimSpace(*input.CloneSourceID)}
		}
	}
	// 连接集合（§8）：非空必填 + registry 组合/mode 词表/base URL validator
	// 全量校验（§2.1/§2.2）。账户类型合法性由同一 registry 组合门承载
	// （IsAllowedConnectorCombo 五元组含 account_type/oauth_type），不再有
	// profile 行的 accountTypes 成员校验。
	vendorCode, connectionDriverCode := accountVendorIdentity(providerCode)
	resolvedConnections, err := resolveConnectionSet(domain, connectionDriverCode, accountType,
		accountOAuthTypeForRegistry(accountType, "", input.Credentials), input.Connections)
	if err != nil {
		return nil, err
	}
	// 账户级协议上下文（profile 行删除后的语义替代，protocol_context.go）：
	// 由连接载荷的 registry preset/surface 确定性推导，喂给过渡期的
	// protocol_code/protocol_version 列与 legacy 词表机器。
	protocol := deriveConnectionProtocolContext(resolvedConnections)

	// 能力域允许集（legacy 词表机器，矩阵门 × 域 lane × 写侧词表；喂参换成
	// 推导上下文，行为不变）。
	profilePredicate := protocol.predicate(providerCode)
	allowedModes := accountscore.AllowedEndpointModes(domain, providerCode, accountType, toProtocolPredicate(profilePredicate))
	if len(allowedModes) == 0 {
		return nil, accountscore.CapabilityDomainUnsupportedError(providerCode, domain)
	}

	// Credentials: normalized through the ported
	// normalizeAccountCredentialsForWrite family, then sealed with the shared
	// AES-GCM envelope plus the fingerprint and mask columns.
	// E2E-FINDING #7 修复：写账户时按 Node deriveOpenAIAccountClientCompatibility
	// 派生并落列（openai/v1 协议下的 gpt api_key/oauth 账户为 codex_responses，
	// 其余 openai_standard）；此前 INSERT 硬编码 openai_standard，导致
	// codex_responses 请求类被调度侧 pinned 比较恒淘汰。
	derivedClientCompatibility := deriveOpenAIAccountClientCompatibility(providerCode, accountType, protocolProfileRef{
		ProviderCode:              providerCode,
		ProtocolCode:              protocol.protocolCode,
		ProtocolVersion:           protocol.protocolVersion,
		ProviderProtocolProfileID: protocol.profileID,
	})
	// modes 缺省（键不存在）时按域落默认集（按域默认能力集；chat 域与现行
	// 默认逐项一致——I3）。显式 null/非数组/空数组不注入，走既有归一化错误
	// 文案（行为与现状一致）。显式提供的 modes 原样进入归一化，越界值由
	// 归一化后的 ⊆ allowed 校验拒绝（新建不允许任何域外声明）。
	createCredentials := input.Credentials
	if _, present := createCredentials["supported_endpoint_modes"]; !present {
		defaultModes := accountscore.DefaultEndpointModesForDomain(domain, providerCode, accountType, toProtocolPredicate(profilePredicate))
		if len(defaultModes) == 0 {
			return nil, accountscore.CapabilityDomainUnsupportedError(providerCode, domain)
		}
		seeded := Credentials{}
		for key, value := range createCredentials {
			seeded[key] = value
		}
		seeded["supported_endpoint_modes"] = anyStrings(defaultModes)
		createCredentials = seeded
	}
	credentials, err := NormalizeAccountCredentialsForWrite(accountType, createCredentials, &EndpointModeDefaultContext{
		ProviderCode:              providerCode,
		AccountType:               accountType,
		ClientCompatibility:       derivedClientCompatibility,
		ProviderProtocolProfileID: protocol.profileID,
		ProtocolCode:              protocol.protocolCode,
		ProtocolVersion:           protocol.protocolVersion,
	})
	if err != nil {
		return nil, err
	}
	// 新建域内校验：归一化后的最终声明集必须 ⊆ allowed（列出越界值）。
	declaredModes := credentialEndpointModes(credentials)
	if outliers := accountscore.OutOfDomainModes(declaredModes, allowedModes); len(outliers) > 0 {
		return nil, accountscore.CapabilityDomainOutlierError(domain, outliers)
	}
	// Gemini 兼容校验联动（计划 §服务端统一校验）：chat 域 gemini（native
	// 档案，api_key/google_oauth）保持「至少一种对话能力」；媒体域 gemini
	// 走 native 档案域内允许集，不适用对话能力要求；chat 域其他供应商不新增
	// 任何既有校验之外的拒绝（I3）。
	if domain == accountscore.CapabilityDomainChat && isGeminiProtocolProfileOf(profilePredicate) {
		if err := assertGeminiEndpointModesCompatible(declaredModes, accountType); err != nil {
			// 断言族返回普通 error（既有批量/测试链原样透传）；创建链按
			// ValidationError 渲染 400，文案保持断言族原文。
			return nil, &ValidationError{Message: err.Error()}
		}
	}
	source, err := requiredAccountCredentialSource(accountType, credentials)
	if err != nil {
		return nil, err
	}
	sealed, err := EncryptJSON(s.secret, map[string]any(credentials))
	if err != nil {
		return nil, err
	}
	fingerprint := accountCredentialFingerprint(source)
	mask := MaskSecret(source)

	// OAuth refresh metadata (gpt/openai oauth only).
	accessTokenExpiresAt := sql.NullString{}
	refreshTokenPresent := 0
	if accountType == "oauth" && (providerCode == "gpt" || providerCode == "openai") {
		if accessToken, ok := credentials["access_token"].(string); ok && strings.TrimSpace(accessToken) != "" {
			if expiresAt, ok := credentials["expires_at"].(string); ok && strings.TrimSpace(expiresAt) != "" {
				if canonical, valid := canonicalRFC3339(expiresAt); valid {
					accessTokenExpiresAt = sql.NullString{String: canonical, Valid: true}
				} else {
					return nil, &ValidationError{Message: "时间必须是有效时间字符串"}
				}
			}
		}
		if refreshToken, ok := credentials["refresh_token"].(string); ok && strings.TrimSpace(refreshToken) != "" {
			refreshTokenPresent = 1
		}
	}

	// Account expiry.
	accountExpiresAt := sql.NullString{}
	if input.AccountExpiresAt != nil && strings.TrimSpace(*input.AccountExpiresAt) != "" {
		canonical, valid := canonicalRFC3339(*input.AccountExpiresAt)
		if !valid {
			return nil, &ValidationError{Message: "账户套餐到期时间必须是有效时间字符串"}
		}
		accountExpiresAt = sql.NullString{String: canonical, Valid: true}
	}

	// Availability schedule + status derivation.
	schedule, err := NormalizeSchedule(input.AvailabilitySchedule)
	if err != nil {
		return nil, err
	}
	// 初始状态按域分流（媒体账户生命周期定案表）：audio/video 域账户不参与
	// 对话健康检查状态机（无后台探针执行），创建默认落 active——显式 disabled
	// 仍 disabled，其余入参（含缺省 pending_test 哨兵值）一律 active；
	// health_check_endpoint_mode 仅为记录值（DefaultHealthCheckEndpointModeFor
	// Domain）。chat/image 域行为不变：仅 active+SkipInitialHealthCheck 才
	// active，否则非 disabled 一律 pending_test 等待后台健康检查。
	initialStatus := input.Status.Status
	if accountscore.SkipsConversationHealthStateMachine(domain) {
		if input.Status.Status != "disabled" && !(input.PreservePendingTest && input.Status.Status == "pending_test") {
			initialStatus = "active"
		}
	} else if input.Status.Status == "active" && input.Status.SkipInitialHealthCheck {
		initialStatus = "active"
	} else if input.Status.Status != "disabled" {
		initialStatus = "pending_test"
	}
	expiredByPackage := isAccountExpired(accountExpiresAt.String, now)
	nextStatus := initialStatus
	if !expiredByPackage {
		if override, ok := ScheduleStatus(schedule, now); ok && (initialStatus == "active" || initialStatus == "disabled") {
			nextStatus = override
		}
	} else {
		nextStatus = "disabled"
	}
	// 媒体域 schedulable 跟随派生状态：入参 status 缺省时 CreationStatus 的
	// Schedulable 哨兵为 false（对应 chat 的 pending_test 暂停调度），媒体域
	// 派生为 active 后必须可调度，否则产生「active 却禁调度」的滞留行；显式
	// disabled 仍由哨兵落 false。套餐过期臂不变。
	schedulable := input.Status.Schedulable
	if accountscore.SkipsConversationHealthStateMachine(domain) && input.Status.Status != "disabled" &&
		!(input.PreservePendingTest && input.Status.Status == "pending_test") {
		schedulable = true
	}
	if expiredByPackage {
		schedulable = false
	}
	lastErrorCode := sql.NullString{}
	lastErrorMessage := sql.NullString{}
	if expiredByPackage {
		lastErrorCode = sql.NullString{String: "account_expired", Valid: true}
		lastErrorMessage = sql.NullString{String: "账户套餐已过期，已自动停用", Valid: true}
	} else if initialStatus == "pending_test" {
		lastErrorMessage = sql.NullString{String: "账户已保存，等待后台健康检查", Valid: true}
	}

	// Supported models + health check model.
	supportedModels := input.SupportedModels
	if supportedModels == nil {
		supportedModels = []string{}
	}
	if err := assertSupportedModelsRequired(supportedModels); err != nil {
		return nil, err
	}
	// 目录断言（旧 provider 模型目录池）已切 models 表（model_catalog_
	// validation.go，#B1 剩余①目录段）：映射断言（协议矩阵门 + target 模型
	// models 身份解析）在分组切换之后骑最终 owner 执行；支持模型断言在分组
	// 切换后与映射同源执行。模式声明联动由连接行词表（§2.2）、映射 §5.3 与
	// 健康检查三字段写侧校验承载。

	// Owner context (the group block below switches the owner for admins
	// binding an explicit group).
	systemAccountID, err := access.OwnerID()
	if err != nil {
		return nil, err
	}

	// Group resolution: explicit group (owner switch for managed owners) or
	// the owner's enabled default group（组无 provider 维度，设计 §8：每个
	// system_account_id 至多一个 is_default=1 分组）.
	explicitGroupID := ""
	if input.GroupID != nil {
		explicitGroupID = strings.TrimSpace(*input.GroupID)
	}
	var explicitGroup *groupReference
	if explicitGroupID != "" {
		explicitGroup, err = s.groupOwnerReference(ctx, tx, explicitGroupID)
		if err != nil {
			return nil, err
		}
		if explicitGroup != nil && access.CanAccessAll() {
			systemAccountID = explicitGroup.systemAccountID
		}
	}
	group := explicitGroup
	if explicitGroupID == "" {
		group, err = s.defaultGroupForWrite(ctx, tx, systemAccountID)
		if err != nil {
			return nil, err
		}
	}
	if explicitGroupID != "" && group == nil {
		return nil, &ValidationError{Message: "账户分组无效"}
	}
	if group == nil || group.id == "" {
		return nil, &ValidationError{Message: "当前用户缺少启用的默认分组"}
	}
	if group.systemAccountID != systemAccountID {
		return nil, &ValidationError{Message: "账户分组无效"}
	}

	// Supported-model catalog assertion（设计 §4，models 表身份门）：每个支持
	// 模型必须解析到 owner 可见且 active 的 models 行；personal 目录按最终
	// owner scope 读取（分组切换之后的 systemAccountID），与映射断言同源。
	if err := s.assertAccountSupportedModelsInProviderCatalog(ctx, tx, supportedModels, providerCode, systemAccountID); err != nil {
		return nil, err
	}
	// Model mapping catalog assertion（设计 §5.1/§5.2，models 表身份门）：
	// 协议矩阵门 + target 模型 models 身份解析；上游端点能力取归一化后凭据的
	// supported_endpoint_modes。
	if err := s.assertAccountModelMappingsInProviderCatalog(ctx, tx, providerCode, systemAccountID, profilePredicate, input.ModelMappings, credentialEndpointModes(credentials)); err != nil {
		return nil, err
	}

	// Dispatch fields.
	concurrencyLimit := defaultAccountConcurrencyLimit
	if input.ConcurrencyLimit != nil {
		if *input.ConcurrencyLimit < 1 {
			return nil, &ValidationError{Message: "并发限制必须是大于 0 的整数"}
		}
		concurrencyLimit = *input.ConcurrencyLimit
	}
	priority := 0
	if input.Priority != nil {
		if *input.Priority < 0 {
			return nil, &ValidationError{Message: "优先级必须是大于等于 0 的整数"}
		}
		priority = *input.Priority
	}
	superPriorityEnabled := input.SuperPriorityEnabled != nil && *input.SuperPriorityEnabled
	fallbackEnabled := input.FallbackEnabled != nil && *input.FallbackEnabled
	if superPriorityEnabled && fallbackEnabled {
		return nil, &ValidationError{Message: "超级优先和降级备用不能同时开启"}
	}

	// Proxy profile (must exist and stay enabled).
	proxyProfileID := sql.NullString{}
	if input.ProxyProfileID != nil && strings.TrimSpace(*input.ProxyProfileID) != "" {
		enabled, err := s.resolveEnabledProxyProfile(ctx, tx, strings.TrimSpace(*input.ProxyProfileID))
		if err != nil {
			return nil, err
		}
		if !enabled {
			return nil, &ValidationError{Message: "代理不存在或已停用，请选择一个已启用的代理"}
		}
		proxyProfileID = sql.NullString{String: strings.TrimSpace(*input.ProxyProfileID), Valid: true}
	}

	// Notes.
	notes := sql.NullString{}
	if input.Notes != nil {
		trimmed := strings.TrimSpace(*input.Notes)
		if trimmed != "" {
			notes = sql.NullString{String: trimmed, Valid: true}
		}
	}

	// Balance query columns: the body parser already normalized the config
	// and validated the capability boundary (Node accounts.routes.ts
	// validateAccountBalanceCapability + normalizeAccountBalanceConfig); the
	// write keeps the normalized JSON contract-complete.
	balanceQueryEnabledInt := 0
	balanceQueryConfigJSON := "{}"
	if input.BalanceQueryEnabled {
		// BalanceQueryConfigCanonical 非空由 createBody 保证（routes.go 开启
		// 查询时强制已归一化 config）；aipublic 不开启余额查询。
		balanceQueryEnabledInt = 1
		balanceQueryConfigJSON = *input.BalanceQueryConfigCanonical
	}

	// Temporary-unavailable continuous probe switch: absent = enabled (1),
	// explicit false persists 0 (normalizeOptionalBooleanInput fallback true).
	temporaryProbeEnabled := 1
	if input.TemporaryUnavailableContinuousProbeEnabled != nil && !*input.TemporaryUnavailableContinuousProbeEnabled {
		temporaryProbeEnabled = 0
	}

	// 特供快速恢复（设计 §8.1/§9）：创建通道同样接受该标记，true 必须在
	// 同一事务内、同一把归属行锁下完成名额校验并计入本次待插入行
	// （当前计数 + 1 ≤ 上限，超限整体回滚）；nil/false 恒为未标记——克隆与
	// 导入创建体不含该键，副本/导入行天然不带标记（§3.10）。
	expeditedRecoveryEnabled := false
	if input.ExpeditedRecoveryEnabled != nil && *input.ExpeditedRecoveryEnabled {
		expeditedRecoveryEnabled = true
		if err := s.assertExpeditedRecoveryLimit(ctx, tx, systemAccountID, 1); err != nil {
			return nil, err
		}
	}

	// Tags.
	tagNames, err := normalizeAccountTagNamesInput(anySliceOrNil(input.Tags))
	if err != nil {
		return nil, err
	}

	scheduleJSONValue := sql.NullString{}
	if rawSchedule, ok := ScheduleJSON(schedule); ok {
		scheduleJSONValue = sql.NullString{String: rawSchedule, Valid: true}
	}
	nextCheckAt := sql.NullString{}
	if rawNextCheck, ok := NextScheduleCheckAt(schedule, now); ok {
		nextCheckAt = sql.NullString{String: rawNextCheck, Valid: true}
	}

	// Creation limit assertion (repositories.ts:2342,2470-2508): the last
	// check before the INSERT, inside the same transaction, so a concurrent
	// create cannot slip past the counted limit.
	if err := s.assertAiAccountCreationLimit(ctx, tx, systemAccountID); err != nil {
		return nil, err
	}

	// Balance / stream-failure seed columns (repositories.ts:2348-2349,2384-2388):
	// stream_failure_count starts at 0 with no window; an enabled balance
	// query seeds the next refresh at the creation instant (Node
	// accountBalanceWriteValues: nextRefreshAt = decision.enabled ? now),
	// disabled stays NULL — direct_input_reader treats a NULL refresh-at as
	// the recovery-arm candidate, so the NULL must not leak into enabled rows.
	balanceNextRefreshAt := sql.NullString{}
	if balanceQueryEnabledInt == 1 {
		balanceNextRefreshAt = sql.NullString{String: nowISO, Valid: true}
	}

	// Vendor identity（设计 §1.1/§2.1）：由 provider 语义推导，见
	// accountVendorIdentity 注释；创建链（直连创建、克隆、批量、导入均汇入
	// 本函数）统一落 vendor_code / connection_driver_code 两列。

	// 健康检查三件套显式提交（供应商与上游协议面重构设计 §8）：三字段必须全部
	// 由载荷显式携带，缺失按 400 拒绝（错误文案列明缺失字段），不再走档案默认
	// /域缺省推导（§2.2「新建账户不推导 endpoint mode 默认值」）。空串值保留
	// 给后续归属/词表校验报既有文案（nil 判缺失）。
	if input.HealthCheckModel == nil {
		return nil, &ValidationError{Message: "创建载荷缺少 healthCheckModel：健康检查三件套（healthCheckConnectionPresetCode、healthCheckEndpointMode、healthCheckModel）必须显式提交"}
	}
	if input.HealthCheckEndpointMode == nil {
		return nil, &ValidationError{Message: "创建载荷缺少 healthCheckEndpointMode：健康检查三件套（healthCheckConnectionPresetCode、healthCheckEndpointMode、healthCheckModel）必须显式提交"}
	}
	if input.HealthCheckConnectionPresetCode == nil {
		return nil, &ValidationError{Message: "创建载荷缺少 healthCheckConnectionPresetCode：健康检查三件套（healthCheckConnectionPresetCode、healthCheckEndpointMode、healthCheckModel）必须显式提交"}
	}
	if !accountHealthCheckEndpointModes[*input.HealthCheckEndpointMode] {
		return nil, &ValidationError{Message: "账户参数无效"}
	}
	healthCheckModel, err := normalizedHealthCheckModel(*input.HealthCheckModel, supportedModels)
	if err != nil {
		return nil, err
	}
	healthCheckEndpointMode := strings.TrimSpace(*input.HealthCheckEndpointMode)
	healthCheckConnectionPresetCode := strings.TrimSpace(*input.HealthCheckConnectionPresetCode)

	// oauth_type 物理列（设计 §2.1：仅 account_type=google_oauth 时非 NULL，
	// 三值 = 创建载荷凭据推导的既有判定值；其他账户恒 SQL NULL）。
	oauthType := accountOAuthTypeForRegistry(accountType, "", credentials)

	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("accounts")+`
		(id, system_account_id, protocol_code, protocol_version,
		 name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
		 oauth_type,
		 oauth_access_token_expires_at, oauth_refresh_token_present, proxy_profile_id, concurrency_limit,
		 priority, super_priority_enabled, fallback_enabled, schedulable,
		 availability_schedule_json, availability_schedule_next_check_at, notes, account_expires_at,
		 cooldown_until, last_error_code, last_error_message,
		 balance_query_enabled, balance_query_config_json, temporary_unavailable_continuous_probe_enabled,
		 expedited_recovery_enabled, capability_domain, vendor_code, connection_driver_code,
		 stream_failure_count, stream_failure_window_started_at, balance_query_next_refresh_at,
		 created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, systemAccountID, protocol.protocolCode, protocol.protocolVersion,
		strings.TrimSpace(input.Name), accountType, nextStatus, sealed, fingerprint, mask,
		nullStringForOAuthType(oauthType),
		accessTokenExpiresAt, refreshTokenPresent, proxyProfileID, concurrencyLimit,
		priority, boolInt(superPriorityEnabled), boolInt(fallbackEnabled), boolInt(schedulable),
		scheduleJSONValue, nextCheckAt, notes, accountExpiresAt,
		sql.NullString{}, lastErrorCode, lastErrorMessage,
		balanceQueryEnabledInt, balanceQueryConfigJSON, temporaryProbeEnabled,
		boolInt(expeditedRecoveryEnabled), domain, vendorCode, connectionDriverCode,
		0, sql.NullString{}, balanceNextRefreshAt,
		nowISO, nowISO); err != nil {
		if duplicate := duplicateAccountNameError(err, strings.TrimSpace(input.Name)); duplicate != nil {
			return nil, duplicate
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_accounts")+`
		(system_account_id, group_id, account_id, local_priority, local_super_priority_enabled,
		 local_fallback_enabled, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`),
		systemAccountID, group.id, id, priority, boolInt(superPriorityEnabled), boolInt(fallbackEnabled),
		nowISO, nowISO); err != nil {
		return nil, err
	}
	// 连接行写入（§2.1 写入序第二、三步）：account_connections（确定性行 ID）
	// + account_endpoint_modes——先于支持模型与入口映射，映射解析（§5.1 preset
	// → enabled 连接）在本事务内即可命中刚写入的连接行（#B1 断点修复）。
	if err := insertAccountConnections(ctx, tx, s, id, resolvedConnections, nowISO); err != nil {
		return nil, err
	}
	if err := s.replaceAccountSupportedModels(ctx, tx, id, providerCode, supportedModels, nowISO); err != nil {
		return nil, err
	}
	if err := s.replaceAccountModelMappings(ctx, tx, id, providerCode, input.ModelMappings, nowISO); err != nil {
		return nil, err
	}
	if err := s.replaceAccountNameSearchTerms(ctx, tx, id, systemAccountID, strings.TrimSpace(input.Name), nowISO); err != nil {
		return nil, err
	}
	if _, err := s.replaceAccountTags(ctx, tx, id, systemAccountID, tagNames, nowISO); err != nil {
		return nil, err
	}
	// health-check 三字段收尾（§2.1 写入序末步）：同一事务内解析 health_check_
	// connection_id（preset → enabled 连接行）并以一条 UPDATE 补齐三字段，提交
	// 前读回校验非 NULL（§9 事务级校验硬规则）。初始 INSERT 三列保持 NULL。
	if err := s.finalizeAccountHealthCheckTrio(ctx, tx, id, vendorCode.String, systemAccountID,
		healthCheckConnectionPresetCode, healthCheckEndpointMode, healthCheckModel, supportedModels); err != nil {
		return nil, err
	}
	// Dispatch revision family advance (repositories.ts:2412-2417): the fresh
	// row has no authorization source, so the family is the account itself;
	// the advance bumps dispatch_revision and lands the pending
	// dispatch_revision_changed circuit outbox row (the shared batch/delete
	// implementation, account-circuit-control-plane.repository.ts:428+).
	if err := s.advanceBatchDispatchRevisionFamily(ctx, tx, batchDispatchRevision{
		AccountID:    id,
		TransitionID: s.newI("dispatch"),
		NowMS:        now.UnixMilli(),
	}); err != nil {
		return nil, err
	}
	// Read the post-advance revision pair back (repositories.ts:2419-2423).
	// The row was just inserted in this transaction, so the scan cannot miss;
	// the missing-row branch keeps the archive J1 copy verbatim.
	var configRevision, dispatchRevision int64
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT config_revision, dispatch_revision FROM `+s.table("accounts")+`
		WHERE id = ?`), id).Scan(&configRevision, &dispatchRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("J1 input outbox 找不到新建账户")
		}
		return nil, err
	}
	// J1 snapshot outbox (repositories.ts:2418-2432): health-capable
	// accounts reserve input version 1 and land the pending snapshot intent
	// (reason 'account_created') in the same transaction, which is what puts
	// the new account into the probe candidate join. Media domains have no
	// conversation probe path. API Key accounts reserve for every vendor:
	// J1 direct reader covers all vendors (deepseek/generic 等) — the legacy
	// gpt/openai gate left non-GPT API Key accounts without an input-version
	// row, so their creation probe was silently dropped by the jobs outbox
	// drain (CurrentProbeInput !ok) and the balance detection intent never
	// appeared (T56 第二层根因，2026-10-24)。OAuth 仍维持 gpt/openai 原范围。
	if !accountscore.SkipsConversationHealthStateMachine(domain) &&
		(accountType == "api_key" || ((providerCode == "gpt" || providerCode == "openai") && accountType == "oauth")) {
		if err := s.reserveAndEnqueueAccountHealthSnapshot(ctx, tx, id, configRevision, dispatchRevision, nowISO); err != nil {
			return nil, err
		}
	}
	// dispatchInitialAccountHealthCheck condition (Node
	// account-health-check-dispatch.service.ts): a pending_test account always
	// probes once; a freshly saved active single-Key API Key account without
	// balance query probes once so the balance detector can classify it.
	initialHealthCheck := !accountscore.SkipsConversationHealthStateMachine(domain) && (nextStatus == "pending_test" ||
		(nextStatus == "active" && accountType == "api_key" &&
			EffectiveAccountApiKeyCount(credentials) == 1 &&
			balanceQueryEnabledInt != 1 &&
			balanceQueryConfigJSON == "{}"))
	return &CreateResult{
		ID: id, Status: nextStatus, ConfigRevision: configRevision, DispatchRevision: dispatchRevision,
		OwnerSystemAccountID: systemAccountID, Name: strings.TrimSpace(input.Name),
		GroupID:                    group.id,
		InitialHealthCheckRequired: initialHealthCheck,
	}, nil
}

// CreateResult mirrors the create response payload plus the fields the
// operation log needs. configRevision/dispatchRevision carry the real
// post-advance revision pair read back in the create transaction (the
// dispatch family advance bumps the schema default 1 to 2).
type CreateResult struct {
	ID                   string `json:"id"`
	Status               string `json:"status"`
	ConfigRevision       int64  `json:"configRevision"`
	DispatchRevision     int64  `json:"dispatchRevision"`
	OwnerSystemAccountID string `json:"-"`
	Name                 string `json:"-"`
	// GroupID is the bound group (the post-commit group stats dirty marker
	// and the group-account-ids flush target).
	GroupID string `json:"-"`
	// InitialHealthCheckRequired mirrors dispatchInitialAccountHealthCheck's
	// activation condition; the Create caller dispatches the probe after the
	// commit (reason='activation').
	InitialHealthCheckRequired bool `json:"-"`
}

// isAccountExpired 阶段 B 下沉 accountscore，根包保留同名转发.
func isAccountExpired(accountExpiresAt string, now time.Time) bool {
	return accountscore.IsAccountExpired(accountExpiresAt, now)
}

// AiAccountCreationLimitSettings is the narrow settings port behind the
// creation limit (Node effectiveAiAccountCreationLimit's settings fallback:
// settingsRepository.getSettings().userAiAccountLimit, repositories.ts:2493).
// The composition adapter wraps the settings store's effective snapshot; nil
// keeps the schema default below (RuntimeCooldownSettings pattern).
type AiAccountCreationLimitSettings interface {
	// UserAiAccountLimit returns the effective userAiAccountLimit setting
	// (already normalized by the settings store's per-key validation).
	UserAiAccountLimit(ctx context.Context) (int64, error)
}

// SetAiAccountLimitSettings wires the settings port (compose handover; nil
// keeps the Node schema default).
func (s *Store) SetAiAccountLimitSettings(settings AiAccountCreationLimitSettings) {
	s.aiAccountLimitSettings = settings
}

// defaultUserAiAccountLimit mirrors the userAiAccountLimit schema default
// (settings defaults: Node defaults.ts / Go settings store default 100).
const defaultUserAiAccountLimit = 100

// assertAiAccountCreationLimit mirrors
// assertAiAccountCreationLimitInClientTransaction (repositories.ts:2470-2491):
// the owner's ai_account_limit override (NULL falls back to the
// userAiAccountLimit setting) gates the count of live owner-owned base
// accounts (deleted_at IS NULL, authorization instances excluded); 0 means
// unlimited. The arm locks the owner row (LIMIT 1 FOR UPDATE).
func (s *Store) assertAiAccountCreationLimit(ctx context.Context, q queryer, systemAccountID string) error {
	var ownerLimit sql.NullInt64
	// A missing owner row falls through to the setting (Node owner = undefined
	// → owner?.ai_account_limit = undefined).
	err := q.QueryRowContext(ctx, s.bind(`SELECT ai_account_limit FROM `+s.table("system_accounts")+`
		WHERE id = ?
		LIMIT 1`+s.forUpdate()), systemAccountID).Scan(&ownerLimit)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	limit, err := s.effectiveAiAccountCreationLimit(ctx, ownerLimit)
	if err != nil {
		return err
	}
	if limit == 0 {
		return nil
	}
	var count int64
	if err := q.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("accounts")+`
		WHERE system_account_id = ?
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL`), systemAccountID).Scan(&count); err != nil {
		return err
	}
	if count >= limit {
		return &ValidationError{Message: "AI 账户数量已达到限制（" + itoa64(limit) + "）"}
	}
	return nil
}

// effectiveAiAccountCreationLimit mirrors effectiveAiAccountCreationLimit
// (repositories.ts:2493-2499): the owner override wins, the setting is the
// fallback, and only the integer range [0, 1000000] is valid.
func (s *Store) effectiveAiAccountCreationLimit(ctx context.Context, ownerOverride sql.NullInt64) (int64, error) {
	if ownerOverride.Valid {
		if ownerOverride.Int64 < 0 || ownerOverride.Int64 > 1_000_000 {
			return 0, &ValidationError{Message: "AI 账户数量限制配置无效"}
		}
		return ownerOverride.Int64, nil
	}
	if s.aiAccountLimitSettings != nil {
		value, err := s.aiAccountLimitSettings.UserAiAccountLimit(ctx)
		if err != nil {
			return 0, err
		}
		if value < 0 || value > 1_000_000 {
			return 0, &ValidationError{Message: "AI 账户数量限制配置无效"}
		}
		return value, nil
	}
	return defaultUserAiAccountLimit, nil
}

// reserveAndEnqueueAccountHealthSnapshot mirrors
// reserveAndEnqueueAccountHealthJobsInputInTransaction(Async) for the create
// path (account-health-jobs-input-outbox.repository.ts:41-72,90-125): bump
// account_health_jobs_input_versions (or seed it at 1), then insert the
// pending snapshot intent keyed by the reserved version. The statement shape
// and column set match the sibling tombstone writer in delete.go and the
// authz grant fanout (downstream.go); now is the caller's transaction
// timestamp, and the reservation must stay inside the create transaction so
// the epoch can never commit without its durable publish intent.
func (s *Store) reserveAndEnqueueAccountHealthSnapshot(ctx context.Context, tx *sql.Tx, accountID string, configRevision, dispatchRevision int64, now string) error {
	normalized := strings.TrimSpace(accountID)
	if normalized == "" {
		return errors.New("J1 snapshot version 缺少 account ID")
	}
	var currentVersion sql.NullInt64
	err := tx.QueryRowContext(ctx, s.bind(`SELECT current_version FROM `+s.table("account_health_jobs_input_versions")+`
		WHERE account_id = ?`), normalized).Scan(&currentVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	nextVersion := int64(1)
	if err == nil && currentVersion.Valid {
		nextVersion = currentVersion.Int64 + 1
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_health_jobs_input_versions")+`
			SET current_version = ?, reserved_at = ? WHERE account_id = ?`), nextVersion, now, normalized); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_health_jobs_input_versions")+`
			(account_id, current_version, reserved_at) VALUES (?, ?, ?)`), normalized, nextVersion, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_health_jobs_input_outbox")+`
		(event_id, account_id, input_version, event_kind, reason,
		 config_revision, dispatch_revision, status, available_at,
		 created_at, updated_at)
		VALUES (?, ?, ?, 'snapshot', 'account_created', ?, ?, 'pending', ?, ?, ?)`),
		s.newI("acchev"), normalized, nextVersion, configRevision, dispatchRevision, now, now, now)
	return err
}

// groupOwnerReference 解析分组行的 owner 归属（组无 provider 维度，设计 §8）；
// 缺属主或行不存在返回 (nil, nil)（调用方按「账户分组无效」渲染）。
func (s *Store) groupOwnerReference(ctx context.Context, q queryer, groupID string) (*groupReference, error) {
	var row groupReference
	var groupKind string
	err := q.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, name, group_kind
		FROM `+s.table("groups")+` WHERE id = ?`), groupID).
		Scan(&row.id, &row.systemAccountID, &row.name, &groupKind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.systemAccountID == "" {
		return nil, nil
	}
	if groupKind == "super" {
		return nil, &ValidationError{Message: "超级分组不能直接绑定账户", Code: "group_kind_conflict"}
	}
	return &row, nil
}

// defaultGroupForWrite 解析 owner 的启用默认分组（设计 §8：每个
// system_account_id 至多一个 is_default=1 分组，无 provider 维度）。
func (s *Store) defaultGroupForWrite(ctx context.Context, q queryer, systemAccountID string) (*groupReference, error) {
	var row groupReference
	err := q.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, name
		FROM `+s.table("groups")+`
		WHERE system_account_id = ?
			AND is_default = 1
			AND enabled = 1
		ORDER BY updated_at DESC, id ASC
		LIMIT 1`), systemAccountID).
		Scan(&row.id, &row.systemAccountID, &row.name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) resolveEnabledProxyProfile(ctx context.Context, q queryer, proxyProfileID string) (bool, error) {
	// proxy_profiles.enabled 在 PostgreSQL 为 boolean，扫描目标用 bool 对齐
	// 驱动类型（同 list.go 的 NullBool 先例）。
	var id string
	var enabled bool
	err := q.QueryRowContext(ctx, s.bind(`SELECT id, enabled FROM `+s.table("proxy_profiles")+`
		WHERE id = ?`), proxyProfileID).Scan(&id, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled, nil
}

// replaceAccountSupportedModels 落账户支持模型（供应商与上游协议面重构设计
// §4 目标形态，B4 W2）：模型名按 models.upstream_model 身份键 + owner 可见性
// 解析 models.id 后写 (account_id, model_id)——该复合键是
// account_entry_mappings.target_model_id 外键的引用目标（§9），解析失败按
// ValidationError（400 族）显式拒绝。owner 取账户行 system_account_id（调用
// 方签名不携带；授权实例场景由上游面保证直连资源账户写入）。
//
// BUG-0335 写序修复：account_entry_mappings 的复合外键
// (account_id, target_model_id) → account_supported_models(account_id, model_id)
// （设计 §5.1，无级联动作）把入口映射 target 锁进同一账户的支持模型集合。
// 先删支持模型集合会让仍指向被替换模型行的入口映射悬空，DELETE 被 23503
// 拦截（生产编辑账户保存 500）。设计 §8 的既定原则是依赖映射在同事务内一并
// 解决（不是拒绝），因此本函数先把 next 模型名全部解析成 models.id（解析只读
// models 表，先解析后删不影响事务原子性，错误语义不变），随后按 next 目标
// 集合做对称的选择性删除：入口映射只删 target_model_id 不在 next 集合内的行，
// 支持模型只删 model_id 不在 next 集合内的行（复合主键 (account_id, model_id)
// 下对幸存行幂等，重插由下方 insertIgnore 跳过）——幸存行全程不被删除，指向
// 它的映射保留有效，外键在事务内每一步都满足。不能全删支持模型集合：即使
// 先清了悬空映射，全删仍会连带删除仍被保留映射引用的幸存行，即刻违反外键。
// 只改支持模型、未带 modelMappings 的 PATCH（输入缺席或与现值相等）不触发
// replaceAccountModelMappings，幸存映射因此不丢；next 集合为空时两个删除均
// 退化为删除该账户全部行。
func (s *Store) replaceAccountSupportedModels(ctx context.Context, q queryer, accountID, providerCode string, models []string, nowISO string) error {
	vendorNull, _ := accountVendorIdentity(providerCode)
	vendor := vendorNull.String
	var owner string
	if err := q.QueryRowContext(ctx, s.bind(`SELECT system_account_id FROM `+s.table("accounts")+` WHERE id = ?`), accountID).Scan(&owner); err != nil {
		return err
	}
	nextIDs := make([]string, 0, len(models))
	for _, model := range models {
		modelID, err := s.resolveEntryModelID(ctx, q, vendor, owner, model)
		if err != nil {
			return err
		}
		nextIDs = append(nextIDs, modelID)
	}
	if len(nextIDs) > 0 {
		args := append([]any{accountID}, accountscore.AnySlice(nextIDs)...)
		if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_entry_mappings")+`
			WHERE account_id = ? AND target_model_id NOT IN (`+accountscore.Placeholders(len(nextIDs))+`)`), args...); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_supported_models")+`
			WHERE account_id = ? AND model_id NOT IN (`+accountscore.Placeholders(len(nextIDs))+`)`), args...); err != nil {
			return err
		}
	} else {
		if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_entry_mappings")+` WHERE account_id = ?`), accountID); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_supported_models")+` WHERE account_id = ?`), accountID); err != nil {
			return err
		}
	}
	for _, modelID := range nextIDs {
		if _, err := q.ExecContext(ctx, s.bind(s.insertIgnore(`INSERT INTO `+s.table("account_supported_models")+`
			(account_id, model_id, created_at) VALUES (?, ?, ?)`,
			` ON CONFLICT (account_id, model_id) DO NOTHING`)), accountID, modelID, nowISO); err != nil {
			return err
		}
	}
	return nil
}

// replaceAccountModelMappings 落账户入口映射（供应商与上游协议面重构设计
// §5.1/§8 目标形态，B4 W2）：整集合替换写 account_entry_mappings，每条映射经
// resolveEntryMappingTargets 双解析（connection preset → enabled
// account_connections.id；target 模型名 → models.id）+ §5.3 family 联动校验；
// 任一步失败按 ValidationError（mapping_target_unsupported 语义，400 族）拒绝，
// 不静默跳过。旧 account_model_mappings 写循环已删除（该表随 A 路线撤除，
// fresh DDL 不再建表）；providerCode 仅用于推导模型身份键的 vendor（§2.1）。
func (s *Store) replaceAccountModelMappings(ctx context.Context, q queryer, accountID, providerCode string, mappings []ModelMapping, nowISO string) error {
	vendorNull, _ := accountVendorIdentity(providerCode)
	vendor := vendorNull.String
	var owner string
	if err := q.QueryRowContext(ctx, s.bind(`SELECT system_account_id FROM `+s.table("accounts")+` WHERE id = ?`), accountID).Scan(&owner); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_entry_mappings")+` WHERE account_id = ?`), accountID); err != nil {
		return err
	}
	for _, mapping := range mappings {
		connectionID, modelID, err := s.resolveEntryMappingTargets(ctx, q, accountID, vendor, owner, mapping)
		if err != nil {
			return err
		}
		enabled := 1
		if mapping.Enabled != nil && !*mapping.Enabled {
			enabled = 0
		}
		if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_entry_mappings")+`
			(id, account_id, source_model, source_endpoint_family, target_connection_id,
			 target_model_id, target_endpoint_family, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (account_id, source_model, source_endpoint_family) DO NOTHING`),
			entryMappingRowID(accountID, mapping.SourceModel, mapping.SourceEndpointFamily),
			accountID, mapping.SourceModel, mapping.SourceEndpointFamily, connectionID,
			modelID, mapping.UpstreamEndpointFamily, enabled, nowISO, nowISO); err != nil {
			return err
		}
	}
	return nil
}

// replaceAccountTags mirrors replaceAccountTags: existing tags are reused by
// (system_account_id, name), missing ones are created with a fresh acctag id.
func (s *Store) replaceAccountTags(ctx context.Context, q queryer, accountID, systemAccountID string, tagNames []string, nowISO string) ([]TagSummary, error) {
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_tag_bindings")+` WHERE account_id = ?`), accountID); err != nil {
		return nil, err
	}
	tags := []TagSummary{}
	for _, name := range tagNames {
		if _, err := q.ExecContext(ctx, s.bind(s.insertIgnore(`INSERT INTO `+s.table("account_tags")+`
			(id, system_account_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			` ON CONFLICT (system_account_id, name) DO NOTHING`)),
			s.newI("acctag"), systemAccountID, name, nowISO, nowISO); err != nil {
			return nil, err
		}
		var tag TagSummary
		if err := q.QueryRowContext(ctx, s.bind(`SELECT id, name FROM `+s.table("account_tags")+`
			WHERE system_account_id = ? AND name = ? LIMIT 1`), systemAccountID, name).
			Scan(&tag.ID, &tag.Name); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	for _, tag := range tags {
		if _, err := q.ExecContext(ctx, s.bind(s.insertIgnore(`INSERT INTO `+s.table("account_tag_bindings")+`
			(account_id, tag_id, system_account_id, created_at) VALUES (?, ?, ?, ?)`,
			` ON CONFLICT (account_id, tag_id) DO NOTHING`)),
			accountID, tag.ID, systemAccountID, nowISO); err != nil {
			return nil, err
		}
	}
	return tags, nil
}

// duplicateAccountNameError mirrors isDuplicateAccountNameError.
// duplicateAccountNameError 阶段 C 下沉 accountscore（导入执行器与 write/patch
// 链共用同一冲突分类），根包保留同名转发.
func duplicateAccountNameError(err error, name string) error {
	return accountscore.DuplicateAccountNameError(err, name)
}

// boolInt 阶段 B 下沉 accountscore，根包保留同名转发.
func boolInt(value bool) int {
	return accountscore.BoolInt(value)
}

// containsString mirrors the contains helper shared by the validation paths
// （REFACTOR-0005 阶段 0 下沉 accountscore，根包保留同名转发）.
func containsString(values []string, target string) bool {
	return accountscore.ContainsString(values, target)
}

func anySliceOrNil(values []string) any {
	if len(values) == 0 {
		return nil
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// stringSliceToAny adapts an ID slice for IN (...) placeholder lists; unlike
// anySliceOrNil it keeps an empty slice as an empty (never nil) driver slice,
// which is what the delete/tombstone clauses rely on.
func stringSliceToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
