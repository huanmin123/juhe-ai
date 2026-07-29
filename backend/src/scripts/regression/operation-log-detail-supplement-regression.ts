import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const workspace = mkdtempSync(join(tmpdir(), 'juhe-operation-log-detail-supplement-'))
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
]
const supplementKeys = [
  'changes',
  'clientIp',
  'method',
  'operationKey',
  'path',
  'resourceId',
  'resourceName',
  'resourceType',
  'targets',
  'viewers',
  'visibilityScope'
].sort()

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'

let closeSqliteReadWorkerPool: (() => Promise<void>) | undefined

try {
  const { runtimeConfig } = await import('../../config/runtime.js')
  runtimeConfig.databaseDriver = 'sqlite'
  runtimeConfig.databasePath = join(workspace, 'business.sqlite')
  runtimeConfig.datasetDatabasePath = join(workspace, 'dataset.sqlite')
  runtimeConfig.usageCatalogDatabasePath = join(workspace, 'usage-catalog.sqlite')
  runtimeConfig.statsDatabasePath = join(workspace, 'stats.sqlite')
  runtimeConfig.secret = 'operation-log-detail-supplement-secret'
  runtimeConfig.processRole = 'worker'
  runtimeConfig.sqliteReadWorkerPoolSize = 1

  const repositories = await import('../../storage/repositories.js')
  const readWorkerPool = await import('../../storage/sqlite-read-worker-pool.js')
  closeSqliteReadWorkerPool = readWorkerPool.closeSqliteReadWorkerPool

  repositories.createOperationLogsBatch([
    operationLogFixture('operation-detail-full', {
      visibilityScope: 'targeted',
      viewers: [{ systemAccountId: 'viewer-full', visibilityReason: 'resource_owner', detailLevel: 'full' }]
    }),
    operationLogFixture('operation-detail-summary', {
      visibilityScope: 'all_users'
    }),
    operationLogFixture('operation-detail-viewer-summary', {
      visibilityScope: 'targeted',
      viewers: [{ systemAccountId: 'viewer-summary', visibilityReason: 'resource_owner', detailLevel: 'summary' }]
    }),
    operationLogFixture('operation-detail-hidden', {
      visibilityScope: 'targeted',
      viewers: [{ systemAccountId: 'someone-else', visibilityReason: 'resource_owner', detailLevel: 'full' }]
    })
  ])

  const adminSupplement = repositories.getOperationLogDetailSupplement('operation-detail-full')
  assert(adminSupplement)
  assert.deepEqual(Object.keys(adminSupplement).sort(), supplementKeys, '详情接口必须只返回列表行的字段差集')
  for (const listKey of listKeys) {
    assert.equal(Object.prototype.hasOwnProperty.call(adminSupplement, listKey), false, `详情增量不得重复返回列表字段 ${listKey}`)
  }
  assert.equal(adminSupplement.changes.length, 1)
  assert.equal(adminSupplement.resourceId, 'account-1')
  assert(adminSupplement.targets.some((target) => target.targetId === 'account-1' && target.targetName === '账户 1' && target.relation === 'affected'))
  assert(adminSupplement.viewers.some((viewer) => viewer.systemAccountId === 'viewer-full'))
  assert.equal(adminSupplement.clientIp, '127.0.0.1')
  assert.deepEqual(
    Object.keys(JSON.parse(JSON.stringify(adminSupplement)) as Record<string, unknown>).sort(),
    supplementKeys,
    '序列化后的 HTTP 数据也必须保持顶层字段白名单'
  )
  const affectedTarget = adminSupplement.targets.find((target) => target.relation === 'affected')
  assert(affectedTarget)
  assert.deepEqual(
    Object.keys(JSON.parse(JSON.stringify(affectedTarget)) as Record<string, unknown>).sort(),
    ['id', 'relation', 'targetId', 'targetName', 'targetType'],
    '影响对象增量不得返回未渲染的 ownerId 或 createdAt'
  )
  const fullViewer = adminSupplement.viewers.find((viewer) => viewer.systemAccountId === 'viewer-full')
  assert(fullViewer)
  assert.deepEqual(
    Object.keys(JSON.parse(JSON.stringify(fullViewer)) as Record<string, unknown>).sort(),
    ['detailLevel', 'systemAccountId', 'visibilityReason'],
    '可见用户增量不得返回未渲染的 createdAt'
  )

  const fullViewerSupplement = repositories.getOperationLogDetailSupplementForViewer('operation-detail-full', 'viewer-full')
  assert(fullViewerSupplement)
  assert.equal(fullViewerSupplement.changes.length, 1)
  assert(fullViewerSupplement.targets.some((target) => target.targetName === '账户 1'))
  assert.deepEqual(fullViewerSupplement.viewers, [], '普通用户完整详情也不得看到可见用户集合')
  assert.equal(fullViewerSupplement.clientIp, undefined, '普通用户不得看到客户端 IP')
  assert.equal(
    Object.prototype.hasOwnProperty.call(JSON.parse(JSON.stringify(fullViewerSupplement)), 'clientIp'),
    false,
    '普通用户 HTTP 响应不得序列化客户端 IP 字段'
  )

  const { getDatasetDatabase } = await import('../../storage/database.js')
  const datasetDatabase = getDatasetDatabase()
  const originalPrepare = datasetDatabase.prepare.bind(datasetDatabase)
  const summaryPreparedSql: string[] = []
  Object.defineProperty(datasetDatabase, 'prepare', {
    configurable: true,
    value: (sql: string) => {
      summaryPreparedSql.push(sql)
      return originalPrepare(sql)
    }
  })
  try {
    assertSummarySupplement(
      repositories.getOperationLogDetailSupplementForViewer('operation-detail-summary', 'viewer-any')
    )
    assertSummarySupplement(
      repositories.getOperationLogDetailSupplementForViewer('operation-detail-viewer-summary', 'viewer-summary')
    )
  } finally {
    Reflect.deleteProperty(datasetDatabase, 'prepare')
  }
  assert.equal(summaryPreparedSql.length, 2, '普通用户摘要详情每次只能执行权限/等级合并后的单条 SQL')
  for (const sql of summaryPreparedSql) {
    assert.equal(sql.includes('changes_json'), false, '普通用户摘要详情不得读取 changes_json')
    assert.equal(sql.includes('FROM operation_log_targets'), false, '普通用户摘要详情不得读取影响对象')
    assert.equal(sql.includes('FROM system_accounts'), false, '普通用户摘要详情不得查询名称映射')
  }
  assert.equal(
    repositories.getOperationLogDetailSupplementForViewer('operation-detail-hidden', 'viewer-full'),
    undefined,
    '普通用户详情必须在 SQL 权限条件中拒绝不可见日志'
  )

  const legacyDetail = repositories.getOperationLogDetail('operation-detail-full')
  assert(legacyDetail)
  assert.equal(legacyDetail.id, 'operation-detail-full', '现有完整详情仓储必须继续供内部调用兼容')
  assert.equal(legacyDetail.resourceId, 'account-1', '列表瘦身不得误删内部完整详情的资源 ID')
  assert.equal(legacyDetail.summary, '更新操作日志 operation-detail-full')
  assert.equal(legacyDetail.userAgent, 'operation-detail-agent')

  runtimeConfig.processRole = 'db-service'
  const handledJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  const asyncSupplement = await repositories.getOperationLogDetailSupplementAsync('operation-detail-full')
  assert(asyncSupplement)
  assert.equal(asyncSupplement.operationKey, 'accounts.update')
  assert(
    readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs >= handledJobsBefore + 1,
    'SQLite DB service 的详情增量读取必须进入 read worker'
  )

  assertSourceContracts()
  console.log('操作日志详情增量回归通过')
} finally {
  await closeSqliteReadWorkerPool?.()
  const { closeStorageDatabases } = await import('../../storage/database.js')
  closeStorageDatabases()
  rmSync(workspace, { recursive: true, force: true })
}

