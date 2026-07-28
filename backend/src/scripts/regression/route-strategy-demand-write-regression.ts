import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'
import { fileURLToPath } from 'node:url'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-route-strategy-demand-write-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'route-strategy-demand-write-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, routeStrategyRepository, routeStrategyRoutes, authRequestContext] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/route-strategy.repository.js'),
  import('../../modules/route-strategies/route-strategies.routes.js'),
  import('../../modules/auth/request-context.js')
])
const routeSource = readFileSync(fileURLToPath(new URL('../../modules/route-strategies/route-strategies.routes.ts', import.meta.url)), 'utf8')
const routePatchSource = sourceBetween(routeSource, "routeStrategiesRouter.patch('/:id'", "routeStrategiesRouter.delete('/:id'")
assert.doesNotMatch(routePatchSource, /findRouteStrategy(?:EditBasicDetail|Summary)Async/, 'PATCH 路由不得在写入前后读取完整策略路由或编辑详情')
assert.match(routePatchSource, /patchRouteStrategyAsync[\s\S]*result: mutation\.result/, 'PATCH 路由必须直接返回仓储最小 mutation result')
assert.match(routePatchSource, /log: mutation\.result\.changedFields\.length[\s\S]*if \(routeStrategy\.changedFields\.length\)[\s\S]*clearNormalRouteSpeedFirstRuntime/, 'no-op PATCH 必须跳过操作日志和运行态清理')
assert.match(routePatchSource, /RouteStrategyVersionConflictError[\s\S]*status\(409\)/, 'PATCH 路由必须把 CAS 冲突映射为 409')

