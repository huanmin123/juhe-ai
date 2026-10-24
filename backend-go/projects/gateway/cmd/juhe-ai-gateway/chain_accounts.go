package main

// G20 phase-3 composition-root account read seam: the full
// gatewayruntimecache.AccountsSelector implementation the runtime cache needs
// to hydrate dispatchable OpenAI accounts for a group.
//
// Node authority (backend/src/storage):
//   - openai-account-selector.repository.ts (listOpenAIAccountsForGroupResult,
//     resolveGroupUsageAccessMetadata, resolveOpenAIAccountAccess,
//     canScheduleAuthorizedAccount, openAIAccountSecretFromRow,
//     runtimeOpenAIAccountCredentials),
//   - gateway-dispatch-candidate-window.repository.ts
//     (listGatewayDispatchCandidateRows + listGatewayDispatchModelCandidateRows
//     + orderGatewayDispatchCandidateRowsForDispatch + the fresh quality rows),
//   - account-api-key-rotation.ts (accountApiKeyEntries +
//     isAccountApiKeyPoolIsolationEnabled + HMAC fingerprints),
//   - account-api-key-runtime-state.repository.ts
//     (loadAccountApiKeyRuntimeStatesByAccountIds),
//   - proxy.repository.ts (resolveProxyUrlsForProfiles + proxyUrlFromRow),
//   - resource-authorization-helpers.ts / request-quota-limits.ts
//     (resourceAuthorizationQuotaLimited),
//   - openai-endpoint-modes.ts + provider-protocol.ts (runtime endpoint modes).
//
// Every field of the Node OpenAIAccountSecret projection is hydrated; nothing
// degrades silently (a missing stats quality table or an undecryptable proxy
// credential surfaces as the Node error / `unavailable` contract).

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// dispatchCandidateScanLimitFor mirrors
// gatewayDispatchAccountCandidateScanLimit = limit * 2
// (openai-account-selector.types.ts:208). The final limit itself is
// configuration: Node dispatchAccountCandidateLimit =
// integerConfig('JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT',
// concurrency.globalMax, 1, 50_000) (runtime.ts:770) — the candidate windows
// (LIMIT ?), the hydration batching and the diagnostics all consume the
// runtime value instead of a fixed constant.
func dispatchCandidateScanLimitFor(finalLimit int) int { return finalLimit * 2 }

// chainAccountsSelector implements gatewayruntimecache.AccountsSelector over
// the business + stats databases.
type chainAccountsSelector struct {
	db       *sql.DB
	statsDB  *sql.DB
	postgres bool
	secret   string
	now      func() time.Time
	// candidateFinalLimit mirrors gatewayDispatchAccountCandidateLimit
	// (runtimeConfig.gateway.dispatchAccountCandidateLimit, default
	// concurrency.globalMax); candidateScanLimit = finalLimit * 2.
	candidateFinalLimit int
	candidateScanLimit  int
}

// newChainAccountsSelector keeps the phase-2 constructor signature; quality
// reads fall back to the business handle when no separate stats handle is
// supplied (the dual-handle constructor is newChainAccountsSelectorWithStats).
// dispatchAccountCandidateLimit comes from the runtime config (Node default
// concurrency.globalMax, bounded 1..50000); out-of-range values fail the
// constructor like the Node integerConfig range error.
func newChainAccountsSelector(db *sql.DB, postgres bool, secret string, now func() time.Time, dispatchAccountCandidateLimit int) (*chainAccountsSelector, error) {
	return newChainAccountsSelectorWithStats(db, db, postgres, secret, now, dispatchAccountCandidateLimit)
}

// newChainAccountsSelectorWithStats wires the stats database the fresh
// account_quality_scores reads run against (Node getStatsDatabase()).
func newChainAccountsSelectorWithStats(db *sql.DB, statsDB *sql.DB, postgres bool, secret string, now func() time.Time, dispatchAccountCandidateLimit int) (*chainAccountsSelector, error) {
	if db == nil {
		return nil, fmt.Errorf("网关链账户选择器需要业务数据库")
	}
	if dispatchAccountCandidateLimit < 1 || dispatchAccountCandidateLimit > 50000 {
		return nil, fmt.Errorf("JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT 必须在 1-50000 范围内: %d", dispatchAccountCandidateLimit)
	}
	if statsDB == nil {
		statsDB = db
	}
	if now == nil {
		now = time.Now
	}
	return &chainAccountsSelector{
		db:                  db,
		statsDB:             statsDB,
		postgres:            postgres,
		secret:              secret,
		now:                 now,
		candidateFinalLimit: dispatchAccountCandidateLimit,
		candidateScanLimit:  dispatchCandidateScanLimitFor(dispatchAccountCandidateLimit),
	}, nil
}

func (s *chainAccountsSelector) table(name string) string {
	if s.postgres {
		return "juhe_business." + name
	}
	return name
}

func (s *chainAccountsSelector) statsTable(name string) string {
	if s.postgres {
		return "juhe_stats." + name
	}
	return name
}

