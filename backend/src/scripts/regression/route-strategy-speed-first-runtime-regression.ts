import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-route-strategy-speed-first-runtime-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'route-strategy-speed-first-runtime-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
runtimeConfig.runtimeStateDriver = 'memory'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  { routeStrategiesRouter },
  { withRequestAuthContext },
  { handleDbServiceParentRuntimeMessage },
  latencyRuntime,
  runtimeFacade
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/route-strategies/route-strategies.routes.js'),
  import('../../modules/auth/request-context.js'),
  import('../../modules/db-service/db-service-ipc.js'),
  import('../../modules/gateway/runtime/normal-route-latency-degradation.service.js'),
  import('../../modules/route-strategies/route-strategy-speed-first-runtime.facade.js')
])

const originalProcessRole = runtimeConfig.processRole
const originalRuntimeStateDriver = runtimeConfig.runtimeStateDriver
const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
const speedConfig = {
  firstByteDeadlineMs: 30_000,
  slowTriggerCount: 2,
  slowWindowSeconds: 60,
  recoverySuccessCount: 3,
  probeIntervalSeconds: 10,
  degradedTtlSeconds: 60,
  maxFirstByteRetriesPerRequest: 1
}

let server: http.Server | undefined
const routeStrategyIdsToClear: string[] = []

