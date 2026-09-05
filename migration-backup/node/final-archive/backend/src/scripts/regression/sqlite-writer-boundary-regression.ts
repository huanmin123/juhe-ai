import { strict as assert } from 'node:assert'
import { existsSync, linkSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = 'true'

const tempRoot = resolve(tmpdir(), `juhe-ai-sqlite-writer-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.chatDatabasePath = join(tempRoot, 'chat.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.runtimeLogDatabasePath = join(tempRoot, 'runtime-log.sqlite3')
runtimeConfig.tableMonitorDatabasePath = join(tempRoot, 'table-monitor.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context', 'state-shards')
runtimeConfig.codexContextStateShardCount = 4
runtimeConfig.secret = 'sqlite-writer-boundary-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const databaseModule = await import('../../storage/database.js')
const usageRecordShards = await import('../../storage/usage-record-shards.js')
const repositories = await import('../../storage/repositories.js')

try {
  assertDistinctPhysicalSqliteStoragePaths(databaseModule)
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('business'), 'db-service')
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('chat'), 'db-service')
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('codex-context-state'), 'db-service')
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('dataset'), 'ingest-worker')
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('runtime-log'), 'go-runtime-log')
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('usage-catalog'), 'ingest-worker')
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('stats'), 'stats-writer')
  assert.equal(databaseModule.sqliteWriterBoundaryStrictModeEnabled(), true)
  assert.equal(databaseModule.mainDatabaseRuntimeInfo('runtime-log').queryOnly, true)

  for (const [processRole, workerRole] of [
    ['server', 'worker'],
    ['db-service', 'worker'],
    ['worker', 'ingest-worker'],
    ['worker', 'stats-worker'],
    ['worker', 'ops-worker'],
    ['worker', 'temporary-maintenance-worker']
  ] as const) {
    runtimeConfig.processRole = processRole
    runtimeConfig.workerRole = workerRole
    assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('runtime-log'), false, `${processRole}/${workerRole} 不能成为 Go F1 SQLite writer`)
  }

  runtimeConfig.processRole = 'db-service'
  runtimeConfig.workerRole = 'worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('chat'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('codex-context-state'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('usage-catalog'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), false)
  assert.equal(usageRecordShards.currentProcessOwnsUsageShardWriter(), false)
  databaseModule.getBusinessDatabase()
  const chatDatabase = databaseModule.getChatDatabase()
  assert.equal(
    (chatDatabase.prepare("SELECT COUNT(*) AS total FROM sqlite_master WHERE type = 'table' AND name = 'chat_messages'").get() as { total?: number }).total,
    1,
    'DB service 应按需创建独立聊天库 schema'
  )
  for (const shardIndex of databaseModule.codexContextStateShardIndexes()) {
    databaseModule.getCodexContextStateShardDatabase(shardIndex)
  }
  databaseModule.closeStorageDatabases()

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'ingest-worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('chat'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('codex-context-state'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('usage-catalog'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), false)
  assert.equal(usageRecordShards.currentProcessOwnsUsageShardWriter(), true)
  databaseModule.getDatasetDatabase()
  databaseModule.getUsageCatalogDatabase()
  const shardLocation = usageRecordShards.usageRecordShardLocationForRecord('usage_20260618_s00_boundary', '2026-06-18T00:00:00.000Z')
  usageRecordShards.getUsageRecordShardDatabase(shardLocation)
  assertIngestWorkerUsageRecordWriteDoesNotTouchReadonlyBusinessDatabase()
  databaseModule.closeStorageDatabases()

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'stats-worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('codex-context-state'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('usage-catalog'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), true)
  assert.equal(usageRecordShards.currentProcessOwnsUsageShardWriter(), false)
  databaseModule.getStatsDatabase()
  databaseModule.closeStorageDatabases()

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'temporary-maintenance-worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('codex-context-state'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('usage-catalog'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), true)
  assert.equal(usageRecordShards.currentProcessOwnsUsageShardWriter(), true)
  databaseModule.getDatasetDatabase()
  databaseModule.getUsageCatalogDatabase()
  databaseModule.getStatsDatabase()
  usageRecordShards.getUsageRecordShardDatabase(shardLocation)
  databaseModule.closeStorageDatabases()

  process.env.JUHE_AI_SQLITE_OFFLINE_MAINTENANCE = '1'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('codex-context-state'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('usage-catalog'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('runtime-log'), false, 'Node offline maintenance 也不能接管 Go F1 SQLite writer')
  databaseModule.getBusinessDatabase()
  databaseModule.getStatsDatabase()
  databaseModule.closeStorageDatabases()
  delete process.env.JUHE_AI_SQLITE_OFFLINE_MAINTENANCE

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'ops-worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('codex-context-state'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('usage-catalog'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), false)
  assert.equal(usageRecordShards.currentProcessOwnsUsageShardWriter(), false)
  assertNonOwnerWriteBlocked(databaseModule.getBusinessDatabase(), '业务库')
  assertNonOwnerWriteBlocked(databaseModule.getChatDatabase(), '聊天库')
  for (const shardIndex of databaseModule.codexContextStateShardIndexes()) {
    assertNonOwnerWriteBlocked(
      databaseModule.getCodexContextStateShardDatabase(shardIndex),
      `Responses 桥接状态索引库分片 ${shardIndex}`
    )
  }
  assertNonOwnerWriteBlocked(databaseModule.getDatasetDatabase(), '数据集目录库')
  assertNonOwnerWriteBlocked(databaseModule.getUsageCatalogDatabase(), '使用记录目录库')
  assertNonOwnerWriteBlocked(databaseModule.getStatsDatabase(), '统计库')
  assertNonOwnerWriteBlocked(usageRecordShards.getUsageRecordShardDatabase(shardLocation), 'usage shard')
  assertCodexContextStateSchemaBoundary()
  assertRuntimeWriteQueueSourceGuards()

  console.log('SQLite writer boundary 回归通过：主库 / usage shard owner 划分、默认严格模式、usage 写入副作用边界、非 owner 写入只读保护和 Node J3b writer 退场边界已就绪')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertDistinctPhysicalSqliteStoragePaths(databaseModule: typeof import('../../storage/database.js')): void {
  databaseModule.assertDistinctStoragePaths()

  const originalDatasetPath = runtimeConfig.datasetDatabasePath
  writeFileSync(runtimeConfig.databasePath, '')
  const hardLinkPath = join(tempRoot, 'dataset-hard-link.sqlite3')
  linkSync(runtimeConfig.databasePath, hardLinkPath)
  runtimeConfig.datasetDatabasePath = hardLinkPath
  assert.throws(
    () => databaseModule.assertDistinctStoragePaths(),
    /同一个 SQLite 物理文件|包含硬链接/,
    '硬链接必须被识别并拒绝，不能绕过 SQLite 单文件单 owner 边界'
  )
  runtimeConfig.datasetDatabasePath = originalDatasetPath
  rmSync(hardLinkPath, { force: true })

  const databaseLinkPath = join(tempRoot, 'dataset-database-link.sqlite3')
  let databaseLinkCreated = false
  try {
    symlinkSync(runtimeConfig.databasePath, databaseLinkPath, 'file')
    databaseLinkCreated = true
  } catch (error) {
    const code = typeof error === 'object' && error !== null && 'code' in error
      ? String((error as { code?: unknown }).code ?? '')
      : undefined
    console.log(`SQLite 路径符号链接碰撞回归跳过：当前环境不允许创建符号链接（${code || (error instanceof Error ? error.message : String(error))}）`)
  }
  if (databaseLinkCreated) {
    runtimeConfig.datasetDatabasePath = databaseLinkPath
    assert.throws(
      () => databaseModule.assertDistinctStoragePaths(),
      /同一个 SQLite 物理文件/,
      '指向已有业务库的符号链接必须被识别为同一 SQLite 物理文件'
    )
    runtimeConfig.datasetDatabasePath = originalDatasetPath
    rmSync(databaseLinkPath, { force: true })
  }
  rmSync(runtimeConfig.databasePath, { force: true })

  const danglingLinkPath = join(tempRoot, 'runtime-log-dangling.sqlite3')
  try {
    symlinkSync(join(tempRoot, 'missing-runtime-log.sqlite3'), danglingLinkPath, 'file')
  } catch (error) {
    const code = typeof error === 'object' && error !== null && 'code' in error
      ? String((error as { code?: unknown }).code ?? '')
      : undefined
    console.log(`SQLite 路径符号链接回归跳过：当前环境不允许创建符号链接（${code || (error instanceof Error ? error.message : String(error))}）`)
    return
  }
  const originalRuntimeLogPath = runtimeConfig.runtimeLogDatabasePath
  try {
    runtimeConfig.runtimeLogDatabasePath = danglingLinkPath
    assert.throws(
      () => databaseModule.assertDistinctStoragePaths(),
      /无法解析的符号链接/,
      '悬空符号链接不能被当作独立 SQLite 文件'
    )
  } finally {
    runtimeConfig.runtimeLogDatabasePath = originalRuntimeLogPath
  }
}

function assertIngestWorkerUsageRecordWriteDoesNotTouchReadonlyBusinessDatabase(): void {
  const createdAt = '2026-06-18T00:00:01.000Z'
  const id = usageRecordShards.generateUsageRecordId(createdAt, 'sqlite-writer-boundary-usage')
  repositories.createUsageRecordsBatch([{
    id,
    systemAccountId: 'sys_admin',
    traceId: 'trace-sqlite-writer-boundary-usage',
    trafficSource: 'gateway',
    accountId: 'missing-account-for-readonly-side-effect',
    endpoint: 'POST /v1/messages',
    providerCode: 'anthropic',
    model: 'claude-sonnet-4-6',
    stream: false,
    statusCode: 200,
    success: true,
    inputTokens: 1,
    outputTokens: 1,
    costUsd: 0.00001,
    createdAt
  }])

  const location = usageRecordShards.usageRecordShardLocationForRecord(id, createdAt)
  const shardRow = usageRecordShards.getUsageRecordShardDatabase(location)
    .prepare('SELECT id FROM usage_records WHERE id = ?')
    .get(id) as { id?: string } | undefined
  assert.equal(shardRow?.id, id, 'ingest-worker 应能写入 usage shard，不应被业务库只读副作用阻塞')

  const entryRow = databaseModule.getUsageCatalogDatabase()
    .prepare('SELECT usage_id FROM usage_record_shard_entries WHERE usage_id = ?')
    .get(id) as { usage_id?: string } | undefined
  assert.equal(entryRow?.usage_id, id, 'ingest-worker 应能写入使用记录目录索引，不应被业务库只读副作用阻塞')
}

function assertNonOwnerWriteBlocked(database: import('node:sqlite').DatabaseSync, label: string): void {
  const queryOnly = database.prepare('PRAGMA query_only').get() as { query_only?: number } | undefined
  assert.equal(queryOnly?.query_only, 1, `${label} 非 owner 连接必须启用 PRAGMA query_only`)
  assert.throws(
    () => database.exec('CREATE TABLE __sqlite_writer_boundary_blocked(id TEXT PRIMARY KEY)'),
    /attempt to write a readonly database|readonly|query_only|SQLITE_READONLY/i,
    `${label} 非 owner 连接必须拒绝写 SQL`
  )
}

function assertRuntimeWriteQueueSourceGuards(): void {
  for (const archivedPath of [
    'src/modules/model-checks/model-checks.service.ts',
    'src/modules/model-checks/model-checks.routes.ts',
    'src/modules/background/background-dataset-writer.ts',
    'src/modules/background/model-quality-scheduled-check.service.ts',
    'src/storage/model-checks.repository.ts',
    'src/storage/model-quality-health.repository.ts',
    'src/storage/model-trust.repository.ts'
  ]) {
    assert.equal(existsSync(resolve(archivedPath)), false, `Node J3b 归档路径不应继续存在：${archivedPath}`)
  }

  const serverSource = readFileSync(resolve('src/server.ts'), 'utf8')
  const systemApiSource = readFileSync(resolve('src/modules/system-api/system-api-app.ts'), 'utf8')
  const backgroundSource = readFileSync(resolve('src/modules/background/background-jobs.ts'), 'utf8')
  assert.doesNotMatch(serverSource, /modelCheckHttpProxy|startModelCheckTokenWorker/)
  assert.doesNotMatch(systemApiSource, /modelChecksRouter/)
  assert.doesNotMatch(backgroundSource, /model-trust-observation-aggregation|model-quality-scheduled-check|model-quality-recovery|model-quality-health-sync-retry/)

  const healthProjectionSource = readFileSync(resolve('src/storage/account-health-projection.repository.ts'), 'utf8')
  assert(healthProjectionSource.includes('projectAccountHealthJobsOutcome'), '业务 SQLite 只能由通用 J1 outcome projector 写入健康事实')
  assert.doesNotMatch(healthProjectionSource, /createRetryQueue|handleOpenAIGatewayRequest|Redis Stream/, 'projector 不得重新承担 Node J1 调度、网关或队列职责')

  const accountTestTaskQueueSource = readFileSync(resolve('src/modules/accounts/account-test-task-queue.service.ts'), 'utf8')
  assert.equal(
    accountTestTaskQueueSource.includes('findAccountForTest('),
    false,
    'ops-worker 手动账号测试队列不能直接调用 findAccountForTest；该函数会先停用过期账号并写业务库'
  )
  assert(
    accountTestTaskQueueSource.includes('find_account_for_test'),
    'ops-worker 手动账号测试队列账号读取必须通过 DB service'
  )
  assert(
    accountTestTaskQueueSource.includes('findAccountForTest: loadAccountForTestViaDbService'),
    'ops-worker 手动账号测试调用测试 service 时必须传入 DB service 账号读取器'
  )

  const accountApiKeyRetestQueueSource = readFileSync(resolve('src/modules/background/account-api-key-cooldown-retest.service.ts'), 'utf8')
  assert.equal(
    accountApiKeyRetestQueueSource.includes('findAccountForTest('),
    false,
    'ops-worker 账户内 API Key 复测队列不能直接调用 findAccountForTest；该函数会先停用过期账号并写业务库'
  )
  assert(
    accountApiKeyRetestQueueSource.includes('find_account_for_test'),
    'ops-worker 账户内 API Key 复测账号读取必须通过 DB service'
  )

  const accountQualityPrecheckQueueSource = readFileSync(resolve('src/modules/background/account-quality-failure-precheck.service.ts'), 'utf8')
  assert.equal(
    accountQualityPrecheckQueueSource.includes('findAccountForTest('),
    false,
    'stats-worker 账号质量失败预确认队列不能直接调用 findAccountForTest；该函数会先停用过期账号并写业务库'
  )
  assert(
    accountQualityPrecheckQueueSource.includes('find_account_for_test'),
    'stats-worker 账号质量失败预确认账号读取必须通过 DB service'
  )
  assert(
    accountQualityPrecheckQueueSource.includes('findAccountForTest: loadAccountForTestViaDbService'),
    'stats-worker 账号质量失败预确认调用测试 service 时必须传入 DB service 账号读取器'
  )

  const gatewayAccountSideEffectsSource = readFileSync(resolve('src/modules/gateway/runtime/account-side-effects.service.ts'), 'utf8')
  assert(
    gatewayAccountSideEffectsSource.includes("type: 'find_account_for_test'"),
    'server 网关事前确认调用测试 service 时必须通过 DB service 读取最终账号状态'
  )

  const openAIOAuthAccessTokenRefreshSource = readFileSync(resolve('src/modules/openai-oauth/openai-oauth-access-token-refresh.service.ts'), 'utf8')
  assert.match(
    openAIOAuthAccessTokenRefreshSource,
    /runtimeConfig\.processRole === 'server' \|\| runtimeConfig\.processRole === 'worker'\s*\?\s*'db-service'\s*:\s*'sync'/,
    'OpenAI OAuth 后台刷新在 worker 默认必须走 DB service，不能因无 IPC 降级到本地写业务库'
  )
  assert.match(
    openAIOAuthAccessTokenRefreshSource,
    /if \(runtimeConfig\.processRole === 'worker'\)[\s\S]{0,240}requestBackgroundWorkerDbService/,
    'OpenAI OAuth worker 写回必须通过 background worker DB service 通道'
  )
  assert.doesNotMatch(
    openAIOAuthAccessTokenRefreshSource,
    /isSingleProcessWorkerRole\(\)[\s\S]{0,200}runLocalOpenAIOAuthDbServiceOperation/,
    'OpenAI OAuth 后台刷新不能给单进程 worker 保留隐式本地写库 fallback'
  )
}

function assertCodexContextStateSchemaBoundary(): void {
  const businessSchemaSource = readFileSync(resolve('src/storage/schema/business-schema.ts'), 'utf8')
  assert.equal(
    businessSchemaSource.includes('codex_context_'),
    false,
    'Responses 桥接状态运行态索引不能放入业务库 schema'
  )

  const codexContextSchemaSource = readFileSync(resolve('src/storage/schema/codex-context-state-schema.ts'), 'utf8')
  assert(
    codexContextSchemaSource.includes('codex_context_sessions')
      && codexContextSchemaSource.includes('codex_context_responses')
      && codexContextSchemaSource.includes('codex_context_compacts'),
    'Responses 桥接状态索引必须保留在独立 schema'
  )
}