let server: ReturnType<ReturnType<typeof express>['listen']> | undefined

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const primaryGroup = repositories.createGroup({
    name: '策略路由按需写主分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const replacementGroup = repositories.createGroup({
    name: '策略路由按需写替换分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const addedGroup = repositories.createGroup({
    name: '策略路由按需写新增分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const routeStrategy = repositories.createRouteStrategy({
    name: '策略路由按需写回归',
    description: '初始说明',
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: primaryGroup.id, priority: 1, weight: 1, status: 'active' }]
  }, access)
  const database = databaseModule.getBusinessDatabase()

  const readCapture = captureQueries(() => routeStrategyRepository.findRouteStrategyEditBasicDetail(routeStrategy.id, access))
  assert(readCapture.result, 'edit-basic 应返回策略路由')
  assert.deepEqual(Object.keys(readCapture.result).sort(), [
    'description',
    'groupBindings',
    'hybridRoutingConfig',
    'id',
    'isDefault',
    'mode',
    'name',
    'normalRoutingConfig',
    'status',
    'systemAccountId',
    'updatedAt'
  ].sort(), 'edit-basic 必须保持精确字段白名单')
  assert.equal(readCapture.queries.length, 2, `edit-basic 只应读取主投影和绑定投影，实际 ${readCapture.queries.length} 条`)
  const editSql = readCapture.queries.join('\n')
  assert.doesNotMatch(editSql, /api_keys|COUNT\s*\(|system_accounts/i, 'edit-basic 不得读取 API Key 计数或 owner 详情')
  assert.doesNotMatch(readCapture.queries[0] ?? '', /created_at/i, 'edit-basic 主投影不得读取创建时间')
  assert.match(readCapture.queries[0] ?? '', /updated_at/i, 'edit-basic 必须携带 CAS 所需的更新时间版本')
  assert.doesNotMatch(editSql, /SELECT\s+\*/i, 'edit-basic 不得使用 SELECT *')

  const initialBinding = bindingRow(routeStrategy.id)
  const initialUpdatedAt = routeStrategyRow(routeStrategy.id).updated_at

  const sameValue = captureDml(() => repositories.patchRouteStrategy(routeStrategy.id, {
    description: '初始说明',
    expectedUpdatedAt: initialUpdatedAt
  }, access))
  assert(sameValue.result, '同值 PATCH 应返回最小 mutation result')
  assert.deepEqual(sameValue.result.result, { id: routeStrategy.id, changedFields: [], rowPatch: {} }, '同值 PATCH 响应必须保持最小 no-op 形态')
  assert.deepEqual(sameValue.statements, [], '同值 PATCH 不得执行任何 DML')
  assert.equal(routeStrategyRow(routeStrategy.id).updated_at, initialUpdatedAt, '同值 PATCH 不得推进更新时间')
  assert.deepEqual(bindingRow(routeStrategy.id), initialBinding, '同值 PATCH 不得重写分组绑定')

  const descriptionPatch = captureDml(() => repositories.patchRouteStrategy(routeStrategy.id, {
    description: '只修改说明',
    expectedUpdatedAt: initialUpdatedAt
  }, access))
  assert(descriptionPatch.result, '说明 PATCH 应返回 mutation result')
  assert.deepEqual(Object.keys(descriptionPatch.result.result).sort(), ['changedFields', 'id', 'rowPatch'], '仓储 PATCH 不得返回完整 RouteStrategySummary')
  assert.deepEqual(descriptionPatch.result.result.changedFields, ['description'])
  assert.equal(descriptionPatch.result.result.rowPatch.description, '只修改说明')
  assert.equal(typeof descriptionPatch.result.result.rowPatch.updatedAt, 'string')
  const descriptionUpdatedAt = descriptionPatch.result.result.rowPatch.updatedAt!
  assert(Date.parse(descriptionUpdatedAt) > Date.parse(initialUpdatedAt), '真实 PATCH 必须单调推进更新时间版本')
  assert.equal(descriptionPatch.statements.length, 1, '说明 PATCH 只应执行一条聚合根 UPDATE')
  assert.match(descriptionPatch.statements[0] ?? '', /SET\s+description\s*=\s*\?,\s*updated_at\s*=\s*\?/i)
  assert.match(descriptionPatch.statements[0] ?? '', /WHERE\s+id\s*=\s*\?\s+AND\s+system_account_id\s*=\s*\?\s+AND\s+updated_at\s*=\s*\?/i, '聚合根 UPDATE 必须同时定位 id、owner 和版本')
  assert.doesNotMatch(descriptionPatch.statements[0] ?? '', /name\s*=|mode\s*=|status\s*=|config_json\s*=/i, '说明 PATCH 不得覆盖未提交列')
  assert.doesNotMatch(descriptionPatch.queries.join('\n'), /api_keys|system_accounts|COUNT\s*\(/i, 'PATCH 审计差异不得触发完整摘要或 owner 宽读')
  assert.deepEqual(bindingRow(routeStrategy.id), initialBinding, '未提交 groupBindings 时不得重写绑定')

  const namePatch = captureDml(() => repositories.patchRouteStrategy(routeStrategy.id, {
    name: '策略路由按需写回归-改名',
    expectedUpdatedAt: descriptionUpdatedAt
  }, access))
  assert.equal(namePatch.statements.length, 1, '名称 PATCH 只应执行一条聚合根 UPDATE')
  assert.match(namePatch.statements[0] ?? '', /SET\s+name\s*=\s*\?,\s*updated_at\s*=\s*\?/i)
  const afterIndependentPatches = repositories.findRouteStrategySummary(routeStrategy.id, access)
  assert.equal(afterIndependentPatches?.name, '策略路由按需写回归-改名')
  assert.equal(afterIndependentPatches?.description, '只修改说明', '先后提交不同字段时，后一次 PATCH 不得覆盖前一次字段')
  assert.deepEqual(bindingRow(routeStrategy.id), initialBinding, '连续标量 PATCH 仍不得重写绑定')

  const sameBindings = captureDml(() => repositories.patchRouteStrategy(routeStrategy.id, {
    groupBindings: [{ groupId: primaryGroup.id, priority: 1, weight: 1, status: 'active' }],
    expectedUpdatedAt: namePatch.result!.result.rowPatch.updatedAt!
  }, access))
  assert.deepEqual(sameBindings.statements, [], '相同 groupBindings PATCH 必须识别为 no-op')
  assert.deepEqual(bindingRow(routeStrategy.id), initialBinding, '相同绑定不得刷新关系行 ID 或时间')

  const changedBindings = captureDml(() => repositories.patchRouteStrategy(routeStrategy.id, {
    groupBindings: [{ groupId: replacementGroup.id, priority: 1, weight: 1, status: 'active' }],
    expectedUpdatedAt: namePatch.result!.result.rowPatch.updatedAt!
  }, access))
  assert.equal(changedBindings.statements.length, 3, '替换一个绑定只应更新聚合根、删除旧关系并插入新关系')
  assert.match(changedBindings.statements[0] ?? '', /UPDATE\s+route_strategies/i)
  assert.match(changedBindings.statements[1] ?? '', /DELETE\s+FROM\s+route_strategy_groups/i)
  assert.match(changedBindings.statements[2] ?? '', /INSERT\s+INTO\s+route_strategy_groups/i)
  assert.equal(bindingRow(routeStrategy.id).group_id, replacementGroup.id)
  assert.equal(changedBindings.result?.result.rowPatch.bindingCount, 1)
  assert.equal(changedBindings.result?.result.rowPatch.groupBindingPreview?.[0]?.groupId, replacementGroup.id)

  const deltaStrategy = repositories.createRouteStrategy({
    name: '策略路由绑定差量回归',
    mode: 'weighted',
    status: 'active',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, weight: 50, status: 'active' },
      { groupId: replacementGroup.id, priority: 2, weight: 50, status: 'active' }
    ]
  }, access)
  const beforeAddRows = bindingRows(deltaStrategy.id)
  const addBindingPatch = captureDml(() => repositories.patchRouteStrategy(deltaStrategy.id, {
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, weight: 50, status: 'active' },
      { groupId: replacementGroup.id, priority: 2, weight: 50, status: 'active' },
      { groupId: addedGroup.id, priority: 3, weight: 1, status: 'active' }
    ],
    expectedUpdatedAt: deltaStrategy.updatedAt
  }, access))
  assert.equal(addBindingPatch.statements.length, 2, '单增绑定只应写聚合根和一条 INSERT')
  assert.match(addBindingPatch.statements[1] ?? '', /INSERT\s+INTO\s+route_strategy_groups/i)
  const afterAddRows = bindingRows(deltaStrategy.id)
  for (const previous of beforeAddRows) {
    assert.equal(afterAddRows.find((row) => row.group_id === previous.group_id)?.id, previous.id, '单增绑定不得重建已有关系行')
  }

  const removeBindingPatch = captureDml(() => repositories.patchRouteStrategy(deltaStrategy.id, {
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, weight: 50, status: 'active' },
      { groupId: replacementGroup.id, priority: 2, weight: 50, status: 'active' }
    ],
    expectedUpdatedAt: addBindingPatch.result!.result.rowPatch.updatedAt!
  }, access))
  assert.equal(removeBindingPatch.statements.length, 2, '单删绑定只应写聚合根和一条 DELETE')
  assert.match(removeBindingPatch.statements[1] ?? '', /DELETE\s+FROM\s+route_strategy_groups/i)
  const afterRemoveRows = bindingRows(deltaStrategy.id)
  assert.equal(afterRemoveRows.some((row) => row.group_id === addedGroup.id), false)
  for (const previous of beforeAddRows) {
    assert.equal(afterRemoveRows.find((row) => row.group_id === previous.group_id)?.id, previous.id, '单删绑定不得重建保留的关系行')
  }

  const asyncNoop = await captureDmlAsync(() => repositories.patchRouteStrategyAsync(routeStrategy.id, {
    name: '策略路由按需写回归-改名',
    expectedUpdatedAt: changedBindings.result!.result.rowPatch.updatedAt!
  }, access))
  assert(asyncNoop.result, '异步同值 PATCH 应返回现有策略路由')
  assert.deepEqual(asyncNoop.statements, [], '异步同值 PATCH 不得执行任何 DML')

  const concurrentInvariantStrategy = repositories.createRouteStrategy({
    name: '策略路由并发模式绑定回归',
    mode: 'weighted',
    status: 'active',
    groupBindings: [{ groupId: primaryGroup.id, priority: 1, weight: 1, status: 'active' }]
  }, access)
  const concurrentExpectedUpdatedAt = concurrentInvariantStrategy.updatedAt
  const concurrentResults = await Promise.allSettled([
    repositories.patchRouteStrategyAsync(concurrentInvariantStrategy.id, {
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, weight: 1, status: 'active' },
        { groupId: replacementGroup.id, priority: 2, weight: 1, status: 'active' }
      ],
      expectedUpdatedAt: concurrentExpectedUpdatedAt
    }, access),
    repositories.patchRouteStrategyAsync(concurrentInvariantStrategy.id, {
      mode: 'normal',
      expectedUpdatedAt: concurrentExpectedUpdatedAt
    }, access)
  ])
  assert.equal(concurrentResults.filter((result) => result.status === 'rejected').length, 1, '互相冲突的并发模式/绑定 PATCH 必须拒绝其中一个')
  const concurrentInvariantSummary = repositories.findRouteStrategySummary(concurrentInvariantStrategy.id, access)
  assert(concurrentInvariantSummary, '并发 PATCH 后应保留策略路由')
  if (concurrentInvariantSummary.mode === 'normal') {
    assert.equal(concurrentInvariantSummary.groupBindings.length, 1, 'normal 模式在并发 PATCH 后仍只能有一个绑定')
  } else {
    assert.equal(concurrentInvariantSummary.mode, 'weighted', '并发 PATCH 后只允许保留原 weighted 模式')
    assert.equal(concurrentInvariantSummary.groupBindings.length, 2, 'weighted 模式成功写入时应保留两个绑定')
  }

  const mixedStatusStrategy = repositories.createRouteStrategy({
    name: '策略路由绑定顺序 no-op 回归',
    mode: 'weighted',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 2, weight: 50, status: 'active' },
      { groupId: replacementGroup.id, priority: 1, weight: 50, status: 'disabled' }
    ]
  }, access)
  const mixedStatusNoop = captureDml(() => repositories.patchRouteStrategy(mixedStatusStrategy.id, {
    groupBindings: [
      { groupId: replacementGroup.id, priority: 1, weight: 50, status: 'disabled' },
      { groupId: primaryGroup.id, priority: 2, weight: 50, status: 'active' }
    ],
    expectedUpdatedAt: mixedStatusStrategy.updatedAt
  }, access))
  assert.deepEqual(mixedStatusNoop.statements, [], 'active 优先展示顺序不得导致相同绑定被误判为变化')

  const otherOwner = repositories.createSystemAccount({
    username: 'route_strategy_demand_write_other',
    displayName: '策略路由按需写其他用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  assert.equal(
    await routeStrategyRepository.findRouteStrategyEditBasicDetailAsync(routeStrategy.id, { systemAccountId: otherOwner.id, role: 'user' }),
    undefined,
    '其他用户不得读取策略路由 edit-basic'
  )
  const wrongOwnerPatch = await captureDmlAsync(() => repositories.patchRouteStrategyAsync(routeStrategy.id, {
    description: '越权修改不得发生',
    expectedUpdatedAt: changedBindings.result!.result.rowPatch.updatedAt!
  }, { systemAccountId: otherOwner.id, role: 'user' }))
  assert.equal(wrongOwnerPatch.result, undefined, '其他用户 PATCH 应表现为资源不存在')
  assert.deepEqual(wrongOwnerPatch.statements, [], '其他用户 PATCH 不得执行 DML')
  assert.match(wrongOwnerPatch.queries[0] ?? '', /WHERE\s+id\s*=\s*\?\s+AND\s+system_account_id\s*=\s*\?/i, '普通用户 owner 条件必须进入首条定位 SQL')

  const app = express()
  app.use(express.json())
  app.use((_req, _res, next) => authRequestContext.withRequestAuthContext({
    systemAccountId: access.systemAccountId,
    username: 'admin',
    displayName: 'Administrator',
    role: access.role,
    mustChangePassword: false,
    sessionId: 'route-strategy-demand-write-session'
  }, next))
  app.use('/route-strategies', routeStrategyRoutes.routeStrategiesRouter)
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', '策略路由 edit-basic HTTP 回归服务地址不可用')
  const response = await fetch(`http://127.0.0.1:${address.port}/route-strategies/${routeStrategy.id}/edit-basic`)
  assert.equal(response.status, 200)
  const payload = await response.json() as { data?: Record<string, unknown> }
  assert(payload.data, 'edit-basic HTTP 响应应包含 data')
  assert.deepEqual(Object.keys(payload.data).sort(), [
    'description',
    'groupBindings',
    'id',
    'isDefault',
    'mode',
    'name',
    'normalRoutingConfig',
    'status',
    'systemAccountId',
    'updatedAt'
  ].sort(), 'HTTP 层不得给 edit-basic 追加完整详情字段')

  const patchResponse = await fetch(`http://127.0.0.1:${address.port}/route-strategies/${routeStrategy.id}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      description: 'HTTP 最小响应说明',
      expectedUpdatedAt: payload.data.updatedAt
    })
  })
  assert.equal(patchResponse.status, 200)
  const patchPayload = await patchResponse.json() as { data?: Record<string, unknown> }
  assert(patchPayload.data, 'PATCH HTTP 响应应包含 data')
  assert.deepEqual(Object.keys(patchPayload.data).sort(), ['changedFields', 'id', 'rowPatch'], 'PATCH HTTP 响应不得返回完整策略路由摘要')
  assert.deepEqual(patchPayload.data.changedFields, ['description'])
  assert.deepEqual(Object.keys(patchPayload.data.rowPatch as Record<string, unknown>).sort(), ['description', 'updatedAt'])
  const httpUpdatedAt = (patchPayload.data.rowPatch as { updatedAt?: string }).updatedAt
  assert(httpUpdatedAt, 'HTTP PATCH 必须返回新的 CAS 版本')

  const noOpHttp = await captureDmlAsync(() => fetch(`http://127.0.0.1:${address.port}/route-strategies/${routeStrategy.id}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      description: 'HTTP 最小响应说明',
      expectedUpdatedAt: httpUpdatedAt
    })
  }))
  assert.equal(noOpHttp.result.status, 200)
  assert.deepEqual(noOpHttp.statements, [], 'HTTP no-op PATCH 必须保持零 DML')
  const noOpPayload = await noOpHttp.result.json() as { data?: Record<string, unknown> }
  assert.deepEqual(noOpPayload.data, { id: routeStrategy.id, changedFields: [], rowPatch: {} }, 'HTTP no-op PATCH 必须返回最小 no-op 结果')

  const staleHttp = await fetch(`http://127.0.0.1:${address.port}/route-strategies/${routeStrategy.id}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      description: '过期版本不得覆盖',
      expectedUpdatedAt: payload.data.updatedAt
    })
  })
  assert.equal(staleHttp.status, 409, '过期版本 PATCH 必须返回 409')
  const stalePayload = await staleHttp.json() as { currentUpdatedAt?: string }
  assert.equal(stalePayload.currentUpdatedAt, httpUpdatedAt, '冲突响应应返回当前版本供前端提示刷新')

  const missingVersionHttp = await fetch(`http://127.0.0.1:${address.port}/route-strategies/${routeStrategy.id}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ description: '缺少版本不得写入' })
  })
  assert.equal(missingVersionHttp.status, 400, '缺少 expectedUpdatedAt 必须在 HTTP 边界返回 400')

  console.log('策略路由按需读写回归通过：PATCH 返回最小 mutation result，审计不宽读，绑定按变化写入，no-op 为零 DML')
} finally {
  await closeServer(server)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function routeStrategyRow(id: string): { updated_at: string } {
  return databaseModule.getBusinessDatabase()
    .prepare('SELECT updated_at FROM route_strategies WHERE id = ?')
    .get(id) as unknown as { updated_at: string }
}

function bindingRow(routeStrategyId: string): { id: string; group_id: string; priority: number; weight: number; status: string; created_at: string; updated_at: string } {
  return databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT id, group_id, priority, weight, status, created_at, updated_at
      FROM route_strategy_groups
      WHERE route_strategy_id = ?
    `)
    .get(routeStrategyId) as unknown as { id: string; group_id: string; priority: number; weight: number; status: string; created_at: string; updated_at: string }
}

function bindingRows(routeStrategyId: string): Array<{ id: string; group_id: string; priority: number; weight: number; status: string; created_at: string; updated_at: string }> {
  return databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT id, group_id, priority, weight, status, created_at, updated_at
      FROM route_strategy_groups
      WHERE route_strategy_id = ?
      ORDER BY priority ASC, id ASC
    `)
    .all(routeStrategyId) as unknown as Array<{ id: string; group_id: string; priority: number; weight: number; status: string; created_at: string; updated_at: string }>
}

