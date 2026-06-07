import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../domain/provider-protocol.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'

const DEFAULT_GPT_GROUP_NAME = '默认 GPT 分组'
const DEFAULT_GPT_GROUP_DESCRIPTION = ''

export function defaultOpenAIGroupIdForSystemAccount(systemAccountId: string): string | undefined {
  const row = getBusinessDatabase()
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_protocol_profile_id = ? AND is_default = 1 ORDER BY updated_at DESC, id ASC LIMIT 1')
    .get(systemAccountId, GPT_OPENAI_V1_PROFILE_ID) as unknown as { id?: string } | undefined
  return row?.id
}

export function defaultGroupIdForSystemAccount(providerProtocolProfileId: string, systemAccountId: string): string | undefined {
  const row = getBusinessDatabase()
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_protocol_profile_id = ? AND is_default = 1 ORDER BY updated_at DESC, id ASC LIMIT 1')
    .get(systemAccountId, providerProtocolProfileId) as unknown as { id?: string } | undefined
  return row?.id
}

export function ensureDefaultOpenAIGroupForSystemAccount(systemAccountId: string, timestamp = nowIso()): void {
  if (defaultOpenAIGroupIdForSystemAccount(systemAccountId)) {
    return
  }

  getBusinessDatabase()
    .prepare(`
      INSERT INTO groups (
        id, system_account_id, name, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        description, enabled, is_default, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)
    `)
    .run(
      newId('grp'),
      systemAccountId,
      DEFAULT_GPT_GROUP_NAME,
      GPT_VENDOR_CODE,
      GPT_OPENAI_V1_PROFILE_ID,
      OPENAI_PROTOCOL_CODE,
      OPENAI_PROTOCOL_VERSION,
      DEFAULT_GPT_GROUP_DESCRIPTION,
      timestamp,
      timestamp
    )
}
