import { getBusinessDatabase, nowIso } from './database.js'
import { runtimeConfig } from '../config/runtime.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

const businessSchemaName = 'juhe_business'

export function normalizeAccountSupportedModelsInput(value: unknown): string[] | undefined {
  if (value === undefined) return undefined
  if (!Array.isArray(value)) {
    throw new Error('账户支持模型必须是字符串数组')
  }
  const models = [...new Set(value.map((item) => {
    if (typeof item !== 'string') {
      throw new Error('账户支持模型必须是字符串数组')
    }
    return item.trim()
  }).filter(Boolean))]
  return models
}

export function replaceAccountSupportedModels(accountId: string, providerCode: string, models: string[] | undefined): void {
  if (models === undefined) return
  const database = getBusinessDatabase()
  database.prepare('DELETE FROM account_supported_models WHERE account_id = ?').run(accountId)
  const normalizedModels = normalizeAccountSupportedModelsInput(models) ?? []
  if (!normalizedModels.length) return

  const insert = database.prepare(`
    INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
    VALUES (?, ?, ?, ?)
  `)
  const createdAt = nowIso()
  for (const model of normalizedModels) {
    insert.run(accountId, providerCode, model, createdAt)
  }
}

export async function replaceAccountSupportedModelsAsync(accountId: string, providerCode: string, models: string[] | undefined): Promise<void> {
  if (models === undefined) return
  if (runtimeConfig.databaseDriver !== 'postgres') {
    replaceAccountSupportedModels(accountId, providerCode, models)
    return
  }

  const normalizedModels = normalizeAccountSupportedModelsInput(models) ?? []
  const createdAt = nowIso()
  const client = await getAccountSupportedModelsDatabaseClient()
  await client.transaction(async (tx) => {
    await tx.execute(`DELETE FROM ${accountSupportedModelsTable(tx)} WHERE account_id = ?`, [accountId])
    for (const model of normalizedModels) {
      await tx.execute(`
        INSERT INTO ${accountSupportedModelsTable(tx)} (account_id, provider_code, model, created_at)
        VALUES (?, ?, ?, ?)
      `, [accountId, providerCode, model, createdAt])
    }
  })
}

export function loadSupportedModelsByAccountIds(accountIds: string[]): Map<string, string[]> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return new Map()

  const rows: Array<{ account_id: string; model: string }> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`
        SELECT account_id, model
        FROM account_supported_models
        WHERE account_id IN (${sqlPlaceholders(chunk.length)})
        ORDER BY account_id ASC, model ASC
      `)
      .all(...chunk) as unknown as Array<{ account_id: string; model: string }>)
  }

  const output = new Map<string, string[]>()
  for (const row of rows) {
    const models = output.get(row.account_id)
    if (models) {
      models.push(row.model)
    } else {
      output.set(row.account_id, [row.model])
    }
  }
  return output
}

export async function loadSupportedModelsByAccountIdsAsync(accountIds: string[]): Promise<Map<string, string[]>> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadSupportedModelsByAccountIds(accountIds)
  }
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const client = await getAccountSupportedModelsDatabaseClient()
  const rows: Array<{ account_id: string; model: string }> = []
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...await client.query<{ account_id: string; model: string }>(`
      SELECT account_id, model
      FROM ${accountSupportedModelsTable(client)}
      WHERE account_id IN (${chunk.map(() => '?').join(', ')})
      ORDER BY account_id ASC, model ASC
    `, chunk))
  }
  const output = new Map<string, string[]>()
  for (const row of rows) {
    const models = output.get(row.account_id)
    if (models) {
      models.push(row.model)
    } else {
      output.set(row.account_id, [row.model])
    }
  }
  return output
}

export function loadSupportedModelsForAccount(accountId: string): string[] {
  return loadSupportedModelsByAccountIds([accountId]).get(accountId) ?? []
}

async function getAccountSupportedModelsDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function accountSupportedModelsTable(client: DatabaseClient): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, 'account_supported_models')
    : client.dialect.quoteIdentifier('account_supported_models')
}
