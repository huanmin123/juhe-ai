import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { closeRedisClients, createDedicatedRedisClient } from '../../shared/redis-client.js'
import { redisNamespacedKey } from '../../shared/redis-namespace.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '真实运行日志 smoke 必须使用 PostgreSQL')
assert.ok(runtimeConfig.postgres.url, '真实运行日志 smoke 缺少 PostgreSQL URL')
assert.ok(runtimeConfig.redis.queueUrl, '真实运行日志 smoke 缺少 Redis queue URL')

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-postgres-smoke-'))
const logDir = join(tempRoot, 'logs')
mkdirSync(logDir)
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.log.directory = logDir
runtimeConfig.log.fileEnabled = true
runtimeConfig.log.consoleEnabled = false
const marker = `pg_runtime_log_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const logPath = join(logDir, 'juhe-ai.log.20260721T121500Z.pg-smoke.log')
const lines = [0, 1].map((index) => JSON.stringify({ time: new Date().toISOString(), level: 30, event: `${marker}_${index}`, msg: `postgres smoke ${index}` })).join('\n') + '\n'

const pool = await getPostgresPool()
const redis = await createDedicatedRedisClient(runtimeConfig.redis.queueUrl, { disableOfflineQueue: true, commandsQueueMaxLength: 8, connectTimeoutMs: 3000 })
const legacyStreamKey = redisNamespacedKey('juhe-ai:queue:runtime-log-index')
const beforeStreamLength = Number(await redis.sendCommand(['XLEN', legacyStreamKey]))
try {
  await pool.query('ALTER TABLE juhe_dataset.runtime_log_file_cursors ADD COLUMN IF NOT EXISTS truncation_generation integer NOT NULL DEFAULT 0')
  const [databaseModule, importer] = await Promise.all([
    import('../../storage/database.js'),
    import('../../modules/runtime-logs/runtime-log-file-import.service.js')
  ])
  writeFileSync(logPath, lines)
  await importer.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'server', kind: 'rotated' })
  const rows = await pool.query('SELECT event, raw_json FROM juhe_dataset.runtime_logs WHERE event LIKE $1 ORDER BY event', [`${marker}%`])
  assert.equal(rows.rowCount, 2, '真实 PostgreSQL 应写入全部 runtime log 行')
  assert.equal(rows.rows.every((row) => typeof row.raw_json === 'string' && row.raw_json.length > 0), true, '真实 PostgreSQL raw_json 不得为空')
  const cursor = await pool.query('SELECT cursor_offset, truncation_generation FROM juhe_dataset.runtime_log_file_cursors WHERE log_file = $1', [logPath])
  assert.equal(cursor.rowCount, 1, '真实 PostgreSQL 应持久化文件 cursor')
  assert.equal(Number(cursor.rows[0]?.cursor_offset), statSync(logPath).size, '真实 PostgreSQL cursor 必须追平文件尾部')
  assert.equal(Number(cursor.rows[0]?.truncation_generation), 0, '首代文件 cursor generation 必须为 0')
  const afterStreamLength = Number(await redis.sendCommand(['XLEN', legacyStreamKey]))
  assert.equal(afterStreamLength, beforeStreamLength, '文件消费不得新增已删除的 runtime-log Redis Stream 消息')
  console.log(JSON.stringify({ message: 'runtime log PostgreSQL/Redis smoke passed', rows: rows.rowCount, streamLength: afterStreamLength }))
  databaseModule.closeStorageDatabases()
} finally {
  await pool.query('DELETE FROM juhe_dataset.runtime_logs WHERE event LIKE $1', [`${marker}%`]).catch(() => undefined)
  await pool.query('DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE log_file = $1', [logPath]).catch(() => undefined)
  await redis.quit?.().catch(() => undefined)
  await closeRedisClients()
  await closePostgresPool()
  rmSync(tempRoot, { recursive: true, force: true })
}
