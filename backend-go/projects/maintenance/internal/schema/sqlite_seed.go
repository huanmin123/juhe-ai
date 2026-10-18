// Port of Node backend/src/storage/schema/seed-defaults.ts (seedDefaults and
// its private helpers) for the six-database SQLite storage. The SQL statements
// run in the Node call order, converged to the 供应商与上游协议面重构 target
// schema: vendors（9 真实厂商，无 parent_code）、groups（无供应商维度）、
// models（目录快照经 modelSeedTargetRows 投影逐行 upsert，过期行按
// ShutdownDate 写 'disabled'）。provider_protocol_profiles 及其 family 绑定
// seed 曾随旧表退役；2026-10-18 修复批恢复旧 providers / profiles / family
// 绑定种子（迁移窗口并存，逐字取回 dbb2543e6^ 版本，幂等语义不变）——运行时
// accounts 写路径仍以 providers × provider_protocol_profiles JOIN 校验账户
// 创建，fresh 种子库缺行会在创建账户时 400。custom 模型同名清理随
// custom_provider_models 表退役（同名冲突改由 models 身份唯一索引与应用层
// 写事务承接）。
//
// The built-in seed constants (vendors, protocols, endpoint families, groups,
// global/system settings, quota windows, external integration source) are the
// shared pgSeed* tables declared in pg_schema.go and reused here so SQLite and
// PostgreSQL seed exactly the same data.
//
// The pricing catalog comes from model_catalog_data_gen.go (generated from the
// gateway pricing catalog); this file applies the target-form projection
// (modelSeedTargetRows) on top.

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedprefdomain"
)

// SQLiteSeedResult summarizes SeedSQLiteDefaults.
type SQLiteSeedResult struct {
	StatementCount int
	// ModelCatalogRows 是本次 seed 写入 models 表的目录快照行数（全量行，
	// 含 ShutdownDate 已过写 'disabled' 的行）。语义随 models 表收敛从「
	// shutdown 过滤后的可见行数」改为「写入行数」；下游消费方按同源口径
	// 使用该值（bootstrap.SQLiteSeedResult 出口）。
	ModelCatalogRows int
	// BuiltinQuestionRows 是本次 seed 尝试写入的系统内置题库题行数
	//（固定 8 题；冲突时按内置行恢复 seed 权威题面，详见
	// sqSeedBuiltinQuestionUpsert）。
	BuiltinQuestionRows int
	// SchedulingPreferenceMigrationSealed 是本次 seed 触发「全新空库自动写调
	// 度偏好迁移完成记录」的结果（调度偏好独立实体 P2 设计门裁决②；仅记录不
	// 存在且 api_keys 无行时为 true）。
	SchedulingPreferenceMigrationSealed bool
}

// SeedSQLiteDefaults ports Node seedDefaults for one business SQLite
// database. The caller must have applied the business schema; the gateway
// preflight and the maintenance CLI run EnsureSQLiteBusiness first, exactly
// like Node getBusinessDatabase (applyBusinessSchema + seedDefaults).
func SeedSQLiteDefaults(ctx context.Context, db *sql.DB, options SeedOptions) (SQLiteSeedResult, error) {
	now := seedTimestamp(options.nowTime())
	result := SQLiteSeedResult{}
	exec := func(query string, args ...any) error {
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("sqlite seed statement %d: %w", result.StatementCount+1, err)
		}
		result.StatementCount++
		return nil
	}

	// Node: INSERT OR IGNORE INTO system_accounts ... ('sys_admin', 'admin', ...).
	adminPasswordHash, err := hashSeedPassword("admin")
	if err != nil {
		return SQLiteSeedResult{}, fmt.Errorf("hash seed admin password: %w", err)
	}
	// must_change_password = 1: BUG-0224, see the adjudication note on
	// pgSeedSystemAccountsInsert in pg_schema.go (shared contract).
	if err := exec(sqSeedSystemAccountsInsert,
		"sys_admin", "admin", "超级管理员", "系统默认超级管理员账户", "super_admin", "active", adminPasswordHash, 1, 0, now, now); err != nil {
		return SQLiteSeedResult{}, err
	}

	for _, setting := range pgSeedGlobalSettings {
		if err := exec(sqSeedGlobalSettingsInsert, setting.Key, setting.ValueJSON, now); err != nil {
			return SQLiteSeedResult{}, err
		}
	}

	for _, hours := range pgSeedRequestQuotaHourlyWindowHours {
		if err := exec(sqSeedQuotaWindowInsert, hours, now, now); err != nil {
			return SQLiteSeedResult{}, err
		}
	}

	// Node: one database.exec with the three scope-binding backfills.
	if err := exec(sqSeedQuotaScopeBindingsBackfill); err != nil {
		return SQLiteSeedResult{}, err
	}

	for _, provider := range pgSeedProviders {
		if err := exec(sqSeedProviderInsert,
			provider.ID, provider.Code, provider.Name, provider.Description, provider.Enabled, provider.DefaultSupportedModelsJSON, now, now); err != nil {
			return SQLiteSeedResult{}, err
		}
		if err := exec(sqSeedProviderDefaultModelsRepair, provider.DefaultSupportedModelsJSON, now, provider.Code); err != nil {
			return SQLiteSeedResult{}, err
		}
	}

	// 2026-10-18 修复批恢复旧 provider 种子（迁移窗口并存）：运行时 accounts
	// 写路径仍以 providers × provider_protocol_profiles JOIN 校验账户创建，
	// fresh 种子库必须写入旧 providers 行。语句与数据逐字取回 dbb2543e6^
	// 版本，幂等语义不变。
	for _, provider := range pgSeedLegacyProviders {
		if err := exec(sqSeedLegacyProviderInsert,
			provider.ID, provider.Code, provider.Name, provider.Description, pgNullableText(provider.ParentCode), provider.Enabled, provider.DefaultSupportedModelsJSON, now, now); err != nil {
			return SQLiteSeedResult{}, err
		}
		if err := exec(sqSeedLegacyProviderDefaultModelsRepair, provider.DefaultSupportedModelsJSON, now, provider.Code); err != nil {
			return SQLiteSeedResult{}, err
		}
	}

	// Node: strip codex-auto-review from the GPT vendor default model list.
	if err := exec(sqSeedGPTVendorCodexAutoReviewRemoval, now, gptVendorCode); err != nil {
		return SQLiteSeedResult{}, err
	}

	// 内置模型目录：models 逐行 upsert（全量目录行，含 disabled）加 stale
	// builtin disable。
	builtInModelKeys, err := seedSQLiteModelCatalog(ctx, db, options, now, &result)
	if err != nil {
		return SQLiteSeedResult{}, err
	}
	if err := seedSQLiteDisableStaleGeneratedModels(ctx, db, exec, now, builtInModelKeys); err != nil {
		return SQLiteSeedResult{}, err
	}

	// 2026-10-18 修复批：旧 provider_model_catalog 目录种子与新 models 种子
	// 并存（数据同源），供运行时目录断言在迁移窗口内读取；upsert 后按旧顺序
	// 执行六生成商 stale 行停用。
	legacyBuiltInModelKeys, err := seedSQLiteLegacyModelCatalog(ctx, db, options, now, &result)
	if err != nil {
		return SQLiteSeedResult{}, err
	}
	if err := seedSQLiteDisableStaleLegacyGeneratedModels(ctx, db, exec, now, legacyBuiltInModelKeys); err != nil {
		return SQLiteSeedResult{}, err
	}

	// 系统内置题库题（模型检测设计 §5.7 系统内置题）：固定 id 幂等预置。
	if err := seedSQLiteBuiltinModelCheckQuestions(ctx, db, now, &result); err != nil {
		return SQLiteSeedResult{}, err
	}

	// 内置调度偏好（调度偏好独立实体 PLAN-20261012T033000000Z）：固定 id
	// 恢复式幂等预置（与 --ensure-schema 补种臂共用
	// sqSeedBuiltinSchedulingPreferenceUpsert，权威值单源于
	// scheduling_preference.go）。gateway SQLite preflight 的 bootstrap 种子
	// 路径经本函数同步获得该行。
	if err := exec(sqSeedBuiltinSchedulingPreferenceUpsert,
		BuiltinSchedulingPreferenceID, BuiltinSchedulingPreferenceName, now, now); err != nil {
		return SQLiteSeedResult{}, err
	}

	// 全新空库自动写调度偏好迁移完成记录（调度偏好独立实体 P2 设计门裁决②）：
	// 迁移记录不存在且 api_keys 无行（本时点在默认路由策略/Key 预置之前，
	// fresh 库恰为空）时原子写入 completed 记录（counts 全零）；记录已存在或
	// api_keys 非空的库一律跳过（存量库由
	// --migrate-scheduling-preferences 负责，见 internal/schedprefmigration）。
	sealed, err := schedprefdomain.SealFreshInstall(ctx, db, capabilitydomain.DialectSQLite)
	if err != nil {
		return SQLiteSeedResult{}, fmt.Errorf("sqlite seed seal scheduling preferences fresh install: %w", err)
	}
	result.SchedulingPreferenceMigrationSealed = sealed

	for _, protocol := range pgSeedProtocols {
		if err := exec(sqSeedProtocolInsert, protocol.ID, protocol.Code, protocol.Version, protocol.Name, protocol.Description, protocol.Enabled, now, now); err != nil {
			return SQLiteSeedResult{}, err
		}
	}

	for _, family := range pgSeedEndpointFamilies {
		if err := exec(sqSeedEndpointFamilyInsert, family.ID, family.ProtocolCode, family.ProtocolVersion, family.Code, family.Name, family.Description, family.Enabled, now, now); err != nil {
			return SQLiteSeedResult{}, err
		}
	}

	// 2026-10-18 修复批恢复旧 profile 种子（迁移窗口并存）：循环顺序沿旧提交
	// ——protocols/families（profiles 外键父表）之后写 profiles，再做
	// account_types 守卫修复与 family 绑定；profile 行 updated_at 按序号
	// 逐毫秒递增的旧语义保留。
	baseTime := options.nowTime()
	for index, profile := range pgSeedLegacyProfiles {
		profileUpdatedAt := seedTimestamp(baseTime.Add(time.Duration(index) * time.Millisecond))
		if err := exec(sqSeedLegacyProfileInsert,
			profile.ID, profile.ProviderCode, profile.Name, profile.Description, profile.Enabled, profile.ProtocolCode, profile.ProtocolVersion, profile.BaseURL, profile.DefaultHealthCheckModel, seedStringify(profile.AccountTypes), seedStringify(profile.Capabilities), now, profileUpdatedAt); err != nil {
			return SQLiteSeedResult{}, err
		}
	}
	if err := seedSQLiteRepairLegacyProfileAccountTypes(ctx, db, exec, now); err != nil {
		return SQLiteSeedResult{}, err
	}

	for _, profile := range pgSeedLegacyProfiles {
		for _, familyCode := range profile.EndpointFamilies {
			if err := exec(sqSeedLegacyProfileFamilyInsert, profile.ID, familyCode, now, now); err != nil {
				return SQLiteSeedResult{}, err
			}
		}
	}

	if err := seedSQLiteBuiltInGroupsForAllSystemAccounts(exec, now); err != nil {
		return SQLiteSeedResult{}, err
	}
	// The admin chat API key seeding was removed with the 2026-10-07 默认资源
	// 收口 — AI chat sessions provision it on demand (EnsureChatAPIKey).
	if err := seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx, db, exec, options, now); err != nil {
		return SQLiteSeedResult{}, err
	}
	if err := seedSQLiteExternalIntegrationTestToken(ctx, db, exec, options, now); err != nil {
		return SQLiteSeedResult{}, err
	}

	for _, setting := range pgSeedSystemSettings {
		if err := exec(sqSeedSystemSettingInsert, "sys_admin", setting.Key, setting.ValueJSON, now); err != nil {
			return SQLiteSeedResult{}, err
		}
	}

	return result, nil
}

