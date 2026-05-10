import { getDatabase, newId, nowIso } from './database.js'

const DEFAULT_OPENAI_GROUP_NAME = '默认 OpenAI 分组'
const DEFAULT_OPENAI_GROUP_DESCRIPTION = ''

export function defaultOpenAIGroupIdForSystemAccount(systemAccountId: string): string | undefined {
  const row = getDatabase()
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND (is_default = 1 OR name = ?) ORDER BY is_default DESC, updated_at DESC, id ASC LIMIT 1')
    .get(systemAccountId, 'openai', DEFAULT_OPENAI_GROUP_NAME) as unknown as { id?: string } | undefined
  return row?.id
}

export function defaultGroupIdForSystemAccount(providerCode: string, systemAccountId: string): string | undefined {
  const row = getDatabase()
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? ORDER BY is_default DESC, updated_at DESC, id ASC LIMIT 1')
    .get(systemAccountId, providerCode) as unknown as { id?: string } | undefined
  if (row?.id) {
    return row.id
  }
  return providerCode === 'openai' ? defaultOpenAIGroupIdForSystemAccount(systemAccountId) : undefined
}

export function ensureDefaultOpenAIGroupForSystemAccount(systemAccountId: string, timestamp = nowIso()): void {
  if (defaultOpenAIGroupIdForSystemAccount(systemAccountId)) {
    return
  }

  getDatabase()
    .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)')
    .run(newId('grp'), systemAccountId, DEFAULT_OPENAI_GROUP_NAME, 'openai', DEFAULT_OPENAI_GROUP_DESCRIPTION, timestamp, timestamp)
}
