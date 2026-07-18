import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
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
  assert.equal(item.tokenCount, 3, '列表应只对当前页批量统计全部 Token')
  assert.equal(item.activeTokenCount, 1, 'activeTokenCount 不应排除已过期但状态仍为 active 的 Token')
  assert.equal(item.primaryToken?.id, activeExpired.id, 'active Token 应优先于创建时间更晚的 disabled/revoked Token')
  assert.equal(item.primaryToken?.expiresAt, '2020-01-01T00:00:00.000Z')
  assert.equal(item.primaryToken?.tokenPrefix, 'juis_ext')
  assert.equal(item.primaryToken?.tokenSuffix, 'ed_token')
  for (const sensitiveField of ['token', 'tokenHash', 'tokenSecretEncrypted']) {
    assert.equal(Object.hasOwn(item.primaryToken ?? {}, sensitiveField), false, `主 Token 摘要不应返回 ${sensitiveField}`)
  }

  const emptyPage = externalSources.listExternalIntegrationSources({
    keyword: '不存在的外部来源',
    page: 999999,
    pageSize: 100
  })
  assert.deepEqual(emptyPage.items, [])
  assert.equal(emptyPage.page, 10, '空页也应保持 1000 行分页窗口上界')

  console.log('外部来源列表批量摘要回归通过：先分页、当前页计数、active 主 Token 优先和敏感字段边界符合契约')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
