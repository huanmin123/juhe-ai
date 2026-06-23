import type { DatabaseSync } from 'node:sqlite'

import { encryptJson, hashPassword } from '../crypto.js'
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
  ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED,
  ANTHROPIC_PROTOCOL_ENDPOINT_FAMILY_SEEDS,
  ANTHROPIC_PROTOCOL_SEED,
  ANTHROPIC_PROVIDER_SEED,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_SEED,
  DEEPSEEK_OPENAI_V1_PROFILE_SEED,
  DEEPSEEK_PROVIDER_SEED,
  DEFAULT_BUILT_IN_GROUPS,
  DEFAULT_GLOBAL_SETTINGS,
  DEFAULT_SYSTEM_SETTINGS,
  GLM_CODING_ANTHROPIC_V1_PROFILE_SEED,
  GLM_CODING_OPENAI_V1_PROFILE_SEED,
  GLM_GENERAL_OPENAI_V1_PROFILE_SEED,
  GLM_PROVIDER_SEED,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_SEED,
  OPENAI_COMPATIBLE_PROVIDER_SEED,
  GPT_OPENAI_V1_PROFILE_SEED,
  GPT_PROVIDER_SEED,
  OPENAI_PROTOCOL_ENDPOINT_FAMILY_SEEDS,
  OPENAI_PROTOCOL_SEED
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
      id, code, name, description, parent_code, enabled, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const provider of [OPENAI_COMPATIBLE_PROVIDER_SEED, GPT_PROVIDER_SEED, DEEPSEEK_PROVIDER_SEED, ANTHROPIC_PROVIDER_SEED, GLM_PROVIDER_SEED]) {
    providerStatement.run(
      provider.id,
      provider.code,
      provider.name,
      provider.description,
      provider.parentCode,
      provider.enabled,
      now,
      now
    )
  }

  const protocolStatement = database.prepare(`
      INSERT OR IGNORE INTO protocols (
        id, code, version, name, description, enabled, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const protocol of [OPENAI_PROTOCOL_SEED, ANTHROPIC_PROTOCOL_SEED]) {
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
  for (const family of [...OPENAI_PROTOCOL_ENDPOINT_FAMILY_SEEDS, ...ANTHROPIC_PROTOCOL_ENDPOINT_FAMILY_SEEDS]) {
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
  const profileSeeds = [
    OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_SEED,
    GPT_OPENAI_V1_PROFILE_SEED,
    DEEPSEEK_ANTHROPIC_V1_PROFILE_SEED,
    DEEPSEEK_OPENAI_V1_PROFILE_SEED,
    ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED,
    GLM_CODING_OPENAI_V1_PROFILE_SEED,
    GLM_CODING_ANTHROPIC_V1_PROFILE_SEED,
    GLM_GENERAL_OPENAI_V1_PROFILE_SEED
  ]
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
  seedBuiltInExternalIntegrationTestToken(database, now)

  const statement = database.prepare(`
    INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES (?, ?, ?, ?)
  `)

  for (const [key, value] of DEFAULT_SYSTEM_SETTINGS) {
    statement.run('sys_admin', key, JSON.stringify(value), now)
  }
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
