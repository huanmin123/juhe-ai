package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

// PatchChange mirrors AccountManagementPatchChange（REFACTOR-0005 阶段 B 下沉
// accountscore，运行时重置子域审计日志与门面 PATCH 面共用）.
type PatchChange = accountscore.PatchChange

// PatchResult mirrors the PATCH response payload: { id, configRevision,
// changedFields } plus the fields the operation log needs.
type PatchResult struct {
	ID                   string        `json:"id"`
	ConfigRevision       int64         `json:"configRevision"`
	ChangedFields        []string      `json:"changedFields"`
	Name                 string        `json:"-"`
	OwnerSystemAccountID string        `json:"-"`
	Changes              []PatchChange `json:"-"`
	Tags                 []TagSummary  `json:"-"`
	// HealthCheckRequired mirrors the repository outcome the route uses to
	// dispatch the post-commit configuration probe
	// (account-management-patch.repository.ts healthCheckReason).
	HealthCheckRequired bool   `json:"-"`
	HealthCheckReason   string `json:"-"`
	// GroupChanged mirrors groupChanged (the enabled binding row switched);
	// it feeds the gateway runtime invalidation condition
	// (gatewayRuntimeAffected: groupChanged || credentialsChanged || ...).
	GroupChanged bool `json:"-"`
	// DispatchBindingChanged mirrors dispatchBindingChanged（归档
	// account-management-patch.repository.ts:514-516）：调度三字段
	// priority/superPriorityEnabled/fallbackEnabled 的终值偏离行内现值。
	// 与 GroupChanged 一起构成 Node groupStatsAffected 联动条件（授权路径
	// 归档 :1036：groupChanged || dispatchBindingChanged → 提交后分组统计
	// 脏标记 refreshGroupAccountStatsAfterWriteAsync）。
	DispatchBindingChanged bool `json:"-"`
	// BalanceIdentityChanged mirrors balanceIdentityChanged (归档
	// :744-761)：余额查询身份（开关/配置/Key 指纹/base URL/代理）发生变化，
	// 提交后触发余额快照旧代次清理端口。
	BalanceIdentityChanged bool `json:"-"`
}

// PatchInput is the validated basic-edit payload: the account-edit-basic
// editable field set plus the expectedConfigRevision optimistic lock. Nil
// pointers mean the field was absent (undefined vs null distinction follows
// the Node optional/nullable schema pairs).
type PatchInput struct {
	ExpectedConfigRevision int64
	Name                   *string
	Notes                  *string
	Status                 *string
	ConcurrencyLimit       *int
	Priority               *int
	SuperPriorityEnabled   *bool
	FallbackEnabled        *bool
	Schedulable            *bool
	Credentials            Credentials
	CredentialsPresent     bool
	// CredentialsPatch marks the credentialsPatch input (BUG-0175 D-164):
	// null patch keys delete fields from the current record, while the legacy
	// credentials input pre-merges through mergeAccountCredentialsForUpdate
	// (D-165 pool replacement).
	CredentialsPatch            bool
	SupportedModels             []string
	SupportedModelsPresent      bool
	HealthCheckModel            *string
	HealthCheckEndpointMode     *string
	Tags                        []string
	TagsPresent                 bool
	AccountExpiresAt            *string
	AccountExpiresAtPresent     bool
	AvailabilitySchedule        any
	AvailabilitySchedulePresent bool
	ClearFailureState           bool
	// ModelMappings mirrors input.modelMappings (accountUpdateSchema).
	ModelMappings        []ModelMapping
	ModelMappingsPresent bool
	// Connections（B1 断点批，设计 §8）是连接集合整替换入参：保留仍存在 preset
	// 的 DB id、新 preset 服务端生成 id、删除未提交 preset；禁用连接时引用它的
	// enabled 映射在同一事务内禁用（§5.1），删除仍被映射引用的连接按 RESTRICT
	// 语义 400（connection_mapping_conflict）。
	Connections        []ConnectionInput
	ConnectionsPresent bool
	// HealthCheckConnectionPresetCode（设计 §8）是健康检查三件套的连接维度
	// 入参：必须解析到补丁后最终连接集合中一行 enabled 连接。
	HealthCheckConnectionPresetCode *string
	// ProxyProfileID mirrors input.proxyProfileId (nullable id tri-state).
	ProxyProfileID        *string
	ProxyProfileIDPresent bool
	// GroupID mirrors input.groupId (required non-empty string when present).
	GroupID        *string
	GroupIDPresent bool
	// Balance fields mirror input.balanceQueryEnabled / balanceQueryConfig;
	// the config arrives already normalized (canonical JSON) from the body
	// parser, like the Node route.
	BalanceQueryEnabled                        *bool
	BalanceQueryConfigCanonical                *string
	BalanceQueryConfigPresent                  bool
	TemporaryUnavailableContinuousProbeEnabled *bool
	// ExpeditedRecoveryEnabled mirrors the expedited-recovery switch (设计
	// §8.1/§9)：false→true 的翻转在补丁事务内做归属名额校验；置 false 与幂等
	// true 不校验。特供消费方是 jobs 恢复道，不进 gatewayRuntimeFields。
	ExpeditedRecoveryEnabled *bool
	// CapabilityDomain 是 M2 域不可变校验的入参（camelCase capabilityDomain）：
	// 携带且 ≠ 行现值 → 400「能力域不可更改」；不携带 = 无操作。域创建后
	// 不可更改（D3），编辑面不允许任何改域路径。
	CapabilityDomain        *string
	CapabilityDomainPresent bool
}

// credentialModesExplicitlyDeclared 判定本次补丁是否显式携带
// supported_endpoint_modes 数组（严格声明校验的触发前提）：显式数组 = 声明集
// 的直接提交，归一化结果必须 ⊆ 域允许集；credentialsPatch 删键（present-null）
// 已在补丁准备处就地替换为按域默认集数组（DefaultEndpointModesForDomain），
// 到这里同样以显式数组出现——按域默认集并不保证 ⊆ 域允许集，越界组合由同一
// 严格校验响亮 400；键缺省（absent）= 不重置声明集，不触发本判定。HTTP 面
// 数组解码为 []any；[]string 兼容 store 层直调。
func credentialModesExplicitlyDeclared(input PatchInput) bool {
	if !input.CredentialsPresent {
		return false
	}
	raw, ok := input.Credentials["supported_endpoint_modes"]
	if !ok || raw == nil {
		return false
	}
	switch raw.(type) {
	case []any, []string:
		return true
	}
	return false
}

// accountPatchChangeLabel mirrors accountPatchChangeLabel (credentials.*
// fields collapse to 凭据).
func accountPatchChangeLabel(field string) string {
	if strings.HasPrefix(field, "credentials.") {
		return "凭据"
	}
	switch field {
	case "name":
		return "名称"
	case "notes":
		return "备注"
	case "credentials":
		return "凭据"
	case "status":
		return "状态"
	case "runtimeState":
		return "运行状态"
	case "concurrencyLimit":
		return "并发限制"
	case "priority":
		return "优先级"
	case "superPriorityEnabled":
		return "超级优先"
	case "fallbackEnabled":
		return "降级备用"
	case "supportedModels":
		return "支持模型"
	case "connections":
		return "连接"
	case "healthCheckConnectionPresetCode":
		return "检查连接"
	case "healthCheckModel":
		return "检查模型"
	case "healthCheckEndpointMode":
		return "检查协议"
	case "temporaryUnavailableContinuousProbeEnabled":
		return "持续恢复探活"
	case "expeditedRecoveryEnabled":
		return "特供恢复"
	case "modelMappings":
		return "模型映射"
	case "tags":
		return "标签"
	case "proxyProfileId":
		return "代理"
	case "schedulable":
		return "参与调度"
	case "accountExpiresAt":
		return "过期时间"
	case "availabilitySchedule":
		return "时间计划"
	case "groupId":
		return "绑定分组"
	case "balanceQueryEnabled":
		return "余额查询"
	case "balanceQueryConfig":
		return "余额查询配置"
	case "clearFailureState":
		return "异常恢复"
	default:
		return field
	}
}