try {
  const ownerA = repositories.createSystemAccount({
    username: `speed_runtime_owner_a_${Date.now()}`,
    displayName: '速度运行态所有者A',
    password: 'password',
    status: 'active',
    mustChangePassword: false
  })
  const ownerB = repositories.createSystemAccount({
    username: `speed_runtime_owner_b_${Date.now()}`,
    displayName: '速度运行态所有者B',
    password: 'password',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAAccess = { systemAccountId: ownerA.id, role: 'user' as const }
  const ownerBAccess = { systemAccountId: ownerB.id, role: 'user' as const }
  const groupA = repositories.createGroup({ name: '速度运行态分组 A', providerCode: 'gpt', enabled: true }, ownerAAccess)
  const groupB = repositories.createGroup({ name: '速度运行态分组 B', providerCode: 'gpt', enabled: true }, ownerBAccess)
  const speedStrategyA = repositories.createRouteStrategy({
    name: '速度运行态策略 A',
    mode: 'normal',
    status: 'active',
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
      firstByteDeadlineMs: speedConfig.firstByteDeadlineMs,
      speedFirstConfig: speedConfig
    },
    groupBindings: [{ groupId: groupA.id, priority: 1, status: 'active' }]
  }, ownerAAccess)
  const speedStrategyB = repositories.createRouteStrategy({
    name: '速度运行态策略 B',
    mode: 'normal',
    status: 'active',
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
      firstByteDeadlineMs: speedConfig.firstByteDeadlineMs,
      speedFirstConfig: speedConfig
    },
    groupBindings: [{ groupId: groupB.id, priority: 1, status: 'active' }]
  }, ownerBAccess)
  const costStrategy = repositories.createRouteStrategy({
    name: '成本优先运行态策略',
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: groupA.id, priority: 1, status: 'active' }]
  }, ownerAAccess)
  routeStrategyIdsToClear.push(speedStrategyA.id, speedStrategyB.id, costStrategy.id)

  const scopeA = latencyRuntime.normalRouteLatencyDegradationScope({
    systemAccountId: ownerA.id,
    routeStrategyId: speedStrategyA.id,
    groupId: groupA.id
  })
  const scopeB = latencyRuntime.normalRouteLatencyDegradationScope({
    systemAccountId: ownerB.id,
    routeStrategyId: speedStrategyB.id,
    groupId: groupB.id
  })
  assert(scopeA && scopeB, '速度优先运行态夹具必须生成有效 scope')
  const accountA = { id: 'account_speed_runtime_a', name: '速度运行态账户 A' }
  const accountB = { id: 'account_speed_runtime_b', name: '速度运行态账户 B' }
  await latencyRuntime.recordNormalRouteFirstByteSlowAsync(accountA, scopeA, speedConfig)
  await latencyRuntime.recordNormalRouteFirstByteSlowAsync(accountA, scopeA, speedConfig)
  await latencyRuntime.recordNormalRouteFirstByteSlowAsync(accountB, scopeB, speedConfig)
  await latencyRuntime.recordNormalRouteFirstByteSlowAsync(accountB, scopeB, speedConfig)

  const ownerFiltered = await latencyRuntime.listNormalRouteLatencyDegradedRuntimeAsync({
    systemAccountId: ownerA.id,
    routeStrategyIds: [speedStrategyA.id, speedStrategyB.id]
  })
  assert.deepEqual(ownerFiltered.map((item) => item.scope.routeStrategyId), [speedStrategyA.id], 'owner 二次过滤不得返回其他系统账户运行态')
  const allOwnerItems = await latencyRuntime.listNormalRouteLatencyDegradedRuntimeAsync({
    routeStrategyIds: [speedStrategyA.id, speedStrategyB.id]
  })
  assert.deepEqual(new Set(allOwnerItems.map((item) => item.scope.routeStrategyId)), new Set([speedStrategyA.id, speedStrategyB.id]), '已授权多 owner 策略 ID 应在单次批量查询中各自返回运行态')
  assert.equal(Object.hasOwn(allOwnerItems[0] ?? {}, 'runtimeKey'), false, '管理 DTO 不得暴露 runtimeKey')
  assert.equal(Object.hasOwn(allOwnerItems[0] ?? {}, 'stateKey'), false, '管理 DTO 不得暴露 stateKey')
  assert.equal(Object.hasOwn(allOwnerItems[0] ?? {}, 'generation'), false, '管理 DTO 不得暴露 generation')
  assert.equal(allOwnerItems[0]?.slowTriggerCount, speedConfig.slowTriggerCount, '管理 DTO 应返回慢样本触发阈值')
  assert.equal(allOwnerItems[0]?.requiredRecoverySuccessCount, speedConfig.recoverySuccessCount, '管理 DTO 应返回真实请求恢复阈值')
  assert.equal(
    (await latencyRuntime.listNormalRouteLatencyDegradedRuntimeAsync({
      routeStrategyIds: [speedStrategyA.id],
      now: Date.now() + (speedConfig.degradedTtlSeconds + 1) * 1000
    })).length,
    0,
    '查询必须过滤按传入时点已过期的降级态'
  )

  const app = express()
  app.use((_req, _res, next) => {
    withRequestAuthContext({
      systemAccountId: adminAccess.systemAccountId,
      role: adminAccess.role,
      username: 'admin',
      displayName: '管理员',
      mustChangePassword: false,
      sessionId: 'route-strategy-speed-first-runtime-regression'
    }, next)
  })
  app.use('/route-strategies', routeStrategiesRouter)
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const baseUrl = `http://127.0.0.1:${serverPort(server)}`

  const list = await getJson<{
    data: { items: Array<Record<string, unknown> & { id: string; speedFirstLatencyRuntime?: { runtimeAvailable: boolean; degradedCount: number } }> }
  }>(baseUrl, '/route-strategies?page=1&pageSize=50')
  const listedA = list.data.items.find((item) => item.id === speedStrategyA.id)
  const listedB = list.data.items.find((item) => item.id === speedStrategyB.id)
  const listedCost = list.data.items.find((item) => item.id === costStrategy.id)
  assert.equal(listedA?.speedFirstLatencyRuntime?.runtimeAvailable, true, '管理员未筛选 owner 的列表应读取运行态')
  assert.equal(listedA?.speedFirstLatencyRuntime?.degradedCount, 1, '列表摘要应返回策略 A 的降级数量')
  assert.equal(listedB?.speedFirstLatencyRuntime?.degradedCount, 1, '列表摘要应返回策略 B 的降级数量')
  assert.equal(Object.hasOwn(listedCost ?? {}, 'speedFirstLatencyRuntime'), false, '非 speed_first 路由不得附带速度运行态摘要')

  const detail = await getJson<{
    data: {
      routeStrategyId: string
      enabled: boolean
      runtimeAvailable: boolean
      degradedCount: number
      items: Array<{ accountId: string; scope: { routeStrategyId: string } }>
    }
  }>(baseUrl, `/route-strategies/${speedStrategyA.id}/speed-first-runtime?systemAccountId=${ownerA.id}`)
  assert.equal(detail.data.routeStrategyId, speedStrategyA.id, '详情必须回显目标策略 ID')
  assert.equal(detail.data.enabled, true, 'speed_first 策略详情必须启用运行态')
  assert.equal(detail.data.runtimeAvailable, true, '正常读取时详情运行态应可用')
  assert.equal(detail.data.degradedCount, 1, '详情必须返回策略自身降级数量')
  assert.deepEqual(detail.data.items.map((item) => item.scope.routeStrategyId), [speedStrategyA.id], '详情不得混入其他策略的运行态')

  const costDetail = await getJson<{
    data: { routeStrategyId: string; generatedAt: string; enabled: boolean; runtimeAvailable: boolean; degradedCount: number; items: unknown[] }
  }>(baseUrl, `/route-strategies/${costStrategy.id}/speed-first-runtime?systemAccountId=${ownerA.id}`)
  assert.deepEqual(costDetail.data, {
    routeStrategyId: costStrategy.id,
    generatedAt: costDetail.data.generatedAt,
    enabled: false,
    runtimeAvailable: true,
    degradedCount: 0,
    items: []
  }, '非 speed_first 策略必须以 enabled=false 与正常零计数区分')
  const deniedDetail = await fetch(`${baseUrl}/route-strategies/${speedStrategyA.id}/speed-first-runtime?systemAccountId=${ownerB.id}`)
  assert.equal(deniedDetail.status, 404, '策略详情必须先按当前 access scope 验证可见性')

  runtimeConfig.runtimeStateDriver = 'redis'
  runtimeConfig.processRole = 'db-service'
  const redisBranch = await runtimeFacade.loadRouteStrategySpeedFirstLatencyRuntimeAsync({
    systemAccountId: ownerA.id,
    routeStrategyId: speedStrategyA.id
  })
  assert.equal(redisBranch.runtimeAvailable, true, 'Redis runtime state 分支应由 DB service 直接读取共享 store')
  assert.equal(redisBranch.degradedCount, 1, 'Redis runtime state 分支应返回当前降级态')

  runtimeConfig.runtimeStateDriver = 'memory'
  const processWithSend = process as typeof process & {
    send?: (...args: unknown[]) => boolean
  }
  const originalSend = processWithSend.send
  try {
    processWithSend.send = (...args: unknown[]) => {
      const message = args[0] as { type?: unknown; requestId?: unknown; input?: unknown }
      const callback = typeof args[1] === 'function'
        ? args[1] as (error: Error | null) => void
        : undefined
      if (message.type === 'db_service_server_runtime_request' && typeof message.requestId === 'string') {
        void latencyRuntime.listNormalRouteLatencyDegradedRuntimeAsync(
          message.input as { systemAccountId?: string; routeStrategyIds: string[] }
        ).then((items) => {
          handleDbServiceParentRuntimeMessage({
            type: 'db_service_server_runtime_response',
            requestId: message.requestId,
            ok: true,
            result: { normalRouteSpeedFirstLatencyRuntime: { items } }
          })
        })
      }
      callback?.(null)
      return true
    }
    const standaloneIpc = await runtimeFacade.loadRouteStrategySpeedFirstLatencyRuntimeAsync({
      systemAccountId: ownerA.id,
      routeStrategyId: speedStrategyA.id
    })
    assert.equal(standaloneIpc.runtimeAvailable, true, 'standalone DB service 必须通过窄 IPC 读取 server memory 运行态')
    assert.equal(standaloneIpc.degradedCount, 1, 'standalone IPC 响应必须保留当前策略的降级数量')

    processWithSend.send = undefined
    const unavailableBranch = await runtimeFacade.loadRouteStrategySpeedFirstLatencyRuntimeAsync({
      systemAccountId: ownerA.id,
      routeStrategyId: speedStrategyA.id
    })
    assert.deepEqual(unavailableBranch, {
      runtimeAvailable: false,
      degradedCount: 0,
      items: []
    }, 'standalone DB service IPC 不可用时必须 fail-open，不得令列表或详情失败')
  } finally {
    processWithSend.send = originalSend
  }

  console.log('策略路由速度优先降级运行态回归通过：批量范围隔离、列表摘要、详情、非速度模式、IPC fail-open 与 Redis 直读分支均符合预期')
} finally {
  runtimeConfig.processRole = originalProcessRole
  runtimeConfig.runtimeStateDriver = originalRuntimeStateDriver
  for (const routeStrategyId of routeStrategyIdsToClear) {
    await latencyRuntime.clearNormalRouteLatencyDegradationForRouteStrategyAsync(routeStrategyId).catch(() => undefined)
  }
  await closeServer(server)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function getJson<T>(baseUrl: string, path: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`)
  const text = await response.text()
  assert.equal(response.status, 200, `GET ${path} 应成功，实际 ${response.status}: ${text}`)
  return JSON.parse(text) as T
}

async function onceListening(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('回归 HTTP server 未返回端口')
  return address.port
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}