function operationLogFixture(
  id: string,
  visibility: {
    visibilityScope: 'targeted' | 'all_users'
    viewers?: Array<{
      systemAccountId: string
      visibilityReason: 'resource_owner'
      detailLevel: 'full' | 'summary'
    }>
  }
) {
  return {
    id,
    traceId: `trace-${id}`,
    actorSystemAccountId: 'sys-admin',
    actorUsername: 'admin',
    actorDisplayName: '管理员',
    actorRole: 'super_admin' as const,
    operationScopeSystemAccountId: 'sys-owner',
    mode: 'admin' as const,
    module: 'accounts',
    action: 'update',
    operationKey: 'accounts.update',
    resourceType: 'account',
    resourceId: 'account-1',
    resourceName: '账户 1',
    summary: `更新操作日志 ${id}`,
    detailLevel: 'full' as const,
    visibilityScope: visibility.visibilityScope,
    changes: [{ field: 'name', label: '名称', before: 'A', after: 'B' }],
    metadata: { proof: 'detail-only' },
    method: 'PATCH',
    path: '/accounts/account-1',
    statusCode: 200,
    clientIp: '127.0.0.1',
    userAgent: 'operation-detail-agent',
    targets: [{
      targetType: 'account',
      targetId: 'account-1',
      targetName: '账户 1',
      relation: 'affected' as const
    }],
    viewers: visibility.viewers,
    createdAt: '2026-07-29T00:00:00.000Z'
  }
}

