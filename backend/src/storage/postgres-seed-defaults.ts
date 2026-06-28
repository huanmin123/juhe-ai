import type { DatabaseClient } from './database-client.js'
import { encryptJson, hashPassword } from './crypto.js'
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
          base_url, default_test_model, account_types_json, capabilities_json, created_at, updated_at
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
        profile.defaultTestModel,
        JSON.stringify(profile.accountTypes),
        JSON.stringify(profile.capabilities),
        now,
        profileUpdatedAt
      ]
    )
  }

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
          id, system_account_id, name, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
          description, enabled, is_default, created_at, updated_at
        )
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, 1, $9, $10)
        ON CONFLICT DO NOTHING
      `,
      [
        group.id,
        group.systemAccountId,
        group.name,
        group.providerCode,
        group.providerProtocolProfileId,
        group.protocolCode,
        group.protocolVersion,
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
