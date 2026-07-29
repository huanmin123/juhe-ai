import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-external-integration-source-list-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'external-integration-source-list-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, externalSources] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/external-integration-source.repository.js')
])

try {
  const source = externalSources.createExternalIntegrationSource({
    name: '外部来源列表批量摘要契约',
    status: 'active',
    scopes: [externalSources.externalIntegrationGroupListReadScope]
  })
  const disabled = externalSources.createExternalIntegrationSourceToken({
    sourceRefId: source.id,
    name: '较新停用 Token',
    token: 'juis_external_source_list_disabled_token',
    status: 'disabled',
    scopes: source.scopes
  })
  const activeExpired = externalSources.createExternalIntegrationSourceToken({
    sourceRefId: source.id,
    name: '已过期但仍 active Token',
    token: 'juis_external_source_list_active_expired_token',
    status: 'active',
    scopes: source.scopes,
    expiresAt: '2020-01-01T00:00:00Z'
  })
  const revoked = externalSources.createExternalIntegrationSourceToken({
    sourceRefId: source.id,
    name: '最新撤销 Token',
    token: 'juis_external_source_list_revoked_token',
    status: 'revoked',
    scopes: source.scopes
  })

  const database = databaseModule.getBusinessDatabase()
  const timestamps = new Map([
    [activeExpired.id, '2026-01-01T00:00:00.000Z'],
    [disabled.id, '2026-01-02T00:00:00.000Z'],
    [revoked.id, '2026-01-03T00:00:00.000Z']
  ])
  for (const [id, createdAt] of timestamps) {
    database.prepare(`
      UPDATE external_integration_source_tokens
      SET created_at = ?, updated_at = ?
      WHERE id = ?
    `).run(createdAt, createdAt, id)
  }

  const listed = externalSources.listExternalIntegrationSources({
    keyword: '外部来源列表批量摘要契约',
    page: 1,
    pageSize: 20
  })
  assert.equal(listed.items.length, 1)
  const item = listed.items[0]
  assert(item)
  assert.deepEqual(
    Object.keys(item).sort(),
    ['expiresAt', 'id', 'isBuiltIn', 'lastUsedAt', 'name', 'notes', 'primaryToken', 'rateLimits', 'scopes', 'status', 'updatedAt'].sort(),
    '列表只应返回页面展示和操作所需字段'
  )
  assert.equal(item.primaryToken?.id, activeExpired.id, 'active Token 应优先于创建时间更晚的 disabled/revoked Token')
  assert.equal(item.primaryToken?.tokenPrefix, 'juis_ext')
  assert.equal(item.primaryToken?.tokenSuffix, 'ed_token')
  assert.deepEqual(
    Object.keys(item.primaryToken ?? {}).sort(),
    ['id', 'tokenPrefix', 'tokenSuffix'].sort(),
    '列表主 Token 只能投影复制动作需要的 id/prefix/suffix'
  )

  const repositorySource = readFileSync(resolve('src/storage/external-integration-source.repository.ts'), 'utf8')
  const listImplementation = repositorySource.slice(
    repositorySource.indexOf('export function listExternalIntegrationSources('),
    repositorySource.indexOf('export function findExternalIntegrationSource(')
  )
  assert(!listImplementation.includes('loadExternalIntegrationSourceTokenStatsBySourceIds'), '列表实现不得执行 Token COUNT/SUM/GROUP BY 聚合')

  const emptyPage = externalSources.listExternalIntegrationSources({
    keyword: '不存在的外部来源',
    page: 999999,
    pageSize: 100
  })
  assert.deepEqual(emptyPage.items, [])
  assert.equal(emptyPage.page, 10, '空页也应保持 1000 行分页窗口上界')

  console.log('外部来源轻量列表回归通过：不做 Token 聚合，主 Token 只返回 id/prefix/suffix')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