// seedSQLiteRepairLegacyProfileAccountTypes ports
// repairBuiltInProviderProfileAccountTypes: merge the built-in account types
// into the stored profile row when the stored JSON lacks them（2026-10-18
// 修复批随旧 profile 种子恢复，逐字取回 dbb2543e6^ 的
// seedSQLiteRepairProfileAccountTypes）.
func seedSQLiteRepairLegacyProfileAccountTypes(ctx context.Context, db *sql.DB, exec func(string, ...any) error, now string) error {
	for _, profile := range pgSeedLegacyProfiles {
		var accountTypesJSON string
		err := db.QueryRowContext(ctx, sqSeedLegacyProfileAccountTypesSelect, profile.ID).Scan(&accountTypesJSON)
		if err != nil {
			continue // missing row or non-text value mirrors the Node repair skip
		}
		current, err := parseSeedStringArray(accountTypesJSON)
		if err != nil {
			continue
		}
		merged := mergeSeedDistinct(current, profile.AccountTypes)
		if seedStringSlicesEqual(merged, current) {
			continue
		}
		if err := exec(sqSeedLegacyProfileAccountTypesUpdate, seedStringify(merged), now, profile.ID); err != nil {
			return err
		}
	}
	return nil
}

// sqSeedSystemAccountsInsert is the Node default super admin insert.
// must_change_password = 1 (BUG-0224): adjudication note on
// pgSeedSystemAccountsInsert in pg_schema.go covers the shared contract.
const sqSeedSystemAccountsInsert = `
      INSERT OR IGNORE INTO system_accounts (
        id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `

// sqSeedGlobalSettingsInsert seeds one global_settings row.
const sqSeedGlobalSettingsInsert = `
    INSERT OR IGNORE INTO global_settings (key, value_json, updated_at)
    VALUES (?, ?, ?)
  `

// sqSeedQuotaWindowInsert seeds one request_quota_hourly_window_configs row.
const sqSeedQuotaWindowInsert = `
    INSERT OR IGNORE INTO request_quota_hourly_window_configs (window_hours, created_at, updated_at)
    VALUES (?, ?, ?)
  `

