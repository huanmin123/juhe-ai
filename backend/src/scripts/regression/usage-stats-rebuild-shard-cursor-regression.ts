import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-stats-rebuild-shard-cursor-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const databasePath = join(tempRoot, 'business.sqlite3')
const datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
const usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
const statsDatabasePath = join(tempRoot, 'stats.sqlite3')
const usageShardRoot = join(tempRoot, 'usage-shards')

runtimeConfig.databasePath = databasePath
runtimeConfig.datasetDatabasePath = datasetDatabasePath
runtimeConfig.statsDatabasePath = statsDatabasePath
runtimeConfig.usageShardRoot = usageShardRoot
runtimeConfig.usageShardCount = 4
runtimeConfig.secret = 'usage-stats-rebuild-shard-cursor-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageStatsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: '统计重建分片游标回归分组', providerCode: 'gpt', enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '统计重建分片游标回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-stats-rebuild-shard-cursor',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '统计重建分片游标回归 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
  }, access)
  const createdAtBase = Date.now() - 60_000
  const records = Array.from({ length: 20 }, (_, index) => ({
    traceId: `trace-usage-stats-rebuild-shard-cursor-${index}`,
    trafficSource: 'gateway' as const,
    apiKeyId: apiKey.id,
    groupId: group.id,
    accountId: account.id,
    endpoint: '/v1/responses',
    providerCode: 'gpt',
    model: 'gpt-5.5',
    stream: false,
    statusCode: 200,
    success: true,
    inputTokens: 10,
    outputTokens: 20,
    costUsd: 0.001,
    createdAt: new Date(createdAtBase + index).toISOString()
  }))

  repositories.createUsageRecordsBatch(records)
  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(1000), records.length, '预聚合应先建立 per-shard 游标')
  assert.equal(apiKeyStatsTotal(apiKey.id), records.length, '预聚合统计应存在')
  assert(usageShardCursorCount() > 0, '预聚合后应存在 usage_shard 游标')

  databaseModule.closeStorageDatabases()
  const result = spawnSync(process.execPath, [
    '--import',
    'tsx',
    'src/scripts/maintenance/rebuild-usage-stats.ts',
    '1000',
    '--confirm-offline'
  ], {
    cwd: backendRoot,
    env: {
      ...process.env,
      JUHE_AI_DATABASE_PATH: databasePath,
      JUHE_AI_DATASET_DATABASE_PATH: datasetDatabasePath,
      JUHE_AI_USAGE_CATALOG_DATABASE_PATH: usageCatalogDatabasePath,
      JUHE_AI_STATS_DATABASE_PATH: statsDatabasePath,
      JUHE_AI_USAGE_SHARD_ROOT: usageShardRoot,
      JUHE_AI_USAGE_SHARD_COUNT: '4',
      JUHE_AI_PROCESS_ROLE: 'worker',
      JUHE_AI_WORKER_ROLE: 'stats-worker',
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    encoding: 'utf8'
  })
  if (result.status !== 0) {
    process.stdout.write(result.stdout)
    process.stderr.write(result.stderr)
    process.exit(result.status ?? 1)
  }

  assert.equal(apiKeyStatsTotal(apiKey.id), records.length, '重建脚本应清理已有 per-shard 游标并从 shard 重新聚合统计')
  console.log('用量统计重建分片游标回归通过：已有 per-shard 游标不会阻止重建全量扫描')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function apiKeyStatsTotal(apiKeyId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare("SELECT request_count FROM usage_stats_totals WHERE system_account_id = 'sys_admin' AND scope_type = 'api_key' AND scope_id = ?")
    .get(apiKeyId) as { request_count?: number } | undefined
  return Number(row?.request_count ?? 0)
}

function usageShardCursorCount(): number {
  const row = databaseModule.getStatsDatabase()
    .prepare("SELECT COUNT(*) AS total FROM stats_job_state WHERE scope_type = 'usage_shard' AND job_name = 'usage_stats_aggregation' AND cursor_id IS NOT NULL")
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
