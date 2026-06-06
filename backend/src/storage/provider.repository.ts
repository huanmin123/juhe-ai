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
  default_test_model: string
  account_types_json: string
  capabilities_json: string
}

const maxProviderDefinitions = 50

export function listProviders(): ProviderDefinition[] {
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, code, name, description, enabled, base_url, default_test_model, account_types_json, capabilities_json
      FROM providers
      ORDER BY name ASC, code ASC
      LIMIT ?
    `)
    .all(maxProviderDefinitions) as unknown as ProviderRow[]
  return rows.map((row) => ({
    id: row.id,
    code: row.code,
    name: row.name,
    description: row.description ?? undefined,
    enabled: row.enabled === 1,
    baseUrl: row.base_url,
    defaultTestModel: row.default_test_model,
    accountTypes: parseJsonArray(row.account_types_json) as AccountType[],
    capabilities: parseJsonArray(row.capabilities_json)
  }))
}

export function findProviderDefaultTestModel(providerCode: string): string | undefined {
  const code = providerCode.trim()
  if (!code) return undefined
  const row = getBusinessDatabase()
    .prepare('SELECT default_test_model FROM providers WHERE code = ? AND enabled = 1 LIMIT 1')
    .get(code) as unknown as { default_test_model?: string | null } | undefined
  const model = row?.default_test_model?.trim()
  return model || undefined
}