// sqSeedQuotaScopeBindingsBackfill backfills the three hourly quota scope
// binding families (Node sends the three INSERT statements in one exec).
const sqSeedQuotaScopeBindingsBackfill = `
    INSERT OR IGNORE INTO request_quota_hourly_window_scope_bindings (
      system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
    )
    SELECT system_account_id, 'api_key', id, 'api_key', id,
      CAST(json_extract(quota_limits_json, '$.hourly.hours') AS INTEGER), created_at, updated_at
    FROM api_keys
    WHERE status = 'active'
      AND quota_limits_json IS NOT NULL
      AND json_valid(quota_limits_json)
      AND json_extract(quota_limits_json, '$.hourly.enabled') = 1
      AND CAST(json_extract(quota_limits_json, '$.hourly.hours') AS INTEGER) BETWEEN 1 AND 720;

    INSERT OR IGNORE INTO request_quota_hourly_window_scope_bindings (
      system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
    )
    SELECT CASE WHEN ra.resource_type = 'account' THEN ra.grantee_system_account_id ELSE ra.resource_owner_system_account_id END,
      CASE WHEN ra.resource_type = 'account' THEN 'account_authorization' ELSE 'group_authorization' END,
      ra.id, 'resource_authorization_grant', grants.id,
      CAST(json_extract(ra.limits_json, '$.hourly.hours') AS INTEGER), ra.created_at, ra.updated_at
    FROM resource_authorizations ra
    INNER JOIN resource_authorization_grants grants
      ON grants.resource_type = ra.resource_type
      AND grants.resource_id = ra.resource_id
      AND grants.status = 'active'
      AND (
        (ra.effective_source_type = 'manual' AND grants.grantee_type = 'system_account' AND grants.grantee_system_account_id = ra.grantee_system_account_id)
        OR
        (ra.effective_source_type = 'team' AND grants.grantee_type = 'team' AND grants.grantee_team_id = ra.effective_source_team_id)
      )
    WHERE ra.status = 'active'
      AND ra.limits_json IS NOT NULL
      AND json_valid(ra.limits_json)
      AND json_extract(ra.limits_json, '$.hourly.enabled') = 1
      AND CAST(json_extract(ra.limits_json, '$.hourly.hours') AS INTEGER) BETWEEN 1 AND 720;

    INSERT OR IGNORE INTO request_quota_hourly_window_scope_bindings (
      system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
    )
    SELECT DISTINCT
      CASE WHEN ra.resource_type = 'account' THEN ra.grantee_system_account_id ELSE ra.resource_owner_system_account_id END,
      CASE WHEN ra.resource_type = 'account' THEN 'account_authorization_team' ELSE 'group_authorization_team' END,
      CASE WHEN ra.resource_type = 'account' THEN instance_accounts.id || ':' || ra.effective_source_team_id ELSE ra.resource_id || ':' || ra.effective_source_team_id END,
      'resource_authorization_grant', grants.id,
      CAST(json_extract(ra.limits_json, '$.hourly.hours') AS INTEGER), ra.created_at, ra.updated_at
    FROM resource_authorizations ra
    INNER JOIN resource_authorization_grants grants
      ON grants.resource_type = ra.resource_type
      AND grants.resource_id = ra.resource_id
      AND grants.grantee_type = 'team'
      AND grants.grantee_team_id = ra.effective_source_team_id
      AND grants.status = 'active'
    LEFT JOIN accounts instance_accounts
      ON ra.resource_type = 'account'
      AND instance_accounts.authorization_instance_authorization_id = ra.id
      AND instance_accounts.system_account_id = ra.grantee_system_account_id
      AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
      AND instance_accounts.deleted_at IS NULL
    WHERE ra.status = 'active'
      AND ra.effective_source_type = 'team'
      AND (ra.resource_type = 'group' OR instance_accounts.id IS NOT NULL)
      AND ra.limits_json IS NOT NULL
      AND json_valid(ra.limits_json)
      AND json_extract(ra.limits_json, '$.hourly.enabled') = 1
      AND CAST(json_extract(ra.limits_json, '$.hourly.hours') AS INTEGER) BETWEEN 1 AND 720;
  `

// sqSeedProviderInsert seeds one vendors row（providers 由 vendors 取代，无
// parent_code 列）.
const sqSeedProviderInsert = `
    INSERT OR IGNORE INTO vendors (
      id, code, name, description, enabled, default_supported_models_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `

// sqSeedProviderDefaultModelsRepair refreshes empty vendor default model lists.
const sqSeedProviderDefaultModelsRepair = `
    UPDATE vendors
    SET default_supported_models_json = ?, updated_at = ?
    WHERE code = ?
      AND (default_supported_models_json IS NULL OR trim(default_supported_models_json) = '' OR default_supported_models_json = '[]')
  `

// sqSeedGPTVendorCodexAutoReviewRemoval drops codex-auto-review from the GPT
// vendor default model list.
const sqSeedGPTVendorCodexAutoReviewRemoval = `
    UPDATE vendors
    SET default_supported_models_json = coalesce((
      SELECT json_group_array(value)
      FROM json_each(vendors.default_supported_models_json)
      WHERE value <> 'codex-auto-review'
    ), '[]'), updated_at = ?
    WHERE code = ?
      AND json_valid(default_supported_models_json)
      AND json_type(default_supported_models_json) = 'array'
  `

// sqSeedModelsUpsert 是 models 表的 manual-override 感知 upsert（目标形态承接
// 旧 provider_model_catalog upsert 语义）；冲突目标与身份唯一索引
// idx_models_identity_unique 的表达式完全一致（COALESCE 空串归一）。
const sqSeedModelsUpsert = `
    INSERT INTO models (
      id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
      catalog_visible, catalog_order, release_date, shutdown_date,
      supported_endpoint_modes_json, supported_service_tiers_json, supported_reasoning_efforts_json,
      default_reasoning_effort, codex_supported_reasoning_levels_json, codex_default_reasoning_level,
      codex_multi_agent_version, context_window_tokens, max_input_tokens, max_output_tokens, max_tokens,
      supports_prompt_caching, source, pricing_notes, capability_notes, notes, created_by, updated_by,
      created_at, updated_at,
      input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m,
      cache_write_1h_usd_per_1m, cache_storage_usd_per_1m_per_hour, service_tier_prices_json,
      long_context_input_token_threshold, long_context_input_token_threshold_inclusive,
      long_context_input_cost_multiplier, long_context_output_cost_multiplier,
      image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
      output_usd_per_image, currency
    ) VALUES (
      ?, ?, ?, ?, 'builtin', NULL, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, NULL, NULL, NULL, NULL, NULL, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      'USD'
    )
    ON CONFLICT (COALESCE(vendor_code, ''), scope, COALESCE(system_account_id, ''), upstream_model) DO UPDATE SET
      status = CASE WHEN models.source IN ('manual-override', 'manual-visibility-override') THEN models.status ELSE excluded.status END,
      public_model = excluded.public_model,
      catalog_visible = CASE
        WHEN models.source IN ('manual-override', 'manual-visibility-override')
        THEN min(models.catalog_visible, excluded.catalog_visible)
        ELSE excluded.catalog_visible
      END,
      catalog_order = excluded.catalog_order,
      release_date = CASE WHEN models.source = 'manual-override' THEN models.release_date ELSE excluded.release_date END,
      shutdown_date = CASE WHEN models.source = 'manual-override' THEN models.shutdown_date ELSE excluded.shutdown_date END,
      supported_endpoint_modes_json = CASE WHEN models.source = 'manual-override' THEN models.supported_endpoint_modes_json ELSE excluded.supported_endpoint_modes_json END,
      supported_service_tiers_json = CASE WHEN models.source = 'manual-override' THEN models.supported_service_tiers_json ELSE excluded.supported_service_tiers_json END,
      supported_reasoning_efforts_json = CASE WHEN models.source = 'manual-override' THEN models.supported_reasoning_efforts_json ELSE excluded.supported_reasoning_efforts_json END,
      default_reasoning_effort = CASE WHEN models.source = 'manual-override' THEN models.default_reasoning_effort ELSE excluded.default_reasoning_effort END,
      codex_supported_reasoning_levels_json = excluded.codex_supported_reasoning_levels_json,
      codex_default_reasoning_level = excluded.codex_default_reasoning_level,
      codex_multi_agent_version = excluded.codex_multi_agent_version,
      context_window_tokens = CASE WHEN models.source = 'manual-override' THEN models.context_window_tokens ELSE excluded.context_window_tokens END,
      max_input_tokens = CASE WHEN models.source = 'manual-override' THEN models.max_input_tokens ELSE excluded.max_input_tokens END,
      max_output_tokens = CASE WHEN models.source = 'manual-override' THEN models.max_output_tokens ELSE excluded.max_output_tokens END,
      max_tokens = excluded.max_tokens,
      input_usd_per_1m = CASE WHEN models.source = 'manual-override' THEN models.input_usd_per_1m ELSE excluded.input_usd_per_1m END,
      output_usd_per_1m = CASE WHEN models.source = 'manual-override' THEN models.output_usd_per_1m ELSE excluded.output_usd_per_1m END,
      cached_input_usd_per_1m = CASE WHEN models.source = 'manual-override' THEN models.cached_input_usd_per_1m ELSE excluded.cached_input_usd_per_1m END,
      cache_write_usd_per_1m = CASE WHEN models.source = 'manual-override' THEN models.cache_write_usd_per_1m ELSE excluded.cache_write_usd_per_1m END,
      cache_write_1h_usd_per_1m = CASE WHEN models.source = 'manual-override' THEN models.cache_write_1h_usd_per_1m ELSE excluded.cache_write_1h_usd_per_1m END,
      cache_storage_usd_per_1m_per_hour = CASE
        WHEN models.source = 'manual-override'
        THEN coalesce(models.cache_storage_usd_per_1m_per_hour, excluded.cache_storage_usd_per_1m_per_hour)
        ELSE excluded.cache_storage_usd_per_1m_per_hour
      END,
      service_tier_prices_json = CASE WHEN models.source = 'manual-override' THEN models.service_tier_prices_json ELSE excluded.service_tier_prices_json END,
      long_context_input_token_threshold = excluded.long_context_input_token_threshold,
      long_context_input_token_threshold_inclusive = excluded.long_context_input_token_threshold_inclusive,
      long_context_input_cost_multiplier = excluded.long_context_input_cost_multiplier,
      long_context_output_cost_multiplier = excluded.long_context_output_cost_multiplier,
      image_input_usd_per_1m = CASE WHEN models.source = 'manual-override' THEN models.image_input_usd_per_1m ELSE excluded.image_input_usd_per_1m END,
      image_output_usd_per_1m = CASE WHEN models.source = 'manual-override' THEN models.image_output_usd_per_1m ELSE excluded.image_output_usd_per_1m END,
      audio_input_usd_per_1m = CASE WHEN models.source = 'manual-override' THEN models.audio_input_usd_per_1m ELSE excluded.audio_input_usd_per_1m END,
      audio_output_usd_per_1m = CASE WHEN models.source = 'manual-override' THEN models.audio_output_usd_per_1m ELSE excluded.audio_output_usd_per_1m END,
      output_usd_per_image = CASE WHEN models.source = 'manual-override' THEN models.output_usd_per_image ELSE excluded.output_usd_per_image END,
      supports_prompt_caching = excluded.supports_prompt_caching,
      source = CASE WHEN models.source IN ('manual-override', 'manual-visibility-override') THEN models.source ELSE excluded.source END,
      updated_at = excluded.updated_at
  `

