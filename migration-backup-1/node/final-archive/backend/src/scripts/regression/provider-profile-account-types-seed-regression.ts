import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED,
  GEMINI_NATIVE_V1BETA_PROFILE_SEED,
  XAI_OPENAI_V1_PROFILE_SEED
} from '../../storage/schema-defaults.js'
import { applyBusinessSchema, seedDefaults } from '../../storage/schema.js'

const database = new DatabaseSync(':memory:')
applyBusinessSchema(database)
seedDefaults(database)

const staleProfileTypes: Array<{ id: string; accountTypes: string[] }> = [
  { id: ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED.id, accountTypes: ['api_key'] },
  { id: GEMINI_NATIVE_V1BETA_PROFILE_SEED.id, accountTypes: ['api_key'] },
  { id: XAI_OPENAI_V1_PROFILE_SEED.id, accountTypes: ['api_key', 'legacy_extension'] }
]
const updateStatement = database.prepare(`
  UPDATE provider_protocol_profiles
  SET account_types_json = ?
  WHERE id = ?
`)
for (const profile of staleProfileTypes) {
  updateStatement.run(JSON.stringify(profile.accountTypes), profile.id)
}

seedDefaults(database)

assert.deepEqual(
  profileAccountTypes(database, ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED.id),
  ['api_key', 'oauth'],
  '重复初始化必须为历史 Anthropic 档案补齐 OAuth 类型'
)
assert.deepEqual(
  profileAccountTypes(database, GEMINI_NATIVE_V1BETA_PROFILE_SEED.id),
  ['api_key', 'google_oauth'],
  '重复初始化必须为历史 Gemini 原生档案补齐 Google OAuth 类型'
)
assert.deepEqual(
  profileAccountTypes(database, XAI_OPENAI_V1_PROFILE_SEED.id),
  ['api_key', 'legacy_extension', 'oauth'],
  '重复初始化必须补齐 xAI OAuth 类型且保留已有扩展类型'
)

database.close()
console.log('默认供应商协议档案账户类型修复回归通过：历史数据可补齐 Anthropic、Gemini 和 xAI OAuth 类型')

function profileAccountTypes(database: DatabaseSync, profileId: string): string[] {
  const row = database.prepare(`
    SELECT account_types_json
    FROM provider_protocol_profiles
    WHERE id = ?
  `).get(profileId) as { account_types_json?: unknown } | undefined
  const accountTypesJson = row?.account_types_json
  if (typeof accountTypesJson !== 'string') {
    assert.fail(`${profileId} 的账户类型字段必须存在`)
  }
  return JSON.parse(accountTypesJson) as string[]
}
