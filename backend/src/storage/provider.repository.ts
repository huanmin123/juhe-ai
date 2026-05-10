import type { AccountType, ProviderCode, ProviderDefinition } from '../domain/types.js'
import { getDatabase } from './database.js'
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
  const rows = getDatabase().prepare('SELECT * FROM providers ORDER BY name ASC, code ASC').all() as unknown as ProviderRow[]
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

export function providerPassthroughEnabled(_provider?: ProviderDefinition): boolean {
  return true
}