// sqSeedGeneratedModelRowsSelect lists the builtin catalog rows for the stale
// disable.
const sqSeedGeneratedModelRowsSelect = `
    SELECT id, vendor_code, upstream_model FROM models
    WHERE scope = 'builtin'
  `

// sqSeedStaleGeneratedModelDisable hides one stale generated model.
const sqSeedStaleGeneratedModelDisable = `
    UPDATE models
    SET status = 'disabled', catalog_visible = 0, updated_at = ?
    WHERE id = ?
  `

// 2026-10-18 修复批恢复旧 provider_model_catalog 目录种子（迁移窗口并存）：
// dbb2543e6 把目录种子改写为新 models 表后旧目录表无种子行，运行时目录断言
// （providers.Store.listProviderModelCatalog 读 provider_model_catalog /
// custom_provider_models）在 fresh 种子库判「支持模型不在目录」。以下语句逐
// 字取回 dbb2543e6^ 版本，幂等语义不变（manual-override 感知 ON CONFLICT）；
// 与新 models 种子并存，数据同源 activeModelCatalogSeedRows。种子语句不计
// DDL golden（Ensure* 只数 CREATE TABLE/INDEX）。
const sqSeedLegacyModelCatalogUpsert = `
    INSERT INTO provider_model_catalog (
      id, provider_code, model, status, mode, catalog_order, release_date, shutdown_date,
      supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
      default_reasoning_effort, codex_supported_reasoning_levels_json, codex_default_reasoning_level,
      codex_multi_agent_version, context_window_tokens, max_input_tokens, max_output_tokens, max_tokens,
      input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m,
      cache_write_1h_usd_per_1m, cache_storage_usd_per_1m_per_hour, service_tier_prices_json,
      long_context_input_token_threshold, long_context_input_token_threshold_inclusive,
      long_context_input_cost_multiplier, long_context_output_cost_multiplier,
      image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
      output_usd_per_image, supports_prompt_caching, catalog_visible, source, created_at, updated_at
    ) VALUES (
      ?, ?, ?, 'active',
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?,
      ?, ?, ?, ?
    )
    ON CONFLICT(provider_code, model) DO UPDATE SET
      status = CASE WHEN provider_model_catalog.source IN ('manual-override', 'manual-visibility-override') THEN provider_model_catalog.status ELSE excluded.status END,
      mode = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.mode ELSE excluded.mode END,
      catalog_order = excluded.catalog_order,
      release_date = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.release_date ELSE excluded.release_date END,
      shutdown_date = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.shutdown_date ELSE excluded.shutdown_date END,
      supported_api_protocols_json = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.supported_api_protocols_json ELSE excluded.supported_api_protocols_json END,
      supported_service_tiers_json = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.supported_service_tiers_json ELSE excluded.supported_service_tiers_json END,
      supported_reasoning_efforts_json = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.supported_reasoning_efforts_json ELSE excluded.supported_reasoning_efforts_json END,
      default_reasoning_effort = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.default_reasoning_effort ELSE excluded.default_reasoning_effort END,
      codex_supported_reasoning_levels_json = excluded.codex_supported_reasoning_levels_json,
      codex_default_reasoning_level = excluded.codex_default_reasoning_level,
      codex_multi_agent_version = excluded.codex_multi_agent_version,
      context_window_tokens = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.context_window_tokens ELSE excluded.context_window_tokens END,
      max_input_tokens = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.max_input_tokens ELSE excluded.max_input_tokens END,
      max_output_tokens = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.max_output_tokens ELSE excluded.max_output_tokens END,
      max_tokens = excluded.max_tokens,
      input_usd_per_1m = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.input_usd_per_1m ELSE excluded.input_usd_per_1m END,
      output_usd_per_1m = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.output_usd_per_1m ELSE excluded.output_usd_per_1m END,
      cached_input_usd_per_1m = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.cached_input_usd_per_1m ELSE excluded.cached_input_usd_per_1m END,
      cache_write_usd_per_1m = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.cache_write_usd_per_1m ELSE excluded.cache_write_usd_per_1m END,
      cache_write_1h_usd_per_1m = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.cache_write_1h_usd_per_1m ELSE excluded.cache_write_1h_usd_per_1m END,
      cache_storage_usd_per_1m_per_hour = CASE
        WHEN provider_model_catalog.source = 'manual-override'
        THEN coalesce(provider_model_catalog.cache_storage_usd_per_1m_per_hour, excluded.cache_storage_usd_per_1m_per_hour)
        ELSE excluded.cache_storage_usd_per_1m_per_hour
      END,
      service_tier_prices_json = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.service_tier_prices_json ELSE excluded.service_tier_prices_json END,
      long_context_input_token_threshold = excluded.long_context_input_token_threshold,
      long_context_input_token_threshold_inclusive = excluded.long_context_input_token_threshold_inclusive,
      long_context_input_cost_multiplier = excluded.long_context_input_cost_multiplier,
      long_context_output_cost_multiplier = excluded.long_context_output_cost_multiplier,
      image_input_usd_per_1m = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.image_input_usd_per_1m ELSE excluded.image_input_usd_per_1m END,
      image_output_usd_per_1m = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.image_output_usd_per_1m ELSE excluded.image_output_usd_per_1m END,
      audio_input_usd_per_1m = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.audio_input_usd_per_1m ELSE excluded.audio_input_usd_per_1m END,
      audio_output_usd_per_1m = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.audio_output_usd_per_1m ELSE excluded.audio_output_usd_per_1m END,
      output_usd_per_image = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.output_usd_per_image ELSE excluded.output_usd_per_image END,
      supports_prompt_caching = excluded.supports_prompt_caching,
      catalog_visible = CASE
        WHEN provider_model_catalog.source IN ('manual-override', 'manual-visibility-override')
        THEN min(provider_model_catalog.catalog_visible, excluded.catalog_visible)
        ELSE excluded.catalog_visible
      END,
      source = CASE WHEN provider_model_catalog.source IN ('manual-override', 'manual-visibility-override') THEN provider_model_catalog.source ELSE excluded.source END,
      updated_at = excluded.updated_at
  `