function captureQueries<T>(operation: () => T): { result: T; queries: string[] } {
  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const queries: string[] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const originalGet = statement.get.bind(statement) as typeof statement.get
    const originalAll = statement.all.bind(statement) as typeof statement.all
    statement.get = ((...params: SQLInputValue[]) => {
      queries.push(sql)
      return originalGet(...params)
    }) as typeof statement.get
    statement.all = ((...params: SQLInputValue[]) => {
      queries.push(sql)
      return originalAll(...params)
    }) as typeof statement.all
    return statement
  }) as typeof database.prepare
  try {
    return { result: operation(), queries }
  } finally {
    database.prepare = originalPrepare
  }
}

function captureDml<T>(operation: () => T): { result: T; statements: string[]; queries: string[] } {
  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const statements: string[] = []
  const queries: string[] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const originalGet = statement.get.bind(statement) as typeof statement.get
    const originalAll = statement.all.bind(statement) as typeof statement.all
    const originalRun = statement.run.bind(statement) as typeof statement.run
    statement.get = ((...params: SQLInputValue[]) => {
      queries.push(sql)
      return originalGet(...params)
    }) as typeof statement.get
    statement.all = ((...params: SQLInputValue[]) => {
      queries.push(sql)
      return originalAll(...params)
    }) as typeof statement.all
    statement.run = ((...params: SQLInputValue[]) => {
      if (/^\s*(?:UPDATE|INSERT|DELETE)\b/i.test(sql)) statements.push(sql)
      return originalRun(...params)
    }) as typeof statement.run
    return statement
  }) as typeof database.prepare
  try {
    return { result: operation(), statements, queries }
  } finally {
    database.prepare = originalPrepare
  }
}