// Patch mirrors patchAccountManagementAsync restricted to the basic editable
// field set: scope-checked row load, config_revision CAS (409 on mismatch),
// field-wise diff, tags/ models/ search-terms maintenance and the revision
// increment. Returns (nil, nil) when the account is missing or outside the
// access scope.
func (s *Store) Patch(ctx context.Context, accountID string, input PatchInput, access AccessScope) (*PatchResult, error) {
	ctx = ensureCtx(ctx)
	// expectedConfigRevision >= 1 由全部 HTTP 入口先行校验（routes.go
	// patchBody/tagsPatchBody/runtime patchBody、m11_routes.go groupBinding
	// 同判据；aipublic 从行内 config_revision 读入），store 层不再重复。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	scoped := access.ManageableID()
	scopeClause := ""
	args := []any{strings.TrimSpace(accountID)}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row struct {
		id                        string
		configRevision            int64
		systemAccountID           string
		name                      string
		notes                     sql.NullString
		accountType               string
		credentialsEncrypted      string
		status                    string
		concurrencyLimit          int
		priority                  int
		superPriorityEnabled      int
		fallbackEnabled           int
		schedulable               int
		availabilitySchedule      sql.NullString
		accountExpiresAt          sql.NullString
		lastErrorCode             sql.NullString
		lastErrorMessage          sql.NullString
		lastErrorTraceID          sql.NullString
		cooldownUntil             sql.NullString
		cooldownRetestCount       int
		cooldownRetestObservation sql.NullString
		cooldownRetestGeneration  sql.NullString
		cooldownRetestLastAt      sql.NullString
		cooldownRetestLastCode    sql.NullInt64
		healthCheckModel          string
		healthCheckEndpointMode   string
		// healthCheckConnectionID / currentHealthCheckPreset（B1 断点批）：
		// 行内健康检查连接 id 与其账户内 preset 反解（§8 健康检查三件套的
		// 连接维度现值）。
		healthCheckConnectionID   sql.NullString
		currentHealthCheckPreset  string
		providerCode              string
		connectionDriverCode      string
		providerProtocolProfileID string
		protocolCode              string
		protocolVersion           string
		clientCompatibility       string
		oauthType                 string
		proxyProfileID            sql.NullString
		balanceQueryEnabled       int
		balanceQueryConfigJSON    string
		balanceQueryNextRefreshAt sql.NullString
		temporaryProbeEnabled     int
		nextHealthCheckAt         sql.NullString
		lastHealthCheckAt         sql.NullString
		lastHealthSuccessAt       sql.NullString
		healthCheckFailureCount   int
		healthCheckFailureStart   sql.NullString
		lastHealthStatusCode      sql.NullInt64
		lastHealthErrorCode       sql.NullString
		lastHealthErrorMessage    sql.NullString
		lastHealthTraceID         sql.NullString
		authorizationID           sql.NullString
		expeditedRecoveryEnabled  int
		capabilityDomain          string
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision,
			accounts.system_account_id, accounts.name, accounts.notes, accounts.type,
			accounts.credentials_encrypted, accounts.status, accounts.concurrency_limit,
			accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
			accounts.schedulable, accounts.availability_schedule_json, accounts.account_expires_at,
			accounts.last_error_code, accounts.last_error_message, accounts.last_error_trace_id,
			accounts.cooldown_until, accounts.cooldown_retest_failure_count,
			accounts.cooldown_retest_observation_started_at, accounts.cooldown_retest_generation,
			accounts.cooldown_retest_last_at, accounts.cooldown_retest_last_status_code,
			accounts.health_check_model, accounts.health_check_endpoint_mode,
			-- B1 断点批：健康检查连接 id + 其账户内 preset 反解（§8 三件套的
			-- 连接维度现值；id 为 NULL 时反解投影为空串）。
			accounts.health_check_connection_id,
			COALESCE((SELECT account_connections.connection_preset_code
				FROM `+s.table("account_connections")+` account_connections
				WHERE account_connections.id = accounts.health_check_connection_id), '') AS current_health_check_preset,
			-- B4 W2 行为键投影：行为键只读 vendor_code；provider_code 已退役，
			-- 不存在旧列回落（vendor 值域与旧 providerCode 同名，NULL 归一空串）。
			COALESCE(accounts.vendor_code, '') AS provider_code,
			COALESCE(accounts.connection_driver_code, '') AS connection_driver_code,
			accounts.protocol_code,
			accounts.protocol_version, '' AS client_compatibility,
			-- oauth_type 物理列（设计 §2.1）：registry 判定读列优先、credentials
			-- 回落（兼容存量行）。
			COALESCE(accounts.oauth_type, '') AS oauth_type, accounts.proxy_profile_id,
			accounts.balance_query_enabled, accounts.balance_query_config_json,
			accounts.balance_query_next_refresh_at,
			accounts.temporary_unavailable_continuous_probe_enabled, accounts.next_health_check_at,
			accounts.last_health_check_at, accounts.last_health_success_at,
			accounts.health_check_failure_count, accounts.health_check_failure_started_at,
			accounts.last_health_check_status_code, accounts.last_health_check_error_code,
			accounts.last_health_check_error_message, accounts.last_health_check_trace_id,
			accounts.authorization_instance_authorization_id,
			accounts.expedited_recovery_enabled, accounts.capability_domain
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.configRevision, &row.systemAccountID, &row.name, &row.notes,
		&row.accountType, &row.credentialsEncrypted, &row.status, &row.concurrencyLimit,
		&row.priority, &row.superPriorityEnabled, &row.fallbackEnabled, &row.schedulable,
		&row.availabilitySchedule, &row.accountExpiresAt, &row.lastErrorCode,
		&row.lastErrorMessage, &row.lastErrorTraceID, &row.cooldownUntil,
		&row.cooldownRetestCount, &row.cooldownRetestObservation, &row.cooldownRetestGeneration,
		&row.cooldownRetestLastAt, &row.cooldownRetestLastCode,
		&row.healthCheckModel, &row.healthCheckEndpointMode,
		&row.healthCheckConnectionID, &row.currentHealthCheckPreset, &row.providerCode,
		&row.connectionDriverCode,
		&row.protocolCode, &row.protocolVersion,
		&row.clientCompatibility, &row.oauthType, &row.proxyProfileID, &row.balanceQueryEnabled,
		&row.balanceQueryConfigJSON, &row.balanceQueryNextRefreshAt, &row.temporaryProbeEnabled,
		&row.nextHealthCheckAt,
		&row.lastHealthCheckAt, &row.lastHealthSuccessAt, &row.healthCheckFailureCount,
		&row.healthCheckFailureStart, &row.lastHealthStatusCode, &row.lastHealthErrorCode,
		&row.lastHealthErrorMessage, &row.lastHealthTraceID, &row.authorizationID,
		&row.expeditedRecoveryEnabled, &row.capabilityDomain)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// 供应商解耦（设计 §2.1）：provider_protocol_profile_id 列已退役，协议档案
	// 由账户健康检查连接的 preset 确定性派生（与 jobs 探针读侧同一映射）。
	if profileID, ok := exactkeyprobe.ConnectionPresetProfileID(row.currentHealthCheckPreset); ok {
		row.providerProtocolProfileID = profileID
	}
	// 说明：此处曾有一道 `!access.CanAccessAll() && row.systemAccountID !=
	// access.ViewerID` 的越权守卫，但非管理员查询已带
	// `system_account_id = manageableID()` 过滤、管理员恒可访问，条件恒为
	// 假，属于不可达防御代码，w13a 移除。
	if row.configRevision != input.ExpectedConfigRevision {
		return nil, &RevisionConflictError{Message: RevisionConflictMessage}
	}
	// 归档 patchAccountFailureStateInTransaction（account-management-patch
	// .repository.ts:1116-1119）：clearFailureState=true 是独立的重新检查/
	// 异常恢复命令，只允许伴随 expectedConfigRevision（以及 Go 编辑面不存在的
	// 内部 runtimeResetRequireUnlocked 栅栏），与任何字段修改混交时以归档
	// 文案拒绝（ValidationError → 400，与归档普通 Error → badRequest(400)
	// 一致）。放在 revision CAS 之后对齐归档顺序：过期版本先返回 409。
	if input.ClearFailureState && patchHasMixedEditField(input) {
		return nil, &ValidationError{Message: "重新检查或异常恢复不能与账户字段修改同时提交"}
	}
	// 健康检查三件套同现同省（供应商与上游协议面重构设计 §8）：healthCheck
	// ConnectionPresetCode / healthCheckEndpointMode / healthCheckModel 必须
	// 同时出现或同时省略，部分提交按 400 拒绝（ValidationError，风格对齐
	// 既有编辑面文案）。出现时的归属/词表校验与完整三字段改选由下方健康检查
	// 块在同一事务执行。
	if healthTrioFields := healthCheckTrioPresentFields(input); healthTrioFields != 0 && healthTrioFields != 3 {
		return nil, &ValidationError{Message: "健康检查三件套（healthCheckConnectionPresetCode、healthCheckEndpointMode、healthCheckModel）必须同时出现或同时省略"}
	}
	// M2 域不可变（D3，计划 §服务端统一校验）：body 携带 capabilityDomain 且
	// ≠ 行现值 → 400；不携带 = 无操作。域创建后不可更改，编辑面没有改域路径。
	if input.CapabilityDomainPresent {
		requested := ""
		if input.CapabilityDomain != nil {
			requested = strings.TrimSpace(*input.CapabilityDomain)
		}
		if requested != row.capabilityDomain {
			return nil, &ValidationError{Message: "能力域不可更改"}
		}
	}

	now := s.now()
	nowISO := isoMillis(now)

	// 人工恢复目标状态（BUG-0288）：clearFailureState 是显式人工确认——冷却态
	// 直接恢复调度（与授权实例 patchAuthorizedDispatchTx 的 clearFailure→
	// active+schedulable 同语义），error 进入待检查复检；目标仍受时间计划与
	// 套餐到期约束，由既有状态机统一裁定。媒体域（audio/video）不参与对话
	// 健康检查状态机（无后台探针复检），error 人工恢复直达 active——落
	// pending_test 会因永远等不到探针而滞留。
	clearFailureRestoreTarget := ""
	if input.ClearFailureState {
		switch row.status {
		case "error":
			if accountscore.SkipsConversationHealthStateMachine(row.capabilityDomain) {
				clearFailureRestoreTarget = "active"
			} else {
				clearFailureRestoreTarget = "pending_test"
			}
		case "temporary_unavailable", "rate_limited":
			clearFailureRestoreTarget = "active"
		}
	}
	// 时间计划门（复用 ForceActivatePending 同款 m11ScheduleAllowed 判定：
	// 无计划/解析失败视为允许）：恢复到 active 仅在行内可用时间计划当前
	// 生效时允许，计划不生效时落停用。
	clearFailureScheduleBlocksActive := clearFailureRestoreTarget == "active" &&
		!m11ScheduleAllowed(row.availabilitySchedule.String, now)

	changes := []PatchChange{}
	addChange := func(field string, before, after any) {
		changes = append(changes, PatchChange{Field: field, Before: before, After: after})
	}
	sets := []string{}
	setArgs := []any{}

	// Health-relevant change tracking (Node account-management-patch.repository.ts
	// :356-360, 575-582, 677-694): a connection change (proxy switch, base URL
	// change or API Key pool membership rotation) resets the persisted health
	// projection; credentials/proxy/supported-models/model-mappings/health
	// check config changes clear next_health_check_at and the route dispatches
	// the post-commit configuration probe.
	credentialsChanged := false
	endpointModesChanged := false
	// balanceIdentityChangedFlag 供提交后的余额快照清理端口使用（缺口 2）。
	balanceIdentityChangedFlag := false
	// 连接面拆分（归档 :556-579）：connectionChanged 在凭据/代理两段之后按
	// proxyChanged || baseURLChanged || (apiKeyMembershipChanged &&
	// !retainedActiveAPIKey) 组装，见下方状态机分支。
	proxyChanged := false
	baseURLChanged := false
	apiKeyMembershipChanged := false
	retainedActiveAPIKey := false
	connectionChanged := false
	supportedModelsChanged := false
	modelMappingsChanged := false
	healthCheckModelChanged := false
	healthCheckEndpointModeChanged := false

	// 运行态列族单赋值通道：Node 的 mainColumns Map 对同一列后写覆盖先写
	// （runtime-state 归一化先落，bounded recovery 臂随后覆盖）。Go 的
	// sets 是平铺列表，这里用 order+map 复刻"同列仅保留最后一次赋值"。
	runtimeColumnClauses := map[string]string{}
	runtimeColumnArgs := map[string][]any{}
	runtimeColumnOrder := []string{}
	setRuntimeColumn := func(column string, value any) {
		if _, seen := runtimeColumnClauses[column]; !seen {
			runtimeColumnOrder = append(runtimeColumnOrder, column)
		}
		if value == nil {
			runtimeColumnClauses[column] = column + " = NULL"
			runtimeColumnArgs[column] = nil
			return
		}
		runtimeColumnClauses[column] = column + " = ?"
		runtimeColumnArgs[column] = []any{value}
	}

	if input.ClearFailureState {
		addChange("clearFailureState", false, true)
		setRuntimeColumn("last_error_code", nil)
		setRuntimeColumn("last_error_message", nil)
		setRuntimeColumn("last_error_trace_id", nil)
		setRuntimeColumn("cooldown_until", nil)
		setRuntimeColumn("health_check_failure_count", 0)
		setRuntimeColumn("health_check_failure_started_at", nil)
		setRuntimeColumn("cooldown_retest_failure_count", 0)
		setRuntimeColumn("cooldown_retest_observation_started_at", nil)
		// 归档 patchAccountFailureStateInTransaction：观察起点清空时代际同步
		// 清空；悬挂代际会让 jobs direct input 把候选判为 cooldown fence 无效。
		setRuntimeColumn("cooldown_retest_generation", nil)
		setRuntimeColumn("cooldown_retest_last_at", nil)
		setRuntimeColumn("cooldown_retest_last_status_code", nil)
	}

	// 授权实例名称级联的目标名与变更标志（归档 :801-805 的 rename 臂在
	// 事务内读取 row.name !== nextName）。
	nextName := row.name
	nameChanged := false
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return nil, &ValidationError{Message: "账户名称不能为空"}
		}
		if len([]rune(name)) > maxAccountNameLength {
			return nil, &ValidationError{Message: "账户名称不能超过 128 个字符"}
		}
		if name != row.name {
			nextName = name
			nameChanged = true
			addChange("name", row.name, name)
			sets = append(sets, "name = ?")
			setArgs = append(setArgs, name)
		}
	}
	if input.Notes != nil {
		trimmed := strings.TrimSpace(*input.Notes)
		var next sql.NullString
		if trimmed != "" {
			next = sql.NullString{String: trimmed, Valid: true}
		}
		if next.Valid != row.notes.Valid || next.String != row.notes.String {
			addChange("notes", nullPtrString(row.notes), nullPtrString(next))
			sets = append(sets, "notes = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.Status != nil {
		if !accountStatusValues[*input.Status] {
			return nil, &ValidationError{Message: "账户状态无效"}
		}
		// 归档 assertStatusMutationAllowed：编辑面只接受 active/pending_test/
		// disabled、同值改写，以及 active → temporary_unavailable 的人工隔离；
		// 状态列写入在连接组装后的状态机分支统一处理。
		if err := assertStatusMutationAllowed(row.status, *input.Status); err != nil {
			return nil, err
		}
	}
	// T62 深度排查（2026-10-24）：授权实例的调度字段（并发上限/优先级/超级
	// 优先/降级备用）只允许经 authorized-dispatch 专用接口写入分组绑定的
	// local_*（核心功能设计「授权实例」节；前端对应控件已禁用）——通用 PATCH
	// 对实例行写 accounts.* 调度标记违反「实例不写账户级调度标记」契约，且会
	// 经 BUG-0258 臂把实例 local_* 覆盖成实例 accounts 值，这里统一拒绝。
	if row.authorizationID.Valid {
		if input.ConcurrencyLimit != nil || input.Priority != nil || input.SuperPriorityEnabled != nil || input.FallbackEnabled != nil {
			return nil, &ValidationError{Message: "授权实例的调度配置（并发上限/优先级/超级优先/降级备用）请使用授权实例专用接口修改", Code: "authorized_instance_dispatch_field_rejected"}
		}
	}
	if input.ConcurrencyLimit != nil {
		if *input.ConcurrencyLimit < 1 {
			return nil, &ValidationError{Message: "并发限制必须是大于 0 的整数"}
		}
		if *input.ConcurrencyLimit != row.concurrencyLimit {
			addChange("concurrencyLimit", row.concurrencyLimit, *input.ConcurrencyLimit)
			sets = append(sets, "concurrency_limit = ?")
			setArgs = append(setArgs, *input.ConcurrencyLimit)
		}
	}
	if input.Priority != nil {
		if *input.Priority < 0 {
			return nil, &ValidationError{Message: "优先级必须是大于等于 0 的整数"}
		}
		if *input.Priority != row.priority {
			addChange("priority", row.priority, *input.Priority)
			sets = append(sets, "priority = ?")
			setArgs = append(setArgs, *input.Priority)
		}
	}
	if input.SuperPriorityEnabled != nil {
		next := boolInt(*input.SuperPriorityEnabled)
		if next != row.superPriorityEnabled {
			if next == 1 && boolInt(input.FallbackEnabled != nil && *input.FallbackEnabled) == 1 {
				return nil, &ValidationError{Message: "超级优先和降级备用不能同时开启"}
			}
			addChange("superPriorityEnabled", row.superPriorityEnabled == 1, *input.SuperPriorityEnabled)
			sets = append(sets, "super_priority_enabled = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.FallbackEnabled != nil {
		next := boolInt(*input.FallbackEnabled)
		if next != row.fallbackEnabled {
			if next == 1 && boolInt(input.SuperPriorityEnabled != nil && *input.SuperPriorityEnabled) == 1 {
				return nil, &ValidationError{Message: "超级优先和降级备用不能同时开启"}
			}
			addChange("fallbackEnabled", row.fallbackEnabled == 1, *input.FallbackEnabled)
			sets = append(sets, "fallback_enabled = ?")
			setArgs = append(setArgs, next)
		}
	}
	// 调度绑定终值派生（归档 account-management-patch.repository.ts:494-516）：
	// 三个终值取输入或行内现值（Go 编辑面无 Node 的互斥自动让位归一化，
	// 双输入同真已由上方校验拒绝），任一偏离现值即 dispatchBindingChanged；
	// 未换组时驱动下方 group_accounts 绑定列同步（else 分支），换组时作为
	// replaceGroupBinding 的入参（归档 :830-834 传 next 值）。
	nextPriority := row.priority
	if input.Priority != nil {
		nextPriority = *input.Priority
	}
	nextSuperPriority := row.superPriorityEnabled
	if input.SuperPriorityEnabled != nil {
		nextSuperPriority = boolInt(*input.SuperPriorityEnabled)
	}
	nextFallback := row.fallbackEnabled
	if input.FallbackEnabled != nil {
		nextFallback = boolInt(*input.FallbackEnabled)
	}
	dispatchBindingChanged := nextPriority != row.priority ||
		nextSuperPriority != row.superPriorityEnabled ||
		nextFallback != row.fallbackEnabled
	// schedulable 的写入移到状态机分支（归档 :596-600 的 nextSchedulable
	// 归一化：过期/强制关调度状态压为 false，其余状态变化恢复 true）。
	// 归档 :583-590 的状态机输入：过期时间变化、时间计划变化与其派生状态。
	// 无过期输入时 nextExpiresAt 取行内现值（启用守卫按现值判断）。
	nextExpiresAt := row.accountExpiresAt
	expiresAtChanged := false
	var nextSchedule *AvailabilitySchedule
	scheduleChanged := false
	if input.AccountExpiresAtPresent {
		var next sql.NullString
		if input.AccountExpiresAt != nil && strings.TrimSpace(*input.AccountExpiresAt) != "" {
			canonical, valid := canonicalRFC3339(*input.AccountExpiresAt)
			if !valid {
				return nil, &ValidationError{Message: "账户套餐到期时间必须是有效时间字符串"}
			}
			next = sql.NullString{String: canonical, Valid: true}
		}
		nextExpiresAt = next
		if next.Valid != row.accountExpiresAt.Valid || next.String != row.accountExpiresAt.String {
			expiresAtChanged = true
			addChange("accountExpiresAt", nullPtrString(row.accountExpiresAt), nullPtrString(next))
			sets = append(sets, "account_expires_at = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.AvailabilitySchedulePresent {
		schedule, err := NormalizeSchedule(input.AvailabilitySchedule)
		if err != nil {
			return nil, err
		}
		nextSchedule = schedule
		var next sql.NullString
		if raw, ok := ScheduleJSON(schedule); ok {
			next = sql.NullString{String: raw, Valid: true}
		}
		if next.Valid != row.availabilitySchedule.Valid || next.String != row.availabilitySchedule.String {
			scheduleChanged = true
			addChange("availabilitySchedule", parseScheduleOrNull(row.availabilitySchedule), schedule)
			sets = append(sets, "availability_schedule_json = ?", "availability_schedule_next_check_at = ?")
			setArgs = append(setArgs, next, scheduleNextCheckArg(schedule, now))
		}
	}

	// Proxy profile: nullable id tri-state; a switch must resolve to an
	// enabled profile (Node resolveEnabledProxyProfileIdInClient →
	// ProxyProfileUnavailableError). Counts as a connection change.
	if input.ProxyProfileIDPresent {
		var requested *string
		if input.ProxyProfileID != nil {
			trimmed := strings.TrimSpace(*input.ProxyProfileID)
			if trimmed == "" {
				return nil, &ValidationError{Message: "代理配置无效"}
			}
			requested = &trimmed
		}
		current := row.proxyProfileID
		changed := (requested != nil) != current.Valid ||
			(requested != nil && (!current.Valid || *requested != current.String))
		if changed {
			var next sql.NullString
			if requested != nil {
				enabled, err := s.resolveEnabledProxyProfile(ctx, tx, *requested)
				if err != nil {
					return nil, err
				}
				if !enabled {
					return nil, &ValidationError{Message: "代理不存在或已停用，请选择一个已启用的代理"}
				}
				next = sql.NullString{String: *requested, Valid: true}
			}
			proxyChanged = true
			addChange("proxyProfileId", nullPtrString(current), nullPtrString(next))
			sets = append(sets, "proxy_profile_id = ?")
			setArgs = append(setArgs, next)
		}
	}

	// Health check model: must remain inside the supported model set.
	if input.HealthCheckModel != nil || input.HealthCheckEndpointMode != nil || input.SupportedModelsPresent {
		supportedModels := []string{}
		modelRows, err := tx.QueryContext(ctx, s.bind(`SELECT models.upstream_model FROM `+s.table("account_supported_models")+` account_supported_models
			INNER JOIN `+s.table("models")+` models ON models.id = account_supported_models.model_id
			WHERE account_supported_models.account_id = ? ORDER BY models.upstream_model ASC`), row.id)
		if err != nil {
			return nil, err
		}
		for modelRows.Next() {
			var model string
			if err := modelRows.Scan(&model); err != nil {
				modelRows.Close()
				return nil, err
			}
			supportedModels = append(supportedModels, model)
		}
		modelRows.Close()
		if err := modelRows.Err(); err != nil {
			return nil, err
		}
		if input.SupportedModelsPresent {
			next, err := normalizeSupportedModelsInput(anySliceOrNil(input.SupportedModels))
			if err != nil {
				return nil, err
			}
			if err := assertSupportedModelsRequired(next); err != nil {
				return nil, err
			}
			// Node normalizedSupportedModelsForPatch
			// (account-management-patch.repository.ts:1513-1527)：无序集合相等
			// （unorderedStringListEquals）即视为未变化，直接沿用当前集合，不触发
			// 目录校验；支持模型发生变化时才校验 models 表身份归属（目录外拒绝）。
			if !unorderedStringListEqual(supportedModels, next) {
				if err := s.assertAccountSupportedModelsInProviderCatalog(ctx, tx, next, row.providerCode, row.systemAccountID); err != nil {
					return nil, err
				}
				addChange("supportedModels", supportedModels, next)
				if err := s.replaceAccountSupportedModels(ctx, tx, row.id, row.providerCode, next, nowISO); err != nil {
					return nil, err
				}
				supportedModels = next
				supportedModelsChanged = true
			}
		}
		if input.HealthCheckModel != nil {
			next, err := normalizedHealthCheckModel(*input.HealthCheckModel, supportedModels)
			if err != nil {
				return nil, err
			}
			if next != strings.TrimSpace(row.healthCheckModel) {
				addChange("healthCheckModel", strings.TrimSpace(row.healthCheckModel), next)
				sets = append(sets, "health_check_model = ?")
				setArgs = append(setArgs, next)
				healthCheckModelChanged = true
			}
		}
		if input.HealthCheckEndpointMode != nil {
			if !accountHealthCheckEndpointModes[*input.HealthCheckEndpointMode] {
				return nil, &ValidationError{Message: "账户参数无效"}
			}
			if *input.HealthCheckEndpointMode != row.healthCheckEndpointMode {
				addChange("healthCheckEndpointMode", row.healthCheckEndpointMode, *input.HealthCheckEndpointMode)
				sets = append(sets, "health_check_endpoint_mode = ?")
				setArgs = append(setArgs, *input.HealthCheckEndpointMode)
				healthCheckEndpointModeChanged = true
			}
		}
	}

	// 连接集合整替换（B1 断点批，设计 §8）第一阶段：先于 modelMappings 落地，
	// 保证映射写入/校验面对的是最终连接集合。registry/validator 校验
	// （§2.1/§2.2）→ 整替换 upsert（保留存量 preset 的 DB id）→ §5.1 禁用
	// 级联；待删除 preset 登记到 removedPatchConnections，删除终裁在映射整
	// 替换之后（finishAccountConnectionsReplacement，支持同事务改指向）。
	removedPatchConnections := []removedConnection{}
	if input.ConnectionsPresent {
		patchCredentials := Credentials{}
		if err := DecryptJSON(s.secret, row.credentialsEncrypted, &patchCredentials); err != nil {
			return nil, err
		}
		patchVendor, patchDriver := accountVendorIdentity(row.providerCode)
		resolvedPatchConnections, err := resolveConnectionSet(row.capabilityDomain, patchDriver, row.accountType,
			accountOAuthTypeForRegistry(row.accountType, row.oauthType, patchCredentials), input.Connections)
		if err != nil {
			return nil, err
		}
		removed, connectionsChanged, err := s.beginAccountConnectionsReplacement(ctx, tx, row.id, resolvedPatchConnections, nowISO)
		if err != nil {
			return nil, err
		}
		removedPatchConnections = removed
		if connectionsChanged {
			addChange("connections", "连接集合", "已更新")
			connectionChanged = true
			// 既有映射对最终连接/mode 集合重校验（§5.3：映射可执行性依赖
			// connection ∩ model endpoint modes；modelMappings 显式提交时由
			// 下方映射块的整替换校验覆盖，此处只补「仅连接参与」的臂）。
			if !input.ModelMappingsPresent {
				currentMappings, err := s.loadAccountModelMappings(ctx, tx, row.id)
				if err != nil {
					return nil, err
				}
				for _, mapping := range currentMappings {
					if _, _, err := s.resolveEntryMappingTargets(ctx, tx, row.id, patchVendor.String, row.systemAccountID, mapping); err != nil {
						return nil, err
					}
				}
			}
		}
	}

	// Model mappings: schema-validated (endpoint family enums in the body
	// parser), diffed against the persisted rows and replaced in-place
	// (account-management-patch.repository.ts modelMappingsChanged).
	var preWriteModelMappings []ModelMapping
	if input.ModelMappingsPresent {
		currentMappings, err := s.loadAccountModelMappings(ctx, tx, row.id)
		if err != nil {
			return nil, err
		}
		// preWriteModelMappings 承载替换前快照：下方目录/协议池校验的"实际变更"
		// 判定必须对照写前集合（归档 normalizedModelMappingsForPatch 的 current
		// 语义；写后重读恒等于输入，会让变更条件退化为恒 false）。
		preWriteModelMappings = currentMappings
		if !modelMappingsEqual(currentMappings, input.ModelMappings) {
			// T52（BUG-0335 独立复审非阻塞发现）：与 batch/import 链同位的映射
			// target 预校验。account_entry_mappings 复合外键
			// (account_id, target_model_id) → account_supported_models(account_id,
			// model_id)（设计 §5.1，无级联动作）把映射 target 锁进支持模型集合；
			// 缺此预校验时越界 target 在生产 PG 由 23503 兜底成 500。校验基准是
			// 本次 PATCH 生效后的支持模型集合：上方 SupportedModelsPresent 块已在
			// 同一事务先落地新集合，此处同事务读回的即生效集合（未提交支持模型时
			// 读到的就是库中当前集合）；错误族与文案同
			// AssertMappingUpstreamsAllowed（batch.go/import.go 调用点同源）。
			supportedModelNames := []string{}
			supportedModelRows, err := tx.QueryContext(ctx, s.bind(`SELECT models.upstream_model FROM `+s.table("account_supported_models")+` account_supported_models
				INNER JOIN `+s.table("models")+` models ON models.id = account_supported_models.model_id
				WHERE account_supported_models.account_id = ? ORDER BY models.upstream_model ASC`), row.id)
			if err != nil {
				return nil, err
			}
			for supportedModelRows.Next() {
				var name string
				if err := supportedModelRows.Scan(&name); err != nil {
					supportedModelRows.Close()
					return nil, err
				}
				supportedModelNames = append(supportedModelNames, name)
			}
			supportedModelRows.Close()
			if err := supportedModelRows.Err(); err != nil {
				return nil, err
			}
			if err := assertMappingUpstreamsAllowed(input.ModelMappings, supportedModelNames); err != nil {
				return nil, err
			}
			addChange("modelMappings", currentMappings, input.ModelMappings)
			if err := s.replaceAccountModelMappings(ctx, tx, row.id, row.providerCode, input.ModelMappings, nowISO); err != nil {
				return nil, err
			}
			modelMappingsChanged = true
		}
	}

	// 连接集合整替换第二阶段（映射整替换之后）：删除终裁。仍被映射引用 →
	// connection_mapping_conflict；仍被 health-check 引用且未改选 →
	// health_check_connection_conflict（改选 preset 由下方健康检查块解析，
	// healthKeepPreset 即显式入参）。
	healthKeepPreset := ""
	if input.HealthCheckConnectionPresetCode != nil {
		healthKeepPreset = strings.TrimSpace(*input.HealthCheckConnectionPresetCode)
	}
	if err := s.finishAccountConnectionsReplacement(ctx, tx, row.id, removedPatchConnections, healthKeepPreset); err != nil {
		return nil, err
	}

	// Health-check 连接预设（B1 断点批，设计 §8）：显式提交时必须解析到最终
	// 连接集合中一行 enabled 连接；未显式提交但连接集合被整替换时，现值也必须
	// 仍解析到 enabled 行（§2.1：健康检查引用必须在同一事务中改选完整三字段，
	// 否则 health_check_connection_conflict）。
	if input.HealthCheckConnectionPresetCode != nil || input.ConnectionsPresent {
		healthPreset := strings.TrimSpace(row.currentHealthCheckPreset)
		if input.HealthCheckConnectionPresetCode != nil {
			healthPreset = strings.TrimSpace(*input.HealthCheckConnectionPresetCode)
		}
		if healthPreset == "" {
			return nil, &ValidationError{Message: "health_check_connection_conflict: 账户没有健康检查连接"}
		}
		var healthConnectionID string
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("account_connections")+`
			WHERE account_id = ? AND connection_preset_code = ? AND enabled = 1`), row.id, healthPreset).Scan(&healthConnectionID); err != nil {
			return nil, &ValidationError{Message: fmt.Sprintf("health_check_connection_conflict: connection preset %q resolves to no enabled account connection", healthPreset)}
		}
		effectiveMode := row.healthCheckEndpointMode
		if input.HealthCheckEndpointMode != nil {
			effectiveMode = strings.TrimSpace(*input.HealthCheckEndpointMode)
		}
		connectionModes, err := s.entryConnectionEndpointModes(ctx, tx, healthConnectionID)
		if err != nil {
			return nil, err
		}
		if !entryModeSet(connectionModes)[effectiveMode] {
			return nil, &ValidationError{Message: fmt.Sprintf("health_check_connection_conflict: 检查形态 %s 不在健康检查连接已启用 mode 内", effectiveMode)}
		}
		effectiveModel := strings.TrimSpace(row.healthCheckModel)
		if input.HealthCheckModel != nil {
			effectiveModel = strings.TrimSpace(*input.HealthCheckModel)
		}
		supportedModelNames := []string{}
		supportedModelRows, err := tx.QueryContext(ctx, s.bind(`SELECT models.upstream_model FROM `+s.table("account_supported_models")+` account_supported_models
			INNER JOIN `+s.table("models")+` models ON models.id = account_supported_models.model_id
			WHERE account_supported_models.account_id = ? ORDER BY models.upstream_model ASC`), row.id)
		if err != nil {
			return nil, err
		}
		for supportedModelRows.Next() {
			var name string
			if err := supportedModelRows.Scan(&name); err != nil {
				supportedModelRows.Close()
				return nil, err
			}
			supportedModelNames = append(supportedModelNames, name)
		}
		supportedModelRows.Close()
		if err := supportedModelRows.Err(); err != nil {
			return nil, err
		}
		if _, err := normalizedHealthCheckModel(effectiveModel, supportedModelNames); err != nil {
			return nil, err
		}
		patchHealthVendor, _ := accountVendorIdentity(row.providerCode)
		modelID, err := s.resolveEntryModelID(ctx, tx, patchHealthVendor.String, row.systemAccountID, effectiveModel)
		if err != nil {
			return nil, &ValidationError{Message: fmt.Sprintf("health_check_connection_conflict: 检查模型 %s 不在本账户可见支持模型内", effectiveModel)}
		}
		modelModes, err := s.entryModelEndpointModes(ctx, tx, modelID)
		if err != nil {
			return nil, err
		}
		if !entryModeSet(modelModes)[effectiveMode] {
			return nil, &ValidationError{Message: fmt.Sprintf("health_check_connection_conflict: 检查模型 %s 未声明检查形态 %s", effectiveModel, effectiveMode)}
		}
		if healthConnectionID != row.healthCheckConnectionID.String {
			addChange("healthCheckConnectionPresetCode", row.currentHealthCheckPreset, healthPreset)
			sets = append(sets, "health_check_connection_id = ?")
			setArgs = append(setArgs, healthConnectionID)
		}
	}

	// Credentials: editable-key merge into the decrypted record, normalize
	// through the ported normalizeAccountCredentialsForWrite family, then
	// re-seal with fresh fingerprint/mask columns when the deep-equal
	// comparison reports a change (Node account-management-patch.repository.ts
	// :336-360). A base URL change or a Key pool membership rotation counts as
	// a connection change; a supported_endpoint_modes change feeds the health
	// reset below.
	var currentCredentials, nextCredentials Credentials
	haveCredentials := false
	if input.CredentialsPresent {
		current := Credentials{}
		if err := DecryptJSON(s.secret, row.credentialsEncrypted, &current); err != nil {
			return nil, err
		}
		// credentialsPatch 删键（present-null）的按域默认集收敛（M2 严格不变
		// 式）：删键若直通归一化，会落入写侧供应商缺省默认集，而该缺省集对
		// 多个合法组合并不 ⊆ 域允许集（minimax 缺省 audio_speech 对 video 域
		// 越界；volcengine/qwen 缺省 video_get 对 chat/audio 域越界；hybrid
		// 缺省为全词表并集对任意单一域越界），D6 域外遗留兼容废除后曾是静默
		// 击穿口。这里在合并前就地把它替换为 DefaultEndpointModesForDomain 的
		// 按域默认集数组（谓词与下方 allowedModes 计算同源，与新建缺省播种
		// write.go 同一收敛点），后续流程把它当显式数组走严格 ⊆ allowed 校验
		// 与持久化，越界组合自然 400 响亮拒绝。legacy credentials 的显式 null
		// （恢复现值语义）不在此列，键缺省（absent）不重置声明集。
		if input.CredentialsPatch {
			if raw, present := input.Credentials["supported_endpoint_modes"]; present && raw == nil {
				input.Credentials["supported_endpoint_modes"] = anyStrings(accountscore.DefaultEndpointModesForDomain(
					row.capabilityDomain, row.providerCode, row.accountType,
					accountscore.ProtocolPredicate{
						ProviderCode:              row.providerCode,
						ProtocolCode:              row.protocolCode,
						ProtocolVersion:           row.protocolVersion,
						ProviderProtocolProfileID: row.providerProtocolProfileID,
					}))
			}
		}
		// 归档 patchOwnerAccountInTransaction（account-management-patch
		// .repository.ts:338-345）：credentialsPatch 走 null=删键合并，
		// legacy credentials 走 mergeAccountCredentialsForUpdate 预合并
		// （api_key 单 Key 替换清空旧池，BUG-0175 D-164/D-165）。
		requested := input.Credentials
		var next Credentials
		if input.CredentialsPatch {
			next = applyAccountCredentialsPatch(current, requested)
		} else {
			next = mergeAccountCredentialsForUpdate(row.accountType, row.oauthType, current, requested)
		}
		normalized, err := NormalizeAccountCredentialsForWrite(row.accountType, next, &EndpointModeDefaultContext{
			ProviderCode:              row.providerCode,
			AccountType:               row.accountType,
			ClientCompatibility:       row.clientCompatibility,
			ProviderProtocolProfileID: row.providerProtocolProfileID,
			ProtocolCode:              row.protocolCode,
			ProtocolVersion:           row.protocolVersion,
		})
		if err != nil {
			return nil, err
		}
		currentCredentials = current
		haveCredentials = true
		if !credentialsDeepEqual(current, normalized) {
			credentialsChanged = true
			nextCredentials = normalized
			if credentialValueJSONText(current["base_url"]) != credentialValueJSONText(normalized["base_url"]) {
				baseURLChanged = true
			}
			if !accountApiKeyPoolMembershipEqual(current, normalized) {
				apiKeyMembershipChanged = true
			}
			if credentialValueJSONText(current["supported_endpoint_modes"]) != credentialValueJSONText(normalized["supported_endpoint_modes"]) {
				endpointModesChanged = true
			}
			source, err := requiredAccountCredentialSource(row.accountType, normalized)
			if err != nil {
				return nil, err
			}
			sealed, err := EncryptJSON(s.secret, map[string]any(normalized))
			if err != nil {
				return nil, err
			}
			fingerprint := accountCredentialFingerprint(source)
			addChange("credentials", "已设置", "已变更")
			// oauth_type 物理列跟随凭据重封（设计 §2.1：仅 account_type=
			// google_oauth 时非 NULL，三值 = 判定值；读列优先、credentials 回落
			// 兼容存量行）。变化伴随 credentials 变更项披露，不单列 changedField。
			nextOAuthType := nullStringForOAuthType(accountOAuthTypeForRegistry(row.accountType, row.oauthType, normalized))
			sets = append(sets, "credentials_encrypted = ?", "credential_fingerprint = ?", "credential_mask = ?", "oauth_type = ?")
			setArgs = append(setArgs, sealed, fingerprint, MaskSecret(source), nextOAuthType)
		}
	}

	// 严格声明校验（账户能力域不变式，全链路同一规则）：请求显式携带
	// supported_endpoint_modes 数组时，归一化后的最终声明集必须 ⊆ 域允许集
	// （allowed 按库中行现值 capability_domain/provider/type/协议档案现算），
	// 越界一律 400 并列出越界值，不存在任何跨域保留状态。modes 键缺省 =
	// 不重置声明集（合并结果 = 现值，域内行天然通过）；credentialsPatch 删键
	// （null）已在补丁准备处替换为按域默认集数组，到这里按显式数组参与校验
	// ——按域默认集不保证 ⊆ allowed（volcengine chat 缺省 video_get 等越界
	// 组合在此响亮 400，不再静默回写）。旧声明一律服务端读库，不信任客户端
	// 标记（I7）。
	if input.CredentialsPresent && credentialModesExplicitlyDeclared(input) {
		declared := credentialEndpointModes(currentCredentials)
		if credentialsChanged {
			declared = credentialEndpointModes(nextCredentials)
		}
		allowedModes := accountscore.AllowedEndpointModes(row.capabilityDomain, row.providerCode, row.accountType, accountscore.ProtocolPredicate{
			ProviderCode:              row.providerCode,
			ProtocolCode:              row.protocolCode,
			ProtocolVersion:           row.protocolVersion,
			ProviderProtocolProfileID: row.providerProtocolProfileID,
		})
		if outliers := accountscore.OutOfDomainModes(declared, allowedModes); len(outliers) > 0 {
			return nil, accountscore.CapabilityDomainOutlierError(row.capabilityDomain, outliers)
		}
	}

	// Provider model-catalog validations (account-management-patch.repository.ts
	// :667 + normalizedModelMappingsForPatch): the mapping assertions ride the
	// final mapping set when modelMappings are present (with an actual change)
	// or the enabled endpoint modes changed. Assertion failures roll the
	// transaction back, so running it after the satellite writes stays
	// observation-equivalent to the Node pre-write ordering. 请求覆盖两字段的
	// 写入面校验（token 形状 + 协议族值域）在 credentials 参与时由
	// NormalizeAccountCredentialsForWrite 执行（2026-10-18 起不再有目录断言）；
	// credentials 未参与时最终凭据为行内存量、写入时已校验，无需重跑。
	//
	// finalCredentials lazily resolves the effective credential record behind
	// the Gemini compatibility guard and the mapping assertions: the
	// normalized next record when the credentials input changed, the decrypted
	// current record otherwise（归档 nextCredentials 语义：无 credentials 输入
	// 时为行内存量凭据）。
	finalCredentials := Credentials{}
	finalCredentialsResolved := false
	resolveFinalCredentials := func() (Credentials, error) {
		if finalCredentialsResolved {
			return finalCredentials, nil
		}
		if input.CredentialsPresent && credentialsChanged {
			finalCredentials = nextCredentials
		} else if input.CredentialsPresent {
			finalCredentials = currentCredentials
		} else if err := DecryptJSON(s.secret, row.credentialsEncrypted, &finalCredentials); err != nil {
			return nil, err
		}
		finalCredentialsResolved = true
		return finalCredentials, nil
	}
	// 坏信封早检：请求覆盖目录断言在 2026-10-18 移除，但它顺带提供的
	// 「凭据或支持模型参与变更时最终凭据必须可解密」契约在这里显式保留
	// （坏信封仍按 500 报错），避免在凭据不可读的账户上静默改配置。
	if input.CredentialsPresent || input.SupportedModelsPresent {
		if _, err := resolveFinalCredentials(); err != nil {
			return nil, err
		}
	}
	// M2 Gemini 对话域联动（计划 §服务端统一校验）：chat 域 gemini（native
	// 档案）且 modes 参与变更时保持「至少一种对话能力」；媒体域 gemini 不适
	// 用对话能力要求；chat 域其他供应商不新增任何既有校验之外的拒绝（I3）。
	if endpointModesChanged && row.capabilityDomain == accountscore.CapabilityDomainChat {
		patchProfile := protocolPredicateInput{
			providerCode:              row.providerCode,
			protocolCode:              row.protocolCode,
			protocolVersion:           row.protocolVersion,
			providerProtocolProfileID: row.providerProtocolProfileID,
		}
		if isGeminiProtocolProfileOf(patchProfile) {
			finalCreds, err := resolveFinalCredentials()
			if err != nil {
				return nil, err
			}
			if err := assertGeminiEndpointModesCompatible(credentialEndpointModes(finalCreds), row.accountType); err != nil {
				// 断言族返回普通 error；编辑链按 ValidationError 渲染 400，
				// 文案保持断言族原文。
				return nil, &ValidationError{Message: err.Error()}
			}
		}
	}
	mappingValidationSource := []ModelMapping{}
	mappingValidationNeeded := false
	if input.ModelMappingsPresent {
		// 对照写前快照判定"实际变更"（归档 normalizedModelMappingsForPatch：
		// !endpointModesChanged && accountModelMappingsEqual(current, normalized)
		// 早退，变化或端点能力变化才校验）。
		mappingValidationNeeded = len(input.ModelMappings) > 0 &&
			(endpointModesChanged || !modelMappingsEqual(preWriteModelMappings, input.ModelMappings))
		mappingValidationSource = input.ModelMappings
	} else if endpointModesChanged {
		currentMappingsForValidation, err := s.loadAccountModelMappings(ctx, tx, row.id)
		if err != nil {
			return nil, err
		}
		mappingValidationNeeded = len(currentMappingsForValidation) > 0
		mappingValidationSource = currentMappingsForValidation
	}
	if mappingValidationNeeded {
		finalCreds, err := resolveFinalCredentials()
		if err != nil {
			return nil, err
		}
		if err := s.assertAccountModelMappingsInProviderCatalog(ctx, tx, row.providerCode, row.systemAccountID, protocolPredicateInput{
			providerCode:              row.providerCode,
			protocolCode:              row.protocolCode,
			protocolVersion:           row.protocolVersion,
			providerProtocolProfileID: row.providerProtocolProfileID,
			// 归档 protocolProfileFromRow(row) 带 providerProtocolProfileId
			// （account-management-patch.repository.ts:1996-2008），Gemini
			// 档案特判依赖该 id；目录成员判定只用 providerCode + protocolCode
			// + protocolVersion。
		}, mappingValidationSource, credentialEndpointModes(finalCreds)); err != nil {
			return nil, err
		}
	}

	// 连接面组装 + 状态机（归档 account-management-patch.repository.ts
	// :556-637）：connectionChanged = 代理切换 || base_url 变化 || (Key 池
	// 成员轮换且没有保留的活跃 Key)。当连接变化、没有显式 status 输入且调度
	// 后状态不是 disabled 时，账户被推入 pending_test 等待后台健康检查；
	// status/schedulable 随后驱动 nextRuntimeState 归一化（缺口登记的
	// 冷却/错误态归一化分支链）。
	if apiKeyMembershipChanged && !baseURLChanged && !proxyChanged &&
		row.status == "active" && row.schedulable == 1 {
		poolCredentials := currentCredentials
		if credentialsChanged {
			poolCredentials = nextCredentials
		}
		if isAccountAPIKeyPoolIsolationEnabled(row.providerCode, row.connectionDriverCode,
			row.protocolCode, row.protocolVersion, row.accountType, poolCredentials) {
			retained, err := s.hasRetainedActiveAccountAPIKeyState(ctx, tx, row.id,
				currentCredentials, poolCredentials)
			if err != nil {
				return nil, err
			}
			retainedActiveAPIKey = retained
		}
	}
	connectionChanged = proxyChanged || baseURLChanged ||
		(apiKeyMembershipChanged && !retainedActiveAPIKey)

	hasStatusInput := input.Status != nil
	requestedStatus := row.status
	if hasStatusInput {
		requestedStatus = *input.Status
	} else if clearFailureRestoreTarget != "" {
		// 人工恢复目标作为 requestedStatus 参与既有状态机（BUG-0288）；
		// 不经 input.Status 以免触碰混交守卫与 assertStatusMutationAllowed。
		requestedStatus = clearFailureRestoreTarget
	}
	requestedSchedulable := row.schedulable == 1
	if input.Schedulable != nil {
		requestedSchedulable = *input.Schedulable
	}
	expiredByPackage := expiresAtChanged && isAccountExpired(nextExpiresAt.String, now)
	scheduledStatus := requestedStatus
	if expiredByPackage {
		scheduledStatus = "disabled"
	} else if scheduleChanged {
		// 归档 accountStatusForScheduleMutation：active/disabled 请求才会被
		// 时间计划派生状态覆盖，其余状态原样保留。
		if override, ok := ScheduleStatus(nextSchedule, now); ok &&
			(requestedStatus == "active" || requestedStatus == "disabled") {
			scheduledStatus = override
		}
	} else if clearFailureScheduleBlocksActive {
		// 人工恢复仍受时间计划约束：计划当前不生效时落停用（对齐
		// ForceActivatePending）；套餐到期臂优先级不变（上文 expiredByPackage）。
		scheduledStatus = "disabled"
	}
	nextStatus := scheduledStatus
	// 连接变化降级臂按域守卫（媒体账户生命周期定案表）：audio/video 域不参
	// 与对话健康检查状态机，凭据/代理编辑保持 scheduledStatus（active 保持
	// active），不落 pending_test——否则同样因无探针复检而永久滞留。chat/
	// image 域行为不变。
	if connectionChanged && scheduledStatus != "disabled" && !hasStatusInput &&
		!accountscore.SkipsConversationHealthStateMachine(row.capabilityDomain) {
		nextStatus = "pending_test"
	}
	statusChanged := row.status != nextStatus
	nextSchedulable := requestedSchedulable
	if expiredByPackage || (statusChanged && accountStatusForcesSchedulableOff(nextStatus)) {
		nextSchedulable = false
	} else if statusChanged && nextStatus != "disabled" {
		nextSchedulable = true
	}
	explicitActivationRequested := hasStatusInput && requestedStatus == "active" &&
		(row.status != "active" || row.schedulable != 1)
	explicitSchedulingEnableRequested := input.Schedulable != nil && requestedSchedulable &&
		row.schedulable != 1
	enablesAccount := explicitActivationRequested || explicitSchedulingEnableRequested ||
		(row.status != "active" && nextStatus == "active") ||
		(row.schedulable != 1 && nextSchedulable)
	if enablesAccount && isAccountExpired(nextExpiresAt.String, now) {
		return nil, &ValidationError{Message: "账户套餐已到期，不能启用或参与调度"}
	}
	if statusChanged {
		addChange("status", row.status, nextStatus)
		sets = append(sets, "status = ?")
		setArgs = append(setArgs, nextStatus)
	}
	if boolInt(nextSchedulable) != row.schedulable {
		addChange("schedulable", row.schedulable == 1, nextSchedulable)
		sets = append(sets, "schedulable = ?")
		setArgs = append(setArgs, boolInt(nextSchedulable))
	}

	// 运行态归一化（归档 nextRuntimeState :619-637 + :1736-1845）：状态变化、
	// 连接变化或套餐过期都会按目标状态重写冷却/错误/重试观察列族；
	// hasStatusInput 传 statusChanged（与归档一致，同值改写不触发归一化）。
	runtimeStateMayChange := statusChanged || connectionChanged || expiredByPackage
	if runtimeStateMayChange {
		before := patchRuntimeStateBefore{
			status:                       row.status,
			cooldownUntil:                row.cooldownUntil,
			lastErrorCode:                row.lastErrorCode,
			lastErrorMessage:             row.lastErrorMessage,
			lastErrorTraceID:             row.lastErrorTraceID,
			cooldownRetestFailureCount:   row.cooldownRetestCount,
			cooldownRetestObservation:    row.cooldownRetestObservation,
			cooldownRetestGeneration:     row.cooldownRetestGeneration,
			cooldownRetestLastAt:         row.cooldownRetestLastAt,
			cooldownRetestLastStatusCode: row.cooldownRetestLastCode,
		}
		state := s.nextRuntimeState(before, patchRuntimeStateInput{
			nextStatus:        nextStatus,
			hasStatusInput:    statusChanged,
			connectionChanged: connectionChanged,
			expiredByPackage:  expiredByPackage,
			now:               now,
		})
		runtimeColumnsBefore := len(runtimeColumnOrder)
		applyRuntimeStateColumns(setRuntimeColumn, before, state)
		// 恢复语义（BUG-0288）：error → pending_test 时归一化会写入“账户配置
		// 已保存，等待后台检查”，对人工恢复不准确——恢复即清错误标记，同列
		// 后写覆盖先写（runtimeColumn 单赋值通道）。
		if input.ClearFailureState && nextStatus == "pending_test" {
			setRuntimeColumn("last_error_message", nil)
		}
		// 归档 :630-636：派生列确实变化且 status/schedulable 都没变时，
		// 以 runtimeState 变更项披露这次归一化。
		if len(runtimeColumnOrder) > runtimeColumnsBefore &&
			!changesHaveField(changes, "status") && !changesHaveField(changes, "schedulable") {
			addChange("runtimeState", "需归一化", "已归一化")
		}
	}

	// Temporary-unavailable continuous probe switch (Node
	// :643-694): absent keeps the current flag; turning it off while the
	// account sits in temporary_unavailable arms the bounded recovery window.
	probeSwitchNext := 0
	probeSwitchChanged := false
	boundedRecoveryActivated := false
	boundedRecoveryGeneration := ""
	if input.TemporaryUnavailableContinuousProbeEnabled != nil {
		next := 0
		if *input.TemporaryUnavailableContinuousProbeEnabled {
			next = 1
		}
		if next != row.temporaryProbeEnabled {
			probeSwitchChanged = true
			probeSwitchNext = next
			// Node boundedRecoveryActivated = current && !next（与来源账户
			// 自身状态无关；实例 UPDATE 的 CASE WHEN 按每行自己的 status 判定）。
			boundedRecoveryActivated = row.temporaryProbeEnabled == 1 && next == 0
			boundedRecoveryGeneration = ""
			if boundedRecoveryActivated {
				boundedRecoveryGeneration = newCooldownGeneration()
			}
			addChange("temporaryUnavailableContinuousProbeEnabled", row.temporaryProbeEnabled == 1, *input.TemporaryUnavailableContinuousProbeEnabled)
			sets = append(sets, "temporary_unavailable_continuous_probe_enabled = ?")
			setArgs = append(setArgs, next)
			if row.temporaryProbeEnabled == 1 && next == 0 && row.status == "temporary_unavailable" {
				// 热修（归档 account-management-patch.repository.ts
				// boundedRecoveryActivated / restartBoundedRecoveryObservation）：
				// bounded recovery 观察窗口重启时写入新生成代际，替代旧的
				// 无条件 NULL——丢失代际会让 jobs 侧冷却恢复候选无法通过
				// fence 认领，恢复任务无法续跑。经 setRuntimeColumn 落列，
				// 覆盖同请求里 runtime-state 归一化先写的同名列（Node Map
				// 后写覆盖语义）。
				setRuntimeColumn("cooldown_retest_failure_count", 0)
				setRuntimeColumn("cooldown_retest_observation_started_at", nowISO)
				setRuntimeColumn("cooldown_retest_generation", boundedRecoveryGeneration)
				setRuntimeColumn("cooldown_retest_last_at", nil)
				setRuntimeColumn("cooldown_retest_last_status_code", nil)
				if cooldown := initialCooldownUntilForStatus("temporary_unavailable", now); cooldown != "" {
					setRuntimeColumn("cooldown_until", cooldown)
				}
			}
		}
	}

	// 特供快速恢复开关（AI账户特供快速恢复通道设计 §8.1/§9）：false→true 的
	// 翻转在同一补丁事务内先锁归属行（FOR UPDATE）再
	// 计数校验，超限整体回滚（ValidationError → 400）；置 false 与幂等 true
	// （本已为特供）不校验、不重复占额。写入随既有无条件 config_revision+1
	// 与 updated_at；特供消费方是 jobs 恢复道，不进
	// accountPatchGatewayRuntimeFields（§8.4）。
	if input.ExpeditedRecoveryEnabled != nil {
		next := 0
		if *input.ExpeditedRecoveryEnabled {
			next = 1
		}
		if next != row.expeditedRecoveryEnabled {
			if next == 1 {
				// 待翻转行当前计数为 0（尚未特供），翻转后归属名下 +1。
				if err := s.assertExpeditedRecoveryLimit(ctx, tx, row.systemAccountID, 1); err != nil {
					return nil, err
				}
			}
			addChange("expeditedRecoveryEnabled", row.expeditedRecoveryEnabled == 1, *input.ExpeditedRecoveryEnabled)
			sets = append(sets, "expedited_recovery_enabled = ?")
			setArgs = append(setArgs, next)
		}
	}

	// Balance query (Node :712-773): any balance-relevant change revalidates
	// the capability boundary, writes the enabled flag plus the normalized
	// config and refreshes the next-refresh generation when the balance
	// identity changed. balanceRelevant（归档 :712-717）还包括代理切换
	// （currentProxyProfileId !== nextProxyProfileId → proxyChanged）：代理
	// 属于余额身份的一部分（identity 比较含 proxyProfileId），纯代理切换也要
	// 重验能力边界并把启用账户的 next_refresh_at 提前到当下。Node rides
	// nextCredentials, falling back to the decrypted current credentials
	// (accountManagementPatchNeedsCredentials 含 balance 字段)，所以余额相关
	// 的 PATCH 在无凭据输入时也要解密现凭据，否则启用查询会被"至少一个有效
	// 的 API Key"误拒。
	if input.BalanceQueryEnabled != nil || input.BalanceQueryConfigPresent || credentialsChanged || proxyChanged {
		requestedEnabled := row.balanceQueryEnabled == 1
		if input.BalanceQueryEnabled != nil {
			requestedEnabled = *input.BalanceQueryEnabled
		}
		currentCredentialsForBalance := currentCredentials
		_ = haveCredentials
		if credentialsChanged {
			currentCredentialsForBalance = nextCredentials
		} else if len(currentCredentialsForBalance) == 0 {
			decrypted := Credentials{}
			if err := DecryptJSON(s.secret, row.credentialsEncrypted, &decrypted); err != nil {
				return nil, err
			}
			currentCredentialsForBalance = decrypted
		}
		authorizedInstance := row.authorizationID.Valid && strings.TrimSpace(row.authorizationID.String) != ""
		enabled, err := ValidateAccountBalanceCapability(BalanceCapabilityInput{
			AccountType:        row.accountType,
			Credentials:        currentCredentialsForBalance,
			AuthorizedInstance: authorizedInstance,
		}, requestedEnabled)
		if err != nil {
			return nil, err
		}
		currentBalanceConfig, err := parseStoredBalanceConfig(row.balanceQueryConfigJSON)
		if err != nil {
			return nil, err
		}
		var nextBalanceConfig map[string]any
		if input.BalanceQueryConfigPresent {
			nextBalanceConfig, err = parseStoredBalanceConfig(*input.BalanceQueryConfigCanonical)
			if err != nil {
				return nil, err
			}
		} else {
			nextBalanceConfig = currentBalanceConfig
		}
		if enabled && nextBalanceConfig == nil {
			return nil, &ValidationError{Message: "开启上游余额查询时必须选择查询类型"}
		}
		nextProxyProfileID := row.proxyProfileID.String
		if input.ProxyProfileIDPresent {
			if input.ProxyProfileID == nil {
				nextProxyProfileID = ""
			} else {
				nextProxyProfileID = strings.TrimSpace(*input.ProxyProfileID)
			}
		}
		nextProxyValue := any(nil)
		if nextProxyProfileID != "" {
			nextProxyValue = nextProxyProfileID
		}
		identityChanged := !balanceIdentityEqual(
			s.balanceIdentityValue(row.balanceQueryEnabled == 1, currentBalanceConfig, row.providerCode,
				row.accountType, currentCredentialsForBalance, row.proxyProfileID.String),
			s.balanceIdentityValue(enabled, nextBalanceConfig, row.providerCode,
				row.accountType, currentCredentialsForBalance, nextProxyValue),
		)
		if identityChanged {
			balanceIdentityChangedFlag = true
		}
		if enabled != (row.balanceQueryEnabled == 1) {
			addChange("balanceQueryEnabled", row.balanceQueryEnabled == 1, enabled)
			sets = append(sets, "balance_query_enabled = ?")
			setArgs = append(setArgs, boolInt(enabled))
		}
		if credentialValueJSONText(currentBalanceConfig) != credentialValueJSONText(nextBalanceConfig) {
			addChange("balanceQueryConfig", currentBalanceConfig, nextBalanceConfig)
			raw := "{}"
			if nextBalanceConfig != nil {
				encoded, err := canonicalBalanceConfigJSON(nextBalanceConfig)
				if err != nil {
					return nil, err
				}
				raw = encoded
			}
			sets = append(sets, "balance_query_config_json = ?")
			setArgs = append(setArgs, raw)
		}
		// next refresh generation: a changed identity schedules an immediate
		// refresh, a kept identity preserves the schedule, a disabled query
		// clears it.
		nextRefreshAt := sql.NullString{}
		if enabled {
			if identityChanged {
				nextRefreshAt = sql.NullString{String: nowISO, Valid: true}
			} else if row.balanceQueryNextRefreshAt.Valid {
				nextRefreshAt = row.balanceQueryNextRefreshAt
			}
		}
		if nextRefreshAt.Valid != row.balanceQueryNextRefreshAt.Valid || nextRefreshAt.String != row.balanceQueryNextRefreshAt.String {
			sets = append(sets, "balance_query_next_refresh_at = ?")
			setArgs = append(setArgs, nextRefreshAt)
		}
	}

	// Tags: replace through the shared tag maintenance.
	var savedTags []TagSummary
	if input.TagsPresent {
		tagNames, err := normalizeAccountTagNamesInput(anySliceOrNil(input.Tags))
		if err != nil {
			return nil, err
		}
		currentTags := []TagSummary{}
		tagRows, err := tx.QueryContext(ctx, s.bind(`SELECT account_tags.id, account_tags.name
			FROM `+s.table("account_tag_bindings")+` account_tag_bindings
			INNER JOIN `+s.table("account_tags")+` account_tags
				ON account_tags.id = account_tag_bindings.tag_id
			WHERE account_tag_bindings.account_id = ?
			ORDER BY account_tags.name ASC, account_tags.id ASC`), row.id)
		if err != nil {
			return nil, err
		}
		for tagRows.Next() {
			var tag TagSummary
			if err := tagRows.Scan(&tag.ID, &tag.Name); err != nil {
				tagRows.Close()
				return nil, err
			}
			currentTags = append(currentTags, tag)
		}
		tagRows.Close()
		if err := tagRows.Err(); err != nil {
			return nil, err
		}
		if !tagListsEqual(currentTags, tagNames) {
			addChange("tags", currentTags, tagNames)
			savedTags, err = s.replaceAccountTags(ctx, tx, row.id, row.systemAccountID, tagNames, nowISO)
			if err != nil {
				return nil, err
			}
		}
	}

	// Group binding (Node :701-710, 1589-1603, 1606-1635): the requested
	// group must exist, stay enabled and match the account owner + provider;
	// the enabled binding row is replaced after the CAS update with the
	// account's dispatch fields preserved.
	groupBindingTarget := ""
	groupChanged := false
	if input.GroupIDPresent {
		requested := strings.TrimSpace(*input.GroupID)
		if requested == "" {
			return nil, &ValidationError{Message: "账户分组不能为空"}
		}
		currentGroupID, err := s.loadEnabledGroupBindingID(ctx, tx, row.id, row.systemAccountID)
		if err != nil {
			return nil, err
		}
		if requested != currentGroupID {
			if err := s.assertGroupCanBind(ctx, tx, requested, row.systemAccountID); err != nil {
				return nil, err
			}
			var before any
			if currentGroupID != "" {
				before = currentGroupID
			}
			addChange("groupId", before, requested)
			groupBindingTarget = requested
			groupChanged = true
		}
	}

	// Health state reset (Node :677-694): any health-relevant change clears
	// the next scheduled check; a connection change also wipes the persisted
	// health projection so the next probe starts clean.
	healthCheckRequired := !accountscore.SkipsConversationHealthStateMachine(row.capabilityDomain) &&
		(connectionChanged || supportedModelsChanged || modelMappingsChanged ||
			healthCheckModelChanged || healthCheckEndpointModeChanged || endpointModesChanged)
	if healthCheckRequired {
		sets = append(sets, "next_health_check_at = NULL")
	}
	// 恢复到 pending_test 立即复检（BUG-0288）：清掉既有排期让路由尾部按
	// HealthCheckRequired 调 DispatchAccountHealthCheck 走既有探针 outbox，
	// reason 词表不新增（configuration）。
	clearFailurePendingTest := input.ClearFailureState && nextStatus == "pending_test"
	if clearFailurePendingTest {
		sets = append(sets, "next_health_check_at = NULL")
	}
	if connectionChanged && !accountscore.SkipsConversationHealthStateMachine(row.capabilityDomain) {
		sets = append(sets,
			"last_health_check_at = NULL",
			"last_health_success_at = NULL",
			"health_check_failure_count = 0",
			"health_check_failure_started_at = NULL",
			"last_health_check_status_code = NULL",
			"last_health_check_error_code = NULL",
			"last_health_check_error_message = NULL",
			"last_health_check_trace_id = NULL")
	}

	result := &PatchResult{
		ID:                     row.id,
		ConfigRevision:         row.configRevision,
		ChangedFields:          []string{},
		Name:                   row.name,
		OwnerSystemAccountID:   row.systemAccountID,
		Changes:                changes,
		BalanceIdentityChanged: balanceIdentityChangedFlag,
	}
	if len(changes) == 0 {
		return result, nil
	}
	// 运行态列族在所有写段之后统一并入 SET（Node mainColumns 在 CAS
	// UPDATE 前组装完毕；同列多次赋值只保留最后一次）。
	for _, column := range runtimeColumnOrder {
		sets = append(sets, runtimeColumnClauses[column])
		if runtimeColumnArgs[column] != nil {
			setArgs = append(setArgs, runtimeColumnArgs[column]...)
		}
	}
	// config_revision = config_revision + 1 with the CAS guard re-checked.
	sets = append(sets, "config_revision = config_revision + 1", "updated_at = ?")
	setArgs = append(setArgs, nowISO)
	updateArgs := append(append([]any{}, setArgs...), row.id, row.configRevision)
	exec, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET
		`+strings.Join(sets, ", ")+`
		WHERE id = ? AND config_revision = ? AND deleted_at IS NULL`), updateArgs...)
	if err != nil {
		if duplicate := duplicateAccountNameError(err, row.name); duplicate != nil {
			return nil, duplicate
		}
		return nil, err
	}
	if affected, _ := exec.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: RevisionConflictMessage}
	}
	// 重命名臂（归档 account-management-patch.repository.ts:801-805）：名称
	// 变更在同一事务内刷新来源账户的名称搜索词，并把每个授权实例的名称沿
	// 唯一性阶梯跟进（:1670-1739）。归档侧还把变更实例 id 归入
	// renamedAuthorizationInstanceIds 驱动事后 per-instance lookup 缓存失效
	// ——Go lookup 失效通道是登记过的 no-op hook（invalidation.go），网关
	// 运行时缓存经 finishPatchSideEffects 的来源账户整面失效，无需额外通道。
	if nameChanged {
		if err := s.replaceAccountNameSearchTerms(ctx, tx, row.id, row.systemAccountID, nextName, nowISO); err != nil {
			return nil, err
		}
		if _, err := s.syncAuthorizationInstanceNames(ctx, tx, row.id, nextName, nowISO); err != nil {
			return nil, err
		}
	}
	// 授权实例探活开关传播链（归档 account-management-patch.repository.ts
	// :805-843 continuousProbeChanged 臂）：来源账户翻转
	// temporaryUnavailableContinuousProbeEnabled 时，同一事务内对每个授权
	// 实例（authorization_instance_source_account_id = 来源 id）跟进同款
	// UPDATE——开关值跟随、config_revision +1，且 bounded recovery 激活时
	// 对 temporary_unavailable 实例重置冷却重试观察窗口（CASE WHEN 逐字段
	// 移植；非 temporary_unavailable 实例的冷却列保持原值）。归档侧还将
	// 实例 id 归入 renamedAuthorizationInstanceIds 驱动事后 per-instance
	// lookup 缓存失效——Go lookup 失效通道是登记过的 no-op hook
	// （invalidation.go），网关运行时缓存经来源账户已翻转的
	// gatewayRuntimeAffected 整面失效，无需额外通道。
	if probeSwitchChanged {
		if err := s.propagateProbeSwitchToAuthorizationInstances(ctx, tx, row.id, probeSwitchNext,
			boundedRecoveryActivated, boundedRecoveryGeneration, now, nowISO); err != nil {
			return nil, err
		}
	}
	for _, change := range changes {
		result.ChangedFields = append(result.ChangedFields, change.Field)
	}
	result.ConfigRevision = row.configRevision + 1
	result.Tags = savedTags
	if healthCheckRequired {
		result.HealthCheckRequired = true
		result.HealthCheckReason = "configuration"
	}
	if clearFailurePendingTest {
		result.HealthCheckRequired = true
		result.HealthCheckReason = "configuration"
	}
	result.GroupChanged = groupChanged
	result.DispatchBindingChanged = dispatchBindingChanged
	if groupChanged {
		// 归档 :830-834：换组重建绑定携带调度字段 next 终值（非行内旧值）。
		rebuildPriority, rebuildSuper, rebuildFallback := nextPriority, nextSuperPriority == 1, nextFallback == 1
		if row.authorizationID.Valid {
			// T62 深度排查（2026-10-24）：授权实例换绑时 bind-group 载荷不携带
			// 调度字段，next 终值来自实例 accounts 行（恒为初始 0）——直接重建
			// 会清掉 authorized-dispatch 写入的 local_* 调度编辑。改从既有启用
			// 绑定行保留 local_*（无既有绑定行时落默认 0 值）。
			var localPriority, localSuper, localFallback sql.NullInt64
			err := tx.QueryRowContext(ctx, s.bind(`SELECT local_priority, local_super_priority_enabled, local_fallback_enabled
				FROM `+s.table("group_accounts")+` WHERE account_id = ? AND system_account_id = ? AND enabled = 1
				ORDER BY created_at ASC LIMIT 1`), row.id, row.systemAccountID).Scan(&localPriority, &localSuper, &localFallback)
			if err == nil {
				if localPriority.Valid {
					rebuildPriority = int(localPriority.Int64)
				}
				rebuildSuper = localSuper.Int64 == 1
				rebuildFallback = localFallback.Int64 == 1
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		if err := s.replaceGroupBinding(ctx, tx, row.id, row.systemAccountID, row.authorizationID,
			groupBindingTarget, rebuildPriority, rebuildSuper, rebuildFallback, nowISO); err != nil {
			return nil, err
		}
	} else if dispatchBindingChanged {
		// 归档 :835-845 的 else-if 臂（BUG-0258）：未换组但调度字段终值变化时，
		// 同一事务内同步启用绑定行的 local_* 快照——网关调度候选排序只读分组
		// 绑定级字段（chain_accounts.go ORDER BY / chain_accounts_secret.go 合成），
		// 只写 accounts 不同步绑定会让排序永远按创建时旧值。
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("group_accounts")+` SET
			local_priority = ?,
			local_super_priority_enabled = ?,
			local_fallback_enabled = ?,
			updated_at = ?
			WHERE account_id = ?
				AND system_account_id = ?
				AND enabled = 1`),
			nextPriority, nextSuperPriority, nextFallback, nowISO, row.id, row.systemAccountID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// 余额快照旧代次清理（归档 accounts.routes.ts:355-364：balanceIdentityChanged
	// 时调用 cleanupAccountBalanceSnapshotAfterSave）。Node 的
	// balanceAutoDisabledForMultipleApiKeys 在归档 validateAccountBalanceCapability
	// 里恒为 false，reason 恒为 balance_configuration_changed；端口未装配
	// （nil）时静默跳过，保持本包自包含。
	if result.BalanceIdentityChanged && s.balanceSnapshotCleaner != nil {
		s.balanceSnapshotCleaner.CleanupBalanceSnapshotAfterSave(BalanceSnapshotCleanupRequest{
			AccountID:      result.ID,
			ConfigRevision: result.ConfigRevision,
			Reason:         BalanceSnapshotCleanupReasonConfigurationChanged,
		})
	}
	// Post-commit invalidation (T2 audit; Node
	// account-management-patch.repository.ts:1877-1896): conditional lookup
	// flush + gateway runtime invalidation, best-effort.
	s.finishPatchSideEffects(ctx, result)
	return result, nil
}

// propagateProbeSwitchToAuthorizationInstances mirrors the Node
// continuousProbeChanged arm (:805-843): lock the authorization instances of
// the source account (Postgres FOR UPDATE), then fan the switch across them
// in the same transaction — flag follows the source, config_revision bumps,
// and an activated bounded recovery window resets the cooldown-retry
// observation fields for temporary_unavailable instances only (CASE WHEN
// keeps other statuses untouched).
func (s *Store) propagateProbeSwitchToAuthorizationInstances(ctx context.Context, tx *sql.Tx, sourceAccountID string, next int, activated bool, generation string, now time.Time, nowISO string) error {
	lockSuffix := " FOR UPDATE"
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("accounts")+`
		WHERE authorization_instance_source_account_id = ?
			AND deleted_at IS NULL
		ORDER BY id ASC`+lockSuffix), sourceAccountID)
	if err != nil {
		return err
	}
	instanceIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		instanceIDs = append(instanceIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(instanceIDs) == 0 {
		return nil
	}
	activatedFlag := 0
	if activated {
		activatedFlag = 1
	}
	observationStarted := any(nil)
	if activated {
		observationStarted = nowISO
	}
	cooldownGeneration := any(nil)
	if activated && generation != "" {
		cooldownGeneration = generation
	}
	cooldownUntil := any(nil)
	if activated {
		if cooldown := initialCooldownUntilForStatus("temporary_unavailable", now); cooldown != "" {
			cooldownUntil = cooldown
		}
	}
	_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET
		temporary_unavailable_continuous_probe_enabled = ?,
		config_revision = config_revision + 1,
		cooldown_retest_failure_count = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN 0 ELSE cooldown_retest_failure_count END,
		cooldown_retest_observation_started_at = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN ? ELSE cooldown_retest_observation_started_at END,
		cooldown_retest_generation = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN ? ELSE cooldown_retest_generation END,
		cooldown_retest_last_at = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN NULL ELSE cooldown_retest_last_at END,
		cooldown_retest_last_status_code = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN NULL ELSE cooldown_retest_last_status_code END,
		cooldown_until = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN ? ELSE cooldown_until END,
		updated_at = ?
		WHERE authorization_instance_source_account_id = ?
			AND deleted_at IS NULL`),
		next,
		activatedFlag, activatedFlag, observationStarted,
		activatedFlag, cooldownGeneration,
		activatedFlag,
		activatedFlag,
		activatedFlag, cooldownUntil,
		nowISO,
		sourceAccountID)
	return err
}

// syncAuthorizationInstanceNames mirrors syncAuthorizationInstanceNamesInClient
// (account-management-patch.repository.ts:1670-1708): every live
// authorization instance of the renamed source account follows the new source
// name through the uniqueness ladder inside the caller's transaction. The
// changed ids feed the archived per-instance lookup flush, which Go covers
// through the source-account-wide invalidation in finishPatchSideEffects.
func (s *Store) syncAuthorizationInstanceNames(ctx context.Context, tx *sql.Tx, sourceAccountID, sourceName, nowISO string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id, system_account_id, authorization_instance_authorization_id, name
		FROM `+s.table("accounts")+`
		WHERE authorization_instance_source_account_id = ?
			AND deleted_at IS NULL
		ORDER BY created_at ASC, id ASC`), sourceAccountID)
	if err != nil {
		return nil, err
	}
	type instanceRow struct {
		id, systemAccountID, authorizationID, name string
	}
	instances := []instanceRow{}
	for rows.Next() {
		var row instanceRow
		var authorizationID, name sql.NullString
		if err := rows.Scan(&row.id, &row.systemAccountID, &authorizationID, &name); err != nil {
			rows.Close()
			return nil, err
		}
		row.authorizationID = authorizationID.String
		row.name = name.String
		instances = append(instances, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	changed := []string{}
	for _, row := range instances {
		if row.id == "" || row.systemAccountID == "" || row.authorizationID == "" {
			continue
		}
		nextName, err := s.uniqueAuthorizedInstanceName(ctx, tx, sourceName, row.systemAccountID, row.authorizationID, row.id)
		if err != nil {
			return nil, err
		}
		if row.name == nextName {
			continue
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
			SET name = ?, updated_at = ? WHERE id = ?`), nextName, nowISO, row.id); err != nil {
			return nil, err
		}
		if err := s.replaceAccountNameSearchTerms(ctx, tx, row.id, row.systemAccountID, nextName, nowISO); err != nil {
			return nil, err
		}
		changed = append(changed, row.id)
	}
	return changed, nil
}