// sqSeedLegacyGeneratedModelRowsSelect lists the generated-provider catalog rows for
// the stale disable.
const sqSeedLegacyGeneratedModelRowsSelect = `
    SELECT id, provider_code, model FROM provider_model_catalog
    WHERE provider_code IN ('gpt', 'anthropic', 'gemini', 'deepseek', 'glm', 'xai')
  `

// sqSeedLegacyStaleGeneratedModelDisable hides one stale generated model.
const sqSeedLegacyStaleGeneratedModelDisable = `
    UPDATE provider_model_catalog
    SET status = 'disabled', catalog_visible = 0, updated_at = ?
    WHERE id = ?
  `

// sqSeedProtocolInsert seeds one protocols row.
const sqSeedProtocolInsert = `
      INSERT OR IGNORE INTO protocols (
        id, code, version, name, description, enabled, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `

// sqSeedEndpointFamilyInsert seeds one protocol_endpoint_families row.
const sqSeedEndpointFamilyInsert = `
    INSERT OR IGNORE INTO protocol_endpoint_families (
      id, protocol_code, protocol_version, family_code, name, description, enabled, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `

// 2026-10-18 修复批恢复旧 provider/profile 种子语句（迁移窗口并存）：逐字取回
// dbb2543e6^ 版本，幂等语义不变（INSERT OR IGNORE / 守卫式 repair）。种子
// 语句不进 Ensure* 的 CREATE TABLE/INDEX 语句计数，sqlite golden
// （goldenSchemaCounts/goldenTotalTables/goldenTotalIndexes）不受影响。
const sqSeedLegacyProviderInsert = `
    INSERT OR IGNORE INTO providers (
      id, code, name, description, parent_code, enabled, default_supported_models_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `

// sqSeedLegacyProviderDefaultModelsRepair refreshes empty provider default model lists.
const sqSeedLegacyProviderDefaultModelsRepair = `
    UPDATE providers
    SET default_supported_models_json = ?, updated_at = ?
    WHERE code = ?
      AND (default_supported_models_json IS NULL OR trim(default_supported_models_json) = '' OR default_supported_models_json = '[]')
  `

const sqSeedLegacyProfileInsert = `
    INSERT OR IGNORE INTO provider_protocol_profiles (
      id, provider_code, name, description, enabled, protocol_code, protocol_version,
      base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `

// sqSeedLegacyProfileAccountTypesSelect reads one profile account_types_json for the
// repair step.
const sqSeedLegacyProfileAccountTypesSelect = `
    SELECT account_types_json
    FROM provider_protocol_profiles
    WHERE id = ?
  `

// sqSeedLegacyProfileAccountTypesUpdate merges missing built-in account types.
const sqSeedLegacyProfileAccountTypesUpdate = `
    UPDATE provider_protocol_profiles
    SET account_types_json = ?, updated_at = ?
    WHERE id = ?
  `

// sqSeedLegacyProfileFamilyInsert seeds one provider_protocol_profile_families row.
const sqSeedLegacyProfileFamilyInsert = `
    INSERT OR IGNORE INTO provider_protocol_profile_families (
      profile_id, family_code, enabled, capabilities_json, created_at, updated_at
    ) VALUES (?, ?, 1, '[]', ?, ?)
  `

// sqSeedGroupDefaultRepair marks the built-in group default when its owner has
// no default yet（设计 §8：判定只按 system_account_id + is_default=1，唯一索引
// idx_groups_owner_default_unique，无供应商维度）.
const sqSeedGroupDefaultRepair = `
      UPDATE groups AS candidate
      SET is_default = 1
      WHERE candidate.is_default = 0
        AND candidate.system_account_id = ?
        AND candidate.id = ?
        AND NOT EXISTS (
          SELECT 1
          FROM groups AS existing_default
          WHERE existing_default.system_account_id = candidate.system_account_id
            AND existing_default.is_default = 1
        )
    `

// sqSeedBuiltInGroupInsert inserts the built-in default group for every system
// account that lacks one (Node fallback-name recursion verbatim；缺组判定只按
// owner 的 is_default=1，无供应商维度).
const sqSeedBuiltInGroupInsert = `
      INSERT OR IGNORE INTO groups (
        id, system_account_id, name,
        description, enabled, is_default, created_at, updated_at
      )
      SELECT
        CASE WHEN system_accounts.id = ? THEN ? ELSE ? || system_accounts.id END,
        system_accounts.id,
        CASE
          WHEN EXISTS (
            SELECT 1
            FROM groups AS same_name
            WHERE same_name.system_account_id = system_accounts.id
              AND lower(same_name.name) = lower(?)
          ) THEN ? || '（系统默认：' || system_accounts.id || (
            WITH RECURSIVE candidate_suffix(suffix) AS (
              SELECT 0
              UNION ALL
              SELECT suffix + 1
              FROM candidate_suffix
              WHERE suffix < (
                SELECT COUNT(*)
                FROM groups AS fallback_name
                WHERE fallback_name.system_account_id = system_accounts.id
                  AND lower(fallback_name.name) LIKE lower(?) || '（系统默认：' || system_accounts.id || '%）'
              )
            )
            SELECT CASE
              WHEN candidate_suffix.suffix = 0 THEN ''
              ELSE ' #' || candidate_suffix.suffix
            END
            FROM candidate_suffix
            WHERE NOT EXISTS (
              SELECT 1
              FROM groups AS existing_fallback_name
              WHERE existing_fallback_name.system_account_id = system_accounts.id
                AND lower(existing_fallback_name.name) = lower(
                  ? || '（系统默认：' || system_accounts.id || CASE
                    WHEN candidate_suffix.suffix = 0 THEN ''
                    ELSE ' #' || candidate_suffix.suffix
                  END || '）'
                )
            )
            ORDER BY candidate_suffix.suffix
            LIMIT 1
          ) || '）'
          ELSE ?
        END,
        ?, 1, 1, ?, ?
      FROM system_accounts
      WHERE NOT EXISTS (
        SELECT 1
        FROM groups AS existing_default
        WHERE existing_default.system_account_id = system_accounts.id
          AND existing_default.is_default = 1
      )
    `

// sqSeedDefaultGroupsSelect lists the admin default group (2026-10-07
// 默认资源收口: only the built-in default group is auto-provisioned, so only
// it derives a default route strategy here — the PG variant iterates the same
// narrowed seed list); 判定只按 owner + is_default=1.
const sqSeedDefaultGroupsSelect = `
      SELECT id, name
      FROM groups
      WHERE system_account_id = 'sys_admin'
        AND is_default = 1
      ORDER BY created_at ASC, id ASC
    `

