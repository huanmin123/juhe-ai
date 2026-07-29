import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const dbServiceIpcPath = fileURLToPath(new URL('../../modules/db-service/db-service-ipc.ts', import.meta.url))
const dbServiceTypesPath = fileURLToPath(new URL('../../modules/db-service/db-service-types.ts', import.meta.url))
const statsRoutesPath = fileURLToPath(new URL('../../modules/stats/stats.routes.ts', import.meta.url))
const dbServiceIpcSource = readFileSync(dbServiceIpcPath, 'utf8')
const dbServiceTypesSource = readFileSync(dbServiceTypesPath, 'utf8')
const statsRoutesSource = readFileSync(statsRoutesPath, 'utf8')

assert.match(
  dbServiceTypesSource,
  /DbServiceServerRuntimeSnapshotScope[\s\S]*'system_metrics'/,
  'DB service runtime IPC 契约必须注册 system_metrics scope'
)
assert.match(
  statsRoutesSource,
  /requestServerSystemMetricsRuntimeSnapshot\(2500\)/,
  'system-metrics runtime 路由必须请求专用窄快照'
)
assert.doesNotMatch(
  statsRoutesSource,
  /requestServerRuntimeSnapshot\(2500\)/,
  'system-metrics runtime 路由不得继续请求 full runtime 快照'
)

const narrowBuilderSource = functionSource(
  dbServiceIpcSource,
  'async function buildServerSystemMetricsRuntimeSnapshot',
  'async function respondToServerAccountRuntimeClearRequest'
)
const fullBuilderSource = functionSource(
  dbServiceIpcSource,
  'async function buildServerRuntimeSnapshot',
  'async function buildServerSystemMetricsRuntimeSnapshot'
)
for (const required of [
  'snapshotAccountConcurrency()',
  'getActiveAuditCaptureCount()',
  'getAuditLogTransportRuntime()',
  'getGatewayUsageFinalizationRuntime()',
  'getGatewayAccountSideEffectState()',
  'pendingMessageCount:',
  'pendingSnapshotRequestCount:',
  'processEventLoopTimeoutStreak:',
  'httpHost:',
  'sqliteReadWorkerPool:'
]) {
  assert.ok(fullBuilderSource.includes(required), `full runtime builder 必须继续保留原有事实：${required}`)
}
for (const required of [
  "import('../background/background-ipc.js')",
  "import('../gateway/runtime/account-side-effects.service.js')",
  "import('../gateway/runtime/high-concurrency-queue.service.js')",
  "import('../accounts/account-balance-snapshot-cleanup.service.js')",
  'observedAt:',
  'accountBalanceSnapshotCleanup:',
  'ingestWorker:',
  'statsWorker:',
  'opsWorker:',
  'dbService:',
  'pendingQueues:',
  'pendingWriteRequestCount:',
  'codexContextStateWriterPool:',
  'highConcurrencyQueues:',
  'gatewayAccountSideEffects:'
]) {
  assert.ok(narrowBuilderSource.includes(required), `system_metrics builder 缺少页面所需事实：${required}`)
}
for (const forbidden of [
  "import('../../shared/account-concurrency.js')",
  "import('../gateway/audit/capture.service.js')",
  "import('../audit-logs/audit-log-transport.service.js')",
  "import('../audit-logs/audit-log-queue.service.js')",
  "import('../gateway/usage/failure-finalization.service.js')",
  'snapshotAccountConcurrency()',
  'getActiveAuditCaptureCount()',
  'getAuditLogTransportRuntime()',
  'getGatewayUsageFinalizationRuntime()',
  'pendingMessageCount:',
  'pendingSnapshotRequestCount:',
  'processEventLoopTimeoutStreak:',
  'lastQueueWaitMs:',
  'httpHost:',
  'sqliteReadWorkerPool:',
  'enqueuedCount:',
  'staleCount:',
  'evictedFailureForSuccessCount:',
  'recoveryProbePendingAccountCount:'
]) {
  assert.ok(!narrowBuilderSource.includes(forbidden), `system_metrics builder 不得构造无关 full runtime 事实：${forbidden}`)
}

runtimeConfig.processRole = 'db-service'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

const dbServiceIpc = await import('../../modules/db-service/db-service-ipc.js')
const originalSend = process.send
let sentMessage: Record<string, unknown> | undefined
const expectedSnapshot = {
  observedAt: '2026-07-29T00:00:00.000Z',
  ingestWorker: { ready: true },
  statsWorker: { ready: true },
  opsWorker: { ready: true },
  dbService: {
    ready: true,
    pendingRequestCount: 0,
    timedOutRequestCount: 0,
    failedRequestCount: 0
  },
  highConcurrencyQueues: [],
  gatewayAccountSideEffects: { queueLength: 0 }
}

try {
  process.send = ((message: unknown, ...args: unknown[]) => {
    sentMessage = message as Record<string, unknown>
    const callback = args.find((value) => typeof value === 'function') as ((error?: Error | null) => void) | undefined
    callback?.(null)
    queueMicrotask(() => {
      dbServiceIpc.handleDbServiceParentRuntimeMessage({
        type: 'db_service_server_runtime_response',
        requestId: sentMessage?.requestId,
        ok: true,
        result: expectedSnapshot
      })
    })
    return true
  }) as typeof process.send

  const snapshot = await dbServiceIpc.requestServerSystemMetricsRuntimeSnapshot(100)
  assert.equal(sentMessage?.type, 'db_service_server_runtime_request')
  assert.equal(sentMessage?.scope, 'system_metrics', '专用 helper 必须通过 IPC 发送 system_metrics scope')
  assert.deepEqual(snapshot, expectedSnapshot, '专用 helper 应按 requestId 返回 system_metrics 快照')
} finally {
  if (originalSend) process.send = originalSend
  else delete process.send
}

console.log('system metrics runtime scope 回归通过：统计路由使用专用窄 IPC，且不构造账户并发、审计与终态队列')

function functionSource(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = source.indexOf(endMarker, start + startMarker.length)
  assert.notEqual(start, -1, `未找到函数起点：${startMarker}`)
  assert.notEqual(end, -1, `未找到函数终点：${endMarker}`)
  return source.slice(start, end)
}
