import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import {
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'

const repoRoot = resolve(fileURLToPath(new URL('../../..', import.meta.url)))
const tempRoot = resolve(tmpdir(), `juhe-ai-account-client-compatibility-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-client-compatibility-boundary-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const schemasSource = readSource('src/modules/accounts/account-request.schemas.ts')
const [repositories, { createApiKeyRecordWithRouteStrategy }] = await Promise.all([
  import('../../storage/repositories.js'),
  import('../shared/route-strategy-fixture.js')
])
const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

const accountCreateSchemaSource = sourceBetween(schemasSource, 'export const accountCreateSchema', 'export const accountUpdateSchema')
const accountUpdateSchemaSource = sourceBetween(schemasSource, 'export const accountUpdateSchema', 'export const accountDraftTestAccountSchema')
const accountDraftTestAccountSchemaSource = sourceBetween(schemasSource, 'export const accountDraftTestAccountSchema', 'export const accountTestSchema')
const accountTestSchemaSource = sourceBetween(schemasSource, 'export const accountTestSchema', 'export const accountDraftTestSchema')
const accountDraftTestSchemaSource = sourceBetween(schemasSource, 'export const accountDraftTestSchema', 'export const accountGroupSchema')

assert.doesNotMatch(accountCreateSchemaSource, /clientCompatibility/, '账号创建接口不得暴露客户端画像字段')
assert.doesNotMatch(accountUpdateSchemaSource, /clientCompatibility/, '账号更新接口不得暴露客户端画像字段')
assert.doesNotMatch(accountDraftTestAccountSchemaSource, /clientCompatibility/, '账号草稿测试账号片段不得暴露客户端画像字段')
assert.doesNotMatch(accountTestSchemaSource, /clientCompatibility/, '账号测试接口不得暴露客户端画像字段')
assert.doesNotMatch(accountDraftTestSchemaSource, /clientCompatibility/, '账号草稿测试接口不得暴露客户端画像字段')

const group = repositories.createGroup({
  name: '账号客户端画像边界分组',
  providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  enabled: true
}, access)
createApiKeyRecordWithRouteStrategy(repositories, {
  name: '账号客户端画像边界 Key',
  groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
}, access)

assert.throws(() => {
  repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: '不允许创建写入客户端画像账号',
    type: 'api_key',
    clientCompatibility: 'codex_responses',
    credentials: {
      api_key: 'sk-client-compatibility-create-boundary',
      base_url: 'https://upstream.example.test/v1'
    },
    groupId: group.id,
    status: 'active'
  }, access)
}, /未知字段|clientCompatibility/, '账号创建仓储入口不得接收 clientCompatibility')

const account = repositories.createAccount({
  providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  name: '客户端画像派生账号',
  type: 'api_key',
  credentials: {
    api_key: 'sk-client-compatibility-derived',
    base_url: 'https://upstream.example.test/v1'
  },
  groupId: group.id,
  status: 'active'
}, access)
assert.equal(account.clientCompatibility, 'openai_standard', '普通 OpenAI 兼容账号客户端画像应由内部派生为标准 OpenAI')

assert.throws(() => {
  repositories.updateAccount(account.id, {
    clientCompatibility: 'codex_responses'
  }, access)
}, /未知字段|clientCompatibility/, '账号更新仓储入口不得接收 clientCompatibility')

console.log('账号客户端画像边界回归通过：创建/更新/测试接口均不暴露 clientCompatibility，仓储按内部规则派生')
try {
  rmSync(tempRoot, { recursive: true, force: true })
} catch {
  // Windows may keep node:sqlite handles alive until process exit.
}

function readSource(path: string): string {
  return readFileSync(resolve(repoRoot, path), 'utf8')
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  assert.notEqual(startIndex, -1, `未找到片段起点：${start}`)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(endIndex, -1, `未找到片段终点：${end}`)
  return source.slice(startIndex, endIndex)
}
