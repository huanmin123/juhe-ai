import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-list-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'api-key-list-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: 'API Key 列表查询防护分组',
    providerCode: 'openai',
    enabled: true
  }, access)
  const matchedByName = repositories.createApiKeyRecord({
    name: '检索目标 Key',
    description: '普通说明',
    groupId: group.id
  }, access)
  const matchedByNamePrefix = repositories.createApiKeyRecord({
    name: '检索目标 Key 扩展',
    description: '普通说明扩展',
    groupId: group.id
  }, access)
  const middleNameOnly = repositories.createApiKeyRecord({
    name: '普通检索目标 Key',
    description: '普通说明中间命中',
    groupId: group.id
  }, access)
  const matchedByDescription = repositories.createApiKeyRecord({
    name: '说明前缀 Key',
    description: '说明前缀命中',
    groupId: group.id
  }, access)
  const middleDescriptionOnly = repositories.createApiKeyRecord({
    name: '普通说明 Key',
    description: '普通说明前缀命中',
    groupId: group.id
  }, access)
  const wildcardLiteral = repositories.createApiKeyRecord({
    name: 'percent%literal Key',
    description: '通配符字面量',
    groupId: group.id
  }, access)
  const wildcardNeighbor = repositories.createApiKeyRecord({
    name: 'percentXliteral Key',
    description: '通配符邻近值',
    groupId: group.id
  }, access)

  const database = databaseModule.getDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+api_keys\b/i.test(sql) && /\bORDER\s+BY\s+updated_at\s+DESC\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare

  try {
    const nameResult = repositories.listApiKeysPage(access, { keyword: '检索目标 Key', page: 1, pageSize: 20 })
    const nameIds = nameResult.items.map((item) => item.id)
    assert(nameIds.includes(matchedByName.id), 'API Key 搜索应命中名称精确值')
    assert(nameIds.includes(matchedByNamePrefix.id), 'API Key 搜索应命中名称前缀值')
    assert(!nameIds.includes(middleNameOnly.id), 'API Key 搜索不应命中名称中间包含值')

    const descriptionResult = repositories.listApiKeysPage(access, { keyword: '说明前缀', page: 1, pageSize: 20 })
    const descriptionIds = descriptionResult.items.map((item) => item.id)
    assert(descriptionIds.includes(matchedByDescription.id), 'API Key 搜索应命中说明前缀值')
    assert(!descriptionIds.includes(middleDescriptionOnly.id), 'API Key 搜索不应命中说明中间包含值')

    const keyPrefix = uniquePrefix(matchedByName.keyPrefix, matchedByNamePrefix.keyPrefix)
    const keyPrefixResult = repositories.listApiKeysPage(access, { keyword: keyPrefix, page: 1, pageSize: 20 })
    assert(keyPrefixResult.items.some((item) => item.id === matchedByName.id), 'API Key 搜索应支持 Key 前缀定位')

    const wildcardResult = repositories.listApiKeysPage(access, { keyword: 'percent%', page: 1, pageSize: 20 })
    const wildcardIds = wildcardResult.items.map((item) => item.id)
    assert(wildcardIds.includes(wildcardLiteral.id), 'API Key 搜索应把 % 当作字面量前缀处理')
    assert(!wildcardIds.includes(wildcardNeighbor.id), 'API Key 搜索不应把用户输入的 % 当作 LIKE 通配符')
  } finally {
    database.prepare = originalPrepare
  }

  assert(capturedCalls.length >= 4, '回归应捕获 API Key 列表 SQL')
  for (const call of capturedCalls) {
    assert(!/\bCOALESCE\s*\(/i.test(call.sql), 'API Key 列表搜索不应通过 COALESCE 扫描说明字段')
    assert(/\bESCAPE\s+'\\'/i.test(call.sql), 'API Key 列表前缀搜索应显式转义 LIKE 通配符')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), 'API Key 列表搜索不应传入前导通配符参数')
  }
  assertBusinessIndexExists('idx_api_keys_key_prefix_lookup')
  assertBusinessIndexExists('idx_api_keys_system_account_key_prefix_lookup')
  assertBusinessIndexExists('idx_api_keys_name_lookup')
  assertBusinessIndexExists('idx_api_keys_system_account_name_lookup')
  assertBusinessIndexExists('idx_api_keys_description_lookup')
  assertBusinessIndexExists('idx_api_keys_system_account_description_lookup')

  console.log('API Key 列表查询防护回归通过：搜索仅支持精确/前缀匹配，不再使用前导通配符或包含匹配')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function uniquePrefix(value: string, otherValue: string): string {
  for (let length = 1; length <= value.length; length += 1) {
    const prefix = value.slice(0, length)
    if (!otherValue.startsWith(prefix)) return prefix
  }
  return value
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}