// sqSeedRouteStrategyInsert seeds one default route strategy.
const sqSeedRouteStrategyInsert = `
    INSERT OR IGNORE INTO route_strategies (
      id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at
    )
    VALUES (?, 'sys_admin', ?, ?, 'normal', 'active', 1, NULL, ?, ?)
  `

// sqSeedRouteStrategyExistsSelect re-reads the strategy after the OR IGNORE
// insert (Node routeStrategyExists).
const sqSeedRouteStrategyExistsSelect = "SELECT id FROM route_strategies WHERE id = ? LIMIT 1"

// sqSeedRouteStrategyGroupBindingInsert seeds one strategy-group binding.
const sqSeedRouteStrategyGroupBindingInsert = `
    INSERT OR IGNORE INTO route_strategy_groups (
      id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
    )
    VALUES (?, ?, 'sys_admin', ?, 1, 1, 'active', ?, ?)
  `

// sqSeedExistingDefaultAPIKeySelect finds an existing default API key for the
// strategy.
const sqSeedExistingDefaultAPIKeySelect = "SELECT id FROM api_keys WHERE route_strategy_id = ? AND is_default = 1 LIMIT 1"

// sqSeedDefaultAPIKeyInsert seeds one default API key. 调度偏好独立实体 P1 起
// 普通 Key 创建缺省持久化内置固定 ID（接口契约「调度偏好」章五写入口矩阵），
// seed 预置的默认 Key 与该契约同形——内置行由本函数更早的
// sqSeedBuiltinSchedulingPreferenceUpsert 预置，列级 REFERENCES 在同一连接内
// 可满足；P1 之前已存在的默认 Key 不受影响（OR IGNORE 跳过，存量 NULL 由
// --migrate-scheduling-preferences 回填）。
const sqSeedDefaultAPIKeyInsert = `
    INSERT OR IGNORE INTO api_keys (
      id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
      key_secret_encrypted, status, is_default, expires_at, quota_limits_json, availability_schedule_json,
      availability_schedule_next_check_at, scheduling_preference_id, created_at, updated_at
    )
    VALUES (?, 'sys_admin', ?, ?, ?, ?, ?, ?, ?, 'active', 1, NULL, NULL, NULL, NULL, 'schpref-builtin-chat-cost-first', ?, ?)
  `

// sqSeedExternalIntegrationSourceInsert seeds the built-in test source row.
const sqSeedExternalIntegrationSourceInsert = `
      INSERT OR IGNORE INTO external_integration_sources (
        id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at
      ) VALUES (?, ?, 'active', ?, ?, NULL, ?, ?, ?)
    `

// sqSeedExternalIntegrationSourceUpdate repairs the built-in test source row.
const sqSeedExternalIntegrationSourceUpdate = `
      UPDATE external_integration_sources
      SET name = ?,
          scopes_json = ?,
          rate_limits_json = ?,
          expires_at = NULL,
          notes = ?,
          updated_at = ?
      WHERE id = ?
    `

// sqSeedExternalIntegrationTokenSelect reads the built-in token id.
const sqSeedExternalIntegrationTokenSelect = "SELECT id FROM external_integration_source_tokens WHERE id = ?"

// sqSeedExternalIntegrationTokenInsert creates the built-in token.
const sqSeedExternalIntegrationTokenInsert = `
        INSERT INTO external_integration_source_tokens (
          id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, 'active', ?, NULL, ?, ?)
      `

// sqSeedExternalIntegrationTokenUpdate repairs the built-in token metadata.
const sqSeedExternalIntegrationTokenUpdate = `
        UPDATE external_integration_source_tokens
        SET source_ref_id = ?,
            name = ?,
            scopes_json = ?,
            expires_at = NULL,
            updated_at = ?
        WHERE id = ?
      `

// sqSeedSystemSettingInsert seeds one system_settings row for sys_admin.
const sqSeedSystemSettingInsert = `
    INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES (?, ?, ?, ?)
  `

// sqSeedBuiltinQuestionUpsert 预置一道系统内置题：固定 id，冲突时仅在
// 目标行仍是内置行时把题面字段恢复为 seed 权威值——应用层禁止编辑内置
// 题，因此该 upsert 要么插入新行、要么把被人工改动的内置行拉回版本值，
// 题面修订得以随版本到达存量环境；updated_at 不在更新列内，重复 seed
// 对已一致的行零写入。
const sqSeedBuiltinQuestionUpsert = `
    INSERT INTO model_check_question_bank
      (id,title,title_norm,question_text,reference_answer,key_points_json,status,reject_reason,created_by,created_scope,is_builtin,reviewed_by,reviewed_at,created_at,updated_at)
    VALUES (?,?,?,?,?,?,'approved',NULL,'system','system',1,NULL,NULL,?,?)
    ON CONFLICT(id) DO UPDATE SET
      title=excluded.title,
      title_norm=excluded.title_norm,
      question_text=excluded.question_text,
      reference_answer=excluded.reference_answer,
      key_points_json=excluded.key_points_json
      WHERE model_check_question_bank.is_builtin=1
  `

// seedSQLiteBuiltinModelCheckQuestions 预置系统内置题库题（模型检测设计
// §5.7）：approved + is_builtin=1，供用户直接选用。
func seedSQLiteBuiltinModelCheckQuestions(ctx context.Context, db *sql.DB, now string, result *SQLiteSeedResult) error {
	for _, question := range builtinModelCheckQuestions {
		keyPointsJSON, err := builtinQuestionKeyPointsJSON(question)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, sqSeedBuiltinQuestionUpsert,
			question.ID,
			question.Title,
			builtinQuestionTitleNorm(question.Title),
			question.QuestionText,
			question.ReferenceAnswer,
			keyPointsJSON,
			now,
			now,
		); err != nil {
			return fmt.Errorf("sqlite seed statement %d (builtin question %s): %w", result.StatementCount+1, question.ID, err)
		}
		result.StatementCount++
		result.BuiltinQuestionRows++
	}
	return nil
}

