package circuitstore

// 投影 LoadItems 的账户列表行读面：对照 Node
// storage/account-management-list.repository.ts listAccountManagementItemsPageDirect
// （access={systemAccountId: viewer, role:'user'}，ids 过滤，page=1，
// pageSize=len(ids)，默认 priority 升序）+ accountManagementListItemFromRow +
// listAccountLockStatesAsync（含 DEAD_CONFIRMED → active 恢复写）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
)

// managementRow 是列表行原始列（base item + status seed + 授权实例来源）。
type managementRow struct {
	id                                  string
	configRevision                      sql.NullInt64
	systemAccountID                     string
	systemAccountName                   sql.NullString
	ownerSystemAccountID                string
	ownerSystemAccountName              sql.NullString
	providerCode                        string
	providerName                        string
	providerProtocolProfileID           string
	protocolCode                        string
	protocolVersion                     string
	name                                string
	notes                               sql.NullString
	accountType                         string
	concurrencyLimit                    any
	priority                            any
	superPriorityEnabled                any
	fallbackEnabled                     any
	clientCompatibility                 sql.NullString
	status                              string
	schedulable                         any
	balanceQueryEnabled                 any
	balanceQueryNextRefreshAt           sql.NullString
	availabilityScheduleJSON            sql.NullString
	accountExpiresAt                    sql.NullString
	cooldownUntil                       sql.NullString
	lastErrorCode                       sql.NullString
	lastErrorMessage                    sql.NullString
	lastErrorTraceID                    sql.NullString
	lastHealthCheckAt                   sql.NullString
	nextHealthCheckAt                   sql.NullString
	lastHealthCheckStatusCode           sql.NullInt64
	lastHealthCheckErrorCode            sql.NullString
	lastHealthCheckErrorMessage         sql.NullString
	lastHealthCheckTraceID              sql.NullString
	cooldownRetestLastAt                sql.NullString
	cooldownRetestLastStatusCode        sql.NullInt64
	cooldownRetestFailureCount          any
	cooldownRetestObservationStartedAt  sql.NullString
	healthCheckModel                    string
	healthCheckEndpointMode             string
	lastUsedAt                          sql.NullString
	lastHealthSuccessAt                 sql.NullString
	healthCheckFailureCount             any
	healthCheckFailureStartedAt         sql.NullString
	streamFailureCount                  any
	streamFailureWindowStartedAt        sql.NullString
	authorizationInstanceOwnerID        sql.NullString
	authorizationInstanceSourceID       sql.NullString
	configuredProxyProfileID            sql.NullString
	authorizationID                     sql.NullString
	authorizationStatus                 sql.NullString
	authorizationExpiresAt              sql.NullString
	authorizationLimitsJSON             sql.NullString
	authorizationEffectiveSourceType    sql.NullString
	authorizationEffectiveSourceTeamID  sql.NullString
	authorizationResourceOwnerID        sql.NullString
	sourceProviderCode                  sql.NullString
	sourceProviderProfileID             sql.NullString
	sourceProtocolCode                  sql.NullString
	sourceProtocolVersion               sql.NullString
	sourceType                          sql.NullString
	sourceStatus                        sql.NullString
	sourceSchedulable                   any
	sourceAvailabilityScheduleJSON      sql.NullString
	sourceAccountExpiresAt              sql.NullString
	sourceCooldownUntil                 sql.NullString
	sourceLastErrorCode                 sql.NullString
	sourceLastErrorMessage              sql.NullString
	sourceLastErrorTraceID              sql.NullString
	sourceCooldownRetestLastAt          sql.NullString
	sourceCooldownRetestLastStatusCode  sql.NullInt64
	sourceLastHealthCheckAt             sql.NullString
	sourceNextHealthCheckAt             sql.NullString
	sourceLastHealthCheckStatusCode     sql.NullInt64
	sourceLastHealthCheckErrorCode      sql.NullString
	sourceLastHealthCheckErrorMessage   sql.NullString
	sourceLastHealthCheckTraceID        sql.NullString
	sourceProxyProfileID                sql.NullString
	sourceConcurrencyLimit              any
	sourceClientCompatibility           sql.NullString
	bindingSystemAccountID              sql.NullString
	boundGroupID                        sql.NullString
	boundGroupName                      sql.NullString
	boundGroupAccountAuthorizationID    sql.NullString
	boundGroupLocalPriority             any
	boundGroupLocalSuperPriorityEnabled any
	boundGroupLocalFallbackEnabled      any
	resolvedProxyProfileID              sql.NullString
	proxyProfileName                    sql.NullString
	proxyProfileType                    sql.NullString
	proxyProfileEnabled                 any
	// group_bindings 列在 status seed 面复用（Node 同行双读）。
	seedBindingSystemAccountID sql.NullString
}

