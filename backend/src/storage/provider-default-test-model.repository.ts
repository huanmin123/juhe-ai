import type { ProviderCode } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { getPostgresPool } from './postgres-client.js'

export interface ProviderDefaultTestModelPreference {
  systemAccountId: string
  providerCode: ProviderCode
  model: string
  createdAt: string
  updatedAt: string
}

interface ProviderDefaultTestModelPreferenceRow {
  system_account_id: string
  provider_code: ProviderCode
  model: string
  created_at: string
  updated_at: string
}

const businessSchemaName = 'juhe_business'

export function findProviderDefaultTestModelPreference(providerCode: string, systemAccountId?: string): string | undefined {
  const normalized = normalizePreferenceKey(providerCode, systemAccountId)
  if (!normalized) return undefined
  const row = getBusinessDatabase()
    .prepare(`
      SELECT model
      FROM provider_default_test_models
      WHERE system_account_id = ?
        AND provider_code = ?
      LIMIT 1
    `)
    .get(normalized.systemAccountId, normalized.providerCode) as unknown as { model?: string | null } | undefined
  return normalizeModel(row?.model)
}

export async function findProviderDefaultTestModelPreferenceAsync(providerCode: string, systemAccountId?: string): Promise<string | undefined> {
  const normalized = normalizePreferenceKey(providerCode, systemAccountId)
  if (!normalized) return undefined
  const client = await getProviderDefaultTestModelDatabaseClient()
  const row = await client.one<{ model?: string | null }>(`
    SELECT model
    FROM ${providerDefaultTestModelTable(client)}
    WHERE system_account_id = ?
      AND provider_code = ?
    LIMIT 1
  `, [normalized.systemAccountId, normalized.providerCode])
  return normalizeModel(row?.model)
}

export async function listProviderDefaultTestModelPreferencesAsync(
  systemAccountId: string | undefined,
  providerCodes: string[] = []
): Promise<Map<string, string>> {
  const normalizedSystemAccountId = normalizeText(systemAccountId)
  if (!normalizedSystemAccountId) return new Map()
  const normalizedProviderCodes = [...new Set(providerCodes.map((code) => normalizeText(code)).filter(Boolean))]
  const client = await getProviderDefaultTestModelDatabaseClient()
  const providerFilter = normalizedProviderCodes.length
    ? `AND provider_code IN (${client.dialect.bindPlaceholders(normalizedProviderCodes.length)})`
    : ''
  const rows = await client.query<Pick<ProviderDefaultTestModelPreferenceRow, 'provider_code' | 'model'>>(`
    SELECT provider_code, model
    FROM ${providerDefaultTestModelTable(client)}
    WHERE system_account_id = ?
      ${providerFilter}
    ORDER BY provider_code ASC
  `, [normalizedSystemAccountId, ...normalizedProviderCodes])
  return new Map(rows
    .map((row) => [normalizeText(row.provider_code), normalizeModel(row.model)] as const)
    .filter((entry): entry is readonly [string, string] => Boolean(entry[0] && entry[1])))
}

export async function upsertProviderDefaultTestModelPreferenceAsync(input: {
  systemAccountId: string
  providerCode: string
  model: string
}): Promise<ProviderDefaultTestModelPreference> {
  const systemAccountId = requiredText(input.systemAccountId, '系统账户')
  const providerCode = requiredText(input.providerCode, '供应商')
  const model = requiredText(input.model, '默认测试模型')
  const now = nowIso()
  const client = await getProviderDefaultTestModelDatabaseClient()
  if (client.driver === 'postgres') {
    await client.execute(`
      INSERT INTO ${providerDefaultTestModelTable(client)} (
        system_account_id, provider_code, model, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?)
      ON CONFLICT (system_account_id, provider_code) DO UPDATE
      SET model = EXCLUDED.model,
          updated_at = EXCLUDED.updated_at
    `, [systemAccountId, providerCode, model, now, now])
  } else {
    await client.execute(`
      INSERT INTO ${providerDefaultTestModelTable(client)} (
        system_account_id, provider_code, model, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?)
      ON CONFLICT(system_account_id, provider_code) DO UPDATE SET
        model = excluded.model,
        updated_at = excluded.updated_at
    `, [systemAccountId, providerCode, model, now, now])
  }
  const saved = await findProviderDefaultTestModelPreferenceRecordAsync(providerCode, systemAccountId)
  if (!saved) {
    throw new Error('默认测试模型保存失败')
  }
  return saved
}

export async function clearProviderDefaultTestModelPreferenceIfModelAsync(input: {
  systemAccountId?: string
  providerCode: string
  model: string
}): Promise<boolean> {
  const systemAccountId = normalizeText(input.systemAccountId)
  const providerCode = normalizeText(input.providerCode)
  const model = normalizeModel(input.model)
  if (!systemAccountId || !providerCode || !model) return false
  const client = await getProviderDefaultTestModelDatabaseClient()
  const result = await client.execute(`
    DELETE FROM ${providerDefaultTestModelTable(client)}
    WHERE system_account_id = ?
      AND provider_code = ?
      AND model = ?
  `, [systemAccountId, providerCode, model])
  return Number(result.changes ?? 0) > 0
}

async function findProviderDefaultTestModelPreferenceRecordAsync(
  providerCodeInput: string,
  systemAccountIdInput?: string
): Promise<ProviderDefaultTestModelPreference | undefined> {
  const normalized = normalizePreferenceKey(providerCodeInput, systemAccountIdInput)
  if (!normalized) return undefined
  const client = await getProviderDefaultTestModelDatabaseClient()
  const row = await client.one<ProviderDefaultTestModelPreferenceRow>(`
    SELECT system_account_id, provider_code, model, created_at, updated_at
    FROM ${providerDefaultTestModelTable(client)}
    WHERE system_account_id = ?
      AND provider_code = ?
    LIMIT 1
  `, [normalized.systemAccountId, normalized.providerCode])
  return row ? preferenceFromRow(row) : undefined
}

function preferenceFromRow(row: ProviderDefaultTestModelPreferenceRow): ProviderDefaultTestModelPreference {
  return {
    systemAccountId: row.system_account_id,
    providerCode: row.provider_code,
    model: row.model,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function normalizePreferenceKey(providerCode: string | undefined, systemAccountId?: string): { providerCode: string; systemAccountId: string } | undefined {
  const normalizedProviderCode = normalizeText(providerCode)
  const normalizedSystemAccountId = normalizeText(systemAccountId)
  return normalizedProviderCode && normalizedSystemAccountId
    ? { providerCode: normalizedProviderCode, systemAccountId: normalizedSystemAccountId }
    : undefined
}

function requiredText(value: unknown, label: string): string {
  const normalized = normalizeText(value)
  if (!normalized) {
    throw new Error(`${label}不能为空`)
  }
  return normalized
}

function normalizeText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function normalizeModel(value: unknown): string | undefined {
  const normalized = normalizeText(value)
  return normalized || undefined
}

async function getProviderDefaultTestModelDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function providerDefaultTestModelTable(client: DatabaseClient): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, 'provider_default_test_models')
    : client.dialect.quoteIdentifier('provider_default_test_models')
}
