import type { DatabaseClient } from './database-client.js'
import { createApiKey, encryptJson, hashPassword, hashSecret } from './crypto.js'
import { GPT_VENDOR_CODE, HYBRID_PROVIDER_CODE } from '../domain/provider-protocol.js'
import { listProviderModelPricing } from '../modules/model-pricing/model-pricing.service.js'
import { providerModelCatalogId } from './provider-model-catalog-id.js'
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
} from './external-integration-source-constants.js'
import { defaultRequestQuotaHourlyWindowHours } from './request-quota-limits.js'
import { parseJsonArray } from './value-utils.js'
import {
  DEFAULT_BUILT_IN_GROUPS,
  DEFAULT_GLOBAL_SETTINGS,
  DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS,
  DEFAULT_PROVIDER_SEEDS,
  DEFAULT_PROTOCOL_ENDPOINT_FAMILY_SEEDS,
  DEFAULT_PROTOCOL_SEEDS,
  DEFAULT_SYSTEM_SETTINGS
} from './schema-defaults.js'

const businessSchemaName = 'juhe_business'

export async function seedPostgresDefaults(client: Pick<DatabaseClient, 'execute' | 'one'>): Promise<{ statementCount: number }> {
  const now = new Date().toISOString()
  let statementCount = 0

  async function query(sql: string, values: readonly unknown[] = []): Promise<void> {
    await client.execute(sql, values)
    statementCount += 1
  }

  await query(
    `
      INSERT INTO ${businessTable('system_accounts')} (
        id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
      ON CONFLICT DO NOTHING
    `,
    [
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
    ]
  )

  for (const [key, value] of DEFAULT_GLOBAL_SETTINGS) {
    await query(
      `
        INSERT INTO ${businessTable('global_settings')} (key, value_json, updated_at)
        VALUES ($1, $2, $3)
        ON CONFLICT DO NOTHING
      `,
      [key, JSON.stringify(value), now]
    )
  }

  for (const hours of defaultRequestQuotaHourlyWindowHours) {
    await query(
      `
        INSERT INTO ${businessTable('request_quota_hourly_window_configs')} (window_hours, created_at, updated_at)
        VALUES ($1, $2, $3)
        ON CONFLICT DO NOTHING
      `,
      [hours, now, now]
    )
  }

  await query(`
    WITH inserted AS (
      INSERT INTO ${businessTable('request_quota_hourly_window_scope_bindings')} (
        system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
      )
      SELECT system_account_id, 'api_key', id, 'api_key', id,
        (quota_limits_json::jsonb #>> '{hourly,hours}')::integer, created_at, updated_at
      FROM ${businessTable('api_keys')}
      WHERE status = 'active'
        AND quota_limits_json IS NOT NULL
        AND quota_limits_json::jsonb #>> '{hourly,enabled}' = 'true'
        AND quota_limits_json::jsonb #>> '{hourly,hours}' ~ '^[0-9]+$'
        AND (quota_limits_json::jsonb #>> '{hourly,hours}')::integer BETWEEN 1 AND 720
      ON CONFLICT(system_account_id, scope_type, scope_id) DO NOTHING
      RETURNING system_account_id, scope_type, scope_id, created_at, updated_at
    )
    INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    )
    SELECT system_account_id, scope_type, scope_id, 1, created_at, updated_at FROM inserted
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
      updated_at = EXCLUDED.updated_at
  `)

  await query(`
    WITH inserted AS (
      INSERT INTO ${businessTable('request_quota_hourly_window_scope_bindings')} (
        system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
      )
      SELECT CASE WHEN ra.resource_type = 'account' THEN ra.grantee_system_account_id ELSE ra.resource_owner_system_account_id END,
        CASE WHEN ra.resource_type = 'account' THEN 'account_authorization' ELSE 'group_authorization' END,
        ra.id, 'resource_authorization_grant', grants.id,
        (ra.limits_json::jsonb #>> '{hourly,hours}')::integer, ra.created_at, ra.updated_at
      FROM ${businessTable('resource_authorizations')} ra
      INNER JOIN ${businessTable('resource_authorization_grants')} grants
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
        AND ra.limits_json::jsonb #>> '{hourly,enabled}' = 'true'
        AND ra.limits_json::jsonb #>> '{hourly,hours}' ~ '^[0-9]+$'
        AND (ra.limits_json::jsonb #>> '{hourly,hours}')::integer BETWEEN 1 AND 720
      ON CONFLICT(system_account_id, scope_type, scope_id) DO NOTHING
      RETURNING system_account_id, scope_type, scope_id, created_at, updated_at
    )
    INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    )
    SELECT system_account_id, scope_type, scope_id, 1, created_at, updated_at FROM inserted
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
      updated_at = EXCLUDED.updated_at
  `)

  await query(`
    WITH candidates AS (
      SELECT DISTINCT
        CASE WHEN ra.resource_type = 'account' THEN ra.grantee_system_account_id ELSE ra.resource_owner_system_account_id END AS system_account_id,
        CASE WHEN ra.resource_type = 'account' THEN 'account_authorization_team' ELSE 'group_authorization_team' END AS scope_type,
        CASE WHEN ra.resource_type = 'account' THEN instance_accounts.id || ':' || ra.effective_source_team_id ELSE ra.resource_id || ':' || ra.effective_source_team_id END AS scope_id,
        grants.id AS source_id,
        (ra.limits_json::jsonb #>> '{hourly,hours}')::integer AS window_hours,
        ra.created_at,
        ra.updated_at
      FROM ${businessTable('resource_authorizations')} ra
      INNER JOIN ${businessTable('resource_authorization_grants')} grants
        ON grants.resource_type = ra.resource_type
        AND grants.resource_id = ra.resource_id
        AND grants.grantee_type = 'team'
        AND grants.grantee_team_id = ra.effective_source_team_id
        AND grants.status = 'active'
      LEFT JOIN ${businessTable('accounts')} instance_accounts
        ON ra.resource_type = 'account'
        AND instance_accounts.authorization_instance_authorization_id = ra.id
        AND instance_accounts.system_account_id = ra.grantee_system_account_id
        AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
        AND instance_accounts.deleted_at IS NULL
      WHERE ra.status = 'active'
        AND ra.effective_source_type = 'team'
        AND (ra.resource_type = 'group' OR instance_accounts.id IS NOT NULL)
        AND ra.limits_json IS NOT NULL
        AND ra.limits_json::jsonb #>> '{hourly,enabled}' = 'true'
        AND ra.limits_json::jsonb #>> '{hourly,hours}' ~ '^[0-9]+$'
        AND (ra.limits_json::jsonb #>> '{hourly,hours}')::integer BETWEEN 1 AND 720
    ), inserted AS (
      INSERT INTO ${businessTable('request_quota_hourly_window_scope_bindings')} (
        system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
      )
      SELECT system_account_id, scope_type, scope_id, 'resource_authorization_grant', source_id,
        window_hours, created_at, updated_at
      FROM candidates
      WHERE true
      ON CONFLICT(system_account_id, scope_type, scope_id) DO NOTHING
      RETURNING system_account_id, scope_type, scope_id, created_at, updated_at
    )
    INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    )
    SELECT system_account_id, scope_type, scope_id, 1, created_at, updated_at FROM inserted
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
      updated_at = EXCLUDED.updated_at
  `)

  for (const provider of DEFAULT_PROVIDER_SEEDS) {
    await query(
      `
        INSERT INTO ${businessTable('providers')} (
          id, code, name, description, parent_code, enabled, default_supported_models_json, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT DO NOTHING
      `,
      [
        provider.id,
        provider.code,
        provider.name,
        provider.description,
        provider.parentCode,
        provider.enabled,
        JSON.stringify(provider.defaultSupportedModels),
        now,
        now
      ]
    )
    await query(
      `
        UPDATE ${businessTable('providers')}
        SET default_supported_models_json = $1, updated_at = $2
        WHERE code = $3
          AND (default_supported_models_json IS NULL OR btrim(default_supported_models_json) = '' OR default_supported_models_json = '[]')
      `,
      [JSON.stringify(provider.defaultSupportedModels), now, provider.code]
    )
  }

  const modelSeedRows: string[] = []
  const modelSeedValues: unknown[] = []
  for (const provider of DEFAULT_PROVIDER_SEEDS) {
    if (provider.code === HYBRID_PROVIDER_CODE || provider.code === 'openai') continue
    for (const model of listProviderModelPricing(provider.code)) {
      const firstParameterIndex = modelSeedValues.length + 1
      modelSeedValues.push(
          providerModelCatalogId(provider.code, model.model),
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
          model.longContextInputTokenThresholdInclusive === true,
          model.longContextInputCostMultiplier ?? null,
          model.longContextOutputCostMultiplier ?? null,
          model.imageInputUsdPer1M ?? null,
          model.imageOutputUsdPer1M ?? null,
          model.audioInputUsdPer1M ?? null,
          model.audioOutputUsdPer1M ?? null,
          model.outputUsdPerImage ?? null,
          model.supportsPromptCaching === true,
          model.catalogVisible !== false,
          model.source,
          now,
          now
      )
      const placeholders = Array.from({ length: 39 }, (_item, index) => `$${firstParameterIndex + index}`)
      modelSeedRows.push(`(${placeholders.slice(0, 3).join(', ')}, 'active', ${placeholders.slice(3).join(', ')})`)
    }
  }
  if (modelSeedRows.length > 0) {
    await query(
      `
        INSERT INTO ${businessTable('provider_model_catalog')} (
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
          ${modelSeedRows.join(',\n          ')}
        ON CONFLICT DO NOTHING
      `,
      modelSeedValues
    )
  }

  for (const protocol of DEFAULT_PROTOCOL_SEEDS) {
    await query(
      `
        INSERT INTO ${businessTable('protocols')} (
          id, code, version, name, description, enabled, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
        ON CONFLICT DO NOTHING
      `,
      [
        protocol.id,
        protocol.code,
        protocol.version,
        protocol.name,
        protocol.description,
        protocol.enabled,
        now,
        now
      ]
    )
  }

  for (const family of DEFAULT_PROTOCOL_ENDPOINT_FAMILY_SEEDS) {
    await query(
      `
        INSERT INTO ${businessTable('protocol_endpoint_families')} (
          id, protocol_code, protocol_version, family_code, name, description, enabled, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT DO NOTHING
      `,
      [
        family.id,
        family.protocolCode,
        family.protocolVersion,
        family.code,
        family.name,
        family.description,
        family.enabled,
        now,
        now
      ]
    )
  }

  const profileSeeds = DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS
  const profileSeedBaseTime = Date.parse(now)
  for (const [index, profile] of profileSeeds.entries()) {
    const profileUpdatedAt = Number.isFinite(profileSeedBaseTime)
      ? new Date(profileSeedBaseTime + index).toISOString()
      : now
    await query(
      `
        INSERT INTO ${businessTable('provider_protocol_profiles')} (
          id, provider_code, name, description, enabled, protocol_code, protocol_version,
          base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
        ON CONFLICT DO NOTHING
      `,
      [
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
      ]
    )
  }
  await repairBuiltInProviderProfileAccountTypes(client, profileSeeds, now, query)

  for (const profile of profileSeeds) {
    for (const familyCode of profile.endpointFamilies) {
      await query(
        `
          INSERT INTO ${businessTable('provider_protocol_profile_families')} (
            profile_id, family_code, enabled, capabilities_json, created_at, updated_at
          ) VALUES ($1, $2, 1, '[]', $3, $4)
          ON CONFLICT DO NOTHING
        `,
        [profile.id, familyCode, now, now]
      )
    }
  }

  for (const group of DEFAULT_BUILT_IN_GROUPS) {
    const existingDefault = await client.one<{ id?: string }>(
      `
        SELECT id
        FROM ${businessTable('groups')}
        WHERE system_account_id = ? AND provider_code = ? AND is_default = 1
        ORDER BY updated_at DESC, id ASC
        LIMIT 1
      `,
      [group.systemAccountId, group.providerCode]
    )
    if (existingDefault?.id) {
      continue
    }
    await query(
      `
        INSERT INTO ${businessTable('groups')} (
          id, system_account_id, name, provider_code,
          description, enabled, is_default, created_at, updated_at
        )
        VALUES ($1, $2, $3, $4, $5, 1, 1, $6, $7)
        ON CONFLICT DO NOTHING
      `,
      [
        group.id,
        group.systemAccountId,
        group.name,
        group.providerCode,
        group.description,
        now,
        now
      ]
    )
    await query(
      `UPDATE ${businessTable('groups')} SET is_default = 1 WHERE id = $1 AND system_account_id = $2`,
      [group.id, group.systemAccountId]
    )
  }

  await seedAdminDefaultRouteStrategiesAndApiKeys(client, query, now)
  await seedAdminChatApiKey(client, query, now)
  await seedBuiltInExternalIntegrationTestToken(client, query, now)

  for (const [key, value] of DEFAULT_SYSTEM_SETTINGS) {
    await query(
      `
        INSERT INTO ${businessTable('system_settings')} (system_account_id, key, value_json, updated_at)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT DO NOTHING
      `,
      ['sys_admin', key, JSON.stringify(value), now]
    )
  }

  return { statementCount }
}