type managementPage struct {
	rows []managementRow
}

const managementRowColumns = `
      account_rows.id,
      account_rows.config_revision,
      account_rows.system_account_id,
      account_rows.provider_code,
      providers.name AS provider_name,
      account_rows.provider_protocol_profile_id,
      account_rows.protocol_code,
      account_rows.protocol_version,
      account_rows.name,
      account_rows.notes,
      account_rows.type,
      account_rows.concurrency_limit,
      account_rows.priority,
      account_rows.super_priority_enabled,
      account_rows.fallback_enabled,
      account_rows.client_compatibility,
      account_rows.status,
      account_rows.schedulable,
      account_rows.balance_query_enabled,
      account_rows.balance_query_next_refresh_at,
      account_rows.availability_schedule_json,
      account_rows.account_expires_at,
      account_rows.cooldown_until,
      account_rows.last_error_code,
      account_rows.last_error_message,
      account_rows.last_error_trace_id,
      account_rows.last_health_check_at,
      account_rows.next_health_check_at,
      account_rows.last_health_check_status_code,
      account_rows.last_health_check_error_code,
      account_rows.last_health_check_error_message,
      account_rows.last_health_check_trace_id,
      account_rows.cooldown_retest_last_at,
      account_rows.cooldown_retest_last_status_code,
      account_rows.cooldown_retest_failure_count,
      account_rows.cooldown_retest_observation_started_at,
      account_rows.health_check_model,
      account_rows.health_check_endpoint_mode,
      account_rows.last_used_at,
      account_rows.last_health_success_at,
      account_rows.health_check_failure_count,
      account_rows.health_check_failure_started_at,
      account_rows.stream_failure_count,
      account_rows.stream_failure_window_started_at,
      account_rows.authorization_instance_owner_system_account_id,
      account_rows.authorization_instance_source_account_id,
      account_rows.configured_proxy_profile_id,
      account_rows.authorization_id,
      account_rows.authorization_status,
      account_rows.authorization_expires_at,
      account_rows.authorization_limits_json,
      account_rows.authorization_effective_source_type,
      account_rows.authorization_effective_source_team_id,
      account_rows.authorization_resource_owner_system_account_id,
      account_rows.source_provider_code,
      account_rows.source_provider_protocol_profile_id,
      account_rows.source_protocol_code,
      account_rows.source_protocol_version,
      account_rows.source_type,
      account_rows.source_status,
      account_rows.source_schedulable,
      account_rows.source_availability_schedule_json,
      account_rows.source_account_expires_at,
      account_rows.source_cooldown_until,
      account_rows.source_last_error_code,
      account_rows.source_last_error_message,
      account_rows.source_last_error_trace_id,
      account_rows.source_cooldown_retest_last_at,
      account_rows.source_cooldown_retest_last_status_code,
      account_rows.source_last_health_check_at,
      account_rows.source_next_health_check_at,
      account_rows.source_last_health_check_status_code,
      account_rows.source_last_health_check_error_code,
      account_rows.source_last_health_check_error_message,
      account_rows.source_last_health_check_trace_id,
      account_rows.source_proxy_profile_id,
      account_rows.source_concurrency_limit,
      account_rows.source_client_compatibility,
      COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id) AS system_account_name,
      COALESCE(
        account_rows.authorization_resource_owner_system_account_id,
        account_rows.authorization_instance_owner_system_account_id,
        account_rows.system_account_id
      ) AS owner_system_account_id,
      COALESCE(
        owner_system_accounts.display_name,
        owner_system_accounts.username,
        account_rows.authorization_resource_owner_system_account_id,
        account_rows.authorization_instance_owner_system_account_id,
        account_rows.system_account_id
      ) AS owner_system_account_name,
      group_bindings.system_account_id AS binding_system_account_id,
      group_bindings.group_id AS bound_group_id,
      bound_groups.name AS bound_group_name,
      group_bindings.account_authorization_id AS bound_group_account_authorization_id,
      group_bindings.local_priority AS bound_group_local_priority,
      group_bindings.local_super_priority_enabled AS bound_group_local_super_priority_enabled,
      group_bindings.local_fallback_enabled AS bound_group_local_fallback_enabled,
      proxy_profiles.id AS resolved_proxy_profile_id,
      proxy_profiles.name AS proxy_profile_name,
      proxy_profiles.type AS proxy_profile_type,
      proxy_profiles.enabled AS proxy_profile_enabled
`

