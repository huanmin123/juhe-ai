import assert from 'node:assert/strict'

import { seedPostgresDefaults } from '../../storage/postgres-seed-defaults.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED,
  GEMINI_NATIVE_V1BETA_PROFILE_SEED,
  XAI_OPENAI_V1_PROFILE_SEED
} from '../../storage/schema-defaults.js'

interface ExecutedStatement {
  sql: string
  values: readonly unknown[]
}

const profileTypes = new Map<string, string>([
  [ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED.id, JSON.stringify(['api_key'])],
  [GEMINI_NATIVE_V1BETA_PROFILE_SEED.id, JSON.stringify(['api_key'])],
  [XAI_OPENAI_V1_PROFILE_SEED.id, JSON.stringify(['api_key', 'legacy_extension'])]
])
const executedStatements: ExecutedStatement[] = []

const client = {
  async execute(sql: string, values: readonly unknown[] = []) {
    executedStatements.push({ sql, values })
    if (isProfileTypeRepair(sql)) {
      const [accountTypesJson, _updatedAt, profileId] = values
      if (typeof accountTypesJson === 'string' && typeof profileId === 'string') {
        profileTypes.set(profileId, accountTypesJson)
      }
    }
    return { changes: 1 }
  },
  async one<T extends object = Record<string, unknown>>(sql: string, values: readonly unknown[] = []) {
    if (!isProfileTypeRead(sql)) return undefined as T | undefined
    const profileId = values[0]
    const accountTypesJson = typeof profileId === 'string' ? profileTypes.get(profileId) : undefined
    return accountTypesJson === undefined ? undefined : { account_types_json: accountTypesJson } as T
  }
}

await seedPostgresDefaults(client)

const firstRepairs = executedStatements.filter((statement) => isProfileTypeRepair(statement.sql))
assert.equal(firstRepairs.length, 3, 'PostgreSQL 重复初始化必须只修复缺少 OAuth 类型的内置档案')
assert.deepEqual(
  firstRepairs.map(({ values }) => [values[2], values[0]]),
  [
    [XAI_OPENAI_V1_PROFILE_SEED.id, JSON.stringify(['api_key', 'legacy_extension', 'oauth'])],
    [ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED.id, JSON.stringify(['api_key', 'oauth'])],
    [GEMINI_NATIVE_V1BETA_PROFILE_SEED.id, JSON.stringify(['api_key', 'google_oauth'])]
  ],
  'PostgreSQL 修复必须补齐 OAuth 类型并保留既有扩展类型'
)
for (const repair of firstRepairs) {
  assert.match(repair.sql, /SET account_types_json = \$1, updated_at = \$2/i, '修复必须参数化写入账户类型和更新时间')
  assert.match(repair.sql, /WHERE id = \$3/i, '修复必须按指定档案 ID 更新')
}

assert.deepEqual(
  JSON.parse(profileTypes.get(ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED.id) ?? 'null'),
  ['api_key', 'oauth'],
  'Anthropic PostgreSQL 历史档案必须补齐 OAuth 类型'
)
assert.deepEqual(
  JSON.parse(profileTypes.get(GEMINI_NATIVE_V1BETA_PROFILE_SEED.id) ?? 'null'),
  ['api_key', 'google_oauth'],
  'Gemini PostgreSQL 历史档案必须补齐 Google OAuth 类型'
)
assert.deepEqual(
  JSON.parse(profileTypes.get(XAI_OPENAI_V1_PROFILE_SEED.id) ?? 'null'),
  ['api_key', 'legacy_extension', 'oauth'],
  'xAI PostgreSQL 历史档案必须保留扩展类型并补齐 OAuth 类型'
)

executedStatements.length = 0
await seedPostgresDefaults(client)
assert.equal(
  executedStatements.filter((statement) => isProfileTypeRepair(statement.sql)).length,
  0,
  '账户类型补齐后再次 PostgreSQL 初始化不得重复更新'
)

console.log('PostgreSQL 供应商协议档案账户类型修复回归通过：历史数据可补齐 Anthropic、Gemini 和 xAI OAuth 类型')

function isProfileTypeRead(sql: string): boolean {
  return /SELECT account_types_json\s+FROM\s+"juhe_business"\."provider_protocol_profiles"\s+WHERE id = \$1/i.test(sql)
}

function isProfileTypeRepair(sql: string): boolean {
  return /UPDATE\s+"juhe_business"\."provider_protocol_profiles"/i.test(sql)
}
