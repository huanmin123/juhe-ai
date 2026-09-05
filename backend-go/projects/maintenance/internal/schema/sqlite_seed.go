// Port of Node backend/src/storage/schema/seed-defaults.ts (seedDefaults and
// its private helpers) for the six-database SQLite storage. The SQL statements
// are byte-for-byte ports of the Node template literals and run in the Node
// call order. Idempotency mirrors Node: every INSERT is INSERT OR IGNORE,
// every UPDATE is either guarded (WHERE ...) or the exact same repair Node
// re-applies on every startup, so with a pinned SeedOptions.Now repeated calls
// leave identical rows.
//
// The built-in seed constants (providers, protocols, endpoint families,
// profiles, groups, global/system settings, quota windows, external
// integration source) are the shared pgSeed* tables declared in pg_schema.go;
// they are the frozen DEFAULT_* values from Node src/storage/schema-defaults.ts
// and are reused here so SQLite and PostgreSQL seed exactly the same data.
//
// The pricing catalog comes from model_catalog_data.go (generated from the
// Node pricing modules); this file applies the Node runtime filters on top
// (shutdown date, hybrid/openai skip).

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SQLiteSeedResult summarizes SeedSQLiteDefaults.
type SQLiteSeedResult struct {
	StatementCount int
	// ModelCatalogRows is the number of pricing rows upserted after the Node
	// shutdown filter (106 for the 2026-09-04 snapshot when nothing is shut
	// down).
	ModelCatalogRows int
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
	if err := exec(sqSeedSystemAccountsInsert,
		"sys_admin", "admin", "超级管理员", "系统默认超级管理员账户", "super_admin", "active", adminPasswordHash, 0, 0, now, now); err != nil {
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
			provider.ID, provider.Code, provider.Name, provider.Description, pgNullableText(provider.ParentCode), provider.Enabled, provider.DefaultSupportedModelsJSON, now, now); err != nil {
			return SQLiteSeedResult{}, err
		}
		if err := exec(sqSeedProviderDefaultModelsRepair, provider.DefaultSupportedModelsJSON, now, provider.Code); err != nil {
			return SQLiteSeedResult{}, err
		}
	}

	// Node: strip codex-auto-review from the GPT vendor default model list.
	if err := exec(sqSeedGPTVendorCodexAutoReviewRemoval, now, gptVendorCode); err != nil {
		return SQLiteSeedResult{}, err
	}

	// Node: provider_model_catalog upsert for every built-in pricing model
	// (hybrid/openai skipped) plus the stale generated-model disable.
	builtInModelKeys, err := seedSQLiteModelCatalog(ctx, db, options, now, &result)
	if err != nil {
		return SQLiteSeedResult{}, err
	}
	if err := seedSQLiteDisableStaleGeneratedModels(ctx, db, exec, now, builtInModelKeys); err != nil {
		return SQLiteSeedResult{}, err
	}

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

	baseTime := options.nowTime()
	for index, profile := range pgSeedProfiles {
		profileUpdatedAt := seedTimestamp(baseTime.Add(time.Duration(index) * time.Millisecond))
		if err := exec(sqSeedProfileInsert,
			profile.ID, profile.ProviderCode, profile.Name, profile.Description, profile.Enabled, profile.ProtocolCode, profile.ProtocolVersion, profile.BaseURL, profile.DefaultHealthCheckModel, seedStringify(profile.AccountTypes), seedStringify(profile.Capabilities), now, profileUpdatedAt); err != nil {
			return SQLiteSeedResult{}, err
		}
	}
	if err := seedSQLiteRepairProfileAccountTypes(ctx, db, exec, now); err != nil {
		return SQLiteSeedResult{}, err
	}

	for _, profile := range pgSeedProfiles {
		for _, familyCode := range profile.EndpointFamilies {
			if err := exec(sqSeedProfileFamilyInsert, profile.ID, familyCode, now, now); err != nil {
				return SQLiteSeedResult{}, err
			}
		}
	}

	if err := seedSQLiteBuiltInGroupsForAllSystemAccounts(exec, now); err != nil {
		return SQLiteSeedResult{}, err
	}
	if err := seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx, db, exec, options, now); err != nil {
		return SQLiteSeedResult{}, err
	}
	if err := seedSQLiteAdminChatAPIKey(ctx, db, exec, options, now); err != nil {
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

// sqSeedSystemAccountsInsert is the Node default super admin insert.
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

// sqSeedProviderInsert seeds one providers row.
const sqSeedProviderInsert = `
    INSERT OR IGNORE INTO providers (
      id, code, name, description, parent_code, enabled, default_supported_models_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `

// sqSeedProviderDefaultModelsRepair refreshes empty provider default model lists.
const sqSeedProviderDefaultModelsRepair = `
    UPDATE providers
    SET default_supported_models_json = ?, updated_at = ?
    WHERE code = ?
      AND (default_supported_models_json IS NULL OR trim(default_supported_models_json) = '' OR default_supported_models_json = '[]')
  `

// sqSeedGPTVendorCodexAutoReviewRemoval drops codex-auto-review from the GPT
// vendor default model list.
const sqSeedGPTVendorCodexAutoReviewRemoval = `
    UPDATE providers
    SET default_supported_models_json = coalesce((
      SELECT json_group_array(value)
      FROM json_each(providers.default_supported_models_json)
      WHERE value <> 'codex-auto-review'
    ), '[]'), updated_at = ?
    WHERE code = ?
      AND json_valid(default_supported_models_json)
      AND json_type(default_supported_models_json) = 'array'
  `

// sqSeedModelCatalogUpsert is the Node provider_model_catalog upsert with the
// manual-override aware ON CONFLICT clause (seed-defaults.ts modelStatement).
const sqSeedModelCatalogUpsert = `
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

// sqSeedGeneratedModelRowsSelect lists the generated-provider catalog rows for
// the stale disable.
const sqSeedGeneratedModelRowsSelect = `
    SELECT id, provider_code, model FROM provider_model_catalog
    WHERE provider_code IN ('gpt', 'anthropic', 'gemini', 'deepseek', 'glm', 'xai')
  `

// sqSeedStaleGeneratedModelDisable hides one stale generated model.
const sqSeedStaleGeneratedModelDisable = `
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

// sqSeedProfileInsert seeds one provider_protocol_profiles row.
const sqSeedProfileInsert = `
    INSERT OR IGNORE INTO provider_protocol_profiles (
      id, provider_code, name, description, enabled, protocol_code, protocol_version,
      base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `

// sqSeedProfileAccountTypesSelect reads one profile account_types_json for the
// repair step.
const sqSeedProfileAccountTypesSelect = `
    SELECT account_types_json
    FROM provider_protocol_profiles
    WHERE id = ?
  `

// sqSeedProfileAccountTypesUpdate merges missing built-in account types.
const sqSeedProfileAccountTypesUpdate = `
    UPDATE provider_protocol_profiles
    SET account_types_json = ?, updated_at = ?
    WHERE id = ?
  `

// sqSeedProfileFamilyInsert seeds one provider_protocol_profile_families row.
const sqSeedProfileFamilyInsert = `
    INSERT OR IGNORE INTO provider_protocol_profile_families (
      profile_id, family_code, enabled, capabilities_json, created_at, updated_at
    ) VALUES (?, ?, 1, '[]', ?, ?)
  `

// sqSeedGroupDefaultRepair marks the built-in group default when its owner has
// no default yet.
const sqSeedGroupDefaultRepair = `
      UPDATE groups AS candidate
      SET is_default = 1
      WHERE candidate.provider_code = ?
        AND candidate.is_default = 0
        AND candidate.system_account_id = ?
        AND candidate.id = ?
        AND NOT EXISTS (
          SELECT 1
          FROM groups AS existing_default
          WHERE existing_default.system_account_id = candidate.system_account_id
            AND existing_default.provider_code = candidate.provider_code
            AND existing_default.is_default = 1
        )
    `

// sqSeedBuiltInGroupInsert inserts the built-in default group for every system
// account that lacks one (Node fallback-name recursion verbatim).
const sqSeedBuiltInGroupInsert = `
      INSERT OR IGNORE INTO groups (
        id, system_account_id, name, provider_code,
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
              AND same_name.provider_code = ?
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
                  AND fallback_name.provider_code = ?
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
                AND existing_fallback_name.provider_code = ?
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
        ?, ?, 1, 1, ?, ?
      FROM system_accounts
      WHERE NOT EXISTS (
        SELECT 1
        FROM groups AS existing_default
        WHERE existing_default.system_account_id = system_accounts.id
          AND existing_default.provider_code = ?
          AND existing_default.is_default = 1
      )
    `

// sqSeedDefaultGroupsSelect lists the admin default groups (hybrid excluded).
const sqSeedDefaultGroupsSelect = `
      SELECT id, name
      FROM groups
      WHERE system_account_id = 'sys_admin'
        AND is_default = 1
        AND provider_code <> ?
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

// sqSeedDefaultAPIKeyInsert seeds one default API key.
const sqSeedDefaultAPIKeyInsert = `
    INSERT OR IGNORE INTO api_keys (
      id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
      key_secret_encrypted, status, is_default, expires_at, quota_limits_json, availability_schedule_json,
      availability_schedule_next_check_at, created_at, updated_at
    )
    VALUES (?, 'sys_admin', ?, ?, ?, ?, ?, ?, ?, 'active', 1, NULL, NULL, NULL, NULL, ?, ?)
  `

// sqSeedChatKeyExistsSelect finds any existing admin chat API key.
const sqSeedChatKeyExistsSelect = "SELECT id FROM api_keys WHERE system_account_id = 'sys_admin' AND purpose = 'chat' LIMIT 1"

// sqSeedChatKeyDefaultGroupSelect picks the admin default GPT group.
const sqSeedChatKeyDefaultGroupSelect = `
    SELECT id FROM groups
    WHERE system_account_id = 'sys_admin' AND provider_code = ? AND is_default = 1
    ORDER BY created_at ASC, id ASC LIMIT 1
  `

// sqSeedChatKeyRouteSelect verifies the derived route strategy is active.
const sqSeedChatKeyRouteSelect = "SELECT id, name FROM route_strategies WHERE id = ? AND status = 'active' LIMIT 1"

// sqSeedChatAPIKeyInsert seeds the admin chat API key.
const sqSeedChatAPIKeyInsert = `
    INSERT OR IGNORE INTO api_keys (
      id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
      key_secret_encrypted, status, is_default, purpose, expires_at, quota_limits_json, availability_schedule_json,
      availability_schedule_next_check_at, created_at, updated_at
    ) VALUES (?, 'sys_admin', ?, 'AI 对话 API Key', ?, ?, ?, ?, ?, 'active', 0, 'chat', NULL, NULL, NULL, NULL, ?, ?)
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

// seedSQLiteModelCatalog ports the Node provider_model_catalog upsert loop and
// returns the built-in (provider \x00 model) key set for the stale disable.
func seedSQLiteModelCatalog(ctx context.Context, db *sql.DB, options SeedOptions, now string, result *SQLiteSeedResult) (map[string]bool, error) {
	asOfUTCDate := seedTimestamp(options.nowTime())[:10]
	rows := activeModelCatalogSeedRows(asOfUTCDate)
	builtInModelKeys := make(map[string]bool, len(rows))
	for _, model := range rows {
		builtInModelKeys[model.ProviderCode+"\x00"+model.Model] = true
		if _, err := db.ExecContext(ctx, sqSeedModelCatalogUpsert,
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
		result.ModelCatalogRows++
	}
	return builtInModelKeys, nil
}

// seedSQLiteDisableStaleGeneratedModels ports the Node stale generated-model
// disable: every catalog row of the six generated providers whose (provider,
// model) is no longer built-in is disabled and hidden. The SQLite variant has
// no manual-source or reference guards (that guard set exists only in the
// Node PostgreSQL seed).
func seedSQLiteDisableStaleGeneratedModels(ctx context.Context, db *sql.DB, exec func(string, ...any) error, now string, builtInModelKeys map[string]bool) error {
	rows, err := db.QueryContext(ctx, sqSeedGeneratedModelRowsSelect)
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
		if err := exec(sqSeedStaleGeneratedModelDisable, now, row.ID); err != nil {
			return err
		}
	}
	return nil
}

// seedSQLiteRepairProfileAccountTypes ports
// repairBuiltInProviderProfileAccountTypes: merge the built-in account types
// into the stored profile row when the stored JSON lacks them.
func seedSQLiteRepairProfileAccountTypes(ctx context.Context, db *sql.DB, exec func(string, ...any) error, now string) error {
	for _, profile := range pgSeedProfiles {
		var accountTypesJSON string
		err := db.QueryRowContext(ctx, sqSeedProfileAccountTypesSelect, profile.ID).Scan(&accountTypesJSON)
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
		if err := exec(sqSeedProfileAccountTypesUpdate, seedStringify(merged), now, profile.ID); err != nil {
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
		if err := exec(sqSeedGroupDefaultRepair, group.ProviderCode, group.SystemAccountID, group.ID); err != nil {
			return err
		}
		if err := exec(sqSeedBuiltInGroupInsert,
			group.SystemAccountID,
			group.ID,
			"grp_default_"+group.ProviderCode+"_",
			group.ProviderCode,
			group.Name,
			group.Name,
			group.ProviderCode,
			group.Name,
			group.ProviderCode,
			group.Name,
			group.Name,
			group.ProviderCode,
			group.Description,
			now,
			now,
			group.ProviderCode,
		); err != nil {
			return err
		}
	}
	return nil
}

// seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys ports
// seedAdminDefaultRouteStrategiesAndApiKeys (SQLite variant: the default
// groups are read from the groups table ordered by created_at, id).
func seedSQLiteAdminDefaultRouteStrategiesAndAPIKeys(ctx context.Context, db *sql.DB, exec func(string, ...any) error, options SeedOptions, now string) error {
	rows, err := db.QueryContext(ctx, sqSeedDefaultGroupsSelect, hybridProviderCode)
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

// seedSQLiteAdminChatAPIKey ports seedAdminChatApiKey (SQLite variant).
func seedSQLiteAdminChatAPIKey(ctx context.Context, db *sql.DB, exec func(string, ...any) error, options SeedOptions, now string) error {
	var existingID string
	err := db.QueryRowContext(ctx, sqSeedChatKeyExistsSelect).Scan(&existingID)
	if err == nil {
		if existingID != "" {
			return nil
		}
	} else if err != sql.ErrNoRows {
		return fmt.Errorf("sqlite seed check chat api key: %w", err)
	}
	var groupID string
	err = db.QueryRowContext(ctx, sqSeedChatKeyDefaultGroupSelect, gptVendorCode).Scan(&groupID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("sqlite seed select chat key default group: %w", err)
	}
	routeStrategyID := defaultRouteStrategyIDForGroup(groupID)
	var routeID, routeName string
	err = db.QueryRowContext(ctx, sqSeedChatKeyRouteSelect, routeStrategyID).Scan(&routeID, &routeName)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("sqlite seed select chat key route strategy: %w", err)
	}
	apiKey, err := seedCreateAPIKey()
	if err != nil {
		return err
	}
	keySecretEncrypted, err := seedEncryptJSONWithOptions(options, map[string]string{"key": apiKey})
	if err != nil {
		return err
	}
	return exec(sqSeedChatAPIKeyInsert,
		"key_chat_sys_admin",
		routeID,
		"AI 对话专用 API Key，默认绑定"+routeName+"。",
		seedHashSecret(apiKey),
		seedKeyPrefix(apiKey),
		seedKeySuffix(apiKey),
		keySecretEncrypted,
		now,
		now,
	)
}

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
