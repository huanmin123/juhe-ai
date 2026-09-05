import type {
  AccountModelMapping,
  AccountModelMappingSourceEndpointFamily,
  AccountModelMappingUpstreamEndpointFamily
} from '../domain/types.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY
} from '../domain/provider-protocol.js'
import {
  accountModelMappingEndpointFamilyLabel
} from './account-model-mapping-protocol-matrix.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { runtimeConfig } from '../config/runtime.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

interface AccountModelMappingRow {
  account_id: string
  source_model: string
  source_endpoint_family: AccountModelMappingSourceEndpointFamily
  upstream_model: string
  upstream_endpoint_family: AccountModelMappingUpstreamEndpointFamily
  enabled: number
}

const businessSchemaName = 'juhe_business'

export function normalizeAccountModelMappingsInput(value: unknown): AccountModelMapping[] | undefined {
  if (value === undefined) return undefined
  if (!Array.isArray(value)) {
    throw new Error('账户模型映射必须是数组')
  }

  const output: AccountModelMapping[] = []
  const seenSources = new Set<string>()
  for (const item of value) {
    if (typeof item !== 'object' || item === null || Array.isArray(item)) {
      throw new Error('账户模型映射项必须是对象')
    }
    const record = item as Record<string, unknown>
    const sourceModel = stringModelValue(record.sourceModel, '下游模型')
    const sourceEndpointFamily = sourceEndpointFamilyValue(record.sourceEndpointFamily)
    const upstreamModel = stringModelValue(record.upstreamModel, '上游模型')
    const upstreamEndpointFamily = upstreamEndpointFamilyValue(record.upstreamEndpointFamily)
    if (sourceModel === upstreamModel && sourceEndpointFamily === upstreamEndpointFamily) {
      continue
    }
    const sourceKey = `${sourceEndpointFamily}\n${sourceModel}`
    if (seenSources.has(sourceKey)) {
      throw new Error(`同一个下游模型和协议只能配置一条映射：${sourceModel} / ${accountModelMappingEndpointFamilyLabel(sourceEndpointFamily)}`)
    }
    seenSources.add(sourceKey)
    output.push({
      sourceModel,
      sourceEndpointFamily,
      upstreamModel,
      upstreamEndpointFamily,
      enabled: record.enabled !== false
    })
  }
  return output
}

export function replaceAccountModelMappings(accountId: string, providerCode: string, mappings: AccountModelMapping[] | undefined): void {
  if (mappings === undefined) return
  const database = getBusinessDatabase()
  database.prepare('DELETE FROM account_model_mappings WHERE account_id = ?').run(accountId)
  const normalizedMappings = normalizeAccountModelMappingsInput(mappings) ?? []
  if (!normalizedMappings.length) return

  const insert = database.prepare(`
    INSERT INTO account_model_mappings (
      account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const timestamp = nowIso()
  for (const mapping of normalizedMappings) {
    insert.run(
      accountId,
      providerCode,
      mapping.sourceModel,
      mapping.sourceEndpointFamily,
      mapping.upstreamModel,
      mapping.upstreamEndpointFamily,
      mapping.enabled ? 1 : 0,
      timestamp,
      timestamp
    )
  }
}

export async function replaceAccountModelMappingsAsync(accountId: string, providerCode: string, mappings: AccountModelMapping[] | undefined): Promise<void> {
  if (mappings === undefined) return
  if (runtimeConfig.databaseDriver !== 'postgres') {
    replaceAccountModelMappings(accountId, providerCode, mappings)
    return
  }

  const normalizedMappings = normalizeAccountModelMappingsInput(mappings) ?? []
  const client = await getAccountModelMappingsDatabaseClient()
  await client.transaction(async (tx) => {
    await replaceAccountModelMappingsInClientAsync(tx, accountId, providerCode, normalizedMappings)
  })
}

export async function replaceAccountModelMappingsInClientAsync(client: DatabaseClient, accountId: string, providerCode: string, mappings: AccountModelMapping[]): Promise<void> {
  const timestamp = nowIso()
  await client.execute(`DELETE FROM ${accountModelMappingsTable(client)} WHERE account_id = ?`, [accountId])
  for (const mapping of mappings) {
    await client.execute(`
      INSERT INTO ${accountModelMappingsTable(client)} (
        account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, [
      accountId,
      providerCode,
      mapping.sourceModel,
      mapping.sourceEndpointFamily,
      mapping.upstreamModel,
      mapping.upstreamEndpointFamily,
      mapping.enabled ? 1 : 0,
      timestamp,
      timestamp
    ])
  }
}

export function loadModelMappingsByAccountIds(accountIds: string[]): Map<string, AccountModelMapping[]> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return new Map()

  const rows: AccountModelMappingRow[] = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`
        SELECT account_id, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
        FROM account_model_mappings
        WHERE account_id IN (${sqlPlaceholders(chunk.length)})
        ORDER BY account_id ASC, source_model ASC, source_endpoint_family ASC
      `)
      .all(...chunk) as unknown as AccountModelMappingRow[])
  }

  const output = new Map<string, AccountModelMapping[]>()
  for (const row of rows) {
    const mapping: AccountModelMapping = {
      sourceModel: row.source_model,
      sourceEndpointFamily: row.source_endpoint_family,
      upstreamModel: row.upstream_model,
      upstreamEndpointFamily: row.upstream_endpoint_family,
      enabled: row.enabled === 1
    }
    const mappings = output.get(row.account_id)
    if (mappings) {
      mappings.push(mapping)
    } else {
      output.set(row.account_id, [mapping])
    }
  }
  return output
}