// managementAccountRowsCTE 对齐 Node account_rows CTE。
func managementAccountRowsCTE(table func(string) string) string {
	return `
    WITH account_rows AS (
      SELECT
        accounts.id,
        accounts.config_revision,
        accounts.system_account_id,
        accounts.provider_code,
        accounts.provider_protocol_profile_id,
        accounts.protocol_code,
        accounts.protocol_version,
        accounts.name,
        accounts.notes,
        accounts.type,
        accounts.status,
        accounts.concurrency_limit,
        accounts.priority,
        accounts.super_priority_enabled,
        accounts.fallback_enabled,
        accounts.client_compatibility,
        accounts.schedulable,
        accounts.balance_query_enabled,
        accounts.balance_query_next_refresh_at,
        accounts.availability_schedule_json,
        accounts.account_expires_at,
        accounts.cooldown_until,
        accounts.last_error_code,
        accounts.last_error_message,
        accounts.last_error_trace_id,
        accounts.last_health_check_at,
        accounts.next_health_check_at,
        accounts.last_health_check_status_code,
        accounts.last_health_check_error_code,
        accounts.last_health_check_error_message,
        accounts.last_health_check_trace_id,
        accounts.cooldown_retest_last_at,
        accounts.cooldown_retest_last_status_code,
        accounts.cooldown_retest_failure_count,
        accounts.cooldown_retest_observation_started_at,
        accounts.health_check_model,
        accounts.health_check_endpoint_mode,
        accounts.last_used_at,
        accounts.last_health_success_at,
        accounts.health_check_failure_count,
        accounts.health_check_failure_started_at,
        accounts.stream_failure_count,
        accounts.stream_failure_window_started_at,
        accounts.created_at,
        accounts.authorization_instance_owner_system_account_id,
        accounts.authorization_instance_source_account_id,
        accounts.proxy_profile_id AS configured_proxy_profile_id,
        authorizations.id AS authorization_id,
        authorizations.status AS authorization_status,
        authorizations.expires_at AS authorization_expires_at,
        authorizations.limits_json AS authorization_limits_json,
        authorizations.effective_source_type AS authorization_effective_source_type,
        authorizations.effective_source_team_id AS authorization_effective_source_team_id,
        authorizations.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
        source_accounts.provider_code AS source_provider_code,
        source_accounts.provider_protocol_profile_id AS source_provider_protocol_profile_id,
        source_accounts.protocol_code AS source_protocol_code,
        source_accounts.protocol_version AS source_protocol_version,
        source_accounts.type AS source_type,
        source_accounts.status AS source_status,
        source_accounts.schedulable AS source_schedulable,
        source_accounts.availability_schedule_json AS source_availability_schedule_json,
        source_accounts.account_expires_at AS source_account_expires_at,
        source_accounts.cooldown_until AS source_cooldown_until,
        source_accounts.last_error_code AS source_last_error_code,
        source_accounts.last_error_message AS source_last_error_message,
        source_accounts.last_error_trace_id AS source_last_error_trace_id,
        source_accounts.cooldown_retest_last_at AS source_cooldown_retest_last_at,
        source_accounts.cooldown_retest_last_status_code AS source_cooldown_retest_last_status_code,
        source_accounts.last_health_check_at AS source_last_health_check_at,
        source_accounts.next_health_check_at AS source_next_health_check_at,
        source_accounts.last_health_check_status_code AS source_last_health_check_status_code,
        source_accounts.last_health_check_error_code AS source_last_health_check_error_code,
        source_accounts.last_health_check_error_message AS source_last_health_check_error_message,
        source_accounts.last_health_check_trace_id AS source_last_health_check_trace_id,
        source_accounts.proxy_profile_id AS source_proxy_profile_id,
        source_accounts.concurrency_limit AS source_concurrency_limit,
        source_accounts.client_compatibility AS source_client_compatibility
      FROM ` + table("accounts") + ` accounts
      LEFT JOIN ` + table("resource_authorizations") + ` authorizations
        ON authorizations.id = accounts.authorization_instance_authorization_id
      LEFT JOIN ` + table("accounts") + ` source_accounts
        ON source_accounts.id = accounts.authorization_instance_source_account_id
        AND source_accounts.deleted_at IS NULL
      WHERE accounts.deleted_at IS NULL
        AND accounts.system_account_id = ?
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR authorizations.status IN ('active', 'paused', 'expired')
        )
    )`
}

