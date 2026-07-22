import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { RedisStreamQueue } from '../../shared/redis-stream-queue.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-delete-cleanup-lifecycle-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.runtimeMode = 'standalone'
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.runtimeStateDriver = 'memory'
runtimeConfig.queueDriver = 'memory'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'account-delete-cleanup-lifecycle-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.redis.queueUrl = 'redis://127.0.0.1:1'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

type DbSummaryFactory = () => unknown

const originalProcessSend = process.send
const originalLoggerError = logger.error
const originalLoggerWarn = logger.warn
const originalRedisEnqueue = RedisStreamQueue.prototype.enqueue
let dbSummaryFactory: DbSummaryFactory | undefined

process.send = ((message: unknown, ...args: unknown[]) => {
  const callback = args.find((item): item is (error: Error | null) => void => typeof item === 'function')
  callback?.(null)
  const record = objectRecord(message)
  if (record?.type === 'background_worker_db_service_request' && typeof record.requestId === 'string') {
    const result = dbSummaryFactory?.()
    setImmediate(() => {
      const emitProcessEvent = process.emit as (...args: unknown[]) => boolean
      emitProcessEvent.call(process, 'message', {
        type: 'background_worker_db_service_response',
        requestId: record.requestId,
        ok: true,
        result
      })
    })
  }
  return true
}) as typeof process.send

const [
  { registerAccountDeleteRoutes },
  { withRequestAuthContext },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  recordMaintenanceQueue,
  { runExpiredDeletedAccountCleanup },
  operationLogQueue
] = await Promise.all([
  import('../../modules/accounts/account-delete.routes.js'),
  import('../../modules/auth/request-context.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/record-maintenance/record-maintenance-queue.service.js'),
  import('../../modules/background/maintenance-cleanup-jobs.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js')
])

const capturedErrors: Array<Record<string, unknown>> = []
const capturedWarnings: Array<Record<string, unknown>> = []
let server: Server | undefined

