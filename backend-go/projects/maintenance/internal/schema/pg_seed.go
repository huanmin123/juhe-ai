// Complete port of Node backend/src/storage/postgres-seed-defaults.ts
// (seedPostgresDefaults and its private helpers). pg_schema.go already carried
// the portable subset (EnsurePostgresSeeds); this file completes it with the
// pieces the pg_schema.go header lists as deliberately omitted:
//
//   - the provider_model_catalog bulk upsert (105+ rows x 39 parameters, the
//     static rows are generated into model_catalog_data.go from the Node
//     pricing modules) and the stale built-in model disable with the Node
//     PostgreSQL reference guards,
//   - default route strategies / default API keys and the admin chat API key
//     (createApiKey + hashSecret + encryptJson),
//   - the external integration source token create/update.
//
// Statement order and idempotency mirror Node exactly: every INSERT uses ON
// CONFLICT DO NOTHING, every UPDATE is the same guarded repair Node re-applies
// on every run, so with a pinned SeedOptions.Now repeated calls leave
// identical rows. Unlike EnsurePostgresSeeds this function is the full seed
// and takes the injected clock.
//
// SeedPostgresDefaults targets a database where applyPostgresSchema
// (EnsurePostgres) already ran, mirroring the Node init script
// backend/src/scripts/maintenance/init-postgres-schema.ts
// (applyPostgresSchema -> seedPostgresDefaults).

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// postgresSeedClient is the SQL surface seedPostgresDefaults needs; *sql.DB
// satisfies it and tests can capture statements instead of opening a server.
type postgresSeedClient interface {
	seedExecutor
	seedRowQuerier
}

// SeedPostgresDefaults ports Node seedPostgresDefaults in full. See the file
// header for the relationship with EnsurePostgresSeeds (the portable subset).
func SeedPostgresDefaults(ctx context.Context, db *sql.DB, options SeedOptions) (PGSeedResult, error) {
	return seedPostgresDefaults(ctx, db, options)
}