// managementPageSQL 组装完整列表行查询；postgres 用 LATERAL 选绑定，
// SQLite 用窗口 CTE（与 Node 双分支一致）。
func (l *ProjectionItemLoader) managementPageSQL(postgres bool, idCount int) string {
	table := func(name string) string {
		if postgres {
			return "juhe_business." + name
		}
		return name
	}
	groupBindingsJoin := `LEFT JOIN LATERAL (
      SELECT
        group_accounts.system_account_id,
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        group_accounts.local_priority,
        group_accounts.local_super_priority_enabled,
        group_accounts.local_fallback_enabled
      FROM ` + table("group_accounts") + ` group_accounts
      WHERE group_accounts.account_id = account_rows.id
        AND group_accounts.system_account_id = account_rows.system_account_id
        AND group_accounts.enabled = 1
      ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
      LIMIT 1
    ) group_bindings ON true`
	rankedCTE := ""
	if !postgres {
		rankedCTE = `,
    ranked_group_bindings AS (
      SELECT
        group_accounts.account_id,
        group_accounts.system_account_id,
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        group_accounts.local_priority,
        group_accounts.local_super_priority_enabled,
        group_accounts.local_fallback_enabled,
        ROW_NUMBER() OVER (
          PARTITION BY group_accounts.account_id, group_accounts.system_account_id
          ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
        ) AS binding_rank
      FROM ` + table("group_accounts") + ` group_accounts
      WHERE group_accounts.enabled = 1
    )`
		groupBindingsJoin = `LEFT JOIN ranked_group_bindings group_bindings
      ON group_bindings.account_id = account_rows.id
      AND group_bindings.system_account_id = account_rows.system_account_id
      AND group_bindings.binding_rank = 1`
	}
	return managementAccountRowsCTE(table) + rankedCTE + `
    SELECT ` + managementRowColumns + `
    FROM account_rows
    ` + groupBindingsJoin + `
    LEFT JOIN ` + table("groups") + ` bound_groups
      ON bound_groups.id = group_bindings.group_id
    LEFT JOIN ` + table("system_accounts") + ` system_accounts
      ON system_accounts.id = account_rows.system_account_id
    LEFT JOIN ` + table("system_accounts") + ` owner_system_accounts
      ON owner_system_accounts.id = COALESCE(
        account_rows.authorization_resource_owner_system_account_id,
        account_rows.authorization_instance_owner_system_account_id,
        account_rows.system_account_id
      )
    LEFT JOIN ` + table("proxy_profiles") + ` proxy_profiles
      ON proxy_profiles.id = COALESCE(account_rows.source_proxy_profile_id, account_rows.configured_proxy_profile_id)
    LEFT JOIN ` + table("providers") + ` providers
      ON providers.code = COALESCE(account_rows.source_provider_code, account_rows.provider_code)
    WHERE account_rows.id IN (` + placeholdersFor(idCount) + `)
    ORDER BY
      CASE WHEN account_rows.authorization_id IS NOT NULL
        THEN COALESCE(group_bindings.local_priority, account_rows.priority)
        ELSE account_rows.priority END ASC,
      account_rows.created_at ASC,
      account_rows.id ASC`
}