// uniqueAuthorizedInstanceName mirrors uniqueAuthorizedAccountNameInClient
// (:1710-1739): the trimmed source name (fallback 授权账户), the first six
// characters of the authorization id tail, then the `-<shortId>` and
// `-<shortId>-<n>` candidate ladder capped at 1000 before the timestamp
// fallback.
func (s *Store) uniqueAuthorizedInstanceName(ctx context.Context, q queryer, sourceName, systemAccountID, authorizationID, exceptAccountID string) (string, error) {
	baseName := strings.TrimSpace(sourceName)
	if baseName == "" {
		baseName = "授权账户"
	}
	shortID := authorizationID
	if idx := strings.LastIndex(authorizationID, "_"); idx >= 0 {
		shortID = authorizationID[idx+1:]
	}
	if len(shortID) > 6 {
		shortID = shortID[:6]
	}
	if shortID == "" {
		shortID = authorizationID
		if len(shortID) > 6 {
			shortID = shortID[len(shortID)-6:]
		}
	}
	candidates := []string{baseName, baseName + "-" + shortID}
	for _, candidate := range candidates {
		available, err := s.authorizedInstanceNameAvailable(ctx, q, systemAccountID, candidate, exceptAccountID)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
	}
	for index := 2; index <= 1000; index++ {
		candidate := baseName + "-" + shortID + "-" + strconv.Itoa(index)
		available, err := s.authorizedInstanceNameAvailable(ctx, q, systemAccountID, candidate, exceptAccountID)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
	}
	return baseName + "-" + shortID + "-" + strconv.FormatInt(time.Now().UnixMilli(), 10), nil
}