async function captureDmlAsync<T>(operation: () => Promise<T>): Promise<{ result: T; statements: string[]; queries: string[] }> {
  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const statements: string[] = []
  const queries: string[] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const originalGet = statement.get.bind(statement) as typeof statement.get
    const originalAll = statement.all.bind(statement) as typeof statement.all
    const originalRun = statement.run.bind(statement) as typeof statement.run
    statement.get = ((...params: SQLInputValue[]) => {
      queries.push(sql)
      return originalGet(...params)
    }) as typeof statement.get
    statement.all = ((...params: SQLInputValue[]) => {
      queries.push(sql)
      return originalAll(...params)
    }) as typeof statement.all
    statement.run = ((...params: SQLInputValue[]) => {
      if (/^\s*(?:UPDATE|INSERT|DELETE)\b/i.test(sql)) statements.push(sql)
      return originalRun(...params)
    }) as typeof statement.run
    return statement
  }) as typeof database.prepare
  try {
    return { result: await operation(), statements, queries }
  } finally {
    database.prepare = originalPrepare
  }
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `缺少源码片段起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码片段终点：${end}`)
  return source.slice(startIndex, endIndex)
}

async function onceListening(value: NonNullable<typeof server>): Promise<void> {
  if (value.listening) return
  await new Promise<void>((resolvePromise, reject) => {
    value.once('listening', resolvePromise)
    value.once('error', reject)
  })
}

async function closeServer(value: typeof server): Promise<void> {
  if (!value) return
  await new Promise<void>((resolvePromise) => value.close(() => resolvePromise()))
}