function assertSummarySupplement(supplement: {
  changes: unknown[]
  method?: string
  path?: string
  clientIp?: string
  userAgent?: string
  targets: unknown[]
  viewers: unknown[]
} | undefined): void {
  assert(supplement)
  assert.deepEqual(supplement.changes, [])
  assert.equal(supplement.method, undefined)
  assert.equal(supplement.path, undefined)
  assert.equal(supplement.clientIp, undefined)
  assert.equal(supplement.userAgent, undefined)
  assert.deepEqual(supplement.targets, [])
  assert.deepEqual(supplement.viewers, [])
}

function assertSourceContracts(): void {
  const repositorySource = readFileSync(new URL('../../storage/operation-log-detail-supplement.repository.ts', import.meta.url), 'utf8')
  const projection = repositorySource.match(/function operationLogAdminDetailSelectColumns[\s\S]*?\n\}/)?.[0] ?? ''
  for (const forbidden of [
    "'id'",
    'trace_id',
    'actor_system_account_id',
    'actor_display_name',
    'operation_scope_system_account_id',
    "'module'",
    "'action'",
    "'summary'",
    'created_at',
    'metadata_json',
    'status_code',
    'actor_role',
    'actor_username'
  ]) {
    assert.equal(projection.includes(forbidden), false, `详情增量主表投影不得重复列表字段 ${forbidden}`)
  }
  assert.equal(repositorySource.includes('SELECT *'), false, '详情增量及关联表查询不得使用 SELECT *')
  const viewerBaseProjection = repositorySource.match(/function operationLogViewerBaseSelectColumns[\s\S]*?\n\}/)?.[0] ?? ''
  for (const payloadColumn of ['changes_json', 'method', 'path', 'client_ip', 'user_agent']) {
    assert.equal(viewerBaseProjection.includes(payloadColumn), false, `普通用户权限/等级首查不得提前读取 ${payloadColumn}`)
  }

  const routesSource = readFileSync(new URL('../../modules/operation-logs/operation-logs.routes.ts', import.meta.url), 'utf8')
  assert.match(routesSource, /getOperationLogDetailSupplementAsync/)
  assert.match(routesSource, /getOperationLogDetailSupplementForViewerAsync/)
  assert.doesNotMatch(routesSource, /\bgetOperationLogDetailAsync\b/)
  assert.doesNotMatch(routesSource, /\bgetOperationLogDetailForViewerAsync\b/)

  const workerSource = readFileSync(new URL('../../storage/sqlite-read-worker.ts', import.meta.url), 'utf8')
  const workerTypesSource = readFileSync(new URL('../../storage/sqlite-read-worker-pool.types.ts', import.meta.url), 'utf8')
  for (const operation of [
    'get_operation_log_detail_supplement_read_only',
    'get_operation_log_detail_supplement_for_viewer_read_only'
  ]) {
    assert(workerSource.includes(`case '${operation}'`), `read worker 必须处理 ${operation}`)
    assert(workerTypesSource.includes(`type: '${operation}'`), `read worker 协议必须声明 ${operation}`)
  }
}