// authorizedInstanceNameAvailable mirrors the :1719-1727 availability probe
// against the partial unique index idx_accounts_owner_name_unique
// (system_account_id, name) WHERE deleted_at IS NULL.
func (s *Store) authorizedInstanceNameAvailable(ctx context.Context, q queryer, systemAccountID, name, exceptAccountID string) (bool, error) {
	var id string
	err := q.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("accounts")+`
		WHERE system_account_id = ? AND name = ? AND id <> ? AND deleted_at IS NULL
		LIMIT 1`), systemAccountID, name, exceptAccountID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func scheduleNextCheckArg(schedule *AvailabilitySchedule, now time.Time) sql.NullString {
	if raw, ok := NextScheduleCheckAt(schedule, now); ok {
		return sql.NullString{String: raw, Valid: true}
	}
	return sql.NullString{}
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func tagListsEqual(tags []TagSummary, names []string) bool {
	if len(tags) != len(names) {
		return false
	}
	for index := range tags {
		if tags[index].Name != names[index] {
			return false
		}
	}
	return true
}

// loadAccountModelMappings mirrors loadModelMappingsInClient: the persisted
// mapping rows projected in the AccountModelMapping shape（B4 W2：读
// account_entry_mappings 目标形态——upstream 名经 target_model_id JOIN
// models.upstream_model，连接只携带账户内自然键 preset code，§8）。
func (s *Store) loadAccountModelMappings(ctx context.Context, q queryer, accountID string) ([]ModelMapping, error) {
	rows, err := q.QueryContext(ctx, s.bind(`SELECT account_entry_mappings.source_model, account_entry_mappings.source_endpoint_family,
			models.upstream_model, account_entry_mappings.target_endpoint_family, account_entry_mappings.enabled,
			account_connections.connection_preset_code
		FROM `+s.table("account_entry_mappings")+` account_entry_mappings
		INNER JOIN `+s.table("models")+` models ON models.id = account_entry_mappings.target_model_id
		INNER JOIN `+s.table("account_connections")+` account_connections
			ON account_connections.id = account_entry_mappings.target_connection_id
		WHERE account_entry_mappings.account_id = ?
		ORDER BY account_entry_mappings.source_model ASC, account_entry_mappings.source_endpoint_family ASC`), accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	mappings := []ModelMapping{}
	for rows.Next() {
		var mapping ModelMapping
		var enabled int
		if err := rows.Scan(&mapping.SourceModel, &mapping.SourceEndpointFamily,
			&mapping.UpstreamModel, &mapping.UpstreamEndpointFamily, &enabled, &mapping.TargetConnectionPresetCode); err != nil {
			return nil, err
		}
		if enabled == 1 {
			enabledFlag := true
			mapping.Enabled = &enabledFlag
		} else {
			disabledFlag := false
			mapping.Enabled = &disabledFlag
		}
		mappings = append(mappings, mapping)
	}
	return mappings, rows.Err()
}

// accountApiKeyPoolMembershipEqual mirrors accountApiKeyFingerprintSetsEqual:
// the effective (trimmed, deduplicated) Key pools must match.
func accountApiKeyPoolMembershipEqual(left, right Credentials) bool {
	leftKeys := EffectiveAccountApiKeys(left)
	rightKeys := EffectiveAccountApiKeys(right)
	if len(leftKeys) != len(rightKeys) {
		return false
	}
	seen := map[string]bool{}
	for _, key := range leftKeys {
		seen[key] = true
	}
	for _, key := range rightKeys {
		if !seen[key] {
			return false
		}
	}
	return true
}

// parseStoredBalanceConfig decodes a stored/canonical balance config JSON
// payload; "{}" and empty text decode to nil (the Node parseBalanceConfig
// fallback for a disabled config).
func parseStoredBalanceConfig(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return nil, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, nil
	}
	if len(parsed) == 0 {
		return nil, nil
	}
	return parsed, nil
}

// balanceIdentityValue mirrors accountBalanceQueryIdentity: the observable
// balance query identity (enabled flag, normalized config, provider/type,
// effective Key fingerprints, normalized base URL and proxy reference).
func (s *Store) balanceIdentityValue(enabled bool, config map[string]any, providerCode, accountType string, credentials Credentials, proxyProfileID any) map[string]any {
	fingerprints := []any{}
	for _, key := range EffectiveAccountApiKeys(credentials) {
		fingerprints = append(fingerprints, s.balanceAPIKeyFingerprint(key))
	}
	identity := map[string]any{
		"enabled":                     enabled,
		"providerCode":                providerCode,
		"accountType":                 accountType,
		"effectiveApiKeyFingerprints": fingerprints,
	}
	if config != nil {
		identity["normalizedConfig"] = config
	}
	identity["normalizedBaseUrl"] = normalizedBalanceBaseURL(credentials["base_url"])
	if text, ok := proxyProfileID.(string); ok && strings.TrimSpace(text) != "" {
		identity["proxyProfileId"] = strings.TrimSpace(text)
	}
	return identity
}

// balanceIdentityEqual deep-compares two identity records via the canonical
// JSON shapes (Node isDeepStrictEqual).
func balanceIdentityEqual(left, right map[string]any) bool {
	return credentialValueJSONText(left) == credentialValueJSONText(right)
}

// credentialValueJSONText renders a decoded-JSON value's canonical text so
// deep-equality checks compare deterministic JSON text (mirrors
// isDeepStrictEqual over decoded records; plain == would panic on slices).
func credentialValueJSONText(value any) string {
	raw, err := json.Marshal(canonicalizeJSONValue(value))
	if err != nil {
		return "unmarshalable"
	}
	return string(raw)
}

// healthCheckTrioPresentFields 统计健康检查三件套在补丁载荷中的出现字段数
// （§8 同现同省契约的判据）。
func healthCheckTrioPresentFields(input PatchInput) int {
	fields := 0
	if input.HealthCheckConnectionPresetCode != nil {
		fields++
	}
	if input.HealthCheckEndpointMode != nil {
		fields++
	}
	if input.HealthCheckModel != nil {
		fields++
	}
	return fields
}

// patchHasMixedEditField mirrors the archive mixed-commit allowlist
// (account-management-patch.repository.ts:1116-1119): every editable field
// beyond expectedConfigRevision/clearFailureState counts as a mixed commit.
// The third whitelisted key runtimeResetRequireUnlocked is an internal
// runtime-reset fence that never appears on the Go edit surface (the Go
// runtime-reset path does not route through Store.Patch).
func patchHasMixedEditField(input PatchInput) bool {
	return input.Name != nil ||
		input.Notes != nil ||
		input.Status != nil ||
		input.ConcurrencyLimit != nil ||
		input.Priority != nil ||
		input.SuperPriorityEnabled != nil ||
		input.FallbackEnabled != nil ||
		input.Schedulable != nil ||
		input.CredentialsPresent ||
		input.SupportedModelsPresent ||
		input.HealthCheckConnectionPresetCode != nil ||
		input.HealthCheckModel != nil ||
		input.HealthCheckEndpointMode != nil ||
		input.TagsPresent ||
		input.AccountExpiresAtPresent ||
		input.AvailabilitySchedulePresent ||
		input.ModelMappingsPresent ||
		input.ProxyProfileIDPresent ||
		input.GroupIDPresent ||
		input.BalanceQueryEnabled != nil ||
		input.BalanceQueryConfigPresent ||
		input.TemporaryUnavailableContinuousProbeEnabled != nil ||
		input.ExpeditedRecoveryEnabled != nil
}

// initialCooldownUntilForStatus mirrors initialCooldownUntilForStatus
// (account-runtime-mutation-helpers.ts:36-55): temporary_unavailable arms the
// bounded-recovery initial backoff of 3 seconds
// (temporaryUnavailableInitialBackoffSeconds = 3，与冷却重试退避表首项一致：
// 归档 JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_BACKOFF_MS 默认 [3s, 5s, ...] / Go jobs
// opsjobs.CircuitBackoffMS[0] = 3s)。仅被两个 bounded-recovery 臂使用（归档
// :664 来源账户观察窗口重启、:837 授权实例传播 CASE WHEN），两处在归档中都是
// 3 秒初始退避，非 5 分钟标准冷却语义。
func initialCooldownUntilForStatus(status string, now time.Time) string {
	if status == "temporary_unavailable" {
		return isoMillis(now.Add(3 * time.Second))
	}
	return ""
}

// loadEnabledGroupBindingID mirrors loadEnabledGroupIdInClient.
func (s *Store) loadEnabledGroupBindingID(ctx context.Context, q queryer, accountID, systemAccountID string) (string, error) {
	var groupID string
	err := q.QueryRowContext(ctx, s.bind(`SELECT group_id
		FROM `+s.table("group_accounts")+`
		WHERE account_id = ?
			AND system_account_id = ?
			AND enabled = 1
		ORDER BY updated_at DESC, group_id ASC, account_id ASC
		LIMIT 1`), accountID, systemAccountID).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return groupID, nil
}

// assertGroupCanBind mirrors assertGroupCanBindInClient: same owner, still
// enabled — otherwise the route renders 400 账户分组无效（组无 provider 维度，
// 供应商解耦设计 §8）.
func (s *Store) assertGroupCanBind(ctx context.Context, q queryer, groupID, systemAccountID string) error {
	var groupOwner string
	var enabled int64
	var groupKind string
	lockSuffix := " FOR UPDATE"
	err := q.QueryRowContext(ctx, s.bind(`SELECT system_account_id, enabled, group_kind
		FROM `+s.table("groups")+`
		WHERE id = ?`+lockSuffix), groupID).Scan(&groupOwner, &enabled, &groupKind)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Message: "账户分组无效"}
	}
	if err != nil {
		return err
	}
	if groupKind == "super" {
		return &ValidationError{Message: "超级分组不能直接绑定账户", Code: "group_kind_conflict"}
	}
	if groupOwner != systemAccountID || enabled != 1 {
		return &ValidationError{Message: "账户分组无效"}
	}
	return nil
}

// replaceGroupBinding mirrors replaceGroupBindingInClient: delete the enabled
// binding rows for the account, then upsert the target binding with the
// account's dispatch fields preserved (local_priority family).
func (s *Store) replaceGroupBinding(ctx context.Context, q queryer, accountID, systemAccountID string, authorizationID sql.NullString, groupID string, priority int, superPriority, fallback bool, nowISO string) error {
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("group_accounts")+`
		WHERE account_id = ?
			AND system_account_id = ?`), accountID, systemAccountID); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_accounts")+`
		(system_account_id, group_id, account_id, account_authorization_id,
		 local_priority, local_super_priority_enabled, local_fallback_enabled,
		 enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(group_id, account_id) DO UPDATE SET
			system_account_id = excluded.system_account_id,
			account_authorization_id = excluded.account_authorization_id,
			local_priority = excluded.local_priority,
			local_super_priority_enabled = excluded.local_super_priority_enabled,
			local_fallback_enabled = excluded.local_fallback_enabled,
			enabled = 1,
			updated_at = excluded.updated_at`),
		systemAccountID, groupID, accountID, authorizationID,
		priority, boolInt(superPriority), boolInt(fallback), nowISO, nowISO); err != nil {
		return err
	}
	return nil
}