// seedSQLiteModelCatalog upserts the full models catalog（含 ShutdownDate 已过
// 写 'disabled' 的行）and returns the built-in (vendor \x00 upstream_model)
// key set for the stale disable.
func seedSQLiteModelCatalog(ctx context.Context, db *sql.DB, options SeedOptions, now string, result *SQLiteSeedResult) (map[string]bool, error) {
	asOfUTCDate := seedTimestamp(options.nowTime())[:10]
	rows, err := modelSeedTargetRows(asOfUTCDate)
	if err != nil {
		return nil, fmt.Errorf("sqlite seed model catalog projection: %w", err)
	}
	builtInModelKeys := make(map[string]bool, len(rows))
	for _, model := range rows {
		builtInModelKeys[model.VendorCode+"\x00"+model.UpstreamModel] = true
		if _, err := db.ExecContext(ctx, sqSeedModelsUpsert,
			model.ID,
			model.VendorCode,
			model.PublicModel,
			model.UpstreamModel,
			model.Status,
			seedBoolInt(model.CatalogVisible),
			seedNullableInt64(model.CatalogOrder),
			seedNullableString(model.ReleaseDate),
			seedNullableString(model.ShutdownDate),
			model.SupportedEndpointModesJSON,
			model.SupportedServiceTiersJSON,
			model.SupportedReasoningEffortsJSON,
			seedNullableString(model.DefaultReasoningEffort),
			model.CodexSupportedReasoningLevelsJSON,
			seedNullableString(model.CodexDefaultReasoningLevel),
			seedNullableString(model.CodexMultiAgentVersion),
			seedNullableInt64(model.ContextWindowTokens),
			seedNullableInt64(model.MaxInputTokens),
			seedNullableInt64(model.MaxOutputTokens),
			seedNullableInt64(model.MaxTokens),
			seedBoolInt(model.SupportsPromptCaching),
			model.Source,
			now,
			now,
			seedNullableFloat64(model.InputUsdPer1M),
			seedNullableFloat64(model.OutputUsdPer1M),
			seedNullableFloat64(model.CachedInputUsdPer1M),
			seedNullableFloat64(model.CacheWriteUsdPer1M),
			seedNullableFloat64(model.CacheWrite1HUsdPer1M),
			seedNullableFloat64(model.CacheStorageUsdPer1MPerHour),
			model.ServiceTierPricesJSON,
			seedNullableInt64(model.LongContextInputTokenThreshold),
			seedBoolInt(model.LongContextInputTokenThresholdInclusive),
			seedNullableFloat64(model.LongContextInputCostMultiplier),
			seedNullableFloat64(model.LongContextOutputCostMultiplier),
			seedNullableFloat64(model.ImageInputUsdPer1M),
			seedNullableFloat64(model.ImageOutputUsdPer1M),
			seedNullableFloat64(model.AudioInputUsdPer1M),
			seedNullableFloat64(model.AudioOutputUsdPer1M),
			seedNullableFloat64(model.OutputUsdPerImage),
		); err != nil {
			return nil, fmt.Errorf("sqlite seed statement %d (models %s): %w", result.StatementCount+1, model.ID, err)
		}
		result.StatementCount++
		result.ModelCatalogRows++
	}
	return builtInModelKeys, nil
}

