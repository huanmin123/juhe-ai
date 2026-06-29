import type { DatabaseSync } from 'node:sqlite'

import { createApiKey, encryptJson, hashPassword, hashSecret } from '../crypto.js'
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
      base_url, default_test_model, account_types_json, capabilities_json, created_at, updated_at
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
      profile.defaultTestModel,
      JSON.stringify(profile.accountTypes),
      JSON.stringify(profile.capabilities),
      now,
      profileUpdatedAt
    )
  }

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
  seedBuiltInExternalIntegrationTestToken(database, now)

  const statement = database.prepare(`
    INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES (?, ?, ?, ?)
  `)

  for (const [key, value] of DEFAULT_SYSTEM_SETTINGS) {
    statement.run('sys_admin', key, JSON.stringify(value), now)
  }
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
      availability_schedule_active, availability_schedule_next_check_at, created_at, updated_at
    )
    VALUES (?, 'sys_admin', ?, ?, ?, ?, ?, ?, ?, 'active', 1, NULL, NULL, NULL, 1, NULL, ?, ?)
  `)
  for (const group of DEFAULT_BUILT_IN_GROUPS.filter((item) => item.systemAccountId === 'sys_admin')) {
    const routeStrategyName = defaultRouteStrategyNameForGroup(group.name)
    const routeStrategyId = defaultRouteStrategyIdForGroup(group.id)
    routeStatement.run(
      routeStrategyId,
      routeStrategyName,
      `系统默认普通路由，绑定${group.name}。`,
      timestamp,
      timestamp
    )
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
      id, system_account_id, name, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
      description, enabled, is_default, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)
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
      group.providerProtocolProfileId,
      group.protocolCode,
      group.protocolVersion,
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