async function repairBuiltInProviderProfileAccountTypes(
  client: Pick<DatabaseClient, 'one'>,
  profiles: ReadonlyArray<{ id: string; accountTypes: readonly string[] }>,
  timestamp: string,
  update: (sql: string, values?: readonly unknown[]) => Promise<void>
): Promise<void> {
  for (const profile of profiles) {
    const row = await client.one<{ account_types_json?: unknown }>(
      `
        SELECT account_types_json
        FROM ${businessTable('provider_protocol_profiles')}
        WHERE id = $1
      `,
      [profile.id]
    )
    if (!row || typeof row.account_types_json !== 'string') continue
    let current: string[]
    try {
      current = parseJsonArray(row.account_types_json)
    } catch {
      continue
    }
    const merged = [...new Set([...current, ...profile.accountTypes])]
    if (merged.length === current.length && merged.every((item, index) => item === current[index])) continue
    await update(
      `
        UPDATE ${businessTable('provider_protocol_profiles')}
        SET account_types_json = $1, updated_at = $2
        WHERE id = $3
      `,
      [JSON.stringify(merged), timestamp, profile.id]
    )
  }
}

async function seedAdminChatApiKey(
  client: Pick<DatabaseClient, 'one'>,
  query: (sql: string, values?: readonly unknown[]) => Promise<void>,
  timestamp: string
): Promise<void> {
  const existing = await client.one<{ id?: string }>(`
    SELECT id FROM ${businessTable('api_keys')} WHERE system_account_id = 'sys_admin' AND purpose = 'chat' LIMIT 1
  `)
  if (existing?.id) return
  const group = await client.one<{ id?: string }>(`
    SELECT id FROM ${businessTable('groups')}
    WHERE system_account_id = 'sys_admin' AND provider_code = $1 AND is_default = 1
    ORDER BY created_at ASC, id ASC LIMIT 1
  `, [GPT_VENDOR_CODE])
  if (!group?.id) return
  const routeStrategyId = defaultRouteStrategyIdForGroup(group.id)
  const route = await client.one<{ id?: string; name?: string }>(`
    SELECT id, name FROM ${businessTable('route_strategies')} WHERE id = $1 AND status = 'active' LIMIT 1
  `, [routeStrategyId])
  if (!route?.id) return
  const key = createApiKey()
  await query(`
    INSERT INTO ${businessTable('api_keys')} (
      id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
      key_secret_encrypted, status, is_default, purpose, expires_at, quota_limits_json, availability_schedule_json,
      availability_schedule_next_check_at, created_at, updated_at
    ) VALUES ($1, 'sys_admin', $2, 'AI 对话 API Key', $3, $4, $5, $6, $7, 'active', 0, 'chat', NULL, NULL, NULL, NULL, $8, $9)
    ON CONFLICT DO NOTHING
  `, [
    'key_chat_sys_admin', route.id, `AI 对话专用 API Key，默认绑定${route.name ?? 'GPT 路由'}。`,
    hashSecret(key), key.slice(0, 8), key.slice(-8), encryptJson({ key }), timestamp, timestamp
  ])
}