// seedSQLiteDisableStaleGeneratedModels ports the Node stale generated-model
// disable: every builtin models row whose (vendor, upstream_model) is no
// longer built-in is disabled and hidden. The SQLite variant has no
// manual-source or reference guards (that guard set exists only in the
// PostgreSQL seed).
func seedSQLiteDisableStaleGeneratedModels(ctx context.Context, db *sql.DB, exec func(string, ...any) error, now string, builtInModelKeys map[string]bool) error {
	rows, err := db.QueryContext(ctx, sqSeedGeneratedModelRowsSelect)
	if err != nil {
		return fmt.Errorf("sqlite seed select generated model rows: %w", err)
	}
	defer rows.Close()
	type generatedModelRow struct{ ID, VendorCode, UpstreamModel string }
	var stale []generatedModelRow
	for rows.Next() {
		var row generatedModelRow
		if err := rows.Scan(&row.ID, &row.VendorCode, &row.UpstreamModel); err != nil {
			return fmt.Errorf("sqlite seed scan generated model row: %w", err)
		}
		if !builtInModelKeys[row.VendorCode+"\x00"+row.UpstreamModel] {
			stale = append(stale, row)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite seed iterate generated model rows: %w", err)
	}
	for _, row := range stale {
		if err := exec(sqSeedStaleGeneratedModelDisable, now, row.ID); err != nil {
			return err
		}
	}
	return nil
}

// seedSQLiteLegacyModelCatalog 逐字取回 dbb2543e6^ 的 seedSQLiteModelCatalog
//（2026-10-18 修复批，迁移窗口并存）：按 activeModelCatalogSeedRows 逐行
// upsert 旧 provider_model_catalog，并执行契约 2026-09-30 的同名 custom
// 行清理。两处结果字段适配：result.ModelCatalogRows 是新 models 种子的钉值
// 口径（TestSeedSQLiteDefaultsIdempotentAndComplete 钉 212），旧目录行数不
// 计入；CustomModelOverlapCleaned 字段已随重构移除，清理语句仍执行并计入
// StatementCount。
func seedSQLiteLegacyModelCatalog(ctx context.Context, db *sql.DB, options SeedOptions, now string, result *SQLiteSeedResult) (map[string]bool, error) {
	asOfUTCDate := seedTimestamp(options.nowTime())[:10]
	rows := activeModelCatalogSeedRows(asOfUTCDate)
	builtInModelKeys := make(map[string]bool, len(rows))
	for _, model := range rows {
		builtInModelKeys[model.ProviderCode+"\x00"+model.Model] = true
		if _, err := db.ExecContext(ctx, sqSeedLegacyModelCatalogUpsert,
			model.ID,
			model.ProviderCode,
			model.Model,
			seedNullableString(model.Mode),
			seedNullableInt64(model.CatalogOrder),
			seedNullableString(model.ReleaseDate),
			seedNullableString(model.ShutdownDate),
			model.SupportedAPIProtocolsJSON,
			model.SupportedServiceTiersJSON,
			model.SupportedReasoningEffortsJSON,
			seedNullableString(model.DefaultReasoningEffort),
			model.CodexSupportedReasoningLevelsJSON,
			seedNullableString(model.CodexDefaultReasoningLevel),
			seedNullableString(model.CodexMultiAgentVersion),
			seedNullableInt64(model.ContextWindowTokens),
			seedNullableInt64(model.MaxInputTokens),
			seedNullableInt64(model.MaxOutputTokens),
			seedNullableInt64(model.MaxTokens),
			seedNullableFloat64(model.InputUsdPer1M),
			seedNullableFloat64(model.OutputUsdPer1M),
			seedNullableFloat64(model.CachedInputUsdPer1M),
			seedNullableFloat64(model.CacheWriteUsdPer1M),
			seedNullableFloat64(model.CacheWrite1HUsdPer1M),
			seedNullableFloat64(model.CacheStorageUsdPer1MPerHour),
			model.ServiceTierPricesJSON,
			seedNullableInt64(model.LongContextInputTokenThreshold),
			seedBoolInt(model.LongContextInputTokenThresholdInclusive),
			seedNullableFloat64(model.LongContextInputCostMultiplier),
			seedNullableFloat64(model.LongContextOutputCostMultiplier),
			seedNullableFloat64(model.ImageInputUsdPer1M),
			seedNullableFloat64(model.ImageOutputUsdPer1M),
			seedNullableFloat64(model.AudioInputUsdPer1M),
			seedNullableFloat64(model.AudioOutputUsdPer1M),
			seedNullableFloat64(model.OutputUsdPerImage),
			seedBoolInt(model.SupportsPromptCaching),
			seedBoolInt(model.CatalogVisible),
			model.Source,
			now,
			now,
		); err != nil {
			return nil, fmt.Errorf("sqlite seed statement %d (model catalog %s): %w", result.StatementCount+1, model.ID, err)
		}
		result.StatementCount++
	}
	// 契约 2026-09-30（docs/functions/自定义模型与模型映射设计.md 第 4 节）：
	// 目录 upsert 后删除与本次 seed 可见内置行（activeModelCatalogSeedRows
	// 的 (ProviderCode, Model) 集合）同名的 custom_provider_models 存量行。
	if len(rows) > 0 {
		valueRows := make([]string, 0, len(rows))
		args := make([]any, 0, len(rows)*2)
		for _, model := range rows {
			valueRows = append(valueRows, "(?, ?)")
			args = append(args, model.ProviderCode, model.Model)
		}
		cleanupResult, err := db.ExecContext(ctx,
			"DELETE FROM custom_provider_models WHERE (provider_code, model) IN (VALUES "+strings.Join(valueRows, ", ")+")", args...)
		if err != nil {
			return nil, fmt.Errorf("sqlite seed statement %d (custom model overlap cleanup): %w", result.StatementCount+1, err)
		}
		if _, err := cleanupResult.RowsAffected(); err != nil {
			return nil, fmt.Errorf("sqlite seed custom model overlap cleanup rows affected: %w", err)
		}
		result.StatementCount++
	}
	return builtInModelKeys, nil
}

// seedSQLiteDisableStaleLegacyGeneratedModels 逐字取回 dbb2543e6^ 的
// seedSQLiteDisableStaleGeneratedModels（2026-10-18 修复批，迁移窗口并存）。
func seedSQLiteDisableStaleLegacyGeneratedModels(ctx context.Context, db *sql.DB, exec func(string, ...any) error, now string, builtInModelKeys map[string]bool) error {
	rows, err := db.QueryContext(ctx, sqSeedLegacyGeneratedModelRowsSelect)
	if err != nil {
		return fmt.Errorf("sqlite seed select generated model rows: %w", err)
	}
	defer rows.Close()
	type generatedModelRow struct{ ID, ProviderCode, Model string }
	var stale []generatedModelRow
	for rows.Next() {
		var row generatedModelRow
		if err := rows.Scan(&row.ID, &row.ProviderCode, &row.Model); err != nil {
			return fmt.Errorf("sqlite seed scan generated model row: %w", err)
		}
		if !builtInModelKeys[row.ProviderCode+"\x00"+row.Model] {
			stale = append(stale, row)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite seed iterate generated model rows: %w", err)
	}
	for _, row := range stale {
		if err := exec(sqSeedLegacyStaleGeneratedModelDisable, now, row.ID); err != nil {
			return err
		}
	}
	return nil
}

// seedSQLiteBuiltInGroupsForAllSystemAccounts ports
// seedDefaultBuiltInGroupsForAllSystemAccounts: mark the built-in group
// default when its owner has no default yet, then insert the built-in group
// for every system account that lacks one (with the Node fallback-name
// recursion verbatim).
func seedSQLiteBuiltInGroupsForAllSystemAccounts(exec func(string, ...any) error, now string) error {
	for _, group := range pgSeedGroups {
		if err := exec(sqSeedGroupDefaultRepair, group.SystemAccountID, group.ID); err != nil {
			return err
		}
		if err := exec(sqSeedBuiltInGroupInsert,
			group.SystemAccountID,
			group.ID,
			"grp_default_gpt_",
			group.Name,
			group.Name,
			group.Name,
			group.Name,
			group.Name,
			group.Description,
			now,
			now,
		); err != nil {
			return err
		}
	}
	return nil
}

// seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys ports
// seedAdminDefaultRouteStrategiesAndApiKeys (SQLite variant: the default GPT
// group is read from the groups table ordered by created_at, id — narrowed to
// gpt with the 2026-10-07 默认资源收口, matching the PG seed list iteration).
func seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx context.Context, db *sql.DB, exec func(string, ...any) error, options SeedOptions, now string) error {
	rows, err := db.QueryContext(ctx, sqSeedDefaultGroupsSelect)
	if err != nil {
		return fmt.Errorf("sqlite seed select default groups: %w", err)
	}
	defer rows.Close()
	type defaultGroup struct{ ID, Name string }
	var groups []defaultGroup
	for rows.Next() {
		var group defaultGroup
		if err := rows.Scan(&group.ID, &group.Name); err != nil {
			return fmt.Errorf("sqlite seed scan default group: %w", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite seed iterate default groups: %w", err)
	}
	for _, group := range groups {
		routeStrategyName := defaultRouteStrategyNameForGroup(group.Name)
		routeStrategyID := defaultRouteStrategyIDForGroup(group.ID)
		if err := exec(sqSeedRouteStrategyInsert, routeStrategyID, routeStrategyName, "系统默认普通路由，绑定"+group.Name+"。", now, now); err != nil {
			return err
		}
		exists, err := seedSQLiteRouteStrategyExists(ctx, db, routeStrategyID)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := exec(sqSeedRouteStrategyGroupBindingInsert, defaultRouteStrategyGroupBindingIDForGroup(group.ID), routeStrategyID, group.ID, now, now); err != nil {
			return err
		}
		existingKeyID, err := seedSQLiteExistingDefaultAPIKeyID(ctx, db, routeStrategyID)
		if err != nil {
			return err
		}
		if existingKeyID != "" {
			continue
		}
		apiKey, err := seedCreateAPIKey()
		if err != nil {
			return err
		}
		keySecretEncrypted, err := seedEncryptJSONWithOptions(options, map[string]string{"key": apiKey})
		if err != nil {
			return err
		}
		if err := exec(sqSeedDefaultAPIKeyInsert,
			defaultAPIKeyIDForRouteStrategy(routeStrategyID),
			routeStrategyID,
			defaultAPIKeyNameForRouteStrategy(routeStrategyName),
			"系统默认 API Key，绑定"+routeStrategyName+"。",
			seedHashSecret(apiKey),
			seedKeyPrefix(apiKey),
			seedKeySuffix(apiKey),
			keySecretEncrypted,
			now,
			now,
		); err != nil {
			return err
		}
	}
	return nil
}

// seedSQLiteAdminChatAPIKey was removed with the 2026-10-07 默认资源收口:
// the sys_admin chat API key is no longer seeded. AI chat sessions provision
// it on demand through the gateway EnsureChatAPIKey chain.

// seedSQLiteExternalIntegrationTestToken ports
// seedBuiltInExternalIntegrationTestToken (SQLite variant).
func seedSQLiteExternalIntegrationTestToken(ctx context.Context, db *sql.DB, exec func(string, ...any) error, options SeedOptions, now string) error {
	source := pgSeedExternalIntegrationSource
	scopesJSON := source.ScopesJSON
	if err := exec(sqSeedExternalIntegrationSourceInsert, source.ID, source.Name, scopesJSON, source.RateLimitsJSON, source.Notes, now, now); err != nil {
		return err
	}
	if err := exec(sqSeedExternalIntegrationSourceUpdate, source.Name, scopesJSON, source.RateLimitsJSON, source.Notes, now, source.ID); err != nil {
		return err
	}
	var existingTokenID string
	err := db.QueryRowContext(ctx, sqSeedExternalIntegrationTokenSelect, externalIntegrationTestTokenID).Scan(&existingTokenID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("sqlite seed select external integration token: %w", err)
	}
	if err == sql.ErrNoRows {
		token, err := seedCreateExternalIntegrationToken()
		if err != nil {
			return err
		}
		tokenSecretEncrypted, err := seedEncryptJSONWithOptions(options, map[string]string{"token": token})
		if err != nil {
			return err
		}
		return exec(sqSeedExternalIntegrationTokenInsert,
			externalIntegrationTestTokenID,
			source.ID,
			externalIntegrationTestTokenName,
			seedHashExternalIntegrationToken(token),
			tokenSecretEncrypted,
			seedKeyPrefix(token),
			seedKeySuffix(token),
			scopesJSON,
			now,
			now,
		)
	}
	return exec(sqSeedExternalIntegrationTokenUpdate, source.ID, externalIntegrationTestTokenName, scopesJSON, now, externalIntegrationTestTokenID)
}

func seedSQLiteRouteStrategyExists(ctx context.Context, db *sql.DB, routeStrategyID string) (bool, error) {
	var id string
	err := db.QueryRowContext(ctx, sqSeedRouteStrategyExistsSelect, routeStrategyID).Scan(&id)
	if err == nil {
		return id != "", nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, fmt.Errorf("sqlite seed check route strategy: %w", err)
}

func seedSQLiteExistingDefaultAPIKeyID(ctx context.Context, db *sql.DB, routeStrategyID string) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, sqSeedExistingDefaultAPIKeySelect, routeStrategyID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err == sql.ErrNoRows {
		return "", nil
	}
	return "", fmt.Errorf("sqlite seed check existing default api key: %w", err)
}