func (s *chainAccountsSelector) bind(query string) string {
	if !s.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprint(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// candidate row (OpenAIGroupAccountSelectionRow)
// ---------------------------------------------------------------------------

// chainCandidateRow is the group_accounts + accounts join row the selector
// hydrates (the full Node OpenAIGroupAccountSelectionRow shape).
type chainCandidateRow struct {
	AccountID              string
	BindingSystemAccountID sql.NullString
	GroupID                sql.NullString
	AccountAuthorizationID sql.NullString
	LocalPriority          sql.NullInt64
	LocalSuperPriority     sql.NullInt64
	LocalFallback          sql.NullInt64
	BindingCreatedAt       sql.NullString // model-candidate window only

	ID              string
	SystemAccountID string
	// VendorCode / ConnectionDriverCode 是供应商解耦后的连接身份列（供应商与
	// 上游协议面重构设计 §1.1/§2.1）：候选窗口按 accounts.vendor_code /
	// accounts.connection_driver_code 投影，授权实例经 source_accounts 的
	// resource_* 别名读取（消费方 resourceVendorCode/
	// resourceConnectionDriverCode）。vendor 值域与旧 providerCode 同名；两列
	// 扫描均用 NullString（vendor_code 目标形态即可空；connection_driver_code
	// 目标形态 NOT NULL，但存量库迁移窗口允许 NULL 行，读取归一为空串）。
	VendorCode                   sql.NullString
	ConnectionDriverCode         sql.NullString
	ResourceVendorCode           sql.NullString
	ResourceConnectionDriverCode sql.NullString
	// ProviderProtocolProfileID / ResourceProviderProtocolProfileID /
	// ClientCompatibility / ResourceClientCompatibility 过渡零值字段：候选窗口
	// 投影已随目标 schema 一次性收敛拆除 accounts.provider_protocol_profile_id /
	// accounts.client_compatibility（设计 §9.11，fresh DDL 无列），SQL 不再赋值，
	// 恒为零值；字段待消费方（chain_gatewaykeymodels.go
	// resourceProviderProtocolProfileID、chain_accounts_secret.go 端点模式归一）
	// 切片与字段移除同批退役。
	ProviderProtocolProfileID                sql.NullString
	ResourceProviderProtocolProfileID        sql.NullString
	ProtocolCode                             sql.NullString
	ProtocolVersion                          sql.NullString
	Name                                     string
	Type                                     string
	Status                                   string
	Schedulable                              int
	ConcurrencyLimit                         int
	Priority                                 int
	SuperPriorityEnabled                     int
	FallbackEnabled                          int
	ClientCompatibility                      sql.NullString
	ConfigRevision                           sql.NullInt64
	DispatchRevision                         sql.NullInt64
	CredentialsEncrypted                     sql.NullString
	ProxyProfileID                           sql.NullString
	CooldownUntil                            sql.NullString
	LastErrorMessage                         sql.NullString
	StreamFailureCount                       sql.NullInt64
	StreamFailureWindowStartedAt             sql.NullString
	AccountExpiresAt                         sql.NullString
	HealthCheckModel                         sql.NullString
	HealthCheckEndpointMode                  sql.NullString
	AuthorizationInstanceSourceAccountID     sql.NullString
	AuthorizationInstanceAuthorizationID     sql.NullString
	AuthorizationInstanceOwnerSystemAccountI sql.NullString

	// ConnectionBaseURL / ConnectionPresetCode 是单连接事实源投影（B4 第一批，
	// 设计 §2.1「Base URL 属于连接行」）：候选窗口按资源账户（授权实例经
	// source_accounts，直连账户落自身）解析唯一连接事实——
	// health_check_connection_id 指向的 enabled 连接优先，否则 enabled 连接按
	// id 升序首个（与 jobs accounthealth resolved_health_connections 同口径）。
	// 同账户多连接多候选展开留后续批：本批同账户仍单候选（secret 装配按该
	// 单连接事实取 BaseURL 与端点模式集，缺失行 fail closed）。
	ResourceAccountID            sql.NullString
	ConnectionBaseURL            sql.NullString
	ConnectionPresetCode         sql.NullString
	ResourceProtocolCode         sql.NullString
	ResourceProtocolVersion      sql.NullString
	ResourceType                 sql.NullString
	ResourceStatus               sql.NullString
	ResourceSchedulable          sql.NullInt64
	ResourceAccountExpiresAt     sql.NullString
	ResourceCooldownUntil        sql.NullString
	ResourceLastErrorCode        sql.NullString
	ResourceCredentialsEncrypted sql.NullString
	ResourceProxyProfileID       sql.NullString
	ResourceConcurrencyLimit     sql.NullInt64
	ResourceClientCompatibility  sql.NullString

	QualityScore        *float64
	QualityState        *string
	QualityFirstTokenMs *float64

	modelResourceID sql.NullString // model-candidate window only
	ModelRank       int            // -1 when the row did not come from a ranked window
}

// chainCandidateColumns 是候选窗口共享投影。供应商解耦目标 schema 收敛：
// accounts.provider_protocol_profile_id / accounts.client_compatibility（含
// source_accounts 同名两列）已随设计 §9.11 拆除，不再投影。
// ConnectionBaseURL / ConnectionPresetCode 是单连接事实源标量子查询（B4 第一
// 批，设计 §2.1「Base URL 属于连接行」）：按资源账户（授权实例经
// source_accounts，直连账户 COALESCE 落自身）取其 enabled 连接——
// health_check_connection_id 指向的连接优先，否则按 id 升序首个（与 jobs
// accounthealth resolved_health_connections 同口径）；同账户多连接多候选展开
// 留后续批，本批同账户仍单候选。端点模式集合经 hydration 批装载
// （loadConnectionEndpointModesByAccountIds，同一选择口径）带出，不在本投影
// 内做方言相关的聚合拼接。
const chainCandidateColumns = `group_accounts.account_id,
			group_accounts.system_account_id AS binding_system_account_id,
			group_accounts.group_id, group_accounts.account_authorization_id,
			group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
			accounts.id, accounts.system_account_id, accounts.vendor_code, accounts.connection_driver_code,
			accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.type, accounts.status,
			accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled,
			accounts.fallback_enabled,
			accounts.config_revision, accounts.dispatch_revision, accounts.credentials_encrypted,
			accounts.proxy_profile_id, accounts.cooldown_until, accounts.last_error_message,
			accounts.stream_failure_count, accounts.stream_failure_window_started_at,
			accounts.account_expires_at, accounts.health_check_model, accounts.health_check_endpoint_mode,
			accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id,
			accounts.authorization_instance_owner_system_account_id,
			source_accounts.id AS resource_account_id,
			(SELECT {connectionsTable}.base_url FROM {connectionsTable}
				WHERE {connectionsTable}.enabled = 1
					AND {connectionsTable}.account_id = COALESCE(source_accounts.id, accounts.id)
				ORDER BY ({connectionsTable}.id = COALESCE(source_accounts.health_check_connection_id, accounts.health_check_connection_id)) DESC, {connectionsTable}.id ASC
				LIMIT 1) AS connection_base_url,
			(SELECT {connectionsTable}.connection_preset_code FROM {connectionsTable}
				WHERE {connectionsTable}.enabled = 1
					AND {connectionsTable}.account_id = COALESCE(source_accounts.id, accounts.id)
				ORDER BY ({connectionsTable}.id = COALESCE(source_accounts.health_check_connection_id, accounts.health_check_connection_id)) DESC, {connectionsTable}.id ASC
				LIMIT 1) AS connection_preset_code,
			source_accounts.vendor_code AS resource_vendor_code,
			source_accounts.connection_driver_code AS resource_connection_driver_code,
			source_accounts.protocol_code AS resource_protocol_code,
			source_accounts.protocol_version AS resource_protocol_version,
			source_accounts.type AS resource_type,
			source_accounts.status AS resource_status,
			source_accounts.schedulable AS resource_schedulable,
			source_accounts.account_expires_at AS resource_account_expires_at,
			source_accounts.cooldown_until AS resource_cooldown_until,
			source_accounts.last_error_code AS resource_last_error_code,
			source_accounts.credentials_encrypted AS resource_credentials_encrypted,
			source_accounts.proxy_profile_id AS resource_proxy_profile_id,
			source_accounts.concurrency_limit AS resource_concurrency_limit`

// chainCandidateModelTail extends the model-candidate window projection.
const chainCandidateModelTail = `,
			group_accounts.created_at AS binding_created_at,
			COALESCE(source_accounts.id, accounts.id) AS model_resource_account_id`

// chainCandidateWhere mirrors the shared candidate window predicates (the
// availability belt both windows repeat verbatim). {mediaStatusArm} 与
// {mediaCooldownArm} 是冷却到期媒体准入臂占位（chainMediaArmReplacement：
// 非媒体请求恒展开空串，SQL 与实参序列和既有形态逐字节一致）。
// 组内候选不再按 provider 收窄（供应商与上游协议面重构设计 §8；组内模型过滤
// 由后续 FilterAccountsByRequestedModel 承担）。
const chainCandidateWhere = `WHERE group_accounts.group_id = ?
			AND group_accounts.system_account_id = ?
			AND group_accounts.enabled = 1
			AND accounts.deleted_at IS NULL
			AND (accounts.status IN ({statusSet}){mediaStatusArm})
			AND accounts.schedulable = 1
			AND ((? = 1 OR accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?){mediaCooldownArm})
			AND (
				(accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth', 'google_oauth'))
				OR (
					accounts.authorization_instance_authorization_id IS NOT NULL
					AND source_accounts.deleted_at IS NULL
					AND source_accounts.type IN ('api_key', 'oauth', 'google_oauth')
					AND source_accounts.status IN ({statusSet})
					AND source_accounts.schedulable = 1
					AND (? = 1 OR source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= ?)
					AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > ?)
					AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
				)
			)
			AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)`

// chainMediaAdmissionStatusArm 冷却到期视频异步任务准入臂的 status 扩展：
// 仅 video_create 账户在四个可调度 status 上可被视频创建请求选中。
const chainMediaAdmissionStatusArm = ` OR (accounts.health_check_endpoint_mode = 'video_create'
				AND accounts.status IN ('active', 'pending_test', 'rate_limited', 'temporary_unavailable'))`

// chainMediaAdmissionCooldownArm 准入臂的冷却门：媒体账户要求冷却已过期或为
// 空（NULL 视为过期）。占位展开自带 `<= ?` 一个占位符，实参由调用方按
// mediaAdmission 追加一个 now。
const chainMediaAdmissionCooldownArm = ` OR (accounts.health_check_endpoint_mode = 'video_create'
				AND accounts.status IN ('active', 'pending_test', 'rate_limited', 'temporary_unavailable')
				AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?))`

// chainMediaAdmissionArmed 仅为视频异步任务创建请求武装恢复准入；TTS 与
// 长音频转写均不经 video 任务轮询与 CAS，不开放此恢复态准入。
func chainMediaAdmissionArmed(requestedEndpointFamily string) bool {
	return strings.TrimSpace(requestedEndpointFamily) == gatewayopenai.FamilyVideoGeneration
}

// chainMediaArmReplacement 展开准入臂占位：armed=false 时两个占位都是空串
// （默认形态），armed=true 时携带对应 OR 臂。
func chainMediaArmReplacement(armed bool) []string {
	if !armed {
		return []string{"{mediaStatusArm}", "", "{mediaCooldownArm}", ""}
	}
	return []string{"{mediaStatusArm}", chainMediaAdmissionStatusArm, "{mediaCooldownArm}", chainMediaAdmissionCooldownArm}
}

// chainCandidateFrom unlocks the index-free variant of the Node candidate
// window (the INDEXED BY hint is SQLite-specific tuning and omitted here).
const chainCandidateFrom = `FROM %s group_accounts
			INNER JOIN %s accounts ON accounts.id = group_accounts.account_id
			LEFT JOIN %s source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id`

// chainEligibleRow mirrors EligibleOpenAIGroupAccountSelection.
type chainEligibleRow struct {
	row    *chainCandidateRow
	access *chainAccountAccess
}

// ListOpenAIAccountsForGroupResult mirrors listOpenAIAccountsForGroupResult.
// ChatPinnedAccountID 非空时分流到 chat-pinned 专用直取路径
// （listChatPinnedOpenAIAccountResult，AI 问答设计 §5.2 2026-10-10 修订）；
// 零值空串走常规候选窗口，/v1 语义不变。
func (s *chainAccountsSelector) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts gatewayruntimecache.OpenAIAccountsForGroupOptions) (gatewayruntimecache.OpenAIAccountsForGroupResult, error) {
	if pinnedID := strings.TrimSpace(opts.ChatPinnedAccountID); pinnedID != "" {
		return s.listChatPinnedOpenAIAccountResult(ctx, groupID, systemAccountID, pinnedID, opts.PreResolvedGroupAccess)
	}
	now := chainNowISO(s.now)
	qualityFreshAfter := chainQualityFreshAfterISO(s.now)
	groupAccess := opts.PreResolvedGroupAccess
	if groupAccess == nil {
		meta, err := s.resolveGroupAccess(ctx, groupID, systemAccountID)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		if meta == nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{}}, nil
		}
		groupAccess = meta
	}

	// Model candidate window (requested model only) + base window merged in
	// Node order (mergeOpenAIGroupAccountRowsForDispatch).
	requestedModel := strings.TrimSpace(opts.RequestedModel)
	// 冷却到期恢复准入只在 video_generation 的选择查询上生效；其余请求
	// （含同步 TTS、chat/images 与空族）的候选行为不变。
	mediaAdmission := chainMediaAdmissionArmed(opts.RequestedEndpointFamily)
	var modelRows []chainCandidateRow
	var modelRanks map[string]int
	if requestedModel != "" {
		rows, ranks, err := s.listModelCandidateRows(ctx, groupID, groupAccess, now, opts.RequestedModel, opts.RequestedEndpointFamily, opts.IncludeUnavailable, mediaAdmission)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		modelRows, modelRanks = rows, ranks
	}
	baseRows, err := s.listCandidateRows(ctx, groupID, groupAccess, now, opts.IncludeUnavailable, mediaAdmission)
	if err != nil {
		return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
	}
	groupAccountRows := baseRows
	if modelRows != nil {
		groupAccountRows = mergeChainCandidateRows(modelRows, baseRows)
	}

	availabilityGate := func(row *chainCandidateRow) (bool, error) {
		return chainAccountAvailableForSelection(row, now, opts.IncludeUnavailable, mediaAdmission)
	}
	return s.assembleOpenAIAccountsForGroupResult(ctx, groupAccess, groupAccountRows, modelRanks, systemAccountID, qualityFreshAfter, availabilityGate)
}

// listChatPinnedOpenAIAccountResult 是 chat-pinned 直取路径（ChatPinnedAccountID
// 非空，AI 问答设计 §5.2 2026-10-10 修订）：分组 access 解析（分组禁用/不存在
// → 空结果，保持既有「禁用分组空候选」语义）→ 专用直取窗口（含禁用/冷却/过期/
// 禁调度账户）→ 跳过可用性闸的共享装配主体。仅收敛到目标账户单元素。
func (s *chainAccountsSelector) listChatPinnedOpenAIAccountResult(ctx context.Context, groupID, systemAccountID, accountID string, preResolved *gatewayruntimecache.GroupUsageAccessMetadata) (gatewayruntimecache.OpenAIAccountsForGroupResult, error) {
	groupAccess := preResolved
	if groupAccess == nil {
		meta, err := s.resolveGroupAccess(ctx, groupID, systemAccountID)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		if meta == nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{}}, nil
		}
		groupAccess = meta
	}
	qualityFreshAfter := chainQualityFreshAfterISO(s.now)
	rows, err := s.listChatPinnedCandidateRows(ctx, groupID, groupAccess.GroupOwnerSystemAccountID, accountID)
	if err != nil {
		return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
	}
	return s.assembleOpenAIAccountsForGroupResult(ctx, groupAccess, rows, nil, systemAccountID, qualityFreshAfter, nil)
}

