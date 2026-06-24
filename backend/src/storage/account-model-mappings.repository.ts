import type {
  AccountModelMapping,
  AccountModelMappingEndpointFamily,
  AccountModelMappingSourceEndpointFamily,
  AccountModelMappingUpstreamEndpointFamily
} from '../domain/types.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY
} from '../domain/provider-protocol.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

interface AccountModelMappingRow {
  account_id: string
  source_model: string
  source_endpoint_family: AccountModelMappingSourceEndpointFamily
  upstream_model: string
  upstream_endpoint_family: AccountModelMappingUpstreamEndpointFamily
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
    const sourceEndpointFamily = sourceEndpointFamilyValue(record.sourceEndpointFamily)
    const upstreamModel = stringModelValue(record.upstreamModel, '上游模型')
    const upstreamEndpointFamily = upstreamEndpointFamilyValue(record.upstreamEndpointFamily)
    assertSupportedEndpointFamilyConversion(sourceEndpointFamily, upstreamEndpointFamily)
    if (sourceModel === upstreamModel && sourceEndpointFamily === upstreamEndpointFamily) {
      continue
    }
    const sourceKey = `${sourceEndpointFamily}\n${sourceModel.toLowerCase()}`
    if (seenSources.has(sourceKey)) {
      throw new Error(`同一个下游模型和协议只能配置一条映射：${sourceModel} / ${endpointFamilyLabel(sourceEndpointFamily)}`)
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

function sourceEndpointFamilyValue(value: unknown): AccountModelMappingSourceEndpointFamily {
  if (value === OPENAI_CHAT_COMPLETIONS_FAMILY || value === OPENAI_RESPONSES_FAMILY) {
    return value
  }
  throw new Error(`下游协议必须是 ${endpointFamilyLabel(OPENAI_CHAT_COMPLETIONS_FAMILY)} 或 ${endpointFamilyLabel(OPENAI_RESPONSES_FAMILY)}`)
}

function upstreamEndpointFamilyValue(value: unknown): AccountModelMappingUpstreamEndpointFamily {
  if (value === OPENAI_CHAT_COMPLETIONS_FAMILY || value === OPENAI_RESPONSES_FAMILY || value === ANTHROPIC_MESSAGES_FAMILY) {
    return value
  }
  throw new Error(`上游协议必须是 ${endpointFamilyLabel(OPENAI_CHAT_COMPLETIONS_FAMILY)}、${endpointFamilyLabel(OPENAI_RESPONSES_FAMILY)} 或 ${endpointFamilyLabel(ANTHROPIC_MESSAGES_FAMILY)}`)
}

function assertSupportedEndpointFamilyConversion(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): void {
  if (upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY && upstreamEndpointFamily !== OPENAI_RESPONSES_FAMILY && upstreamEndpointFamily !== ANTHROPIC_MESSAGES_FAMILY) {
    throw new Error(`暂不支持转发到 ${endpointFamilyLabel(upstreamEndpointFamily)}`)
  }
}

function endpointFamilyLabel(value: AccountModelMappingEndpointFamily): string {
  if (value === OPENAI_RESPONSES_FAMILY) return 'Responses'
  if (value === ANTHROPIC_MESSAGES_FAMILY) return 'Messages'
  return 'Chat Completions'
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
