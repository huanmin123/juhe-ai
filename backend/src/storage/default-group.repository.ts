import { getBusinessDatabase, newId, nowIso } from './database.js'
import { GPT_VENDOR_CODE } from '../domain/provider-protocol.js'
import { DEFAULT_BUILT_IN_GROUPS } from './schema-defaults.js'

export function defaultGptGroupIdForSystemAccount(systemAccountId: string): string | undefined {
  const gptGroup = DEFAULT_BUILT_IN_GROUPS.find((group) => group.providerCode === GPT_VENDOR_CODE)
  if (!gptGroup) return undefined
  const row = getBusinessDatabase()
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND is_default = 1 ORDER BY updated_at DESC, id ASC LIMIT 1')
    .get(systemAccountId, gptGroup.providerCode) as unknown as { id?: string } | undefined
  return row?.id
}

export function ensureDefaultBuiltInGroupsForSystemAccount(systemAccountId: string, timestamp = nowIso()): void {
  const statement = getBusinessDatabase().prepare(`
      INSERT INTO groups (
        id, system_account_id, name, provider_code,
        description, enabled, is_default, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)
    `)
  for (const group of DEFAULT_BUILT_IN_GROUPS) {
    if (defaultGroupIdForProviderCode(group.providerCode, systemAccountId)) {
      continue
    }
    statement.run(
      newId('grp'),
      systemAccountId,
      group.name,
      group.providerCode,
      group.description,
      timestamp,
      timestamp
    )
  }
}

export function defaultGroupIdForProviderCode(providerCode: string, systemAccountId: string): string | undefined {
  const row = getBusinessDatabase()
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND is_default = 1 ORDER BY updated_at DESC, id ASC LIMIT 1')
    .get(systemAccountId, providerCode) as unknown as { id?: string } | undefined
  return row?.id
}
