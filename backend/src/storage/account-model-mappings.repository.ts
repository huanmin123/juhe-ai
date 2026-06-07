import type { AccountModelMapping } from '../domain/types.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

interface AccountModelMappingRow {
  account_id: string
  source_model: string
  upstream_model: string
  enabled: number
}

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
    const upstreamModel = stringModelValue(record.upstreamModel, '上游模型')
    if (sourceModel === upstreamModel) {
      continue
    }
    if (seenSources.has(sourceModel)) {
      throw new Error(`同一个下游模型只能配置一条映射：${sourceModel}`)
    }
    seenSources.add(sourceModel)
    output.push({
      sourceModel,
      upstreamModel,
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
      account_id, provider_code, source_model, upstream_model, enabled, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?)
  `)
  const timestamp = nowIso()
  for (const mapping of normalizedMappings) {
    insert.run(
      accountId,
      providerCode,
      mapping.sourceModel,
      mapping.upstreamModel,
      mapping.enabled ? 1 : 0,
      timestamp,
      timestamp
    )
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
        SELECT account_id, source_model, upstream_model, enabled
        FROM account_model_mappings
        WHERE account_id IN (${sqlPlaceholders(chunk.length)})
        ORDER BY account_id ASC, source_model ASC
      `)
      .all(...chunk) as unknown as AccountModelMappingRow[])
  }

  const output = new Map<string, AccountModelMapping[]>()
  for (const row of rows) {
    const mapping: AccountModelMapping = {
      sourceModel: row.source_model,
      upstreamModel: row.upstream_model,
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

export function loadModelMappingsForAccount(accountId: string): AccountModelMapping[] {
  return loadModelMappingsByAccountIds([accountId]).get(accountId) ?? []
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