func seedPostgresDefaults(ctx context.Context, client postgresSeedClient, options SeedOptions) (PGSeedResult, error) {
	now := seedTimestamp(options.nowTime())
	var result PGSeedResult
	exec := func(query string, args ...any) error {
		if _, err := client.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("postgres seed statement %d: %w", result.StatementCount+1, err)
		}
		result.StatementCount++
		return nil
	}

	adminPasswordHash, err := hashSeedPassword("admin")
	if err != nil {
		return PGSeedResult{}, fmt.Errorf("hash seed admin password: %w", err)
	}
	if err := exec(pgSeedSystemAccountsInsert, "sys_admin", "admin", "超级管理员", "系统默认超级管理员账户", "super_admin", "active", adminPasswordHash, 0, 0, now, now); err != nil {
		return PGSeedResult{}, err
	}
	for _, setting := range pgSeedGlobalSettings {
		if err := exec(pgSeedGlobalSettingsInsert, setting.Key, setting.ValueJSON, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, hours := range pgSeedRequestQuotaHourlyWindowHours {
		if err := exec(pgSeedWindowConfigInsert, hours, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, cte := range []string{pgSeedQuotaScopeBindingsFromAPIKeysCTE, pgSeedQuotaScopeBindingsFromAuthorizationsCTE, pgSeedQuotaScopeBindingsFromTeamGrantsCTE} {
		if err := exec(cte); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, provider := range pgSeedProviders {
		if err := exec(pgSeedProviderInsert, provider.ID, provider.Code, provider.Name, provider.Description, pgNullableText(provider.ParentCode), provider.Enabled, provider.DefaultSupportedModelsJSON, now, now); err != nil {
			return PGSeedResult{}, err
		}
		if err := exec(pgSeedProviderDefaultModelsRepair, provider.DefaultSupportedModelsJSON, now, provider.Code); err != nil {
			return PGSeedResult{}, err
		}
	}

	// Node: the bulk provider_model_catalog upsert plus the guarded stale
	// built-in model disable.
	if err := seedPostgresModelCatalog(ctx, client, exec, options, now, &result); err != nil {
		return PGSeedResult{}, err
	}

	for _, protocol := range pgSeedProtocols {
		if err := exec(pgSeedProtocolInsert, protocol.ID, protocol.Code, protocol.Version, protocol.Name, protocol.Description, protocol.Enabled, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, family := range pgSeedEndpointFamilies {
		if err := exec(pgSeedEndpointFamilyInsert, family.ID, family.ProtocolCode, family.ProtocolVersion, family.Code, family.Name, family.Description, family.Enabled, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	baseTime := options.nowTime()
	for index, profile := range pgSeedProfiles {
		profileUpdatedAt := seedTimestamp(baseTime.Add(time.Duration(index) * time.Millisecond))
		if err := exec(pgSeedProfileInsert, profile.ID, profile.ProviderCode, profile.Name, profile.Description, profile.Enabled, profile.ProtocolCode, profile.ProtocolVersion, profile.BaseURL, profile.DefaultHealthCheckModel, seedStringify(profile.AccountTypes), seedStringify(profile.Capabilities), now, profileUpdatedAt); err != nil {
			return PGSeedResult{}, err
		}
	}
	if err := seedPostgresRepairProfileAccountTypes(ctx, client, exec, now); err != nil {
		return PGSeedResult{}, err
	}
	for _, profile := range pgSeedProfiles {
		for _, familyCode := range profile.EndpointFamilies {
			if err := exec(pgSeedProfileFamilyInsert, profile.ID, familyCode, now, now); err != nil {
				return PGSeedResult{}, err
			}
		}
	}
	for _, group := range pgSeedGroups {
		if err := exec(pgSeedGroupDefaultRepair, group.ProviderCode, group.SystemAccountID, group.ID); err != nil {
			return PGSeedResult{}, err
		}
		defaultGroupIDPrefix := "grp_default_" + group.ProviderCode + "_"
		if err := exec(pgSeedGroupInsert, group.SystemAccountID, group.ID, defaultGroupIDPrefix, group.ProviderCode, group.Name, group.Name, group.Description, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}

	// Node: seedAdminDefaultRouteStrategiesAndApiKeys, seedAdminChatApiKey,
	// seedBuiltInExternalIntegrationTestToken (with the token-free source row
	// insert/update at its head).
	if err := seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(ctx, client, exec, options, now); err != nil {
		return PGSeedResult{}, err
	}
	if err := seedPostgresAdminChatAPIKey(ctx, client, exec, options, now); err != nil {
		return PGSeedResult{}, err
	}
	if err := seedPostgresExternalIntegrationTestToken(ctx, client, exec, options, now); err != nil {
		return PGSeedResult{}, err
	}

	for _, setting := range pgSeedSystemSettings {
		if err := exec(pgSeedSystemSettingInsert, "sys_admin", setting.Key, setting.ValueJSON, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	return result, nil
}

// pgSeedModelCatalogColumns lists the 40 target columns of the Node bulk
// upsert (status is the 'active' literal, the other 39 are parameters).
const pgSeedModelCatalogColumns = `
        INSERT INTO "juhe_business"."provider_model_catalog" (
          id, provider_code, model, status, mode, catalog_order, release_date, shutdown_date,
          supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
          default_reasoning_effort, codex_supported_reasoning_levels_json, codex_default_reasoning_level,
          codex_multi_agent_version, context_window_tokens, max_input_tokens, max_output_tokens, max_tokens,
          input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m,
          cache_write_1h_usd_per_1m, cache_storage_usd_per_1m_per_hour, service_tier_prices_json,
          long_context_input_token_threshold, long_context_input_token_threshold_inclusive, long_context_input_cost_multiplier, long_context_output_cost_multiplier,
          image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
          output_usd_per_image, supports_prompt_caching, catalog_visible, source, created_at, updated_at
        ) VALUES
          `

// pgSeedModelCatalogBulkUpsertSuffix is the manual-override aware ON CONFLICT
// clause of the Node bulk upsert (postgres-seed-defaults.ts).
const pgSeedModelCatalogBulkUpsertSuffix = `
        ON CONFLICT(provider_code, model) DO UPDATE SET
          status = CASE
            WHEN "juhe_business"."provider_model_catalog".source IN ('manual-override', 'manual-visibility-override')
            THEN "juhe_business"."provider_model_catalog".status
            ELSE excluded.status
          END,
          mode = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".mode ELSE excluded.mode END,
          catalog_order = excluded.catalog_order,
          release_date = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".release_date ELSE excluded.release_date END,
          shutdown_date = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".shutdown_date ELSE excluded.shutdown_date END,
          supported_api_protocols_json = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".supported_api_protocols_json ELSE excluded.supported_api_protocols_json END,
          supported_service_tiers_json = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".supported_service_tiers_json ELSE excluded.supported_service_tiers_json END,
          supported_reasoning_efforts_json = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".supported_reasoning_efforts_json ELSE excluded.supported_reasoning_efforts_json END,
          default_reasoning_effort = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".default_reasoning_effort ELSE excluded.default_reasoning_effort END,
          codex_supported_reasoning_levels_json = excluded.codex_supported_reasoning_levels_json,
          codex_default_reasoning_level = excluded.codex_default_reasoning_level,
          codex_multi_agent_version = excluded.codex_multi_agent_version,
          context_window_tokens = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".context_window_tokens ELSE excluded.context_window_tokens END,
          max_input_tokens = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".max_input_tokens ELSE excluded.max_input_tokens END,
          max_output_tokens = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".max_output_tokens ELSE excluded.max_output_tokens END,
          max_tokens = excluded.max_tokens,
          input_usd_per_1m = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".input_usd_per_1m ELSE excluded.input_usd_per_1m END,
          output_usd_per_1m = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".output_usd_per_1m ELSE excluded.output_usd_per_1m END,
          cached_input_usd_per_1m = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".cached_input_usd_per_1m ELSE excluded.cached_input_usd_per_1m END,
          cache_write_usd_per_1m = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".cache_write_usd_per_1m ELSE excluded.cache_write_usd_per_1m END,
          cache_write_1h_usd_per_1m = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".cache_write_1h_usd_per_1m ELSE excluded.cache_write_1h_usd_per_1m END,
          cache_storage_usd_per_1m_per_hour = CASE
            WHEN "juhe_business"."provider_model_catalog".source = 'manual-override'
            THEN coalesce("juhe_business"."provider_model_catalog".cache_storage_usd_per_1m_per_hour, excluded.cache_storage_usd_per_1m_per_hour)
            ELSE excluded.cache_storage_usd_per_1m_per_hour
          END,
          service_tier_prices_json = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".service_tier_prices_json ELSE excluded.service_tier_prices_json END,
          long_context_input_token_threshold = excluded.long_context_input_token_threshold,
          long_context_input_token_threshold_inclusive = excluded.long_context_input_token_threshold_inclusive,
          long_context_input_cost_multiplier = excluded.long_context_input_cost_multiplier,
          long_context_output_cost_multiplier = excluded.long_context_output_cost_multiplier,
          image_input_usd_per_1m = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".image_input_usd_per_1m ELSE excluded.image_input_usd_per_1m END,
          image_output_usd_per_1m = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".image_output_usd_per_1m ELSE excluded.image_output_usd_per_1m END,
          audio_input_usd_per_1m = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".audio_input_usd_per_1m ELSE excluded.audio_input_usd_per_1m END,
          audio_output_usd_per_1m = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".audio_output_usd_per_1m ELSE excluded.audio_output_usd_per_1m END,
          output_usd_per_image = CASE WHEN "juhe_business"."provider_model_catalog".source = 'manual-override' THEN "juhe_business"."provider_model_catalog".output_usd_per_image ELSE excluded.output_usd_per_image END,
          supports_prompt_caching = excluded.supports_prompt_caching,
          catalog_visible = CASE
            WHEN "juhe_business"."provider_model_catalog".source IN ('manual-override', 'manual-visibility-override')
            THEN "juhe_business"."provider_model_catalog".catalog_visible AND excluded.catalog_visible
            ELSE excluded.catalog_visible
          END,
          source = CASE
            WHEN "juhe_business"."provider_model_catalog".source IN ('manual-override', 'manual-visibility-override')
            THEN "juhe_business"."provider_model_catalog".source
            ELSE excluded.source
          END,
          updated_at = excluded.updated_at
      `

// pgSeedStaleBuiltInModelsDisable is the Node stale built-in model disable
// with the PostgreSQL reference guards (manual sources, supported models,
// mappings, health-check models and account health-check models).
const pgSeedStaleBuiltInModelsDisable = `
    UPDATE "juhe_business"."provider_model_catalog"
    SET status = 'disabled', catalog_visible = false, updated_at = $1
    WHERE provider_code IN ('gpt', 'anthropic', 'gemini', 'deepseek', 'glm', 'xai')
      AND source NOT IN ('manual-override', 'manual-visibility-override')
      AND NOT EXISTS (
        SELECT 1
        FROM jsonb_to_recordset($2::jsonb) AS built_in(provider_code text, model text)
        WHERE built_in.provider_code = "juhe_business"."provider_model_catalog".provider_code
          AND built_in.model = "juhe_business"."provider_model_catalog".model
      )
      AND NOT EXISTS (
        SELECT 1
        FROM "juhe_business"."account_supported_models" AS supported_model
        WHERE supported_model.provider_code = "juhe_business"."provider_model_catalog".provider_code
          AND supported_model.model = "juhe_business"."provider_model_catalog".model
      )
      AND NOT EXISTS (
        SELECT 1
        FROM "juhe_business"."account_model_mappings" AS model_mapping
        WHERE model_mapping.provider_code = "juhe_business"."provider_model_catalog".provider_code
          AND (
            model_mapping.source_model = "juhe_business"."provider_model_catalog".model
            OR model_mapping.upstream_model = "juhe_business"."provider_model_catalog".model
          )
      )
      AND NOT EXISTS (
        SELECT 1
        FROM "juhe_business"."provider_default_health_check_models" AS default_health_model
        WHERE default_health_model.provider_code = "juhe_business"."provider_model_catalog".provider_code
          AND default_health_model.model = "juhe_business"."provider_model_catalog".model
      )
      AND NOT EXISTS (
        SELECT 1
        FROM "juhe_business"."provider_system_default_health_check_models" AS system_default_health_model
        WHERE system_default_health_model.provider_code = "juhe_business"."provider_model_catalog".provider_code
          AND system_default_health_model.model = "juhe_business"."provider_model_catalog".model
      )
      AND NOT EXISTS (
        SELECT 1
        FROM "juhe_business"."accounts" AS account
        WHERE account.provider_code = "juhe_business"."provider_model_catalog".provider_code
          AND account.health_check_model = "juhe_business"."provider_model_catalog".model
      )
  `

// pgSeedAdminRouteStrategyInsert seeds one admin default route strategy.
const pgSeedAdminRouteStrategyInsert = `
        INSERT INTO "juhe_business"."route_strategies" (
          id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at
        ) VALUES ($1, 'sys_admin', $2, $3, 'normal', 'active', 1, NULL, $4, $5)
        ON CONFLICT DO NOTHING
      `

// pgSeedAdminRouteStrategySelect re-reads the strategy after the insert.
const pgSeedAdminRouteStrategySelect = `
        SELECT id
        FROM "juhe_business"."route_strategies"
        WHERE id = $1
        LIMIT 1
      `

// pgSeedAdminRouteStrategyGroupBindingInsert seeds one strategy-group binding.
const pgSeedAdminRouteStrategyGroupBindingInsert = `
        INSERT INTO "juhe_business"."route_strategy_groups" (
          id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
        ) VALUES ($1, $2, 'sys_admin', $3, 1, 1, 'active', $4, $5)
        ON CONFLICT DO NOTHING
      `

// pgSeedAdminExistingDefaultAPIKeySelect finds an existing default API key.
const pgSeedAdminExistingDefaultAPIKeySelect = `
        SELECT id
        FROM "juhe_business"."api_keys"
        WHERE route_strategy_id = $1 AND is_default = 1
        LIMIT 1
      `

// pgSeedAdminDefaultAPIKeyInsert seeds one admin default API key.
const pgSeedAdminDefaultAPIKeyInsert = `
        INSERT INTO "juhe_business"."api_keys" (
          id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
          key_secret_encrypted, status, is_default, expires_at, quota_limits_json, availability_schedule_json,
          availability_schedule_next_check_at, created_at, updated_at
        ) VALUES ($1, 'sys_admin', $2, $3, $4, $5, $6, $7, $8, 'active', 1, NULL, NULL, NULL, NULL, $9, $10)
        ON CONFLICT DO NOTHING
      `

// pgSeedAdminDefaultGroupSelect picks the provider default group per built-in
// group seed (PostgreSQL orders by updated_at DESC, unlike the SQLite variant).
const pgSeedAdminDefaultGroupSelect = `
        SELECT id, name
        FROM "juhe_business"."groups"
        WHERE system_account_id = $1
          AND provider_code = $2
          AND is_default = 1
        ORDER BY updated_at DESC, id ASC
        LIMIT 1
      `

// pgSeedAdminChatKeyExistsSelect finds any existing admin chat API key.
const pgSeedAdminChatKeyExistsSelect = `
    SELECT id FROM "juhe_business"."api_keys" WHERE system_account_id = 'sys_admin' AND purpose = 'chat' LIMIT 1
  `

// pgSeedAdminChatKeyDefaultGroupSelect picks the admin default GPT group.
const pgSeedAdminChatKeyDefaultGroupSelect = `
    SELECT id FROM "juhe_business"."groups"
    WHERE system_account_id = 'sys_admin' AND provider_code = $1 AND is_default = 1
    ORDER BY created_at ASC, id ASC LIMIT 1
  `

// pgSeedAdminChatKeyRouteSelect verifies the derived route strategy is active.
const pgSeedAdminChatKeyRouteSelect = `
    SELECT id, name FROM "juhe_business"."route_strategies" WHERE id = $1 AND status = 'active' LIMIT 1
  `

// pgSeedAdminChatAPIKeyInsert seeds the admin chat API key.
const pgSeedAdminChatAPIKeyInsert = `
    INSERT INTO "juhe_business"."api_keys" (
      id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
      key_secret_encrypted, status, is_default, purpose, expires_at, quota_limits_json, availability_schedule_json,
      availability_schedule_next_check_at, created_at, updated_at
    ) VALUES ($1, 'sys_admin', $2, 'AI 对话 API Key', $3, $4, $5, $6, $7, 'active', 0, 'chat', NULL, NULL, NULL, NULL, $8, $9)
    ON CONFLICT DO NOTHING
  `

// pgSeedExternalIntegrationTokenSelect reads the built-in token id.
const pgSeedExternalIntegrationTokenSelect = `
      SELECT id
      FROM "juhe_business"."external_integration_source_tokens"
      WHERE id = $1
    `

// pgSeedExternalIntegrationTokenInsert creates the built-in token.
const pgSeedExternalIntegrationTokenInsert = `
        INSERT INTO "juhe_business"."external_integration_source_tokens" (
          id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, NULL, $9, $10)
      `

// pgSeedExternalIntegrationTokenUpdate repairs the built-in token metadata.
const pgSeedExternalIntegrationTokenUpdate = `
      UPDATE "juhe_business"."external_integration_source_tokens"
      SET source_ref_id = $1,
          name = $2,
          scopes_json = $3,
          expires_at = NULL,
          updated_at = $4
      WHERE id = $5
    `

// pgSeedBuiltInModelKey mirrors one jsonb_to_recordset row of the stale
// disable parameter.
type pgSeedBuiltInModelKey struct {
	ProviderCode string `json:"provider_code"`
	Model        string `json:"model"`
}

// seedPostgresModelCatalog ports the Node bulk provider_model_catalog upsert
// and the guarded stale disable, returning those statement counts.
func seedPostgresModelCatalog(ctx context.Context, client postgresSeedClient, exec func(string, ...any) error, options SeedOptions, now string, result *PGSeedResult) error {
	asOfUTCDate := seedTimestamp(options.nowTime())[:10]
	rows := activeModelCatalogSeedRows(asOfUTCDate)
	if len(rows) > 0 {
		query, args := buildPostgresModelSeedUpsert(rows, now)
		if _, err := client.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("postgres seed statement %d (model catalog bulk upsert): %w", result.StatementCount+1, err)
		}
		result.StatementCount++
	}
	builtIn := make([]pgSeedBuiltInModelKey, 0, len(rows))
	for _, row := range rows {
		builtIn = append(builtIn, pgSeedBuiltInModelKey{ProviderCode: row.ProviderCode, Model: row.Model})
	}
	builtInJSON, err := seedJSONStringify(builtIn)
	if err != nil {
		return err
	}
	staleResult, err := client.ExecContext(ctx, pgSeedStaleBuiltInModelsDisable, now, string(builtInJSON))
	if err != nil {
		return fmt.Errorf("postgres seed statement %d (stale built-in model disable): %w", result.StatementCount+1, err)
	}
	// Node statementCount += staleBuiltInModels.changes.
	changes, err := staleResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres seed stale disable rows affected: %w", err)
	}
	result.StatementCount += int(changes)
	return nil
}

// buildPostgresModelSeedUpsert renders the Node bulk upsert: one statement
// with 39 bound parameters per row (status is the 'active' literal) using
// global $n placeholders.
func buildPostgresModelSeedUpsert(rows []modelCatalogSeedRow, now string) (string, []any) {
	args := make([]any, 0, len(rows)*39)
	rowTemplates := make([]string, 0, len(rows))
	for _, model := range rows {
		first := len(args) + 1
		args = append(args,
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
			model.LongContextInputTokenThresholdInclusive,
			seedNullableFloat64(model.LongContextInputCostMultiplier),
			seedNullableFloat64(model.LongContextOutputCostMultiplier),
			seedNullableFloat64(model.ImageInputUsdPer1M),
			seedNullableFloat64(model.ImageOutputUsdPer1M),
			seedNullableFloat64(model.AudioInputUsdPer1M),
			seedNullableFloat64(model.AudioOutputUsdPer1M),
			seedNullableFloat64(model.OutputUsdPerImage),
			model.SupportsPromptCaching,
			model.CatalogVisible,
			model.Source,
			now,
			now,
		)
		// Node: (${first}, ${first+1}, ${first+2}, 'active', ${first+3}, ...).
		row := "($" + strconv.Itoa(first) + ", $" + strconv.Itoa(first+1) + ", $" + strconv.Itoa(first+2) + ", 'active'"
		for index := 3; index < 39; index++ {
			row += ", $" + strconv.Itoa(first+index)
		}
		row += ")"
		rowTemplates = append(rowTemplates, row)
	}
	return pgSeedModelCatalogColumns + strings.Join(rowTemplates, ",\n          ") + pgSeedModelCatalogBulkUpsertSuffix, args
}

// seedPostgresRepairProfileAccountTypes ports
// repairBuiltInProviderProfileAccountTypes.
func seedPostgresRepairProfileAccountTypes(ctx context.Context, client postgresSeedClient, exec func(string, ...any) error, now string) error {
	for _, profile := range pgSeedProfiles {
		var accountTypesJSON string
		err := client.QueryRowContext(ctx, pgSeedProfileAccountTypesSelect, profile.ID).Scan(&accountTypesJSON)
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
		if err := exec(pgSeedProfileAccountTypesUpdate, seedStringify(merged), now, profile.ID); err != nil {
			return err
		}
	}
	return nil
}

// seedPostgresAdminDefaultRouteStrategiesAndAPIKeys ports
// seedAdminDefaultRouteStrategiesAndApiKeys (PostgreSQL variant: iterate the
// built-in group seeds, pick each provider default group by updated_at DESC).
func seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(ctx context.Context, client postgresSeedClient, exec func(string, ...any) error, options SeedOptions, now string) error {
	for _, groupSeed := range pgSeedGroups {
		if groupSeed.SystemAccountID != "sys_admin" || groupSeed.ProviderCode == hybridProviderCode {
			continue
		}
		var groupID, groupName string
		err := client.QueryRowContext(ctx, pgSeedAdminDefaultGroupSelect, groupSeed.SystemAccountID, groupSeed.ProviderCode).Scan(&groupID, &groupName)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return fmt.Errorf("postgres seed select admin default group: %w", err)
		}
		routeStrategyName := defaultRouteStrategyNameForGroup(groupName)
		routeStrategyID := defaultRouteStrategyIDForGroup(groupID)
		if err := exec(pgSeedAdminRouteStrategyInsert, routeStrategyID, routeStrategyName, "系统默认普通路由，绑定"+groupName+"。", now, now); err != nil {
			return err
		}
		var strategyID string
		err = client.QueryRowContext(ctx, pgSeedAdminRouteStrategySelect, routeStrategyID).Scan(&strategyID)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return fmt.Errorf("postgres seed select admin route strategy: %w", err)
		}
		if err := exec(pgSeedAdminRouteStrategyGroupBindingInsert, defaultRouteStrategyGroupBindingIDForGroup(groupID), routeStrategyID, groupID, now, now); err != nil {
			return err
		}
		var existingAPIKeyID string
		err = client.QueryRowContext(ctx, pgSeedAdminExistingDefaultAPIKeySelect, routeStrategyID).Scan(&existingAPIKeyID)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("postgres seed select existing default api key: %w", err)
		}
		if err == nil {
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
		if err := exec(pgSeedAdminDefaultAPIKeyInsert,
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

// seedPostgresAdminChatAPIKey ports seedAdminChatApiKey (PostgreSQL variant).
func seedPostgresAdminChatAPIKey(ctx context.Context, client postgresSeedClient, exec func(string, ...any) error, options SeedOptions, now string) error {
	var existingID string
	err := client.QueryRowContext(ctx, pgSeedAdminChatKeyExistsSelect).Scan(&existingID)
	if err == nil {
		return nil
	} else if err != sql.ErrNoRows {
		return fmt.Errorf("postgres seed check chat api key: %w", err)
	}
	var groupID string
	err = client.QueryRowContext(ctx, pgSeedAdminChatKeyDefaultGroupSelect, gptVendorCode).Scan(&groupID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("postgres seed select chat key default group: %w", err)
	}
	routeStrategyID := defaultRouteStrategyIDForGroup(groupID)
	var routeID, routeName string
	err = client.QueryRowContext(ctx, pgSeedAdminChatKeyRouteSelect, routeStrategyID).Scan(&routeID, &routeName)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("postgres seed select chat key route strategy: %w", err)
	}
	apiKey, err := seedCreateAPIKey()
	if err != nil {
		return err
	}
	keySecretEncrypted, err := seedEncryptJSONWithOptions(options, map[string]string{"key": apiKey})
	if err != nil {
		return err
	}
	return exec(pgSeedAdminChatAPIKeyInsert,
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

// seedPostgresExternalIntegrationTestToken ports
// seedBuiltInExternalIntegrationTestToken (PostgreSQL variant, including the
// token-free source row insert/update at its head).
func seedPostgresExternalIntegrationTestToken(ctx context.Context, client postgresSeedClient, exec func(string, ...any) error, options SeedOptions, now string) error {
	source := pgSeedExternalIntegrationSource
	scopesJSON := source.ScopesJSON
	if err := exec(pgSeedExternalIntegrationSourceInsert, source.ID, source.Name, scopesJSON, source.RateLimitsJSON, source.Notes, now, now); err != nil {
		return err
	}
	if err := exec(pgSeedExternalIntegrationSourceUpdate, source.Name, scopesJSON, source.RateLimitsJSON, source.Notes, now, source.ID); err != nil {
		return err
	}
	var existingTokenID string
	err := client.QueryRowContext(ctx, pgSeedExternalIntegrationTokenSelect, externalIntegrationTestTokenID).Scan(&existingTokenID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("postgres seed select external integration token: %w", err)
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
		return exec(pgSeedExternalIntegrationTokenInsert,
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
	return exec(pgSeedExternalIntegrationTokenUpdate, source.ID, externalIntegrationTestTokenName, scopesJSON, now, externalIntegrationTestTokenID)
}
