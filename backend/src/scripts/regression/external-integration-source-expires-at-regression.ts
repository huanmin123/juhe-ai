import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-external-integration-source-expires-at-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'external-integration-source-expires-at-secret'
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
    name: '外部来源过期时间契约',
    status: 'active',
    scopes: [externalSources.externalIntegrationGroupListReadScope],
    expiresAt: '2026-06-01T00:00:00Z'
  })
  assert.equal(source.expiresAt, '2026-06-01T00:00:00.000Z', '标准过期时间应按 ISO 入库')

  assert.throws(() => externalSources.createExternalIntegrationSource({
    name: '外部来源宽松斜杠日期',
    expiresAt: '2026/06/01'
  }), /过期时间无效/, '来源系统 expiresAt 不应接受斜杠日期')
  assert.throws(() => externalSources.createExternalIntegrationSource({
    name: '外部来源不存在日历日期',
    expiresAt: '2026-02-31T00:00:00'
  }), /过期时间无效/, '来源系统 expiresAt 不应被 Date 自动修正')
  assert.throws(() => externalSources.updateExternalIntegrationSource(source.id, {
    expiresAt: ''
  }), /过期时间无效/, '来源系统 expiresAt 空字符串不应被兼容为清空')
  assert.throws(() => externalSources.updateExternalIntegrationSource(source.id, {
    expiresAt: 'June 1, 2026'
  }), /过期时间无效/, '来源系统 expiresAt 不应接受英文日期')
  assert.equal(externalSources.findExternalIntegrationSource(source.id)?.expiresAt, '2026-06-01T00:00:00.000Z', '非法更新来源系统 expiresAt 不应改变原值')

  const clearedSource = externalSources.updateExternalIntegrationSource(source.id, { expiresAt: null })
  assert.equal(clearedSource?.expiresAt, undefined, '来源系统 expiresAt 必须用 null 显式清空')

  const token = externalSources.createExternalIntegrationSourceToken({
    sourceRefId: source.id,
    name: '外部来源 token 过期时间契约',
    token: 'juis_external_source_expires_contract_token',
    expiresAt: '2026-06-01T01:00:00Z'
  })
  assert.equal(token.expiresAt, '2026-06-01T01:00:00.000Z', 'Token 标准过期时间应按 ISO 入库')

  assert.throws(() => externalSources.createExternalIntegrationSourceToken({
    sourceRefId: source.id,
    name: '外部来源 token 宽松日期',
    token: 'juis_external_source_expires_contract_bad_token',
    expiresAt: '2026/06/01'
  }), /过期时间无效/, '来源系统 token expiresAt 不应接受斜杠日期')
  assert.throws(() => externalSources.updateExternalIntegrationSourceToken(source.id, token.id, {
    expiresAt: '2026-02-31T00:00:00'
  }), /过期时间无效/, '来源系统 token expiresAt 不应被 Date 自动修正')
  assert.equal(externalSources.findExternalIntegrationSource(source.id)?.tokens?.find((item) => item.id === token.id)?.expiresAt, '2026-06-01T01:00:00.000Z', '非法更新 token expiresAt 不应改变原值')

  const clearedToken = externalSources.updateExternalIntegrationSourceToken(source.id, token.id, { expiresAt: null })
  assert.equal(clearedToken?.expiresAt, undefined, '来源系统 token expiresAt 必须用 null 显式清空')

  console.log('外部来源过期时间契约回归通过：不再接受 Date.parse 宽松日期或空字符串清空')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
