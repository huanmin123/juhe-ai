import type { AccountType, ProviderCode, ProviderDefinition } from '../domain/types.js'
import { getBusinessDatabase } from './database.js'
import { parseJsonArray } from './value-utils.js'

interface ProviderRow {
  id: string
  code: ProviderCode
  name: string
  description: string | null
  enabled: number
  base_url: string
  account_types_json: string
  capabilities_json: string
}

export function listProviders(): ProviderDefinition[] {
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, code, name, description, enabled, base_url, account_types_json, capabilities_json
      FROM providers
      ORDER BY name ASC, code ASC
    `)
    .all() as unknown as ProviderRow[]
  return rows.map((row) => ({
    id: row.id,
    code: row.code,
    name: row.name,
    description: row.description ?? undefined,
    enabled: row.enabled === 1,
    baseUrl: row.base_url,
    accountTypes: parseJsonArray(row.account_types_json) as AccountType[],
    capabilities: parseJsonArray(row.capabilities_json)
  }))
}