// loadManagementPage 拉取可见范围行（Node listAccountManagementItemsPageDirect
// 的 worker 消费面：ids 过滤、无 keyword/其他过滤器）。
func (l *ProjectionItemLoader) loadManagementPage(ctx context.Context, viewer string, ids []string) (*managementPage, error) {
	query := l.managementPageSQL(l.postgres, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, viewer)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &managementPage{rows: []managementRow{}}
	for rows.Next() {
		row, scanErr := scanManagementRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		page.rows = append(page.rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, convertScanError(err)
	}
	// Node takePageRows：请求 pageSize=len(ids)，越界 id 静默缺失；
	// service 层对缺失 id 抛错（缺行 → loadItems 错误 → 释放重放）。
	return page, nil
}

func scanManagementRow(rows *sql.Rows) (managementRow, error) {
	var row managementRow
	err := rows.Scan(
		&row.id,
		&row.configRevision,
		&row.systemAccountID,
		&row.providerCode,
		&row.providerName,
		&row.providerProtocolProfileID,
		&row.protocolCode,
		&row.protocolVersion,
		&row.name,
		&row.notes,
		&row.accountType,
		&row.concurrencyLimit,
		&row.priority,
		&row.superPriorityEnabled,
		&row.fallbackEnabled,
		&row.clientCompatibility,
		&row.status,
		&row.schedulable,
		&row.balanceQueryEnabled,
		&row.balanceQueryNextRefreshAt,
		&row.availabilityScheduleJSON,
		&row.accountExpiresAt,
		&row.cooldownUntil,
		&row.lastErrorCode,
		&row.lastErrorMessage,
		&row.lastErrorTraceID,
		&row.lastHealthCheckAt,
		&row.nextHealthCheckAt,
		&row.lastHealthCheckStatusCode,
		&row.lastHealthCheckErrorCode,
		&row.lastHealthCheckErrorMessage,
		&row.lastHealthCheckTraceID,
		&row.cooldownRetestLastAt,
		&row.cooldownRetestLastStatusCode,
		&row.cooldownRetestFailureCount,
		&row.cooldownRetestObservationStartedAt,
		&row.healthCheckModel,
		&row.healthCheckEndpointMode,
		&row.lastUsedAt,
		&row.lastHealthSuccessAt,
		&row.healthCheckFailureCount,
		&row.healthCheckFailureStartedAt,
		&row.streamFailureCount,
		&row.streamFailureWindowStartedAt,
		&row.authorizationInstanceOwnerID,
		&row.authorizationInstanceSourceID,
		&row.configuredProxyProfileID,
		&row.authorizationID,
		&row.authorizationStatus,
		&row.authorizationExpiresAt,
		&row.authorizationLimitsJSON,
		&row.authorizationEffectiveSourceType,
		&row.authorizationEffectiveSourceTeamID,
		&row.authorizationResourceOwnerID,
		&row.sourceProviderCode,
		&row.sourceProviderProfileID,
		&row.sourceProtocolCode,
		&row.sourceProtocolVersion,
		&row.sourceType,
		&row.sourceStatus,
		&row.sourceSchedulable,
		&row.sourceAvailabilityScheduleJSON,
		&row.sourceAccountExpiresAt,
		&row.sourceCooldownUntil,
		&row.sourceLastErrorCode,
		&row.sourceLastErrorMessage,
		&row.sourceLastErrorTraceID,
		&row.sourceCooldownRetestLastAt,
		&row.sourceCooldownRetestLastStatusCode,
		&row.sourceLastHealthCheckAt,
		&row.sourceNextHealthCheckAt,
		&row.sourceLastHealthCheckStatusCode,
		&row.sourceLastHealthCheckErrorCode,
		&row.sourceLastHealthCheckErrorMessage,
		&row.sourceLastHealthCheckTraceID,
		&row.sourceProxyProfileID,
		&row.sourceConcurrencyLimit,
		&row.sourceClientCompatibility,
		&row.systemAccountName,
		&row.ownerSystemAccountID,
		&row.ownerSystemAccountName,
		&row.bindingSystemAccountID,
		&row.boundGroupID,
		&row.boundGroupName,
		&row.boundGroupAccountAuthorizationID,
		&row.boundGroupLocalPriority,
		&row.boundGroupLocalSuperPriorityEnabled,
		&row.boundGroupLocalFallbackEnabled,
		&row.resolvedProxyProfileID,
		&row.proxyProfileName,
		&row.proxyProfileType,
		&row.proxyProfileEnabled,
	)
	return row, err
}

// ---- base item 映射（accountManagementListItemFromRow）----

// buildBasePayload 把列表行映射为 AccountManagementListBaseItem 的 JSON 形状
// （role='user'：includeSystemAccountFields=false，无
// systemAccountId/systemAccountName；canAccessAll=false，
// proxyProfileErrorMessage 恒省略）。Node undefined 字段不写入。
func buildBasePayload(row managementRow, tags []map[string]any, lock *accountLockView) map[string]any {
	authorized := row.authorizationID.Valid && strings.TrimSpace(row.authorizationID.String) != ""
	providerCode := row.providerCode
	profileID := row.providerProtocolProfileID
	protocolCode := row.protocolCode
	protocolVersion := row.protocolVersion
	accountType := row.accountType
	concurrencyLimit := numberValue(row.concurrencyLimit)
	if authorized {
		if row.sourceProviderCode.Valid && row.sourceProviderCode.String != "" {
			providerCode = row.sourceProviderCode.String
		}
		if row.sourceProviderProfileID.Valid && row.sourceProviderProfileID.String != "" {
			profileID = row.sourceProviderProfileID.String
		}
		if row.sourceProtocolCode.Valid && row.sourceProtocolCode.String != "" {
			protocolCode = row.sourceProtocolCode.String
		}
		if row.sourceProtocolVersion.Valid && row.sourceProtocolVersion.String != "" {
			protocolVersion = row.sourceProtocolVersion.String
		}
		if row.sourceType.Valid && row.sourceType.String != "" {
			accountType = row.sourceType.String
		}
		if row.sourceConcurrencyLimit != nil {
			concurrencyLimit = numberValue(row.sourceConcurrencyLimit)
		}
	}
	compatibility := normalizeOpenAIAccountClientCompatibility(
		providerCode, accountType,
		clientCompatibilityValue(row, authorized),
		protocolCode, protocolVersion, profileID,
	)
	payload := map[string]any{
		"id":                        row.id,
		"configRevision":            numberValue(row.configRevision, 1),
		"ownerSystemAccountId":      row.ownerSystemAccountID,
		"providerCode":              providerCode,
		"providerName":              row.providerName,
		"providerProtocolProfileId": profileID,
		"protocolCode":              protocolCode,
		"protocolVersion":           protocolVersion,
		"name":                      row.name,
		"type":                      accountType,
		"concurrencyLimit":          concurrencyLimit,
		"clientCompatibility":       compatibility,
		"tags":                      tags,
		"healthCheckModel":          strings.TrimSpace(row.healthCheckModel),
		"healthCheckEndpointMode":   row.healthCheckEndpointMode,
		"accessType":                accessTypeOf(authorized),
		"permissions":               permissionsOf(authorized, row.authorizationEffectiveSourceType),
	}
	// notes?: string | null → undefined 省略。
	if row.notes.Valid && row.notes.String != "" {
		payload["notes"] = row.notes.String
	}
	if row.ownerSystemAccountName.Valid && row.ownerSystemAccountName.String != "" {
		payload["ownerSystemAccountName"] = row.ownerSystemAccountName.String
	}
	// priority/super/fallback：authorized 行取 group local 值。
	if authorized {
		payload["priority"] = numberValue(coalesceAny(row.boundGroupLocalPriority, row.priority))
		payload["superPriorityEnabled"] = booleanValue(row.boundGroupLocalSuperPriorityEnabled)
		payload["fallbackEnabled"] = booleanValue(row.boundGroupLocalFallbackEnabled)
	} else {
		payload["priority"] = numberValue(row.priority)
		payload["superPriorityEnabled"] = booleanValue(row.superPriorityEnabled)
		payload["fallbackEnabled"] = booleanValue(row.fallbackEnabled)
	}
	if proxyProfileID := resolvedProxyProfileID(row, authorized); proxyProfileID != "" {
		payload["proxyProfileId"] = proxyProfileID
	}
	if row.proxyProfileName.Valid && row.proxyProfileName.String != "" {
		payload["proxyProfileName"] = row.proxyProfileName.String
	}
	if proxyType := proxyProfileType(row.proxyProfileType); proxyType != "" {
		payload["proxyProfileType"] = proxyType
	}
	if row.proxyProfileEnabled != nil {
		payload["proxyProfileEnabled"] = booleanValue(row.proxyProfileEnabled)
	}
	proxyUnavailable := proxyUnavailable(row, authorized)
	if proxyUnavailable {
		payload["proxyProfileUnavailable"] = true
		// proxyProfileErrorMessage 仅 canAccessAll 行携带（role='user' 无）。
	}
	if schedule := parseScheduleText(row.availabilityScheduleJSON.String, row.availabilityScheduleJSON.Valid); schedule != nil {
		payload["availabilitySchedule"] = schedule
	}
	if row.boundGroupID.Valid && row.boundGroupID.String != "" {
		payload["boundGroupId"] = row.boundGroupID.String
	}
	if row.boundGroupName.Valid && row.boundGroupName.String != "" {
		payload["boundGroupName"] = row.boundGroupName.String
	}
	if groupBindStatus := groupBindStatusOf(row); groupBindStatus != "" {
		payload["groupBindStatus"] = groupBindStatus
	}
	if authorized && row.boundGroupID.Valid && row.boundGroupID.String != "" &&
		row.bindingSystemAccountID.Valid && row.bindingSystemAccountID.String != "" {
		payload["bindingSystemAccountId"] = row.bindingSystemAccountID.String
	}
	if authorized && row.authorizationID.Valid {
		payload["accountAuthorizationId"] = row.authorizationID.String
	}
	if row.authorizationInstanceSourceID.Valid && row.authorizationInstanceSourceID.String != "" {
		payload["authorizationInstanceSourceAccountId"] = row.authorizationInstanceSourceID.String
	}
	if lock != nil {
		payload["lockEnabled"] = lock.enabled
		if lock.lockState != "" {
			payload["lockState"] = lock.lockState
		}
		if lock.lockDeathTimeoutSeconds != nil {
			payload["lockDeathTimeoutSeconds"] = *lock.lockDeathTimeoutSeconds
		}
		if lock.lockRetryIntervalSeconds != nil {
			payload["lockRetryIntervalSeconds"] = *lock.lockRetryIntervalSeconds
		}
	}
	return payload
}

func clientCompatibilityValue(row managementRow, authorized bool) any {
	value := row.clientCompatibility
	if authorized && row.sourceClientCompatibility.Valid {
		return row.sourceClientCompatibility.String
	}
	if value.Valid {
		return value.String
	}
	return nil
}

func resolvedProxyProfileID(row managementRow, authorized bool) string {
	if authorized {
		if row.sourceProxyProfileID.Valid && row.sourceProxyProfileID.String != "" {
			return row.sourceProxyProfileID.String
		}
		return ""
	}
	if row.configuredProxyProfileID.Valid && row.configuredProxyProfileID.String != "" {
		return row.configuredProxyProfileID.String
	}
	return ""
}

func proxyUnavailable(row managementRow, authorized bool) bool {
	proxyProfileID := resolvedProxyProfileID(row, authorized)
	if proxyProfileID == "" {
		return false
	}
	if !row.resolvedProxyProfileID.Valid || row.resolvedProxyProfileID.String == "" {
		return true
	}
	return !booleanValue(row.proxyProfileEnabled)
}

func proxyProfileType(value sql.NullString) string {
	switch value.String {
	case "http", "https", "socks5", "socks5h":
		return value.String
	}
	return ""
}

func accessTypeOf(authorized bool) string {
	if authorized {
		return "authorized"
	}
	return "owner"
}

func permissionsOf(authorized bool, sourceType sql.NullString) map[string]any {
	if authorized {
		return map[string]any{
			"canUse":                true,
			"canEdit":               false,
			"canDelete":             false,
			"canReturnAuthorization": sourceType.String == "manual",
			"canAuthorize":          false,
			"canViewCredentials":    false,
			"canLock":               true,
		}
	}
	return map[string]any{
		"canUse":                true,
		"canEdit":               true,
		"canDelete":             true,
		"canReturnAuthorization": false,
		"canAuthorize":          true,
		"canViewCredentials":    true,
		"canLock":               true,
	}
}

func groupBindStatusOf(row managementRow) string {
	if !row.boundGroupID.Valid || row.boundGroupID.String == "" {
		return ""
	}
	if row.bindingSystemAccountID.String != row.systemAccountID {
		return ""
	}
	if row.boundGroupAccountAuthorizationID.String != row.authorizationID.String {
		return "authorization_unavailable"
	}
	return "bound"
}

func parseScheduleText(raw string, valid bool) map[string]any {
	if !valid || raw == "" {
		return nil
	}
	schedule, err := oauthrefresh.ParseScheduleJSON(raw)
	if err != nil {
		return nil
	}
	return schedulePayload(schedule)
}

// schedulePayload 把规范化后的计划文档序列化为 payload 形状（Node
// parseAccountAvailabilityScheduleJson 输出对象等价）。
func schedulePayload(schedule *oauthrefresh.AvailabilitySchedule) map[string]any {
	if schedule == nil {
		return nil
	}
	encoded, err := json.Marshal(schedule)
	if err != nil {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil
	}
	return payload
}

// normalizeOpenAIAccountClientCompatibility 对齐 Node 同名函数
// （domain/account-client-compatibility.ts）：gpt vendor + openai 协议 profile
// 时 oauth 固定 codex_responses、否则沿用配置并回退 codex_responses；
// 其余一律 openai_standard。
func normalizeOpenAIAccountClientCompatibility(providerCode, accountType string, configured any, protocolCode, protocolVersion, profileID string) string {
	if normalizeProviderToken(providerCode) == "gpt" && isOpenAIProtocolProfile(protocolCode, protocolVersion) {
		if accountType == "oauth" {
			return "codex_responses"
		}
		if text, ok := configured.(string); ok && isValidClientCompatibility(text) {
			return text
		}
		return "codex_responses"
	}
	return "openai_standard"
}

func isOpenAIProtocolProfile(protocolCode, protocolVersion string) bool {
	return normalizeProviderToken(protocolCode) == "openai" && normalizeProviderToken(protocolVersion) == "v1"
}

func isValidClientCompatibility(value string) bool {
	switch value {
	case "openai_standard", "codex_responses", "anthropic_native", "claude_code":
		return true
	}
	return false
}

// ---- 锁状态（listAccountLockStatesAsync + DEAD_CONFIRMED 恢复写）----

type accountLockView struct {
	enabled                bool
	lockState              string
	lockDeathTimeoutSeconds *int
	lockRetryIntervalSeconds *int
}

// loadAccountLockViews 对齐 listAccountLockStatesAsync：逐 id 查询（含
// active+schedulable 账户的 DEAD_CONFIRMED → LOCKED_IDLE 恢复写，与 Node
// findAccountLockStateAsync 相同的 generation 围栏）。
func (l *ProjectionItemLoader) loadAccountLockViews(ctx context.Context, accountIDs []string) (map[string]*accountLockView, error) {
	output := map[string]*accountLockView{}
	for _, id := range accountIDs {
		lock, recovered, err := l.loadAccountLockView(ctx, id)
		if err != nil {
			return nil, err
		}
		_ = recovered
		if lock != nil {
			output[id] = lock
		}
	}
	return output, nil
}

func (l *ProjectionItemLoader) loadAccountLockView(ctx context.Context, accountID string) (*accountLockView, bool, error) {
	const columns = `enabled, lock_state, lock_death_timeout_seconds, lock_retry_interval_seconds, generation`
	row := l.db.QueryRowContext(ctx, `SELECT `+columns+` FROM `+l.table("account_lock_states")+` WHERE account_id = ?`, accountID)
	var (
		enabled        any
		lockState      string
		deathTimeout   sql.NullInt64
		retryInterval  sql.NullInt64
		generation     sql.NullInt64
	)
	if err := row.Scan(&enabled, &lockState, &deathTimeout, &retryInterval, &generation); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	view := &accountLockView{
		enabled:                booleanValue(enabled),
		lockState:              lockState,
		lockDeathTimeoutSeconds: nullableInt(deathTimeout),
		lockRetryIntervalSeconds: nullableInt(retryInterval),
	}
	if !view.enabled || lockState != "DEAD_CONFIRMED" {
		return view, false, nil
	}
	// Node recovery：账户 active 且 schedulable 时回写 LOCKED_IDLE。
	var (
		status      string
		schedulable any
	)
	accountRow := l.db.QueryRowContext(ctx,
		`SELECT status, schedulable FROM `+l.table("accounts")+` WHERE id = ? AND deleted_at IS NULL`, accountID)
	if err := accountRow.Scan(&status, &schedulable); err != nil {
		if err == sql.ErrNoRows {
			return view, false, nil
		}
		return nil, false, err
	}
	if status != "active" || !booleanValue(schedulable) {
		return view, false, nil
	}
	result, err := l.db.ExecContext(ctx, `
      UPDATE `+l.table("account_lock_states")+`
      SET lock_state = 'LOCKED_IDLE', incident_id = NULL, incident_started_at = NULL, deadline_at = NULL,
          original_status = NULL, provenance = NULL, next_retry_at_ms = NULL, lease_id = NULL,
          lease_until_ms = NULL, updated_at = ?
      WHERE account_id = ? AND lock_state = 'DEAD_CONFIRMED' AND generation = ?`,
		timeParamText(l.postgres, l.now().UTC()), accountID, generation.Int64)
	if err != nil {
		return nil, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if changed == 1 {
		view.lockState = "LOCKED_IDLE"
		return view, true, nil
	}
	// generation 已变：重读一次（Node findAccountLockStateAsync 重入）。
	return l.loadAccountLockView(ctx, accountID)
}

func nullableInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}

// loadAccountTags 对齐 loadAccountTagsByAccountIdsAsync（PG/SQLite 同 SQL，
// order name ASC, id ASC）。
//
// D4 登记（常驻审查第五轮）：此处投影表物化的 tags 形状是 {id,name} 子集，
// Node account-tags.repository.ts 载入五字段（id, system_account_id, name,
// created_at, updated_at）并经 accountTagSummaryFromRow 输出完整
// AccountTagSummary；Go gateway 的账户读取面迁移到该投影表之前，必须先把
// 缺失的三列补进本查询与行形状，否则 gateway 列表 JSON 的 tags 字段会比
// Node 少字段。
func (l *ProjectionItemLoader) loadAccountTags(ctx context.Context, accountIDs []string) (map[string][]map[string]any, error) {
	output := map[string][]map[string]any{}
	if len(accountIDs) == 0 {
		return output, nil
	}
	args := make([]any, 0, len(accountIDs))
	for _, id := range accountIDs {
		args = append(args, id)
	}
	query := `
      SELECT account_tag_bindings.account_id,
        account_tags.id, account_tags.name
      FROM ` + l.table("account_tag_bindings") + ` account_tag_bindings
      INNER JOIN ` + l.table("account_tags") + ` account_tags
        ON account_tags.id = account_tag_bindings.tag_id
      WHERE account_tag_bindings.account_id IN (` + placeholdersFor(len(accountIDs)) + `)
      ORDER BY account_tags.name ASC, account_tags.id ASC`
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, tagID, tagName string
		if err := rows.Scan(&accountID, &tagID, &tagName); err != nil {
			return nil, err
		}
		output[accountID] = append(output[accountID], map[string]any{"id": tagID, "name": tagName})
	}
	return output, rows.Err()
}

func (l *ProjectionItemLoader) table(name string) string {
	if l.postgres {
		return "juhe_business." + name
	}
	return name
}
