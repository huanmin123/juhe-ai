import assert from 'node:assert/strict'
import { existsSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

const tempRoot = resolve(tmpdir(), `juhe-ai-performance-no-sqlite-fallback-${Date.now()}-${Math.random().toString(16).slice(2)}`)

process.env.JUHE_AI_RUNTIME_MODE = 'performance'
process.env.JUHE_AI_DATABASE_DRIVER = 'postgres'
process.env.JUHE_AI_CACHE_DRIVER = 'redis'
process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'redis'
process.env.JUHE_AI_QUEUE_DRIVER = 'redis_stream'
process.env.JUHE_AI_POSTGRES_URL = 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai'
process.env.JUHE_AI_REDIS_CACHE_URL = 'redis://:cache-secret@127.0.0.1:6379/0'
process.env.JUHE_AI_REDIS_STATE_URL = 'redis://:state-secret@127.0.0.1:6380/0'
process.env.JUHE_AI_REDIS_QUEUE_URL = 'redis://:queue-secret@127.0.0.1:6381/0'
process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-state-shards')
process.env.JUHE_AI_PROCESS_ROLE = 'db-service'
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'

try {
  const databaseModule = await import('../../storage/database.js')
  const usageRecordShards = await import('../../storage/usage-record-shards.js')
  const dbServiceHandlers = await import('../../modules/db-service/db-service-handlers.js')
  const openAIOAuthRefresh = await import('../../modules/openai-oauth/openai-oauth-access-token-refresh.service.js')

  assert.throws(() => databaseModule.getBusinessDatabase(), /不能回退写入 SQLite/, 'PG 模式不能打开 SQLite business DB')
  assert.throws(() => databaseModule.getDatasetDatabase(), /不能回退写入 SQLite/, 'PG 模式不能打开 SQLite dataset DB')
  assert.throws(() => databaseModule.getUsageCatalogDatabase(), /不能回退写入 SQLite/, 'PG 模式不能打开 SQLite usage catalog DB')
  assert.throws(() => databaseModule.getStatsDatabase(), /不能回退写入 SQLite/, 'PG 模式不能打开 SQLite stats DB')
  assert.throws(() => databaseModule.getCodexContextStateShardDatabase(0), /不能回退写入 SQLite/, 'PG 模式不能打开 Codex context SQLite shard DB')

  const shardLocation = usageRecordShards.usageRecordShardLocationForRecord('usage_20260101_s000_1_abcd', '2026-01-01T00:00:00.000Z')
  assert.throws(
    () => usageRecordShards.getUsageRecordShardDatabase(shardLocation),
    /PostgreSQL 模式禁止访问 SQLite 使用记录分片/,
    'PG 模式不能直接打开 usage shard DB'
  )
  assert.equal(existsSync(shardLocation.filePath), false, 'PG 模式 usage shard guard 不能创建 SQLite shard 文件')

  await assert.rejects(
    () => dbServiceHandlers.handleDbServiceOperation({
      type: 'save_codex_context_response_state',
      input: {}
    } as never),
    /PostgreSQL 模式暂未接入 Codex context state/,
    'PG 模式 Codex context state DB service 操作必须显式 fail-fast'
  )

  await assert.rejects(
    () => openAIOAuthRefresh.refreshDueOpenAIOAuthAccessTokens({ persistMode: 'sync' }),
    /高性能 PostgreSQL 模式禁止 OpenAI OAuth sync persistMode/,
    'PG 模式不能显式使用 OpenAI OAuth sync persistMode 绕过 DB service'
  )
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('performance-no-sqlite-fallback-regression passed')
