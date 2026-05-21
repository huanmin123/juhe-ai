import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-table-monitor-cleanup-targets-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'table-monitor-cleanup-targets-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { tableMonitorRouter },
  { requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/table-monitor/table-monitor.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/table-monitor', requireAdmin, tableMonitorRouter)

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface ApiKeyRecordCleanupQueueTarget {
  apiKeyId: string
  systemAccountId: string
  createdAt: string
  updatedAt: string
  attemptCount: number
  lastAttemptAt?: string
  lastBlockedReason?: string
  lastErrorMessage?: string
}

let server: ReturnType<typeof app.listen> | undefined

try {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  seedCleanupTargets(admin.id)
  const adminCookie = sessionCookie(admin.id)

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('表监控清理队列回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`

  const recordDatabase = databaseModule.getRecordDatabase()
  const originalPrepare = recordDatabase.prepare.bind(recordDatabase) as typeof recordDatabase.prepare
  const capturedSelects: Array<{ sql: string; params: unknown[] }> = []
  recordDatabase.prepare = ((sql: string) => {
    if (/^\s*SELECT\b/i.test(sql)) {
      assert(!/\bFROM\s+usage_records\b/i.test(sql), 'API Key 删除清理队列查看不应扫描 usage_records')
      assert(!/\bFROM\s+audit_logs\b/i.test(sql), 'API Key 删除清理队列查看不应扫描 audit_logs')
      assert(!/\bFROM\s+table_storage_snapshots\b/i.test(sql), 'API Key 删除清理队列查看不应读取表监控历史')
      assert(!/\bFROM\s+database_storage_snapshots\b/i.test(sql), 'API Key 删除清理队列查看不应读取库监控历史')
    }
    const statement = originalPrepare(sql)
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+api_key_record_cleanup_targets\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedSelects.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof recordDatabase.prepare

  try {
    const targets = await getEnvelope<ApiKeyRecordCleanupQueueTarget[]>(
      baseUrl,
      '/__aisys__/api/table-monitor/record-maintenance/api-key-cleanup-targets?limit=2',
      adminCookie
    )
    assert.equal(targets.length, 2, 'API Key 删除清理队列接口应遵守 limit')
    assert.deepEqual(targets.map((target) => target.apiKeyId), ['key_failed', 'key_blocked'], '失败和阻塞目标应优先展示')
    assert.equal(targets[0]?.lastErrorMessage, 'forced failure')
    assert.equal(targets[1]?.lastBlockedReason, 'waiting cursor')
  } finally {
    recordDatabase.prepare = originalPrepare
  }

  assert.equal(capturedSelects.length, 1, 'API Key 删除清理队列接口应只执行一次目标队列查询')
  const [queueSelect] = capturedSelects
  assert(queueSelect?.sql.includes('ORDER BY'), 'API Key 删除清理队列查询应有稳定排序')
  assert(/\bLIMIT\s+\?/i.test(queueSelect?.sql ?? ''), 'API Key 删除清理队列查询必须带 LIMIT')
  assert.equal(queueSelect?.params.at(-1), 2, 'API Key 删除清理队列查询应把 HTTP limit 下推到 SQL')

  console.log('表监控 API Key 删除清理队列回归通过：查看队列只读目标表并遵守 limit，不扫描使用记录和审计明细')
} finally {
  await closeServer(server)
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedCleanupTargets(systemAccountId: string): void {
  const database = databaseModule.getRecordDatabase()
  const now = '2026-01-01T00:00:00.000Z'
  database.prepare(`
    INSERT INTO api_key_record_cleanup_targets (
      api_key_id, system_account_id, created_at, updated_at, attempt_count, last_attempt_at, last_blocked_reason, last_error_message
    ) VALUES
      ('key_waiting', ?, ?, ?, 0, NULL, NULL, NULL),
      ('key_blocked', ?, ?, ?, 1, ?, 'waiting cursor', NULL),
      ('key_failed', ?, ?, ?, 2, ?, NULL, 'forced failure')
  `).run(systemAccountId, now, now, systemAccountId, now, now, now, systemAccountId, now, now, now)
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => {
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
  })
}