export async function loadModelMappingsByAccountIdsAsync(accountIds: string[]): Promise<Map<string, AccountModelMapping[]>> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadModelMappingsByAccountIds(accountIds)
  }
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const client = await getAccountModelMappingsDatabaseClient()
  const rows: AccountModelMappingRow[] = []
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...await client.query<AccountModelMappingRow>(`
      SELECT account_id, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
      FROM ${accountModelMappingsTable(client)}
      WHERE account_id IN (${chunk.map(() => '?').join(', ')})
      ORDER BY account_id ASC, source_model ASC, source_endpoint_family ASC
    `, chunk))
  }
  return accountModelMappingsFromRows(rows)
}

export function loadModelMappingsForAccountModel(accountIdInput: string, sourceModelInput: string): AccountModelMapping[] {
  const accountId = accountIdInput.trim()
  const sourceModel = sourceModelInput.trim()
  if (!accountId || !sourceModel) return []

  const database = getBusinessDatabase()
  const rows = database.prepare(`
    SELECT account_id, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
    FROM account_model_mappings
    WHERE account_id = ?
      AND source_model = ?
    ORDER BY source_endpoint_family ASC
  `).all(accountId, sourceModel) as unknown as AccountModelMappingRow[]
  return rows.map(accountModelMappingFromRow)
}

export async function loadModelMappingsForAccountModelAsync(accountIdInput: string, sourceModelInput: string): Promise<AccountModelMapping[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadModelMappingsForAccountModel(accountIdInput, sourceModelInput)
  }
  const accountId = accountIdInput.trim()
  const sourceModel = sourceModelInput.trim()
  if (!accountId || !sourceModel) return []

  const client = await getAccountModelMappingsDatabaseClient()
  const rows = await client.query<AccountModelMappingRow>(`
    SELECT account_id, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
    FROM ${accountModelMappingsTable(client)}
    WHERE account_id = ?
      AND source_model = ?
    ORDER BY source_endpoint_family ASC
  `, [accountId, sourceModel])
  return rows.map(accountModelMappingFromRow)
}

function accountModelMappingsFromRows(rows: AccountModelMappingRow[]): Map<string, AccountModelMapping[]> {
  const output = new Map<string, AccountModelMapping[]>()
  for (const row of rows) {
    const mapping = accountModelMappingFromRow(row)
    const mappings = output.get(row.account_id)
    if (mappings) {
      mappings.push(mapping)
    } else {
      output.set(row.account_id, [mapping])
    }
  }
  return output
}

function accountModelMappingFromRow(row: AccountModelMappingRow): AccountModelMapping {
  return {
    sourceModel: row.source_model,
    sourceEndpointFamily: row.source_endpoint_family,
    upstreamModel: row.upstream_model,
    upstreamEndpointFamily: row.upstream_endpoint_family,
    enabled: row.enabled === 1
  }
}

function sourceEndpointFamilyValue(value: unknown): AccountModelMappingSourceEndpointFamily {
  if (
    value === OPENAI_CHAT_COMPLETIONS_FAMILY
    || value === OPENAI_RESPONSES_FAMILY
    || value === ANTHROPIC_MESSAGES_FAMILY
    || value === GEMINI_GENERATE_CONTENT_FAMILY
    || value === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
  ) {
    return value
  }
  throw new Error(`下游协议必须是 ${accountModelMappingEndpointFamilyLabel(OPENAI_CHAT_COMPLETIONS_FAMILY)}、${accountModelMappingEndpointFamilyLabel(OPENAI_RESPONSES_FAMILY)}、${accountModelMappingEndpointFamilyLabel(ANTHROPIC_MESSAGES_FAMILY)}、${accountModelMappingEndpointFamilyLabel(GEMINI_GENERATE_CONTENT_FAMILY)} 或 ${accountModelMappingEndpointFamilyLabel(GEMINI_STREAM_GENERATE_CONTENT_FAMILY)}`)
}

function upstreamEndpointFamilyValue(value: unknown): AccountModelMappingUpstreamEndpointFamily {
  if (
    value === OPENAI_CHAT_COMPLETIONS_FAMILY
    || value === OPENAI_RESPONSES_FAMILY
    || value === ANTHROPIC_MESSAGES_FAMILY
    || value === GEMINI_GENERATE_CONTENT_FAMILY
  ) {
    return value
  }
  throw new Error(`上游协议必须是 ${accountModelMappingEndpointFamilyLabel(OPENAI_CHAT_COMPLETIONS_FAMILY)}、${accountModelMappingEndpointFamilyLabel(OPENAI_RESPONSES_FAMILY)}、${accountModelMappingEndpointFamilyLabel(ANTHROPIC_MESSAGES_FAMILY)} 或 ${accountModelMappingEndpointFamilyLabel(GEMINI_GENERATE_CONTENT_FAMILY)}`)
}

export function loadModelMappingsForAccount(accountId: string): AccountModelMapping[] {
  return loadModelMappingsByAccountIds([accountId]).get(accountId) ?? []
}

async function getAccountModelMappingsDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function accountModelMappingsTable(client: DatabaseClient): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, 'account_model_mappings')
    : client.dialect.quoteIdentifier('account_model_mappings')
}

function stringModelValue(value: unknown, label: string): string {
  if (typeof value !== 'string') {
    throw new Error(`${label}必须是字符串`)
  }
  const trimmed = value.trim()
  if (!trimmed) {
    throw new Error(`${label}不能为空`)
  }
  return trimmed
}
