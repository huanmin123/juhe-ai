import assert from 'node:assert/strict'
import { readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const tempRoot = resolve(tmpdir(), `juhe-ai-performance-no-sqlite-fallback-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const backendSrcRoot = fileURLToPath(new URL('../..', import.meta.url))

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
  const openAIOAuthRefresh = await import('../../modules/openai-oauth/openai-oauth-access-token-refresh.service.js')

  assert.throws(() => databaseModule.getBusinessDatabase(), /不能回退写入 SQLite/, 'PG 模式不能打开 SQLite business DB')
  assert.throws(() => databaseModule.getDatasetDatabase(), /不能回退写入 SQLite/, 'PG 模式不能打开 SQLite dataset DB')
  assert.throws(() => databaseModule.getUsageCatalogDatabase(), /不能回退写入 SQLite/, 'PG 模式不能打开 SQLite usage catalog DB')
  assert.throws(() => databaseModule.getStatsDatabase(), /不能回退写入 SQLite/, 'PG 模式不能打开 SQLite stats DB')
  assert.throws(() => databaseModule.getCodexContextStateShardDatabase(0), /不能回退写入 SQLite/, 'PG 模式不能打开 Codex context SQLite shard DB')

  const shardLocation = {
    shardKey: '20260101:s000',
    bucketDate: '2026-01-01',
    bucketDateKey: '20260101',
    shardId: 0,
    filePath: join(tempRoot, 'usage-shards', '20260101', 'usage-20260101-s000.sqlite3')
  }
  assert.throws(
    () => usageRecordShards.getUsageRecordShardDatabase(shardLocation),
    /PostgreSQL 模式禁止访问 SQLite 使用记录分片/,
    'PG 模式不能直接打开 usage shard DB'
  )

  await assert.rejects(
    () => openAIOAuthRefresh.refreshDueOpenAIOAuthAccessTokens({ persistMode: 'sync' }),
    /高性能 PostgreSQL 模式禁止 OpenAI OAuth sync persistMode/,
    'PG 模式不能显式使用 OpenAI OAuth sync persistMode 绕过 DB service'
  )

  const databaseSource = readFileSync(resolve(backendSrcRoot, 'storage/database.ts'), 'utf8')
  for (const functionName of [
    'getBusinessDatabase',
    'getDatasetDatabase',
    'getUsageCatalogDatabase',
    'getStatsDatabase',
    'getCodexContextStateShardDatabase'
  ]) {
    const body = functionBody(databaseSource, functionName)
    assertBefore(body, /assertSqliteDatabaseDriver\(\)/, /mkdirSync|createSqliteDatabase|open\w*Database|assertDistinctStoragePaths/, `${functionName} 必须先检查 database driver，再触碰 SQLite 路径或打开 DB`)
  }

  const usageShardSource = readFileSync(resolve(backendSrcRoot, 'storage/usage-record-shards.ts'), 'utf8')
  assert.match(usageShardSource, /export function getUsageRecordShardDatabase[\s\S]*assertSqliteUsageRecordShardAccess\('getUsageRecordShardDatabase'\)[\s\S]*mkdirSync/, 'usage shard 打开前必须先检查 SQLite shard driver 边界')
  assert.match(usageShardSource, /function usageRecordShardDatabaseIfOpenOrExists[\s\S]*assertSqliteUsageRecordShardAccess\('usageRecordShardDatabaseIfOpenOrExists'\)[\s\S]*existsSync/, 'usage shard existsSync 前必须先检查 SQLite shard driver 边界')
  assert.match(usageShardSource, /export function writeUsageRecordShardRows[\s\S]*assertSqliteUsageRecordShardAccess\('writeUsageRecordShardRows'\)[\s\S]*getUsageRecordShardDatabase/, 'usage shard 写入前必须先检查 SQLite shard driver 边界')

  const preflightSource = readFileSync(resolve(backendSrcRoot, 'scripts/preflight/check-node-sqlite.ts'), 'utf8')
  assertBefore(preflightSource, /if \(runtimeMode === 'performance' \|\| databaseDriver === 'postgres'\) \{[\s\S]*process\.exit\(0\)/, /const checkResult = spawnSync/, 'performance/postgres 预检必须在 node:sqlite 检测前直接跳过')

  const usageRecordsRepositorySource = readFileSync(resolve(backendSrcRoot, 'storage/usage-records.repository.ts'), 'utf8')
  const postgresWriteBody = functionBody(usageRecordsRepositorySource, 'createUsageRecordsBatchPostgres')
  const postgresCatalogBody = functionBody(usageRecordsRepositorySource, 'recordPostgresUsageRecordShardEntries')
  const postgresLogicalLocationBody = functionBody(usageRecordsRepositorySource, 'usageRecordLogicalShardLocationForPostgres')
  assert.match(postgresWriteBody, /shardLocationMode:\s*'postgres'/, 'PG 使用记录写入必须使用逻辑 shard location，不能读取 SQLite usage shard root')
  assert.match(usageRecordsRepositorySource, /function usageRecordLogicalShardLocationForPostgres[\s\S]*postgres:juhe_usage\.usage_records/, 'PG 使用记录 shard catalog 必须写逻辑位置而不是本地 SQLite filePath')
  assert.doesNotMatch(postgresCatalogBody, /usageRecordShardLocationForRecord/, 'PG shard catalog 补齐 location 不能调用 SQLite shard location helper')
  assert.doesNotMatch(postgresWriteBody, /usageRecordShardLocationForRecord|getUsageRecordShardDatabase|writeUsageRecordShardRows|recordUsageRecordShardEntries|getUsageCatalogDatabase|getBusinessDatabase/, 'PG 使用记录写入不能调用 SQLite shard/business/catalog 路径')
  assert.doesNotMatch(postgresCatalogBody, /getUsageRecordShardDatabase|writeUsageRecordShardRows|getUsageCatalogDatabase|getBusinessDatabase/, 'PG shard catalog 补齐不能触碰 SQLite shard/business/catalog 路径')
  assert.doesNotMatch(postgresLogicalLocationBody, /join\(|usageRecordShardRoot|usageCatalogDatabasePath/, 'PG 逻辑 shard location 不能拼接本地 SQLite 路径')

  const dbServiceHandlersSource = readFileSync(resolve(backendSrcRoot, 'modules/db-service/db-service-handlers.ts'), 'utf8')
  assert.match(dbServiceHandlersSource, /case 'save_codex_context_response_state':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*saveCodexContextResponseStateIndexAsync\(operation\.input\)[\s\S]*saveCodexContextResponseStateIndexWithWriterPool\(operation\.input\)/, 'PG 模式 Codex context state DB service 操作必须走 PG async，不能派发到 SQLite writer pool')
  assert.doesNotMatch(dbServiceHandlersSource, /PostgreSQL 模式暂未接入 Codex context state/, 'Codex context state 已接入 PG，不能保留暂未接入文案')
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('performance-no-sqlite-fallback-regression passed')

function functionBody(sourceText: string, functionName: string): string {
  const start = sourceText.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  const openBrace = sourceText.indexOf('{', start)
  assert(openBrace >= 0, `函数 ${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) return sourceText.slice(openBrace, index + 1)
    }
  }
  throw new Error(`函数 ${functionName} 函数体解析失败`)
}

function assertBefore(sourceText: string, first: RegExp, second: RegExp, message: string): void {
  const firstIndex = sourceText.search(first)
  const secondIndex = sourceText.search(second)
  assert(firstIndex >= 0, `${message}：缺少 ${first}`)
  assert(secondIndex >= 0, `${message}：缺少 ${second}`)
  assert(firstIndex < secondIndex, message)
}
