import type { ApiKeyGroupBindingSummary } from '../domain/types.js'
import { normalizeApiKeyGroupBindingWeight } from '../domain/api-key-routing.js'
import { getDatabase } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export interface ApiKeyGroupBindingRow {
  id: string
  api_key_id: string
  system_account_id: string
  group_id: string
  group_name: string | null
  provider_code: string | null
  group_enabled: number | null
  priority: number
  weight?: number | null
  status: 'active' | 'disabled' | string
}

export function loadApiKeyGroupBindingSummariesByApiKeyIds(apiKeyIds: string[]): Map<string, ApiKeyGroupBindingSummary[]> {
  const ids = [...new Set(apiKeyIds.filter(Boolean))]
  const result = new Map<string, ApiKeyGroupBindingSummary[]>()
  if (!ids.length) return result

  const database = getDatabase()
  for (const chunk of chunkValues(ids, 500)) {
    const rows = database
      .prepare(`
        SELECT
          api_key_group_bindings.id,
          api_key_group_bindings.api_key_id,
          api_key_group_bindings.system_account_id,
          api_key_group_bindings.group_id,
          api_key_group_bindings.priority,
          api_key_group_bindings.weight,
          api_key_group_bindings.status,
          groups.name AS group_name,
          groups.provider_code,
          groups.enabled AS group_enabled
        FROM api_key_group_bindings
        LEFT JOIN groups ON groups.id = api_key_group_bindings.group_id
        WHERE api_key_group_bindings.api_key_id IN (${sqlPlaceholders(chunk.length)})
        ORDER BY api_key_group_bindings.api_key_id ASC,
          CASE WHEN api_key_group_bindings.status = 'active' THEN 0 ELSE 1 END ASC,
          api_key_group_bindings.priority ASC,
          api_key_group_bindings.created_at ASC,
          api_key_group_bindings.id ASC
      `)
      .all(...chunk) as unknown as ApiKeyGroupBindingRow[]
    for (const row of rows) {
      const item: ApiKeyGroupBindingSummary = {
        id: row.id,
        groupId: row.group_id,
        groupName: row.group_name ?? undefined,
        providerCode: row.provider_code ?? undefined,
        priority: Number.isFinite(row.priority) ? row.priority : 1,
        weight: normalizeApiKeyGroupBindingWeight(row.weight),
        status: row.status === 'disabled' ? 'disabled' : 'active',
        groupEnabled: row.group_enabled !== 0
      }
      const existing = result.get(row.api_key_id) ?? []
      existing.push(item)
      result.set(row.api_key_id, existing)
    }
  }
  return result
}
