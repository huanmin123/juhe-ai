import { runtimeConfig } from '../config/runtime.js'
import type { ProviderCode } from '../domain/types.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { getPostgresPool } from './postgres-client.js'

export interface ProviderSystemDefaultHealthCheckModel {
  providerCode: ProviderCode
  model: string
  createdAt: string
  updatedAt: string
}

interface ProviderSystemDefaultHealthCheckModelRow {
  provider_code: ProviderCode
  model: string
  created_at: string
  updated_at: string
}

const businessSchemaName = 'juhe_business'

export function findProviderSystemDefaultHealthCheckModel(providerCode: string): string | undefined {
  const code = normalizeText(providerCode)
  if (!code) return undefined
  const row = getBusinessDatabase()
    .prepare(`
      SELECT model
      FROM provider_system_default_health_check_models
      WHERE provider_code = ?
      LIMIT 1
    `)
    .get(code) as unknown as { model?: string | null } | undefined
  return normalizeText(row?.model) || undefined
}

export async function findProviderSystemDefaultHealthCheckModelAsync(providerCode: string): Promise<string | undefined> {
  const code = normalizeText(providerCode)
  if (!code) return undefined
  const client = await databaseClient()
  const row = await client.one<{ model?: string | null }>(`
    SELECT model
    FROM ${table(client)}
    WHERE provider_code = ?
    LIMIT 1
  `, [code])
  return normalizeText(row?.model) || undefined
}

export async function listProviderSystemDefaultHealthCheckModelsAsync(
  providerCodes: string[] = []
): Promise<Map<string, string>> {
  const codes = [...new Set(providerCodes.map(normalizeText).filter(Boolean))]
  const client = await databaseClient()
  const filter = codes.length
    ? `WHERE provider_code IN (${client.dialect.bindPlaceholders(codes.length)})`
    : ''
  const rows = await client.query<Pick<ProviderSystemDefaultHealthCheckModelRow, 'provider_code' | 'model'>>(`
    SELECT provider_code, model
    FROM ${table(client)}
    ${filter}
    ORDER BY provider_code ASC
  `, codes)
  return new Map(rows
    .map((row) => [normalizeText(row.provider_code), normalizeText(row.model)] as const)
    .filter((entry): entry is readonly [string, string] => Boolean(entry[0] && entry[1])))
}

export async function upsertProviderSystemDefaultHealthCheckModelAsync(input: {
  providerCode: string
  model: string
}): Promise<ProviderSystemDefaultHealthCheckModel> {
  const providerCode = requiredText(input.providerCode, '供应商')
  const model = requiredText(input.model, '系统默认检查模型')
  const now = nowIso()
  const client = await databaseClient()
  const conflict = client.driver === 'postgres'
    ? `ON CONFLICT (provider_code) DO UPDATE
       SET model = EXCLUDED.model,
           updated_at = EXCLUDED.updated_at`
    : `ON CONFLICT(provider_code) DO UPDATE SET
         model = excluded.model,
         updated_at = excluded.updated_at`
  await client.execute(`
    INSERT INTO ${table(client)} (
      provider_code, model, created_at, updated_at
    ) VALUES (?, ?, ?, ?)
    ${conflict}
  `, [providerCode, model, now, now])
  const saved = await findRecordAsync(providerCode)
  if (!saved) {
    throw new Error('系统默认检查模型保存失败')
  }
  return saved
}

export async function clearProviderSystemDefaultHealthCheckModelIfModelAsync(input: {
  providerCode: string
  model: string
}): Promise<boolean> {
  const providerCode = normalizeText(input.providerCode)
  const model = normalizeText(input.model)
  if (!providerCode || !model) return false
  const client = await databaseClient()
  const result = await client.execute(`
    DELETE FROM ${table(client)}
    WHERE provider_code = ?
      AND model = ?
  `, [providerCode, model])
  return Number(result.changes ?? 0) > 0
}

async function findRecordAsync(providerCode: string): Promise<ProviderSystemDefaultHealthCheckModel | undefined> {
  const client = await databaseClient()
  const row = await client.one<ProviderSystemDefaultHealthCheckModelRow>(`
    SELECT provider_code, model, created_at, updated_at
    FROM ${table(client)}
    WHERE provider_code = ?
    LIMIT 1
  `, [providerCode])
  return row
    ? {
        providerCode: row.provider_code,
        model: row.model,
        createdAt: row.created_at,
        updatedAt: row.updated_at
      }
    : undefined
}

async function databaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function table(client: DatabaseClient): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, 'provider_system_default_health_check_models')
    : client.dialect.quoteIdentifier('provider_system_default_health_check_models')
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
