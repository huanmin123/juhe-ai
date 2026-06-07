import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-list-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
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
    providerCode: 'gpt',
    enabled: true
  }, access)
  const matchedByName = repositories.createApiKeyRecord({
    name: '检索目标 Key',
    description: '普通说明',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
  }, access)
  const matchedByNamePrefix = repositories.createApiKeyRecord({
    name: '检索目标 Key 扩展',
    description: '普通说明扩展',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
  }, access)
  const middleNameOnly = repositories.createApiKeyRecord({
    name: '普通检索目标 Key',
    description: '普通说明中间命中',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
  }, access)
  const matchedByDescription = repositories.createApiKeyRecord({
    name: '说明字段 Key',
    description: '说明前缀命中',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
  }, access)
  const middleDescriptionOnly = repositories.createApiKeyRecord({
    name: '普通说明 Key',
    description: '普通说明前缀命中',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
  }, access)
  const wildcardLiteral = repositories.createApiKeyRecord({
    name: 'percent%literal Key',
    description: '通配符字面量',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
  }, access)
  const wildcardNeighbor = repositories.createApiKeyRecord({
    name: 'percentXliteral Key',
    description: '通配符邻近值',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
  }, access)

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+api_keys\b/i.test(sql) && /\bORDER\s+BY\s+(?:api_keys\.)?updated_at\s+DESC\b/i.test(sql)) {
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
    assert.equal(nameResult.items.find((item) => item.id === matchedByName.id)?.key, '', 'API Key 列表不应重复返回完整本地密钥')
    assert.equal(matchedByName.key.startsWith(matchedByName.keyPrefix), true, 'API Key 创建响应仍应返回一次完整密钥供用户保存')
    assert.equal(matchedByName.key.endsWith(matchedByName.keySuffix), true, 'API Key 创建响应应返回后缀供列表安全识别')

    const descriptionResult = repositories.listApiKeysPage(access, { keyword: '说明前缀', page: 1, pageSize: 20 })
    const descriptionIds = descriptionResult.items.map((item) => item.id)
    assert(!descriptionIds.includes(matchedByDescription.id), 'API Key 搜索不应通过说明字段命中，避免通用关键词扫描长文本')
    assert(!descriptionIds.includes(middleDescriptionOnly.id), 'API Key 搜索不应命中说明中间包含值')

    const keyPrefixResult = repositories.listApiKeysPage(access, { keyword: matchedByName.keyPrefix, page: 1, pageSize: 20 })
    assert(!keyPrefixResult.items.some((item) => item.id === matchedByName.id), 'API Key 搜索不应通过 Key 前缀命中')

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
    assert(!/\bapi_keys\.key_prefix\s+(?:=|LIKE)\s+\?/i.test(call.sql), 'API Key 列表名称搜索不应把 Key 前缀放进通用关键词 WHERE')
    assert(!/\bapi_keys\.description\s+(?:COLLATE|LIKE)\b/i.test(call.sql), 'API Key 列表搜索不应把说明字段放进通用关键词 WHERE')
    assert(/\bESCAPE\s+'\\'/i.test(call.sql), 'API Key 列表前缀搜索应显式转义 LIKE 通配符')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), 'API Key 列表搜索不应传入前导通配符参数')
  }
  assertBusinessIndexExists('idx_api_keys_name_lookup')
  assertBusinessIndexExists('idx_api_keys_system_account_name_lookup')
  assertBusinessIndexMissing('idx_api_keys_key_prefix_lookup')
  assertBusinessIndexMissing('idx_api_keys_system_account_key_prefix_lookup')
  assertBusinessIndexMissing('idx_api_keys_description_lookup')
  assertBusinessIndexMissing('idx_api_keys_system_account_description_lookup')

  console.log('API Key 列表查询防护回归通过：搜索仅按名称精确/前缀匹配，列表不返回完整密钥')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}

function assertBusinessIndexMissing(indexName: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, undefined, `业务库不应创建 API Key 长文本搜索索引 ${indexName}`)
}