async function seedAdminDefaultRouteStrategiesAndApiKeys(
  client: Pick<DatabaseClient, 'one'>,
  query: (sql: string, values?: readonly unknown[]) => Promise<void>,
  timestamp: string
): Promise<void> {
  const defaultGroups = DEFAULT_BUILT_IN_GROUPS.filter((group) => group.systemAccountId === 'sys_admin' && group.providerCode !== HYBRID_PROVIDER_CODE)
  for (const groupSeed of defaultGroups) {
    const group = await client.one<{ id?: string; name?: string }>(
      `
        SELECT id, name
        FROM ${businessTable('groups')}
        WHERE system_account_id = $1
          AND provider_code = $2
          AND is_default = 1
        ORDER BY updated_at DESC, id ASC
        LIMIT 1
      `,
      [groupSeed.systemAccountId, groupSeed.providerCode]
    )
    if (!group?.id) {
      continue
    }
    const groupName = group.name ?? groupSeed.name
    const routeStrategyName = defaultRouteStrategyNameForGroup(groupName)
    const routeStrategyId = defaultRouteStrategyIdForGroup(group.id)
    await query(
      `
        INSERT INTO ${businessTable('route_strategies')} (
          id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at
        ) VALUES ($1, 'sys_admin', $2, $3, 'normal', 'active', 1, NULL, $4, $5)
        ON CONFLICT DO NOTHING
      `,
      [
        routeStrategyId,
        routeStrategyName,
        `系统默认普通路由，绑定${groupName}。`,
        timestamp,
        timestamp
      ]
    )
    const routeStrategy = await client.one<{ id?: string }>(
      `
        SELECT id
        FROM ${businessTable('route_strategies')}
        WHERE id = $1
        LIMIT 1
      `,
      [routeStrategyId]
    )
    if (!routeStrategy?.id) {
      continue
    }
    await query(
      `
        INSERT INTO ${businessTable('route_strategy_groups')} (
          id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
        ) VALUES ($1, $2, 'sys_admin', $3, 1, 1, 'active', $4, $5)
        ON CONFLICT DO NOTHING
      `,
      [
        defaultRouteStrategyGroupBindingIdForGroup(group.id),
        routeStrategyId,
        group.id,
        timestamp,
        timestamp
      ]
    )
    const existingApiKey = await client.one<{ id?: string }>(
      `
        SELECT id
        FROM ${businessTable('api_keys')}
        WHERE route_strategy_id = $1 AND is_default = 1
        LIMIT 1
      `,
      [routeStrategyId]
    )
    if (existingApiKey?.id) {
      continue
    }
    const apiKey = createApiKey()
    await query(
      `
        INSERT INTO ${businessTable('api_keys')} (
          id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
          key_secret_encrypted, status, is_default, expires_at, quota_limits_json, availability_schedule_json,
          availability_schedule_next_check_at, created_at, updated_at
        ) VALUES ($1, 'sys_admin', $2, $3, $4, $5, $6, $7, $8, 'active', 1, NULL, NULL, NULL, NULL, $9, $10)
        ON CONFLICT DO NOTHING
      `,
      [
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
      ]
    )
  }
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

async function seedBuiltInExternalIntegrationTestToken(
  client: Pick<DatabaseClient, 'one'>,
  query: (sql: string, values?: readonly unknown[]) => Promise<void>,
  timestamp: string
): Promise<void> {
  const scopesJson = JSON.stringify(externalIntegrationScopeOptions.map((item) => item.value).sort())
  const rateLimitsJson = JSON.stringify(builtInExternalIntegrationTestRateLimits)

  await query(
    `
      INSERT INTO ${businessTable('external_integration_sources')} (
        id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at
      ) VALUES ($1, $2, 'active', $3, $4, NULL, $5, $6, $7)
      ON CONFLICT DO NOTHING
    `,
    [
      builtInExternalIntegrationTestSourceId,
      builtInExternalIntegrationTestSourceName,
      scopesJson,
      rateLimitsJson,
      builtInExternalIntegrationTestTokenNotes,
      timestamp,
      timestamp
    ]
  )

  await query(
    `
      UPDATE ${businessTable('external_integration_sources')}
      SET name = $1,
          scopes_json = $2,
          rate_limits_json = $3,
          expires_at = NULL,
          notes = $4,
          updated_at = $5
      WHERE id = $6
    `,
    [
      builtInExternalIntegrationTestSourceName,
      scopesJson,
      rateLimitsJson,
      builtInExternalIntegrationTestTokenNotes,
      timestamp,
      builtInExternalIntegrationTestSourceId
    ]
  )

  const existingToken = await client.one<{ id: string }>(
    `
      SELECT id
      FROM ${businessTable('external_integration_source_tokens')}
      WHERE id = $1
    `,
    [builtInExternalIntegrationTestTokenId]
  )

  if (!existingToken) {
    const token = createExternalIntegrationSourceTokenValue()
    await query(
      `
        INSERT INTO ${businessTable('external_integration_source_tokens')} (
          id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, NULL, $9, $10)
      `,
      [
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
      ]
    )
    return
  }

  await query(
    `
      UPDATE ${businessTable('external_integration_source_tokens')}
      SET source_ref_id = $1,
          name = $2,
          scopes_json = $3,
          expires_at = NULL,
          updated_at = $4
      WHERE id = $5
    `,
    [
      builtInExternalIntegrationTestSourceId,
      builtInExternalIntegrationTestTokenName,
      scopesJson,
      timestamp,
      builtInExternalIntegrationTestTokenId
    ]
  )
}

function businessTable(tableName: string): string {
  return `${quoteIdentifier(businessSchemaName)}.${quoteIdentifier(tableName)}`
}

function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`
}
