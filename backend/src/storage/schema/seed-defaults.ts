import type { DatabaseSync } from 'node:sqlite'

import { createApiKey, encryptJson, hashPassword, hashSecret } from '../crypto.js'
import { GPT_VENDOR_CODE, HYBRID_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { listProviderModelPricing } from '../../modules/model-pricing/model-pricing.service.js'
import { providerModelCatalogId } from '../provider-model-catalog-id.js'
import {
  builtInExternalIntegrationTestRateLimits,
  builtInExternalIntegrationTestSourceId,
  builtInExternalIntegrationTestSourceName,
  builtInExternalIntegrationTestTokenId,
  builtInExternalIntegrationTestTokenName,
  builtInExternalIntegrationTestTokenNotes,
  createExternalIntegrationSourceTokenValue,
  externalIntegrationScopeOptions,
  hashExternalIntegrationSourceTokenValue
} from '../external-integration-source-constants.js'
import { defaultRequestQuotaHourlyWindowHours } from '../request-quota-limits.js'
import { parseJsonArray } from '../value-utils.js'
import {
  DEFAULT_BUILT_IN_GROUPS,
  DEFAULT_GLOBAL_SETTINGS,
  DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS,
  DEFAULT_PROVIDER_SEEDS,
  DEFAULT_PROTOCOL_ENDPOINT_FAMILY_SEEDS,
  DEFAULT_PROTOCOL_SEEDS,
  DEFAULT_SYSTEM_SETTINGS
} from '../schema-defaults.js'

export function seedDefaults(database: DatabaseSync): void {
  const now = new Date().toISOString()

  database
    .prepare(`
      INSERT OR IGNORE INTO system_accounts (
        id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      'sys_admin',
      'admin',
      '超级管理员',
      '系统默认超级管理员账户',
      'super_admin',
      'active',
      hashPassword('admin'),
      0,
      0,
      now,
      now
    )

  const globalStatement = database.prepare(`
    INSERT OR IGNORE INTO global_settings (key, value_json, updated_at)
    VALUES (?, ?, ?)
  `)
  for (const [key, value] of DEFAULT_GLOBAL_SETTINGS) {
    globalStatement.run(key, JSON.stringify(value), now)
  }

  const quotaWindowStatement = database.prepare(`
    INSERT OR IGNORE INTO request_quota_hourly_window_configs (window_hours, created_at, updated_at)
    VALUES (?, ?, ?)
  `)
  for (const hours of defaultRequestQuotaHourlyWindowHours) {
    quotaWindowStatement.run(hours, now, now)
  }

  database.exec(`
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
  `)

  const providerStatement = database.prepare(`
    INSERT OR IGNORE INTO providers (
      id, code, name, description, parent_code, enabled, default_supported_models_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const providerDefaultsStatement = database.prepare(`
    UPDATE providers
    SET default_supported_models_json = ?, updated_at = ?
    WHERE code = ?
      AND (default_supported_models_json IS NULL OR trim(default_supported_models_json) = '' OR default_supported_models_json = '[]')
  `)
  for (const provider of DEFAULT_PROVIDER_SEEDS) {
    const defaultSupportedModelsJson = JSON.stringify(provider.defaultSupportedModels)
    providerStatement.run(
      provider.id,
      provider.code,
      provider.name,
      provider.description,
      provider.parentCode,
      provider.enabled,
      defaultSupportedModelsJson,
      now,
      now
    )
    providerDefaultsStatement.run(defaultSupportedModelsJson, now, provider.code)
  }
  database.prepare(`
    UPDATE providers
    SET default_supported_models_json = coalesce((
      SELECT json_group_array(value)
      FROM json_each(providers.default_supported_models_json)
      WHERE value <> 'codex-auto-review'
    ), '[]'), updated_at = ?
    WHERE code = ?
      AND json_valid(default_supported_models_json)
      AND json_type(default_supported_models_json) = 'array'
  `).run(now, GPT_VENDOR_CODE)

  const currentBuiltInModelKeys = new Set<string>()
  const modelStatement = database.prepare(`
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
  `)
  for (const provider of DEFAULT_PROVIDER_SEEDS) {
    if (provider.code === 'hybrid' || provider.code === 'openai') continue
    for (const model of listProviderModelPricing(provider.code)) {
      const modelId = providerModelCatalogId(provider.code, model.model)
      currentBuiltInModelKeys.add(`${provider.code}\u0000${model.model}`)
      modelStatement.run(
        modelId,
        provider.code,
        model.model,
        model.mode ?? null,
        model.catalogOrder ?? null,
        model.releaseDate ?? null,
        model.shutdownDate ?? null,
        JSON.stringify(model.supportedApiProtocols),
        JSON.stringify(model.supportedServiceTiers),
        JSON.stringify(model.supportedReasoningEfforts),
        model.defaultReasoningEffort ?? null,
        JSON.stringify(model.codexSupportedReasoningLevels),
        model.codexDefaultReasoningLevel ?? null,
        model.codexMultiAgentVersion ?? null,
        model.contextWindowTokens ?? null,
        model.maxInputTokens ?? null,
        model.maxOutputTokens ?? null,
        model.maxTokens ?? null,
        model.inputUsdPer1M ?? null,
        model.outputUsdPer1M ?? null,
        model.cachedInputUsdPer1M ?? null,
        model.cacheWriteUsdPer1M ?? null,
        model.cacheWrite1hUsdPer1M ?? null,
        model.cacheStorageUsdPer1MPerHour ?? null,
        JSON.stringify(model.serviceTierPrices ?? {}),
        model.longContextInputTokenThreshold ?? null,
        model.longContextInputTokenThresholdInclusive ? 1 : 0,
        model.longContextInputCostMultiplier ?? null,
        model.longContextOutputCostMultiplier ?? null,
        model.imageInputUsdPer1M ?? null,
        model.imageOutputUsdPer1M ?? null,
        model.audioInputUsdPer1M ?? null,
        model.audioOutputUsdPer1M ?? null,
        model.outputUsdPerImage ?? null,
        model.supportsPromptCaching ? 1 : 0,
        model.catalogVisible === false ? 0 : 1,
        model.source,
        now,
        now
      )
    }
  }
  const generatedModelRows = database.prepare(`
    SELECT id, provider_code, model FROM provider_model_catalog
    WHERE provider_code IN ('gpt', 'anthropic', 'gemini', 'deepseek', 'glm', 'xai')
  `).all() as unknown as Array<{ id: string; provider_code: string; model: string }>
  const disableStaleGeneratedModel = database.prepare(`
    UPDATE provider_model_catalog
    SET status = 'disabled', catalog_visible = 0, updated_at = ?
    WHERE id = ?
  `)
  for (const row of generatedModelRows) {
    if (!currentBuiltInModelKeys.has(`${row.provider_code}\u0000${row.model}`)) disableStaleGeneratedModel.run(now, row.id)
  }

  const protocolStatement = database.prepare(`
      INSERT OR IGNORE INTO protocols (
        id, code, version, name, description, enabled, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const protocol of DEFAULT_PROTOCOL_SEEDS) {
    protocolStatement.run(
      protocol.id,
      protocol.code,
      protocol.version,
      protocol.name,
      protocol.description,
      protocol.enabled,
      now,
      now
    )
  }

  const endpointFamilyStatement = database.prepare(`
    INSERT OR IGNORE INTO protocol_endpoint_families (
      id, protocol_code, protocol_version, family_code, name, description, enabled, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const family of DEFAULT_PROTOCOL_ENDPOINT_FAMILY_SEEDS) {
    endpointFamilyStatement.run(
      family.id,
      family.protocolCode,
      family.protocolVersion,
      family.code,
      family.name,
      family.description,
      family.enabled,
      now,
      now
    )
  }

  const profileStatement = database.prepare(`
    INSERT OR IGNORE INTO provider_protocol_profiles (
      id, provider_code, name, description, enabled, protocol_code, protocol_version,
      base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const profileSeeds = DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS
  const profileSeedBaseTime = Date.parse(now)
  for (const [index, profile] of profileSeeds.entries()) {
    const profileUpdatedAt = Number.isFinite(profileSeedBaseTime)
      ? new Date(profileSeedBaseTime + index).toISOString()
      : now
    profileStatement.run(
      profile.id,
      profile.providerCode,
      profile.name,
      profile.description,
      profile.enabled,
      profile.protocolCode,
      profile.protocolVersion,
      profile.baseUrl,
      profile.defaultHealthCheckModel,
      JSON.stringify(profile.accountTypes),
      JSON.stringify(profile.capabilities),
      now,
      profileUpdatedAt
    )
  }
  repairBuiltInProviderProfileAccountTypes(database, profileSeeds, now)

  const profileFamilyStatement = database.prepare(`
    INSERT OR IGNORE INTO provider_protocol_profile_families (
      profile_id, family_code, enabled, capabilities_json, created_at, updated_at
    ) VALUES (?, ?, 1, '[]', ?, ?)
  `)
  for (const profile of profileSeeds) {
    for (const familyCode of profile.endpointFamilies) {
      profileFamilyStatement.run(profile.id, familyCode, now, now)
    }
  }

  seedAdminDefaultBuiltInGroups(database, now)
  seedAdminDefaultRouteStrategiesAndApiKeys(database, now)
  seedAdminChatApiKey(database, now)
  seedBuiltInExternalIntegrationTestToken(database, now)

  const statement = database.prepare(`
    INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES (?, ?, ?, ?)
  `)

  for (const [key, value] of DEFAULT_SYSTEM_SETTINGS) {
    statement.run('sys_admin', key, JSON.stringify(value), now)
  }
}

function repairBuiltInProviderProfileAccountTypes(
  database: DatabaseSync,
  profiles: ReadonlyArray<{ id: string; accountTypes: readonly string[] }>,
  timestamp: string
): void {
  const selectStatement = database.prepare(`
    SELECT account_types_json
    FROM provider_protocol_profiles
    WHERE id = ?
  `)
  const updateStatement = database.prepare(`
    UPDATE provider_protocol_profiles
    SET account_types_json = ?, updated_at = ?
    WHERE id = ?
  `)
  for (const profile of profiles) {
    const row = selectStatement.get(profile.id) as { account_types_json?: unknown } | undefined
    if (!row || typeof row.account_types_json !== 'string') continue
    let current: string[]
    try {
      current = parseJsonArray(row.account_types_json)
    } catch {
      continue
    }
    const merged = [...new Set([...current, ...profile.accountTypes])]
    if (merged.length === current.length && merged.every((item, index) => item === current[index])) continue
    updateStatement.run(JSON.stringify(merged), timestamp, profile.id)
  }
}

function seedAdminChatApiKey(database: DatabaseSync, timestamp: string): void {
  const existing = database.prepare("SELECT id FROM api_keys WHERE system_account_id = 'sys_admin' AND purpose = 'chat' LIMIT 1").get() as { id?: string } | undefined
  if (existing?.id) return
  const group = database.prepare(`
    SELECT id FROM groups
    WHERE system_account_id = 'sys_admin' AND provider_code = ? AND is_default = 1
    ORDER BY created_at ASC, id ASC LIMIT 1
  `).get(GPT_VENDOR_CODE) as { id?: string } | undefined
  if (!group?.id) return
  const routeStrategyId = defaultRouteStrategyIdForGroup(group.id)
  const route = database.prepare("SELECT id, name FROM route_strategies WHERE id = ? AND status = 'active' LIMIT 1").get(routeStrategyId) as { id?: string; name?: string } | undefined
  if (!route?.id) return
  const key = createApiKey()
  database.prepare(`
    INSERT OR IGNORE INTO api_keys (
      id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
      key_secret_encrypted, status, is_default, purpose, expires_at, quota_limits_json, availability_schedule_json,
      availability_schedule_next_check_at, created_at, updated_at
    ) VALUES (?, 'sys_admin', ?, 'AI 对话 API Key', ?, ?, ?, ?, ?, 'active', 0, 'chat', NULL, NULL, NULL, NULL, ?, ?)
  `).run(
    'key_chat_sys_admin', route.id, `AI 对话专用 API Key，默认绑定${route.name ?? 'GPT 路由'}。`,
    hashSecret(key), key.slice(0, 8), key.slice(-8), encryptJson({ key }), timestamp, timestamp
  )
}

function seedAdminDefaultRouteStrategiesAndApiKeys(database: DatabaseSync, timestamp: string): void {
  const routeStatement = database.prepare(`
    INSERT OR IGNORE INTO route_strategies (
      id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at
    )
    VALUES (?, 'sys_admin', ?, ?, 'normal', 'active', 1, NULL, ?, ?)
  `)
  const routeGroupStatement = database.prepare(`
    INSERT OR IGNORE INTO route_strategy_groups (
      id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
    )
    VALUES (?, ?, 'sys_admin', ?, 1, 1, 'active', ?, ?)
  `)
  const apiKeyStatement = database.prepare(`
    INSERT OR IGNORE INTO api_keys (
      id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
      key_secret_encrypted, status, is_default, expires_at, quota_limits_json, availability_schedule_json,
      availability_schedule_next_check_at, created_at, updated_at
    )
    VALUES (?, 'sys_admin', ?, ?, ?, ?, ?, ?, ?, 'active', 1, NULL, NULL, NULL, NULL, ?, ?)
  `)
  for (const group of defaultRouteSeedGroups(database)) {
    const routeStrategyName = defaultRouteStrategyNameForGroup(group.name)
    const routeStrategyId = defaultRouteStrategyIdForGroup(group.id)
    routeStatement.run(
      routeStrategyId,
      routeStrategyName,
      `系统默认普通路由，绑定${group.name}。`,
      timestamp,
      timestamp
    )
    if (!routeStrategyExists(database, routeStrategyId)) {
      continue
    }
    routeGroupStatement.run(
      defaultRouteStrategyGroupBindingIdForGroup(group.id),
      routeStrategyId,
      group.id,
      timestamp,
      timestamp
    )
    if (existingDefaultApiKeyIdForRouteStrategy(database, routeStrategyId)) {
      continue
    }
    const apiKey = createApiKey()
    apiKeyStatement.run(
      defaultApiKeyIdForRouteStrategy(routeStrategyId),
      routeStrategyId,
      defaultApiKeyNameForRouteStrategy(routeStrategyName),
      `系统默认 API Key，绑定${routeStrategyName}。`,
      hashSecret(apiKey),
      apiKey.slice(0, 8),
      apiKey.slice(-8),
      encryptJson({ key: apiKey }),
      timestamp,
      timestamp
    )
  }
}

function defaultRouteSeedGroups(database: DatabaseSync): Array<{ id: string; name: string }> {
  return database
    .prepare(`
      SELECT id, name
      FROM groups
      WHERE system_account_id = 'sys_admin'
        AND is_default = 1
        AND provider_code <> ?
      ORDER BY created_at ASC, id ASC
    `)
    .all(HYBRID_PROVIDER_CODE) as Array<{ id: string; name: string }>
}

function routeStrategyExists(database: DatabaseSync, routeStrategyId: string): boolean {
  const row = database
    .prepare('SELECT id FROM route_strategies WHERE id = ? LIMIT 1')
    .get(routeStrategyId) as { id?: string } | undefined
  return Boolean(row?.id)
}

function existingDefaultApiKeyIdForRouteStrategy(database: DatabaseSync, routeStrategyId: string): string | undefined {
  const row = database
    .prepare('SELECT id FROM api_keys WHERE route_strategy_id = ? AND is_default = 1 LIMIT 1')
    .get(routeStrategyId) as { id?: string } | undefined
  return row?.id
}

function defaultRouteStrategyIdForGroup(groupId: string): string {
  return groupId.replace(/^grp_/, 'route_strategy_')
}

function defaultRouteStrategyGroupBindingIdForGroup(groupId: string): string {
  return groupId.replace(/^grp_/, 'rsg_')
}

function defaultRouteStrategyNameForGroup(groupName: string): string {
  return groupName.replace(/分组$/, '路由')
}

function defaultApiKeyIdForRouteStrategy(routeStrategyId: string): string {
  return routeStrategyId.replace(/^route_strategy_/, 'key_default_')
}

function defaultApiKeyNameForRouteStrategy(routeStrategyName: string): string {
  return routeStrategyName.replace(/路由$/, 'API Key')
}

function seedAdminDefaultBuiltInGroups(database: DatabaseSync, timestamp: string): void {
  const insertStatement = database.prepare(`
    INSERT OR IGNORE INTO groups (
      id, system_account_id, name, provider_code,
      description, enabled, is_default, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)
  `)
  const updateStatement = database.prepare('UPDATE groups SET is_default = 1 WHERE id = ? AND system_account_id = ?')
  for (const group of DEFAULT_BUILT_IN_GROUPS) {
    const existingDefault = database
      .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND is_default = 1 ORDER BY updated_at DESC, id ASC LIMIT 1')
      .get(group.systemAccountId, group.providerCode) as { id?: string } | undefined
    if (existingDefault?.id) {
      continue
    }
    insertStatement.run(
      group.id,
      group.systemAccountId,
      group.name,
      group.providerCode,
      group.description,
      timestamp,
      timestamp
    )
    updateStatement.run(group.id, group.systemAccountId)
  }
}

function seedBuiltInExternalIntegrationTestToken(database: DatabaseSync, timestamp: string): void {
  const scopesJson = JSON.stringify(externalIntegrationScopeOptions.map((item) => item.value).sort())
  const rateLimitsJson = JSON.stringify(builtInExternalIntegrationTestRateLimits)
  database
    .prepare(`
      INSERT OR IGNORE INTO external_integration_sources (
        id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at
      ) VALUES (?, ?, 'active', ?, ?, NULL, ?, ?, ?)
    `)
    .run(
      builtInExternalIntegrationTestSourceId,
      builtInExternalIntegrationTestSourceName,
      scopesJson,
      rateLimitsJson,
      builtInExternalIntegrationTestTokenNotes,
      timestamp,
      timestamp
    )

  database
    .prepare(`
      UPDATE external_integration_sources
      SET name = ?,
          scopes_json = ?,
          rate_limits_json = ?,
          expires_at = NULL,
          notes = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(
      builtInExternalIntegrationTestSourceName,
      scopesJson,
      rateLimitsJson,
      builtInExternalIntegrationTestTokenNotes,
      timestamp,
      builtInExternalIntegrationTestSourceId
    )

  const existingToken = database
    .prepare('SELECT id FROM external_integration_source_tokens WHERE id = ?')
    .get(builtInExternalIntegrationTestTokenId) as { id?: string } | undefined
  if (!existingToken) {
    const token = createExternalIntegrationSourceTokenValue()
    database
      .prepare(`
        INSERT INTO external_integration_source_tokens (
          id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, 'active', ?, NULL, ?, ?)
      `)
      .run(
        builtInExternalIntegrationTestTokenId,
        builtInExternalIntegrationTestSourceId,
        builtInExternalIntegrationTestTokenName,
        hashExternalIntegrationSourceTokenValue(token),
        encryptJson({ token }),
        token.slice(0, 8),
        token.slice(-8),
        scopesJson,
        timestamp,
        timestamp
      )
  } else {
    database
      .prepare(`
        UPDATE external_integration_source_tokens
        SET source_ref_id = ?,
            name = ?,
            scopes_json = ?,
            expires_at = NULL,
            updated_at = ?
        WHERE id = ?
      `)
      .run(
        builtInExternalIntegrationTestSourceId,
        builtInExternalIntegrationTestTokenName,
        scopesJson,
        timestamp,
        builtInExternalIntegrationTestTokenId
      )
  }
}