// assembleOpenAIAccountsForGroupResult 是常规候选窗口与 chat-pinned 直取共享
// 的装配主体：authorizations 预取 → 资格闸（access 解析 + 授权调度 + 授权绑定
// 闸；availabilityGate 为 nil 时跳过可用性闸——pinned 直取行来自专用窗口，含
// 非可用状态账户）→ 质量行 → 派发排序 → hydration 批次。
func (s *chainAccountsSelector) assembleOpenAIAccountsForGroupResult(ctx context.Context, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, groupAccountRows []chainCandidateRow, modelRanks map[string]int, systemAccountID, qualityFreshAfter string, availabilityGate func(row *chainCandidateRow) (bool, error)) (gatewayruntimecache.OpenAIAccountsForGroupResult, error) {
	// Account authorizations for owner-scope accounts
	// (loadAccountAuthorizationsForSelection: nil map for authorized groups —
	// the per-row reads then hit the database like the Node fallback).
	authorizations, err := s.loadAccountAuthorizationsForSelection(ctx, groupAccountRows, groupAccess, systemAccountID)
	if err != nil {
		return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
	}

	// Eligibility: schedulable access + availability gates + binding match.
	eligible := make([]chainEligibleRow, 0, len(groupAccountRows))
	for index := range groupAccountRows {
		row := &groupAccountRows[index]
		access, err := s.resolveChainAccountAccess(ctx, row, systemAccountID, groupAccess, row.AccountAuthorizationID.String, authorizations)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		if access == nil {
			continue
		}
		if !canScheduleChainAuthorizedAccount(row, authorizations, access) {
			continue
		}
		if access.accountAccessType == chainAccountAccessAuthorized && !row.ResourceAccountID.Valid {
			continue
		}
		if availabilityGate != nil {
			available, err := availabilityGate(row)
			if err != nil {
				return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
			}
			if !available {
				continue
			}
		}
		if access.accountAccessType == chainAccountAccessAuthorized {
			// isOpenAIAccountAvailableForSelection binding gate.
			if !row.GroupID.Valid || row.GroupID.String == "" ||
				!row.AccountAuthorizationID.Valid || row.AccountAuthorizationID.String == "" ||
				row.AccountAuthorizationID.String != *access.accountAuthorizationID {
				continue
			}
		}
		eligible = append(eligible, chainEligibleRow{row: row, access: access})
	}

	// Fresh quality rows (stats account_quality_scores, 24h window).
	accountIDs := make([]string, 0, len(eligible))
	for _, item := range eligible {
		accountIDs = append(accountIDs, item.row.AccountID)
	}
	qualityByAccountID, err := s.loadFreshQualityRows(ctx, accountIDs, qualityFreshAfter)
	if err != nil {
		return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
	}
	for index := range eligible {
		row := eligible[index].row
		if quality, ok := qualityByAccountID[row.ID]; ok {
			row.QualityScore = quality.score
			row.QualityState = quality.state
			row.QualityFirstTokenMs = quality.firstTokenMs
		}
	}

	ordered := orderChainCandidateRowsForDispatch(eligible, modelRanks)

	// Hydration batches of gatewayDispatchAccountCandidateLimit.
	accountsOut := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(ordered))
	hydrationBatchCount := 0
	hydrationDroppedCount := 0
	for offset := 0; offset < len(ordered) && len(accountsOut) < s.candidateFinalLimit; offset += s.candidateFinalLimit {
		end := offset + s.candidateFinalLimit
		if end > len(ordered) {
			end = len(ordered)
		}
		batch := ordered[offset:end]
		hydrationBatchCount++
		resourceIDs := make([]string, 0, len(batch))
		for _, item := range batch {
			resourceIDs = append(resourceIDs, item.row.resourceAccountID())
		}
		supportedModels, err := s.loadSupportedModelsByAccountIds(ctx, resourceIDs)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		modelMappings, err := s.loadModelMappingsByAccountIds(ctx, resourceIDs)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		apiKeyRuntimeStates, err := s.loadAPIKeyRuntimeStatesByAccountIds(ctx, resourceIDs)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		connectionModes, err := s.loadConnectionEndpointModesByAccountIds(ctx, resourceIDs)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		proxyProfileIDs := make([]string, 0, len(batch))
		for _, item := range batch {
			if id := item.row.resourceProxyProfileID(); id != "" {
				proxyProfileIDs = append(proxyProfileIDs, id)
			}
		}
		proxyProfiles, err := s.resolveProxyURLsForProfiles(ctx, proxyProfileIDs)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		for _, item := range batch {
			account, err := s.openAIAccountSecretFromRow(ctx, item.row, groupAccess, item.access, chainSecretOptions{
				proxyProfiles:      proxyProfiles,
				supportedModels:    supportedModels,
				modelMappings:      modelMappings,
				apiKeyRuntimeState: apiKeyRuntimeStates,
				connectionModes:    connectionModes,
			})
			if err != nil {
				return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
			}
			if account != nil {
				accountsOut = append(accountsOut, *account)
				if len(accountsOut) >= s.candidateFinalLimit {
					break
				}
			} else {
				hydrationDroppedCount++
			}
		}
	}

	return gatewayruntimecache.OpenAIAccountsForGroupResult{
		Accounts: accountsOut,
		Diagnostics: &gatewayruntimecache.OpenAIAccountsForGroupDiagnostics{
			ScanLimit:             s.candidateScanLimit,
			FinalLimit:            s.candidateFinalLimit,
			CandidateRowCount:     len(groupAccountRows),
			ScannedRowCount:       len(groupAccountRows),
			EligibleRowCount:      len(eligible),
			HydrationBatchCount:   hydrationBatchCount,
			HydratedAccountCount:  len(accountsOut),
			HydrationDroppedCount: hydrationDroppedCount,
			FinalAccountCount:     len(accountsOut),
			ScanLimitReached:      len(groupAccountRows) >= s.candidateScanLimit,
		},
	}, nil
}

// expandConnectionsTable 展开 chainCandidateColumns 内单连接事实源子查询的
// {connectionsTable} 占位（方言限定的 account_connections 表名）。
// NewReplacer 单趟扫描不会重入替换文本，必须在模板替换后二段执行。
func (s *chainAccountsSelector) expandConnectionsTable(query string) string {
	return strings.ReplaceAll(query, "{connectionsTable}", s.table("account_connections"))
}

