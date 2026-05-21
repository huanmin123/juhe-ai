import { getDatabase, nowIso } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export function normalizeAccountSupportedModelsInput(value: unknown): string[] | undefined {
  if (value === undefined) return undefined
  const values = Array.isArray(value)
    ? value
    : typeof value === 'string'
      ? value.split(',')
      : undefined
  if (!values) {
    throw new Error('账户支持模型必须是字符串数组')
  }
  const models = [...new Set(values
    .map((item) => typeof item === 'string' ? item.trim() : '')
    .filter(Boolean))]
  return models
}

export function replaceAccountSupportedModels(accountId: string, providerCode: string, models: string[] | undefined): void {
  if (models === undefined) return
  const database = getDatabase()
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

export function loadSupportedModelsByAccountIds(accountIds: string[]): Map<string, string[]> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return new Map()

  const rows: Array<{ account_id: string; model: string }> = []
  const database = getDatabase()
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

export function loadSupportedModelsForAccount(accountId: string): string[] {
  return loadSupportedModelsByAccountIds([accountId]).get(accountId) ?? []
}