try {
  logger.error = ((fields: Record<string, unknown>) => {
    capturedErrors.push(fields)
  }) as typeof logger.error
  logger.warn = ((fields: Record<string, unknown>) => {
    capturedWarnings.push(fields)
  }) as typeof logger.warn

  const owner = repositories.createSystemAccount({
    username: `account_delete_lifecycle_${Date.now()}`,
    displayName: '账户删除生命周期回归',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const group = repositories.createGroup({
    name: '账户删除生命周期分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户删除生命周期测试账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-delete-cleanup-lifecycle',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, ownerAccess)
  seedAccountAuditRecord(account.id, owner.id)

  const deleteRouter = express.Router()
  registerAccountDeleteRoutes(deleteRouter)
  const app = express()
  app.use(requestContextMiddleware)
  app.use((_, __, next) => withRequestAuthContext({
    systemAccountId: owner.id,
    role: 'user',
    username: owner.username,
    displayName: owner.displayName,
    mustChangePassword: false,
    sessionId: 'account-delete-lifecycle-session'
  }, next))
  app.use('/accounts', deleteRouter)
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)

  const response = await fetch(`http://127.0.0.1:${serverPort(server)}/accounts/${account.id}`, {
    method: 'DELETE'
  })
  assert.equal(response.status, 204, '正式账户删除路由应成功返回 204')
  assert.equal(await response.text(), '', '204 删除响应不应带响应体')
  assert.ok(accountDeletedAt(account.id), '正式账户删除路由必须写入 deleted_at')
  assert.equal(accountAuditRecordExists(account.id), true, '正式账户删除路由不能即时清理账户审计记录')
  assert.equal(accountCleanupTargetExists(account.id), false, '正式账户删除路由不能即时登记旧 cleanup service 目标')

  ageDeletedAccount(account.id)
  dbSummaryFactory = () => repositories.cleanupExpiredLogicallyDeletedAccounts({ limit: 10 })
  runtimeConfig.queueDriver = 'redis_stream'
  recordMaintenanceQueue.clearRecordMaintenanceQueueForTest()

  const enqueuedJobs: unknown[] = []
  let releaseDelayedEnqueue: (() => void) | undefined
  let delayedEnqueueStarted = false
  let delayedRunSettled = false
  RedisStreamQueue.prototype.enqueue = (async function (payload: unknown): Promise<string> {
    enqueuedJobs.push(payload)
    delayedEnqueueStarted = true
    await new Promise<void>((resolvePromise) => {
      releaseDelayedEnqueue = resolvePromise
    })
    return '1-0'
  }) as typeof RedisStreamQueue.prototype.enqueue

  const delayedRun = runExpiredDeletedAccountCleanup().finally(() => {
    delayedRunSettled = true
  })
  await waitFor(() => delayedEnqueueStarted, '过期账户清理未进入 Redis Stream 异步投递')
  await Promise.resolve()
  assert.equal(delayedRunSettled, false, '过期账户清理必须等待 Redis Stream XADD 完成')
  releaseDelayedEnqueue?.()
  await delayedRun
  assert.equal(delayedRunSettled, true, 'Redis Stream XADD 成功后过期账户清理应完成')
  assertAccountCleanupJob(enqueuedJobs[0], account.id, owner.id)

  const redisFailure = new Error('record-maintenance-xadd-regression-marker')
  RedisStreamQueue.prototype.enqueue = (async function (): Promise<string> {
    throw redisFailure
  }) as typeof RedisStreamQueue.prototype.enqueue
  await runExpiredDeletedAccountCleanup()

  const enqueueFailureEvent = capturedErrors.find((event) => event.event === 'record_maintenance_redis_stream_enqueue_failed')
  assert(enqueueFailureEvent, 'Redis Stream XADD 失败必须写入结构化错误事件')
  assert.equal(enqueueFailureEvent.jobType, 'account_related_cleanup', 'Redis Stream XADD 失败事件必须保留任务类型')
  const capturedRedisError = enqueueFailureEvent.err as { message?: unknown; stack?: unknown } | undefined
  assert.equal(capturedRedisError?.message, redisFailure.message, 'Redis Stream XADD 失败事件必须保留原始错误消息')
  assert.match(String(capturedRedisError?.stack), /record-maintenance-xadd-regression-marker/, 'Redis Stream XADD 失败事件必须保留原始错误堆栈')
  assert(capturedWarnings.some((event) => (
    event.event === 'background_expired_deleted_account_record_cleanup_enqueue_failed'
      && event.accountId === account.id
      && event.droppedReason === 'redis_stream_enqueue_failed'
  )), '过期账户清理必须记录可诊断的异步投递失败结果')

  assert.ok(accountDeletedAt(account.id), 'Redis Stream XADD 失败后逻辑删除账户必须继续持久化')
  assert.equal(accountAuditRecordExists(account.id), true, 'Redis Stream XADD 失败后关联记录必须保留等待重试')
  const retrySummary = repositories.cleanupExpiredLogicallyDeletedAccounts({ limit: 10 })
  assert.equal(retrySummary.recordCleanupTargets.length, 1, 'Redis Stream XADD 失败后下次扫描必须重新得到持久清理目标')
  assert.equal(retrySummary.recordCleanupTargets[0]?.accountId, account.id, '重试清理目标必须仍指向原逻辑删除账户')

  let retryJob: unknown
  RedisStreamQueue.prototype.enqueue = (async function (payload: unknown): Promise<string> {
    retryJob = payload
    return '2-0'
  }) as typeof RedisStreamQueue.prototype.enqueue
  await runExpiredDeletedAccountCleanup()
  assertAccountCleanupJob(retryJob, account.id, owner.id)

  console.log('账户删除清理生命周期回归通过：正式路由仅逻辑删除，过期任务等待 Redis Stream，XADD 失败保留诊断与可重试目标')
} finally {
  await closeServer(server)
  runtimeConfig.queueDriver = 'memory'
  RedisStreamQueue.prototype.enqueue = originalRedisEnqueue
  logger.error = originalLoggerError
  logger.warn = originalLoggerWarn
  process.send = originalProcessSend
  operationLogQueue.clearOperationLogQueueForTest()
  recordMaintenanceQueue.clearRecordMaintenanceQueueForTest()
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedAccountAuditRecord(accountId: string, systemAccountId: string): void {
  const createdAt = new Date().toISOString()
  databaseModule.getDatasetDatabase()
    .prepare(`
      INSERT INTO audit_logs (
        id, trace_id, traffic_source, system_account_id, account_id, method, path, audit_outcome,
        success, sample_bucket, sample_reason, started_at, ended_at, created_at
      ) VALUES (?, ?, 'gateway', ?, ?, 'POST', '/v1/chat/completions', 'success', 1, 0, 'regression', ?, ?, ?)
    `)
    .run(
      `audit_account_delete_lifecycle_${accountId}`,
      `trace_account_delete_lifecycle_${accountId}`,
      systemAccountId,
      accountId,
      createdAt,
      createdAt,
      createdAt
    )
}

function accountDeletedAt(accountId: string): string | undefined {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT deleted_at FROM accounts WHERE id = ?')
    .get(accountId) as { deleted_at?: string | null } | undefined
  return row?.deleted_at ?? undefined
}

function accountAuditRecordExists(accountId: string): boolean {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT id FROM audit_logs WHERE account_id = ? LIMIT 1')
    .get(accountId) as { id?: string } | undefined
  return Boolean(row?.id)
}

function accountCleanupTargetExists(accountId: string): boolean {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT account_id FROM account_record_cleanup_targets WHERE account_id = ? LIMIT 1')
    .get(accountId) as { account_id?: string } | undefined
  return Boolean(row?.account_id)
}

function ageDeletedAccount(accountId: string): void {
  const deletedAt = new Date(Date.now() - 40 * 24 * 60 * 60 * 1000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL')
    .run(deletedAt, deletedAt, accountId)
}

function assertAccountCleanupJob(value: unknown, accountId: string, systemAccountId: string): void {
  const job = objectRecord(value)
  assert.equal(job?.type, 'account_related_cleanup', 'Redis Stream 必须接收账户关联清理任务')
  assert.equal(job?.accountId, accountId, 'Redis Stream 账户清理任务必须保留账户 ID')
  assert.equal(job?.systemAccountId, systemAccountId, 'Redis Stream 账户清理任务必须保留账户所有者')
}

function objectRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

async function waitFor(predicate: () => boolean, message: string): Promise<void> {
  const deadline = Date.now() + 2_000
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error(message)
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 5))
  }
}

async function onceListening(listeningServer: Server): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: Server): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

function serverPort(listeningServer: Server): number {
  const address = listeningServer.address()
  assert(address && typeof address !== 'string', '账户删除生命周期回归服务地址不可用')
  return address.port
}
