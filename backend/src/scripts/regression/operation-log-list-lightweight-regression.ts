import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const workspace = mkdtempSync(join(tmpdir(), 'juhe-operation-log-list-'))

const listKeys = [
  'id',
  'traceId',
  'actorSystemAccountId',
  'actorDisplayName',
  'actorSystemAccountName',
  'operationScopeSystemAccountId',
  'operationScopeSystemAccountName',
  'module',
  'action',
  'summary',
  'createdAt'
].sort()

try {
  const { runtimeConfig } = await import('../../config/runtime.js')
  runtimeConfig.databaseDriver = 'sqlite'
  runtimeConfig.databasePath = join(workspace, 'business.sqlite')
  runtimeConfig.datasetDatabasePath = join(workspace, 'dataset.sqlite')
  runtimeConfig.statsDatabasePath = join(workspace, 'stats.sqlite')
  runtimeConfig.secret = 'operation-log-lightweight-secret'
  runtimeConfig.processRole = 'worker'
  const {
    createOperationLogsBatch,
    getOperationLogDetail,
    listOperationLogs
  } = await import('../../storage/repositories.js')

  createOperationLogsBatch([{
    id: 'operation-log-lightweight-1',
    traceId: 'trace-lightweight',
    actorSystemAccountId: 'sys_admin',
    actorUsername: 'admin',
    actorDisplayName: '管理员',
    actorRole: 'super_admin',
    mode: 'admin',
    module: 'accounts',
    action: 'update',
    operationKey: 'accounts.update',
    resourceType: 'account',
    resourceId: 'account-lightweight',
    resourceName: '轻量账户',
    summary: '更新轻量账户',
    changes: [{ field: 'name', label: '名称', before: 'A', after: 'B' }],
    metadata: { secretProof: 'detail-only' },
    method: 'PATCH',
    path: '/accounts/account-lightweight',
    statusCode: 200,
    clientIp: '127.0.0.1',
    userAgent: 'operation-list-wide-proof',
    createdAt: '2026-07-22T00:00:00.000Z'
  }])

  const list = listOperationLogs({ page: 1, pageSize: 20 })
  assert.equal(list.items.length, 1)
  assert.deepEqual(Object.keys(list.items[0] ?? {}).sort(), listKeys, 'repository 列表必须直接返回专用轻量 DTO')

  const detail = getOperationLogDetail('operation-log-lightweight-1')
  assert(detail)
  assert.equal(detail.userAgent, 'operation-list-wide-proof')
  assert.equal(detail.metadata.secretProof, 'detail-only')
  assert.equal(detail.changes.length, 1)

  const repositorySource = await import('node:fs').then(({ readFileSync }) => readFileSync(new URL('../../storage/operation-log-read.repository.ts', import.meta.url), 'utf8'))
  const listProjection = repositorySource.match(/function operationLogListSelectColumns[\s\S]*?\n\}/)?.[0] ?? ''
  for (const forbidden of ['actor_username', 'actor_role', 'mode', 'operation_key', 'resource_type', 'resource_id', 'resource_name', 'changes_json', 'metadata_json', 'method', 'path', 'status_code', 'client_ip', 'user_agent']) {
    assert.equal(listProjection.includes(forbidden), false, `列表 SQL 不得投影 ${forbidden}`)
  }

  console.log('操作日志轻量列表回归通过')
} finally {
  const { closeStorageDatabases } = await import('../../storage/database.js')
  closeStorageDatabases()
  rmSync(workspace, { recursive: true, force: true })
}