// listChatPinnedCandidateRows 是 chat-pinned 直取专用窗口（AI 问答设计 §5.2
// 2026-10-10 修订）：按账户 ID 直取其在启用分组中的候选行。与共享候选窗
// chainCandidateWhere 的差异只有可用性条件——不限定 status/schedulable/
// cooldown/account_expires_at（含禁用、错误、限流、冷却、过期与禁调度账户，
// 绕过可用性窗口）；保留存在性与凭据结构条件（deleted_at IS NULL、类型集合、
// 分组启用、授权实例戳行排除——chat 绑定口径本就排除实例戳行，LEFT JOIN 仅为
// 维持 chainCandidateColumns 列形状）。provider 匹配谓词已随供应商解耦拆除
// （供应商与上游协议面重构设计 §8；组内候选不再按 provider 收窄）。
// chainCandidateWhere 常量本身不被修改，/v1 常规窗口语义不变。
func (s *chainAccountsSelector) listChatPinnedCandidateRows(ctx context.Context, groupID, groupOwnerSystemAccountID, accountID string) ([]chainCandidateRow, error) {
	query := s.expandConnectionsTable(strings.NewReplacer(
		"{columns}", chainCandidateColumns,
		"{from}", fmt.Sprintf(chainCandidateFrom, s.table("group_accounts"), s.table("accounts"), s.table("accounts")),
	).Replace(`SELECT {columns},
			NULL AS quality_score, NULL AS quality_state, NULL AS quality_ewma_first_token_ms
			{from}
			WHERE group_accounts.group_id = ?
				AND group_accounts.system_account_id = ?
				AND group_accounts.enabled = 1
				AND accounts.deleted_at IS NULL
				AND accounts.id = ?
				AND accounts.authorization_instance_authorization_id IS NULL
				AND accounts.type IN ('api_key', 'oauth', 'google_oauth')
			ORDER BY group_accounts.created_at ASC, group_accounts.account_id ASC
			LIMIT 1`))
	rows, err := s.db.QueryContext(ctx, s.bind(query), groupID, groupOwnerSystemAccountID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chainCandidateRow{}
	for rows.Next() {
		var row chainCandidateRow
		dest := candidateScanDest(&row)
		dest = append(dest, &nullStringSink{}, &nullStringSink{}, &nullStringSink{})
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row.ModelRank = -1
		out = append(out, row)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// SQL windows
// ---------------------------------------------------------------------------

// candidateScanDest builds the scan destination for the base window column
// order (chainCandidateColumns)。vendor/connection_driver 投影位与 SELECT 列序
// 逐位对应（accounts 两列在 system_account_id 后、resource 两别名在
// resource_account_id 后）；provider_code / provider_protocol_profile_id /
// client_compatibility 投影已拆除（B4 W2 收口后 chainCandidateRow 不再保留
// ProviderCode/ResourceProviderCode 过渡字段；ProviderProtocolProfileID 等
// 其余过渡字段恒零值不赋值）；
// 列序尾部为单连接事实源两投影（connection_base_url / connection_preset_code）。
func candidateScanDest(row *chainCandidateRow) []any {
	return []any{
		&row.AccountID, &row.BindingSystemAccountID, &row.GroupID, &row.AccountAuthorizationID,
		&row.LocalPriority, &row.LocalSuperPriority, &row.LocalFallback,
		&row.ID, &row.SystemAccountID, &row.VendorCode, &row.ConnectionDriverCode,
		&row.ProtocolCode, &row.ProtocolVersion, &row.Name, &row.Type, &row.Status,
		&row.Schedulable, &row.ConcurrencyLimit, &row.Priority, &row.SuperPriorityEnabled,
		&row.FallbackEnabled,
		&row.ConfigRevision, &row.DispatchRevision, &row.CredentialsEncrypted,
		&row.ProxyProfileID, &row.CooldownUntil, &row.LastErrorMessage,
		&row.StreamFailureCount, &row.StreamFailureWindowStartedAt,
		&row.AccountExpiresAt, &row.HealthCheckModel, &row.HealthCheckEndpointMode,
		&row.AuthorizationInstanceSourceAccountID, &row.AuthorizationInstanceAuthorizationID,
		&row.AuthorizationInstanceOwnerSystemAccountI,
		&row.ResourceAccountID,
		&row.ConnectionBaseURL,
		&row.ConnectionPresetCode,
		&row.ResourceVendorCode,
		&row.ResourceConnectionDriverCode,
		&row.ResourceProtocolCode,
		&row.ResourceProtocolVersion,
		&row.ResourceType,
		&row.ResourceStatus,
		&row.ResourceSchedulable,
		&row.ResourceAccountExpiresAt,
		&row.ResourceCooldownUntil,
		&row.ResourceLastErrorCode,
		&row.ResourceCredentialsEncrypted,
		&row.ResourceProxyProfileID,
		&row.ResourceConcurrencyLimit,
	}
}

// listCandidateRows ports listGatewayDispatchCandidateRows. mediaAdmission
// 仅由 video_generation 请求武装视频恢复准入臂。
func (s *chainAccountsSelector) listCandidateRows(ctx context.Context, groupID string, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, now string, includeUnavailable bool, mediaAdmission bool) ([]chainCandidateRow, error) {
	statusSet := "'active'"
	if includeUnavailable {
		statusSet = "'active', 'rate_limited', 'temporary_unavailable'"
	}
	includeFlag := 0
	if includeUnavailable {
		includeFlag = 1
	}
	replacements := append([]string{"{statusSet}", statusSet}, chainMediaArmReplacement(mediaAdmission)...)
	where := strings.NewReplacer(replacements...).Replace(chainCandidateWhere)
	query := s.expandConnectionsTable(strings.NewReplacer(
		"{columns}", chainCandidateColumns,
		"{from}", fmt.Sprintf(chainCandidateFrom, s.table("group_accounts"), s.table("accounts"), s.table("accounts")),
		"{where}", where,
	).Replace(`SELECT {columns},
			NULL AS quality_score, NULL AS quality_state, NULL AS quality_ewma_first_token_ms
			{from} {where}
			ORDER BY
				(CASE WHEN COALESCE(group_accounts.local_fallback_enabled, 0) = 1 OR COALESCE(accounts.fallback_enabled, 0) = 1 THEN 1 ELSE 0 END) ASC,
				(CASE WHEN COALESCE(group_accounts.local_super_priority_enabled, 0) = 1 OR COALESCE(accounts.super_priority_enabled, 0) = 1 THEN 1 ELSE 0 END) DESC,
				group_accounts.local_priority ASC,
				group_accounts.created_at ASC,
				group_accounts.account_id ASC
			LIMIT ?`))
	args := []any{groupID, groupAccess.GroupOwnerSystemAccountID, includeFlag, now}
	if mediaAdmission {
		// 准入臂占位自带一个 `<= ?`：追加到默认冷却对之后（SQL 文本顺序一致）。
		args = append(args, now)
	}
	args = append(args, includeFlag, now, now, now, s.candidateScanLimit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chainCandidateRow{}
	for rows.Next() {
		var row chainCandidateRow
		dest := candidateScanDest(&row)
		dest = append(dest, &nullStringSink{}, &nullStringSink{}, &nullStringSink{})
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row.ModelRank = -1
		out = append(out, row)
	}
	return out, rows.Err()
}

// listModelCandidateRows ports listGatewayDispatchModelCandidateRows: the
// CTE model-rank window (0 = direct supported model, 1 = enabled mapping,
// 2 = no supported-model list, everything else dropped). mediaAdmission 仅由
// video_generation 请求武装视频恢复准入臂。方案B（2026-10-24 用户裁决）：映射
// 臂拆两档——同族改名映射（target_endpoint_family = source_endpoint_family，
// 保留非恒等门）与直连同权记 0，跨协议转换映射（两族不同）记 1；口径与
// gatewayrouting/gatewaydispatch 两层 Go 过滤器一致
// （docs/functions/AI账户模型映射选择规则.md §2.4）。
func (s *chainAccountsSelector) listModelCandidateRows(ctx context.Context, groupID string, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, now, requestedModel, requestedEndpointFamily string, includeUnavailable bool, mediaAdmission bool) ([]chainCandidateRow, map[string]int, error) {
	model := strings.TrimSpace(requestedModel)
	if model == "" {
		return []chainCandidateRow{}, map[string]int{}, nil
	}
	statusSet := "'active'"
	if includeUnavailable {
		statusSet = "'active', 'rate_limited', 'temporary_unavailable'"
	}
	includeFlag := 0
	if includeUnavailable {
		includeFlag = 1
	}
	replacements := append([]string{"{statusSet}", statusSet}, chainMediaArmReplacement(mediaAdmission)...)
	where := strings.NewReplacer(replacements...).Replace(chainCandidateWhere)
	tables := fmt.Sprintf(chainCandidateFrom, s.table("group_accounts"), s.table("accounts"), s.table("accounts"))
	supportedModelsTable := s.table("account_supported_models")
	// B4 第一批（供应商与上游协议面重构设计 §5.1/§4）：映射档读
	// account_entry_mappings，target 模型经 target_model_id JOIN models 取
	// upstream_model（V1 批 GKM 装载器同款形态）；direct 档与映射有效性门的
	// 模型名同样经 model_id JOIN models 解析——direct 请求名按 public_model
	//（§4「direct 候选要求 request.model == models.public_model」，与 GKM
	// union 装载器 public/upstream 双集合分工一致），映射门在 upstream 名空间
	// 比较（与旧行为 `supported.model = mappings.upstream_model` 同构）。
	mappingsTable := s.table("account_entry_mappings")
	modelsTable := s.table("models")
	// direct 档模型谓词：请求模型命中 volcengine TTS 占位名↔现役名等价组时
	// 展开为组内全部成员（IN (?, ...)），账户声明任一名字都命中 direct 档
	//（契约 §9.2「派发匹配等价」的候选窗口层——选组/派发两层 Go 匹配与
	// adapter 归一共用该等价事实，本查询是 preflight 选组的候选窗口，若只
	// 精确匹配会在两层匹配之前就把账户滤空）；等价组外维持 `= ?` 精确匹配。
	directModelArgs := gatewayrouting.VolcengineTTSModelAliasVariantsOf(model)
	directModelPredicate := "direct_models.public_model = ?"
	mappedAliasPredicate := "mapped_alias_models.upstream_model = mapped_models.upstream_model"
	var mappedAliasArgs []any
	if len(directModelArgs) > 1 {
		directModelPredicate = fmt.Sprintf("direct_models.public_model IN (%s)", strings.TrimSuffix(strings.Repeat("?, ", len(directModelArgs)), ", "))
		// 映射档 mapped_supported 检查与 Go 映射门同构展开：映射产出模型与
		// 账户声明模型只要都落等价组即视为一致（否则「映射 upstream=现役名、
		// 声明=占位名」的账户在本窗口即被剔成 rank 3，Go 层已钉住的映射放行
		// 行为到不了生产）。谓词仅在请求模型命中组时替换，组外 SQL 与参数
		// 数量维持原形态。
		variantPlaceholders := strings.TrimSuffix(strings.Repeat("?, ", len(directModelArgs)), ", ")
		mappedAliasPredicate = "mapped_alias_models.upstream_model = mapped_models.upstream_model OR (mapped_models.upstream_model IN (" +
			variantPlaceholders + ") AND mapped_alias_models.upstream_model IN (" + variantPlaceholders + "))"
		for _, variant := range directModelArgs {
			mappedAliasArgs = append(mappedAliasArgs, variant)
		}
		for _, variant := range directModelArgs {
			mappedAliasArgs = append(mappedAliasArgs, variant)
		}
	}
	query := s.expandConnectionsTable(strings.NewReplacer(
		"{columns}", chainCandidateColumns,
		"{tail}", chainCandidateModelTail,
		"{tables}", tables,
		"{where}", where,
		"{supportedModels}", supportedModelsTable,
		"{mappings}", mappingsTable,
		"{models}", modelsTable,
		"{directModelPredicate}", directModelPredicate,
		"{mappedAliasPredicate}", mappedAliasPredicate,
	).Replace(`WITH eligible_rows AS (
		SELECT {columns}{tail}
		{tables} {where}
		),
		ranked_rows AS (
			SELECT
				CASE
					WHEN EXISTS (
						SELECT 1 FROM {supportedModels} direct_supported
						INNER JOIN {models} direct_models ON direct_models.id = direct_supported.model_id
						WHERE direct_supported.account_id = eligible_rows.model_resource_account_id
							AND {directModelPredicate}
					) THEN 0
					WHEN EXISTS (
						SELECT 1 FROM {mappings} model_mappings
						INNER JOIN {models} mapped_models ON mapped_models.id = model_mappings.target_model_id
						WHERE model_mappings.account_id = eligible_rows.model_resource_account_id
							AND model_mappings.source_model = ?
							AND model_mappings.source_endpoint_family = ?
							AND model_mappings.enabled = 1
							AND model_mappings.target_endpoint_family = model_mappings.source_endpoint_family
							AND (
								mapped_models.upstream_model <> model_mappings.source_model
								OR model_mappings.target_endpoint_family <> model_mappings.source_endpoint_family
							)
							AND (
								NOT EXISTS (
									SELECT 1 FROM {supportedModels} limited_supported
									WHERE limited_supported.account_id = eligible_rows.model_resource_account_id
								)
								OR EXISTS (
									SELECT 1 FROM {supportedModels} mapped_supported
									INNER JOIN {models} mapped_alias_models ON mapped_alias_models.id = mapped_supported.model_id
									WHERE mapped_supported.account_id = eligible_rows.model_resource_account_id
										AND ({mappedAliasPredicate})
								)
							)
					) THEN 0
					WHEN EXISTS (
						SELECT 1 FROM {mappings} model_mappings
						INNER JOIN {models} mapped_models ON mapped_models.id = model_mappings.target_model_id
						WHERE model_mappings.account_id = eligible_rows.model_resource_account_id
							AND model_mappings.source_model = ?
							AND model_mappings.source_endpoint_family = ?
							AND model_mappings.enabled = 1
							AND model_mappings.target_endpoint_family <> model_mappings.source_endpoint_family
							AND (
								mapped_models.upstream_model <> model_mappings.source_model
								OR model_mappings.target_endpoint_family <> model_mappings.source_endpoint_family
							)
							AND (
								NOT EXISTS (
									SELECT 1 FROM {supportedModels} limited_supported
									WHERE limited_supported.account_id = eligible_rows.model_resource_account_id
								)
								OR EXISTS (
									SELECT 1 FROM {supportedModels} mapped_supported
									INNER JOIN {models} mapped_alias_models ON mapped_alias_models.id = mapped_supported.model_id
									WHERE mapped_supported.account_id = eligible_rows.model_resource_account_id
										AND ({mappedAliasPredicate})
								)
							)
					) THEN 1
					WHEN NOT EXISTS (
						SELECT 1 FROM {supportedModels} limited_models
						WHERE limited_models.account_id = eligible_rows.model_resource_account_id
					) THEN 2
					ELSE 3
				END AS model_rank,
				eligible_rows.*
			FROM eligible_rows
		)
		SELECT * FROM ranked_rows
		WHERE model_rank < 3
		ORDER BY
			model_rank ASC,
			(CASE WHEN COALESCE(local_fallback_enabled, 0) = 1 OR COALESCE(fallback_enabled, 0) = 1 THEN 1 ELSE 0 END) ASC,
			(CASE WHEN COALESCE(local_super_priority_enabled, 0) = 1 OR COALESCE(super_priority_enabled, 0) = 1 THEN 1 ELSE 0 END) DESC,
			COALESCE(local_priority, priority, 0) ASC,
			binding_created_at ASC,
			account_id ASC
		LIMIT ?`))
	args := []any{groupID, groupAccess.GroupOwnerSystemAccountID, includeFlag, now}
	if mediaAdmission {
		// 准入臂占位自带一个 `<= ?`：追加到默认冷却对之后（SQL 文本顺序一致）。
		args = append(args, now)
	}
	args = append(args, includeFlag, now, now, now)
	for _, variant := range directModelArgs {
		args = append(args, variant)
	}
	// 方案B：同族改名臂与跨协议臂各带一份 (source_model, source_endpoint_family)
	// 与 mappedAlias 等价组参数，顺序与 SQL 文本中两臂占位符出现顺序一致。
	for arm := 0; arm < 2; arm++ {
		args = append(args, model, requestedEndpointFamily)
		for _, variant := range mappedAliasArgs {
			args = append(args, variant)
		}
	}
	args = append(args, s.candidateScanLimit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := []chainCandidateRow{}
	for rows.Next() {
		var row chainCandidateRow
		var modelRank int
		dest := []any{&modelRank}
		dest = append(dest, candidateScanDest(&row)...)
		dest = append(dest, &row.BindingCreatedAt, &row.modelResourceID)
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, err
		}
		row.ModelRank = modelRank
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	modelRankByAccountID := map[string]int{}
	unique := make([]chainCandidateRow, 0, len(out))
	seen := map[string]bool{}
	for _, row := range out {
		accountID := row.AccountID
		if accountID == "" {
			accountID = row.ID
		}
		if seen[accountID] {
			continue
		}
		seen[accountID] = true
		modelRankByAccountID[row.ID] = row.ModelRank
		modelRankByAccountID[accountID] = row.ModelRank
		unique = append(unique, row)
	}
	return unique, modelRankByAccountID, nil
}

// mergeChainCandidateRows mirrors mergeOpenAIGroupAccountRowsForDispatch:
// model-window rows keep their order, base-window duplicates are dropped.
func mergeChainCandidateRows(preferred, fallback []chainCandidateRow) []chainCandidateRow {
	seen := map[string]bool{}
	rows := make([]chainCandidateRow, 0, len(preferred)+len(fallback))
	for _, row := range preferred {
		accountID := row.AccountID
		if accountID == "" {
			accountID = row.ID
		}
		if seen[accountID] {
			continue
		}
		seen[accountID] = true
		rows = append(rows, row)
	}
	for _, row := range fallback {
		accountID := row.AccountID
		if accountID == "" {
			accountID = row.ID
		}
		if seen[accountID] {
			continue
		}
		seen[accountID] = true
		rows = append(rows, row)
	}
	return rows
}

// ---------------------------------------------------------------------------
// dispatch ordering (orderGatewayDispatchCandidateRowsForDispatch)
// ---------------------------------------------------------------------------

// orderChainCandidateRowsForDispatch orders the eligible rows: model rank,
// fallback, super, priority, then the quality tie-break inside buckets with
// >=2 members and the zh-CN name/id tail.
func orderChainCandidateRowsForDispatch(rows []chainEligibleRow, modelRanks map[string]int) []chainEligibleRow {
	out := append([]chainEligibleRow{}, rows...)
	collator := chainNameCollator()
	sort.SliceStable(out, func(left, right int) bool {
		l, r := out[left].row, out[right].row
		if lr, rr := chainModelRank(l, modelRanks), chainModelRank(r, modelRanks); lr != rr {
			return lr < rr
		}
		if lf, rf := chainFallbackRank(l), chainFallbackRank(r); lf != rf {
			return lf < rf
		}
		if ls, rs := chainSuperRank(l), chainSuperRank(r); ls != rs {
			return ls > rs
		}
		if lp, rp := chainPriorityRank(l), chainPriorityRank(r); lp != rp {
			return lp < rp
		}
		// 到达此处时 model/fallback/super/priority 全平 ⇒ bucket key 相同且
		// 桶内行数 >=2 ⇒ compareChainQuality 必然执行；它对相异行以
		// collator（名字）或 ID 收尾恒非零，返回 0 仅剩“名称与 ID 双重复”
		// 的行，此时 ID 比较也恒 false（稳定序保持原位）。
		if chainBucketKey(l) == chainBucketKey(r) && chainBucketSize(out, chainBucketKey(l)) >= 2 {
			if delta := compareChainQuality(l, r, collator); delta != 0 {
				return delta < 0
			}
		}
		return l.ID < r.ID
	})
	return out
}

func chainModelRank(row *chainCandidateRow, modelRanks map[string]int) int {
	if modelRanks == nil {
		return 0
	}
	if rank, ok := modelRanks[row.ID]; ok {
		return rank
	}
	if rank, ok := modelRanks[row.AccountID]; ok {
		return rank
	}
	return 3
}

// chainFallbackRank / chainSuperRank 与 dispatchPriority 同型回退：自有账户的
// 调度标记写在 accounts.*（核心功能设计「更多」菜单节），local_* 仅授权实例
// 分组绑定的门控；任一为真即生效（2026-10-24 T62 第二层根因修复）。
func chainFallbackRank(row *chainCandidateRow) int {
	return boolToInt((row.LocalFallback.Valid && row.LocalFallback.Int64 == 1) || row.FallbackEnabled == 1)
}

func chainSuperRank(row *chainCandidateRow) int {
	return boolToInt((row.LocalSuperPriority.Valid && row.LocalSuperPriority.Int64 == 1) || row.SuperPriorityEnabled == 1)
}

func chainPriorityRank(row *chainCandidateRow) int {
	if row.LocalPriority.Valid {
		return int(row.LocalPriority.Int64)
	}
	return row.Priority
}

func chainBucketKey(row *chainCandidateRow) string {
	return fmt.Sprintf("%d:%d:%d", chainFallbackRank(row), chainSuperRank(row), chainPriorityRank(row))
}

func chainBucketSize(rows []chainEligibleRow, key string) int {
	count := 0
	for _, item := range rows {
		if chainBucketKey(item.row) == key {
			count++
		}
	}
	return count
}

// compareChainQuality mirrors compareGatewayDispatchCandidateRowsByQuality:
// rows with a fresh score order first, then ascending score, then name/id.
func compareChainQuality(left, right *chainCandidateRow, collator *collate.Collator) int {
	leftHas, rightHas := left.QualityScore != nil, right.QualityScore != nil
	if leftHas != rightHas {
		if leftHas {
			return -1
		}
		return 1
	}
	if leftHas && rightHas && *left.QualityScore != *right.QualityScore {
		if *left.QualityScore < *right.QualityScore {
			return -1
		}
		return 1
	}
	if delta := collator.CompareString(left.Name, right.Name); delta != 0 {
		return delta
	}
	return strings.Compare(left.ID, right.ID)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// chainNameCollator builds the zh-CN collator (Node localeCompare('zh-CN')).
func chainNameCollator() *collate.Collator { return collate.New(language.Chinese) }

// ---------------------------------------------------------------------------
// group access metadata (resolveGroupUsageAccessMetadata)
// ---------------------------------------------------------------------------

// resolveGroupAccess mirrors resolveGroupUsageAccessMetadata including the
// authorized-group branch (group authorization row + local settings
// overrides). groups.provider_code 列已随供应商解耦退役（供应商与上游协议面
// 重构设计 §8，fresh DDL 无该列），查询列集与
// internal/gatewayruntimecache/sqlmodels.go 的 ResolveGroupUsageAccessMetadata
// 一致；GroupUsageAccessMetadata.ProviderCode 过渡字段已随 B4 W2 删除。
func (s *chainAccountsSelector) resolveGroupAccess(ctx context.Context, groupID, systemAccountID string) (*gatewayruntimecache.GroupUsageAccessMetadata, error) {
	var ownerID string
	var enabled int
	var groupType, schedulingPolicyJSON sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT system_account_id, enabled, group_type, scheduling_policy_json FROM `+s.table("groups")+` WHERE id = ?`),
		groupID).Scan(&ownerID, &enabled, &groupType, &schedulingPolicyJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ownerID == "" {
		return nil, nil
	}
	if enabled != 1 {
		return nil, nil
	}
	normalizedType, err := chainNormalizeGroupType(groupType)
	if err != nil {
		return nil, err
	}
	policy, err := chainParseGroupSchedulingPolicy(schedulingPolicyJSON, normalizedType)
	if err != nil {
		return nil, err
	}
	if ownerID == systemAccountID {
		return &gatewayruntimecache.GroupUsageAccessMetadata{
			GroupOwnerSystemAccountID: ownerID,
			GroupAccessType:           gatewayruntimecache.GroupAccessTypeOwner,
			GroupType:                 normalizedType,
			SchedulingPolicy:          policy,
		}, nil
	}
	authorization, err := s.activeResourceAuthorization(ctx, "group", groupID, systemAccountID)
	if err != nil {
		return nil, err
	}
	if authorization == nil {
		return nil, nil
	}
	var localEnabled sql.NullInt64
	var localType, localPolicyJSON sql.NullString
	err = s.db.QueryRowContext(ctx, s.bind(`SELECT enabled, group_type, scheduling_policy_json FROM `+s.table("group_authorization_settings")+` WHERE authorization_id = ? AND system_account_id = ? AND group_id = ? LIMIT 1`),
		authorization.id, systemAccountID, groupID).Scan(&localEnabled, &localType, &localPolicyJSON)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if localEnabled.Valid && localEnabled.Int64 == 0 {
		return nil, nil
	}
	localTypeValue := normalizedType
	if localType.Valid && strings.TrimSpace(localType.String) != "" {
		normalized, typeErr := chainNormalizeGroupType(localType)
		if typeErr != nil {
			return nil, typeErr
		}
		localTypeValue = normalized
	}
	policyJSON := localPolicyJSON
	if !policyJSON.Valid || strings.TrimSpace(policyJSON.String) == "" {
		policyJSON = schedulingPolicyJSON
	}
	localPolicy, err := chainParseGroupSchedulingPolicy(policyJSON, localTypeValue)
	if err != nil {
		return nil, err
	}
	return &gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID:      ownerID,
		GroupAccessType:                gatewayruntimecache.GroupAccessTypeAuthorized,
		GroupType:                      localTypeValue,
		SchedulingPolicy:               localPolicy,
		GroupAuthorizationID:           nullStringPtr(sql.NullString{String: authorization.id, Valid: true}),
		GroupAuthorizationExpiresAt:    authorization.expiresAt,
		GroupAuthorizationQuotaLimited: chainResourceAuthorizationQuotaLimited(authorization.limitsJSON),
		GroupAuthorizationSourceType:   authorization.effectiveSourceType,
		GroupAuthorizationSourceTeamID: authorization.effectiveSourceTeamID,
	}, nil
}

// chainAuthorizationRow mirrors the ResourceAuthorizationRow subset the
// selector consumes.
type chainAuthorizationRow struct {
	id                    string
	resourceID            string
	resourceOwnerID       string
	expiresAt             *string
	effectiveSourceType   *string
	effectiveSourceTeamID *string
	limitsJSON            *string
}

const chainAuthorizationColumns = `id, resource_id, resource_owner_system_account_id, expires_at, effective_source_type, effective_source_team_id, limits_json`

// activeResourceAuthorization mirrors activeResourceAuthorization.
func (s *chainAccountsSelector) activeResourceAuthorization(ctx context.Context, resourceType, resourceID, granteeSystemAccountID string) (*chainAuthorizationRow, error) {
	now := chainNowISO(s.now)
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1`,
		chainAuthorizationColumns, s.table("resource_authorizations"))
	var authorization chainAuthorizationRow
	var resourceOwner sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(query), resourceType, resourceID, granteeSystemAccountID, now).
		Scan(&authorization.id, &authorization.resourceID, &resourceOwner, &authorization.expiresAt,
			&authorization.effectiveSourceType, &authorization.effectiveSourceTeamID, &authorization.limitsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	authorization.resourceOwnerID = resourceOwner.String
	return &authorization, nil
}

// activeResourceAuthorizationByID mirrors activeResourceAuthorizationById.
func (s *chainAccountsSelector) activeResourceAuthorizationByID(ctx context.Context, authorizationID, granteeSystemAccountID string) (*chainAuthorizationRow, error) {
	now := chainNowISO(s.now)
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1`,
		chainAuthorizationColumns, s.table("resource_authorizations"))
	var authorization chainAuthorizationRow
	var resourceOwner sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(query), authorizationID, granteeSystemAccountID, now).
		Scan(&authorization.id, &authorization.resourceID, &resourceOwner, &authorization.expiresAt,
			&authorization.effectiveSourceType, &authorization.effectiveSourceTeamID, &authorization.limitsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	authorization.resourceOwnerID = resourceOwner.String
	return &authorization, nil
}

// activeResourceAuthorizationsByIDs mirrors activeResourceAuthorizationsByIds
// keyed by both id and resource id (Node builds the merged lookup map).
func (s *chainAccountsSelector) activeResourceAuthorizationsByIDs(ctx context.Context, authorizationIDs []string, granteeSystemAccountID string) (map[string]*chainAuthorizationRow, error) {
	ids := uniqueNonEmpty(authorizationIDs)
	result := map[string]*chainAuthorizationRow{}
	if len(ids) == 0 {
		return result, nil
	}
	now := chainNowISO(s.now)
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) AND id IN (%s)`,
		chainAuthorizationColumns, s.table("resource_authorizations"), placeholders)
	args := []any{granteeSystemAccountID, now}
	args = append(args, rowsArgs(ids)...)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var authorization chainAuthorizationRow
		var resourceOwner sql.NullString
		if err := rows.Scan(&authorization.id, &authorization.resourceID, &resourceOwner, &authorization.expiresAt,
			&authorization.effectiveSourceType, &authorization.effectiveSourceTeamID, &authorization.limitsJSON); err != nil {
			return nil, err
		}
		authorization.resourceOwnerID = resourceOwner.String
		result[authorization.id] = &authorization
		result[authorization.resourceID] = &authorization
	}
	return result, rows.Err()
}

// loadAccountAuthorizationsForSelection mirrors loadAccountAuthorizationsForSelection:
// only owner-scope groups prefetch the account authorizations (nil map keeps
// the Node authorized-group fallback semantics at the call sites).
func (s *chainAccountsSelector) loadAccountAuthorizationsForSelection(ctx context.Context, rows []chainCandidateRow, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, systemAccountID string) (map[string]*chainAuthorizationRow, error) {
	if groupAccess.GroupAccessType == gatewayruntimecache.GroupAccessTypeAuthorized {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if value := strings.TrimSpace(row.AuthorizationInstanceAuthorizationID.String); value != "" {
			ids = append(ids, value)
		}
	}
	return s.activeResourceAuthorizationsByIDs(ctx, ids, systemAccountID)
}

// ---------------------------------------------------------------------------
// access resolution (resolveOpenAIAccountAccess + canScheduleAuthorizedAccount)
// ---------------------------------------------------------------------------

type chainAccountAccessType string

const (
	chainAccountAccessOwner           chainAccountAccessType = "owner"
	chainAccountAccessAuthorized      chainAccountAccessType = "account_authorized"
	chainAccountAccessGroupAuthorized chainAccountAccessType = "group_authorized"
)

// chainAccountAccess mirrors OpenAIAccountAccess.
type chainAccountAccess struct {
	accountAccessType      chainAccountAccessType
	accountOwnerID         *string
	accountAuthorizationID *string
	expiresAt              *string
	quotaLimited           *bool
	sourceType             *string
	sourceTeamID           *string
}

// chainAccountAccessFromAuthorization mirrors the authorized arm of
// resolveOpenAIAccountAccess.
func chainAccountAccessFromAuthorization(authorization *chainAuthorizationRow) *chainAccountAccess {
	return &chainAccountAccess{
		accountAccessType:      chainAccountAccessAuthorized,
		accountOwnerID:         strPtr(authorization.resourceOwnerID),
		accountAuthorizationID: strPtr(authorization.id),
		expiresAt:              authorization.expiresAt,
		quotaLimited:           chainResourceAuthorizationQuotaLimited(authorization.limitsJSON),
		sourceType:             authorization.effectiveSourceType,
		sourceTeamID:           authorization.effectiveSourceTeamID,
	}
}

// resolveChainAccountAccess mirrors resolveOpenAIAccountAccess: the prefetch
// map is authoritative for owner-scope groups (nil map = authorized group,
// the per-row database read then applies like the Node fallback).
func (s *chainAccountsSelector) resolveChainAccountAccess(ctx context.Context, row *chainCandidateRow, callerSystemAccountID string, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, boundAccountAuthorizationID string, authorizations map[string]*chainAuthorizationRow) (*chainAccountAccess, error) {
	accountOwnerID := row.SystemAccountID
	if authID := strings.TrimSpace(row.AuthorizationInstanceAuthorizationID.String); authID != "" {
		if accountOwnerID != callerSystemAccountID {
			return nil, nil
		}
		var authorization *chainAuthorizationRow
		var err error
		if authorizations != nil {
			authorization = authorizations[authID]
		} else {
			authorization, err = s.activeResourceAuthorizationByID(ctx, authID, callerSystemAccountID)
			if err != nil {
				return nil, err
			}
		}
		if authorization == nil {
			return nil, nil
		}
		if boundAccountAuthorizationID != "" && authorization.id != boundAccountAuthorizationID {
			return nil, nil
		}
		return chainAccountAccessFromAuthorization(authorization), nil
	}
	if accountOwnerID == callerSystemAccountID {
		return &chainAccountAccess{accountAccessType: chainAccountAccessOwner}, nil
	}
	if groupAccess.GroupAccessType == gatewayruntimecache.GroupAccessTypeAuthorized {
		if accountOwnerID == groupAccess.GroupOwnerSystemAccountID {
			return &chainAccountAccess{accountAccessType: chainAccountAccessGroupAuthorized}, nil
		}
		return nil, nil
	}
	var authorization *chainAuthorizationRow
	var err error
	if authorizations != nil {
		lookup := boundAccountAuthorizationID
		if lookup == "" {
			lookup = row.ID
		}
		authorization = authorizations[lookup]
	} else {
		authorization, err = s.activeResourceAuthorization(ctx, "account", row.ID, callerSystemAccountID)
		if err != nil {
			return nil, err
		}
	}
	if boundAccountAuthorizationID != "" && (authorization == nil || authorization.id != boundAccountAuthorizationID) {
		return nil, nil
	}
	if authorization == nil {
		return nil, nil
	}
	return chainAccountAccessFromAuthorization(authorization), nil
}

// canScheduleChainAuthorizedAccount mirrors canScheduleAuthorizedAccount.
func canScheduleChainAuthorizedAccount(row *chainCandidateRow, authorizations map[string]*chainAuthorizationRow, access *chainAccountAccess) bool {
	if access.accountAccessType == chainAccountAccessOwner || access.accountAccessType == chainAccountAccessGroupAuthorized {
		return true
	}
	if access.accountAuthorizationID == nil || *access.accountAuthorizationID == "" {
		return false
	}
	authorizationID := *access.accountAuthorizationID
	if authorizations != nil {
		authorization := authorizations[authorizationID]
		if authorization == nil {
			authorization = authorizations[row.ID]
		}
		return authorization != nil && authorization.id == authorizationID
	}
	return false
}

// ---------------------------------------------------------------------------
// availability gates (isOpenAIAccountAvailableForSelection)
// ---------------------------------------------------------------------------

func (row *chainCandidateRow) resourceAccountID() string {
	if row.ResourceAccountID.Valid && row.ResourceAccountID.String != "" {
		return row.ResourceAccountID.String
	}
	return row.ID
}

func (row *chainCandidateRow) resourceProxyProfileID() string {
	if row.ResourceProxyProfileID.Valid && row.ResourceProxyProfileID.String != "" {
		return row.ResourceProxyProfileID.String
	}
	if row.ProxyProfileID.Valid {
		return row.ProxyProfileID.String
	}
	return ""
}

// resourceVendorCode / resourceConnectionDriverCode 返回模型事实主体的连接
// 身份（供应商与上游协议面重构设计 §2.1：授权实例的连接列物理为空，读取经
// 来源账户 resource_account_id 投影；直连账户落回自身列）。vendor 值域与旧
// providerCode 同名（gpt/xai/deepseek/anthropic/gemini/glm/minimax/volcengine/
// qwen，generic/NULL vendor 账户为空串）。
func (row *chainCandidateRow) resourceVendorCode() string {
	if row.ResourceVendorCode.Valid && row.ResourceVendorCode.String != "" {
		return row.ResourceVendorCode.String
	}
	if row.VendorCode.Valid {
		return row.VendorCode.String
	}
	return ""
}

func (row *chainCandidateRow) resourceConnectionDriverCode() string {
	if row.ResourceConnectionDriverCode.Valid && row.ResourceConnectionDriverCode.String != "" {
		return row.ResourceConnectionDriverCode.String
	}
	if row.ConnectionDriverCode.Valid {
		return row.ConnectionDriverCode.String
	}
	return ""
}

func (row *chainCandidateRow) resourceProtocolCode() string {
	if row.ResourceProtocolCode.Valid && row.ResourceProtocolCode.String != "" {
		return row.ResourceProtocolCode.String
	}
	return row.ProtocolCode.String
}

func (row *chainCandidateRow) resourceProtocolVersion() string {
	if row.ResourceProtocolVersion.Valid && row.ResourceProtocolVersion.String != "" {
		return row.ResourceProtocolVersion.String
	}
	return row.ProtocolVersion.String
}

func (row *chainCandidateRow) resourceType() string {
	if row.ResourceType.Valid && row.ResourceType.String != "" {
		return row.ResourceType.String
	}
	return row.Type
}

func (row *chainCandidateRow) resourceCredentialsEncrypted() string {
	if row.ResourceCredentialsEncrypted.Valid && row.ResourceCredentialsEncrypted.String != "" {
		return row.ResourceCredentialsEncrypted.String
	}
	return row.CredentialsEncrypted.String
}

func (row *chainCandidateRow) resourceConcurrencyLimit() int {
	if row.ResourceConcurrencyLimit.Valid {
		return int(row.ResourceConcurrencyLimit.Int64)
	}
	return row.ConcurrencyLimit
}

// chainAccountAvailableForSelection mirrors isOpenAIAccountAvailableForSelection
// (physical gates + resource-account gates; the authorized-binding gate runs
// in the eligibility loop where the access object is known). mediaAdmission
// 武装视频异步任务恢复臂，与候选窗口 SQL 谓词对应。
func chainAccountAvailableForSelection(row *chainCandidateRow, now string, includeUnavailable bool, mediaAdmission bool) (bool, error) {
	nowMillis, err := chainRFC3339Millis(now)
	if err != nil {
		return false, fmt.Errorf("账户选择当前时间必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	physical, err := chainPhysicalAccountAvailable(row, nowMillis, includeUnavailable, mediaAdmission)
	if err != nil {
		return false, err
	}
	if !physical {
		return false, nil
	}
	return chainResourceAccountAvailable(row, nowMillis, includeUnavailable)
}

// chainMediaAdmissionAccountEligible 是冷却到期视频准入臂的行级判定（与
// chainMediaAdmissionStatusArm/CooldownArm 的 SQL 谓词镜像）：video_create
// 账户处于四个可调度
// status 且冷却已过期或为空（NULL 视为过期）。套餐过期与 schedulable 门不
// 在本判定内——由调用方的既有闸先行裁定，准入臂不豁免它们。
//
// 与 SQL 臂的镜像边界：includeUnavailable=true 的恢复专用读（preflight 等
// 待）若与准入臂组合，SQL status 臂会先放行 pending_test 等行、冷却门由本
// 判定最终裁定——行级为契约语义权威，SQL 臂仅作候选预过滤。
func chainMediaAdmissionAccountEligible(row *chainCandidateRow, nowMillis int64) bool {
	if !row.HealthCheckEndpointMode.Valid {
		return false
	}
	if strings.TrimSpace(row.HealthCheckEndpointMode.String) != "video_create" {
		return false
	}
	switch row.Status {
	case "active", "pending_test", "rate_limited", "temporary_unavailable":
	default:
		return false
	}
	if !row.CooldownUntil.Valid || strings.TrimSpace(row.CooldownUntil.String) == "" {
		return true
	}
	cooldown, err := chainRFC3339Millis(row.CooldownUntil.String)
	if err != nil {
		return false
	}
	return cooldown <= nowMillis
}

func chainPhysicalAccountAvailable(row *chainCandidateRow, nowMillis int64, includeUnavailable bool, mediaAdmission bool) (bool, error) {
	if row.AccountExpiresAt.Valid && strings.TrimSpace(row.AccountExpiresAt.String) != "" {
		expires, err := chainRFC3339Millis(row.AccountExpiresAt.String)
		if err != nil {
			return false, fmt.Errorf("AI 账户 accountExpiresAt必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		if expires <= nowMillis {
			return false, nil
		}
	}
	if row.Schedulable != 1 {
		return false, nil
	}
	// 冷却到期视频恢复臂：armed 的视频创建查询上，冷却已过期/为空的
	// video_create 账户直接可用——不改变套餐过期与 schedulable 门。
	if mediaAdmission && chainMediaAdmissionAccountEligible(row, nowMillis) {
		return true, nil
	}
	if includeUnavailable {
		return row.Status == "active" || row.Status == "rate_limited" || row.Status == "temporary_unavailable", nil
	}
	if row.Status != "active" {
		return false, nil
	}
	if !row.CooldownUntil.Valid || strings.TrimSpace(row.CooldownUntil.String) == "" {
		return true, nil
	}
	cooldown, err := chainRFC3339Millis(row.CooldownUntil.String)
	if err != nil {
		return false, fmt.Errorf("AI 账户 cooldownUntil必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return cooldown <= nowMillis, nil
}

func chainResourceAccountAvailable(row *chainCandidateRow, nowMillis int64, includeUnavailable bool) (bool, error) {
	if !row.AuthorizationInstanceAuthorizationID.Valid || strings.TrimSpace(row.AuthorizationInstanceAuthorizationID.String) == "" {
		return true, nil
	}
	if !row.ResourceAccountID.Valid || row.ResourceAccountID.String == "" || !row.ResourceStatus.Valid || row.ResourceStatus.String == "" {
		return false, nil
	}
	if row.ResourceAccountExpiresAt.Valid && strings.TrimSpace(row.ResourceAccountExpiresAt.String) != "" {
		expires, err := chainRFC3339Millis(row.ResourceAccountExpiresAt.String)
		if err != nil {
			return false, fmt.Errorf("授权来源账户 accountExpiresAt必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		if expires <= nowMillis {
			return false, nil
		}
	}
	if row.ResourceLastErrorCode.Valid && row.ResourceLastErrorCode.String == "account_expired" {
		return false, nil
	}
	if !row.ResourceSchedulable.Valid || row.ResourceSchedulable.Int64 != 1 {
		return false, nil
	}
	if includeUnavailable {
		status := row.ResourceStatus.String
		return status == "active" || status == "rate_limited" || status == "temporary_unavailable", nil
	}
	if row.ResourceStatus.String != "active" {
		return false, nil
	}
	if !row.ResourceCooldownUntil.Valid || strings.TrimSpace(row.ResourceCooldownUntil.String) == "" {
		return true, nil
	}
	cooldown, err := chainRFC3339Millis(row.ResourceCooldownUntil.String)
	if err != nil {
		return false, fmt.Errorf("授权来源账户 cooldownUntil必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return cooldown <= nowMillis, nil
}

// ---------------------------------------------------------------------------
// fresh quality rows (loadFreshGatewayDispatchCandidateQualityRows)
// ---------------------------------------------------------------------------

type chainQualityRow struct {
	score        *float64
	state        *string
	firstTokenMs *float64
}

// loadFreshQualityRows mirrors loadFreshGatewayDispatchCandidateQualityRows:
// one chunked read against the stats account_quality_scores table with the
// 24h freshness window.
func (s *chainAccountsSelector) loadFreshQualityRows(ctx context.Context, accountIDs []string, freshAfter string) (map[string]chainQualityRow, error) {
	ids := uniqueNonEmpty(accountIDs)
	result := map[string]chainQualityRow{}
	if len(ids) == 0 {
		return result, nil
	}
	for start := 0; start < len(ids); start += 900 {
		end := start + 900
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		placeholders := strings.TrimRight(strings.Repeat("?, ", len(chunk)), ", ")
		query := fmt.Sprintf(`SELECT account_id, quality_score, quality_state, ewma_first_token_ms
			FROM %s WHERE account_id IN (%s) AND last_sample_at >= ?`, s.statsTable("account_quality_scores"), placeholders)
		args := rowsArgs(chunk)
		args = append(args, freshAfter)
		rows, err := s.statsDB.QueryContext(ctx, s.bind(query), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var accountID string
			var quality chainQualityRow
			if err := rows.Scan(&accountID, &quality.score, &quality.state, &quality.firstTokenMs); err != nil {
				rows.Close()
				return nil, err
			}
			result[accountID] = quality
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

// chainQualityFreshAfterISO mirrors gatewayDispatchCandidateQualityFreshAfterIso.
func chainQualityFreshAfterISO(now func() time.Time) string {
	return now().UTC().Add(-24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// ---------------------------------------------------------------------------
// hydration loads (models / mappings / rotation states / proxy profiles)
// ---------------------------------------------------------------------------

// loadSupportedModelsByAccountIds mirrors loadSupportedModelsByAccountIds.
// B4 第一批目标形态（供应商与上游协议面重构设计 §4）：account_supported_models
// 收敛为 (account_id, model_id, created_at)，支持模型名经 model_id INNER JOIN
// models.upstream_model 解析（写入解析键同语义，GKM 装载器同款形态）。
// 资格门（模型 status/owner-visible、请求级 mode 交集）归派发消费方（W2
// runtime gating），本装载只做数据链名字解析。
func (s *chainAccountsSelector) loadSupportedModelsByAccountIds(ctx context.Context, accountIDs []string) (map[string][]string, error) {
	result := map[string][]string{}
	ids := uniqueNonEmpty(accountIDs)
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	query := fmt.Sprintf(`SELECT asm.account_id, m.upstream_model
		FROM %s asm
		INNER JOIN %s m ON m.id = asm.model_id
		WHERE asm.account_id IN (%s)
		ORDER BY asm.created_at ASC, m.upstream_model ASC`, s.table("account_supported_models"), s.table("models"), placeholders)
	rows, err := s.db.QueryContext(ctx, s.bind(query), rowsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, model string
		if err := rows.Scan(&accountID, &model); err != nil {
			return nil, err
		}
		result[accountID] = append(result[accountID], model)
	}
	return result, rows.Err()
}

// loadModelMappingsByAccountIds mirrors loadModelMappingsByAccountIds. B4 第一
// 批目标形态：account_entry_mappings LEFT JOIN models（V1 批 GKM 装载器同款
// 形态）——upstream_model 经 target_model_id 解析（LEFT JOIN 对齐 GKM 读取
// 先例；FK 世界里目标恒存在），target_endpoint_family 即上游端点族语义；
// enabled 行与 disabled 行都读并投影（secret.ModelMappings.Enabled 契约保持
// 不变，派发侧启用过滤归消费方）。
func (s *chainAccountsSelector) loadModelMappingsByAccountIds(ctx context.Context, accountIDs []string) (map[string][]gatewayruntimecache.AccountModelMapping, error) {
	result := map[string][]gatewayruntimecache.AccountModelMapping{}
	ids := uniqueNonEmpty(accountIDs)
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	query := fmt.Sprintf(`SELECT em.account_id, em.source_model, em.source_endpoint_family, m.upstream_model, em.target_endpoint_family, em.enabled
		FROM %s em
		LEFT JOIN %s m ON m.id = em.target_model_id
		WHERE em.account_id IN (%s)
		ORDER BY em.account_id ASC, em.source_model ASC, em.source_endpoint_family ASC, em.id ASC`,
		s.table("account_entry_mappings"), s.table("models"), placeholders)
	rows, err := s.db.QueryContext(ctx, s.bind(query), rowsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var mapping gatewayruntimecache.AccountModelMapping
		var sourceFamily, upstreamFamily sql.NullString
		var enabled int
		if err := rows.Scan(&accountID, &mapping.SourceModel, &sourceFamily, &mapping.UpstreamModel, &upstreamFamily, &enabled); err != nil {
			return nil, err
		}
		mapping.SourceEndpointFamily = sourceFamily.String
		mapping.UpstreamEndpointFamily = upstreamFamily.String
		mapping.Enabled = enabled == 1
		result[accountID] = append(result[accountID], mapping)
	}
	return result, rows.Err()
}

// loadConnectionEndpointModesByAccountIds 按模型事实主体 ID 集装载单连接事实源
// 的 account_endpoint_modes 实际集合（secret 端点模式投影的连接行事实源）。
// 连接选择口径与候选窗口 connection_base_url/connection_preset_code 标量子
// 查询及 jobs accounthealth resolved_health_connections 完全一致：
// health_check_connection_id 指向的 enabled 连接优先，否则 enabled 连接按 id
// 升序首个——SQL 按该确定性排序取每账户首连接分组，模式集合在 Go 侧收敛
// （同账户多连接多候选展开留后续批，本批同账户仍单候选）。连接未配 mode 的
// 老数据产出空集，secret 投影回落既有 vendor 默认分支（chain_accounts_secret.go
// 兼容分支）。
func (s *chainAccountsSelector) loadConnectionEndpointModesByAccountIds(ctx context.Context, accountIDs []string) (map[string][]string, error) {
	result := map[string][]string{}
	ids := uniqueNonEmpty(accountIDs)
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	query := fmt.Sprintf(`SELECT c.account_id, c.id, m.endpoint_mode
		FROM %s c
		INNER JOIN %s a ON a.id = c.account_id
		LEFT JOIN %s m ON m.connection_id = c.id
		WHERE c.enabled = 1 AND c.account_id IN (%s)
		ORDER BY c.account_id ASC, (c.id = COALESCE(a.health_check_connection_id, '')) DESC, c.id ASC, m.endpoint_mode ASC`,
		s.table("account_connections"), s.table("accounts"), s.table("account_endpoint_modes"), placeholders)
	rows, err := s.db.QueryContext(ctx, s.bind(query), rowsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chosen := map[string]string{}
	for rows.Next() {
		var accountID, connectionID string
		var mode sql.NullString
		if err := rows.Scan(&accountID, &connectionID, &mode); err != nil {
			return nil, err
		}
		if seen, ok := chosen[accountID]; !ok {
			// 排序首位即该账户的选定连接（hc 优先 / id 升序），只收它的分组。
			chosen[accountID] = connectionID
		} else if seen != connectionID {
			continue
		}
		if mode.Valid && mode.String != "" {
			result[accountID] = append(result[accountID], mode.String)
		}
	}
	return result, rows.Err()
}

// loadAPIKeyRuntimeStatesByAccountIds mirrors
// loadAccountApiKeyRuntimeStatesByAccountIds. The Go runtime-state struct
// carries the dispatch subset (fingerprint / disabled / cooldown /
// recovery); key_index and next_probe_at stay Node-owned columns.
func (s *chainAccountsSelector) loadAPIKeyRuntimeStatesByAccountIds(ctx context.Context, accountIDs []string) (map[string][]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, error) {
	result := map[string][]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState{}
	ids := uniqueNonEmpty(accountIDs)
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	query := fmt.Sprintf(`SELECT account_id, key_fingerprint, key_index, status, cooldown_until, next_probe_at, recovery_started_at
		FROM %s WHERE account_id IN (%s)`, s.table("account_api_key_runtime_states"), placeholders)
	rows, err := s.db.QueryContext(ctx, s.bind(query), rowsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var state gatewayruntimecache.AccountAPIKeyRuntimeSelectionState
		var keyIndex sql.NullInt64
		var status sql.NullString
		var cooldown, nextProbe, recovery sql.NullString
		if err := rows.Scan(&accountID, &state.Fingerprint, &keyIndex, &status, &cooldown, &nextProbe, &recovery); err != nil {
			return nil, err
		}
		state.Disabled = !status.Valid || status.String != "active"
		state.CooldownUntil = nullStringPtr(cooldown)
		state.RecoveryStartedAt = nullStringPtr(recovery)
		_ = keyIndex
		_ = nextProbe
		result[accountID] = append(result[accountID], state)
	}
	return result, rows.Err()
}

// chainProxyProfileResolution mirrors ProxyProfileUrlResolution.
type chainProxyProfileResolution struct {
	proxyURL     *string
	unavailable  *bool
	errorMessage *string
}

// resolveProxyURLsForProfiles mirrors resolveProxyUrlsForProfiles: the proxy
// profiles table read with the AES-GCM password decryption; disabled or
// missing profiles surface the Node unavailable contract (never an error).
func (s *chainAccountsSelector) resolveProxyURLsForProfiles(ctx context.Context, proxyProfileIDs []string) (map[string]chainProxyProfileResolution, error) {
	ids := uniqueNonEmpty(proxyProfileIDs)
	output := map[string]chainProxyProfileResolution{}
	if len(ids) == 0 {
		return output, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	query := fmt.Sprintf(`SELECT id, type, host, port, username, password_encrypted, enabled FROM %s WHERE id IN (%s)`, s.table("proxy_profiles"), placeholders)
	rows, err := s.db.QueryContext(ctx, s.bind(query), rowsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	type proxyRow struct {
		id                string
		proxyType         string
		host              string
		port              int
		username          sql.NullString
		passwordEncrypted sql.NullString
		// proxy_profiles.enabled 在权威 PG DDL 中是 boolean（sqlite 为
		// INTEGER 0/1）：NullBool 经 database/sql 双方言可扫（int64 0/1 经
		// driver.Bool 转换），NullInt64 会在 PG 上 bool→int64 扫描失败。
		enabled sql.NullBool
	}
	rowsByID := map[string]*proxyRow{}
	for rows.Next() {
		proxy := &proxyRow{}
		if err := rows.Scan(&proxy.id, &proxy.proxyType, &proxy.host, &proxy.port, &proxy.username, &proxy.passwordEncrypted, &proxy.enabled); err != nil {
			rows.Close()
			return nil, err
		}
		rowsByID[proxy.id] = proxy
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, id := range ids {
		proxy := rowsByID[id]
		if proxy == nil || !proxy.enabled.Valid || !proxy.enabled.Bool {
			output[id] = chainProxyUnavailable("代理不存在或已停用，请选择一个已启用的代理")
			continue
		}
		url, err := chainProxyURLFromRow(s.secret, proxy.proxyType, proxy.host, proxy.port, proxy.username.String, proxy.passwordEncrypted.String)
		if err != nil {
			output[id] = chainProxyUnavailable("代理凭据不可解密，请检查代理配置")
			continue
		}
		output[id] = chainProxyProfileResolution{proxyURL: &url}
	}
	return output, nil
}

func chainProxyUnavailable(message string) chainProxyProfileResolution {
	unavailable := true
	return chainProxyProfileResolution{unavailable: &unavailable, errorMessage: &message}
}

// chainProxyURLFromRow mirrors proxyUrlFromRow + proxyPassword.
func chainProxyURLFromRow(secret, proxyType, host string, port int, username, passwordEncrypted string) (string, error) {
	protocol := proxyType
	if proxyType == "socks5h" || proxyType == "socks5" {
		protocol = "socks5h"
	}
	password := ""
	if passwordEncrypted != "" {
		var envelope struct {
			Password *string `json:"password"`
		}
		if err := accounts.DecryptJSON(secret, passwordEncrypted, &envelope); err != nil {
			return "", err
		}
		if envelope.Password != nil {
			password = *envelope.Password
		}
	}
	credentials := ""
	if username != "" {
		credentials = chainURIEncode(username)
		if password != "" {
			credentials += ":" + chainURIEncode(password)
		}
		credentials += "@"
	}
	return fmt.Sprintf("%s://%s%s:%d", protocol, credentials, host, port), nil
}

// ---------------------------------------------------------------------------
// shared small helpers
// ---------------------------------------------------------------------------

func chainNowISO(now func() time.Time) string {
	return now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func rowsArgs(values []string) []any {
	args := make([]any, 0, len(values))
	for _, value := range values {
		args = append(args, value)
	}
	return args
}

func strPtr(value string) *string { return &value }

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

type nullStringSink = sql.NullString
