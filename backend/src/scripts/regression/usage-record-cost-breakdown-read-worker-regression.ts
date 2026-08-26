import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import type { UsageRecordSummary } from '../../storage/repositories.js'

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-cost-read-worker-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context')
runtimeConfig.secret = 'usage-cost-read-worker-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.sqliteReadWorkerPoolSize = 2
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, readWorkerPool, usageRecordsRoutes] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../modules/usage-records/usage-records.routes.js')
])

try {
  databaseModule.getBusinessDatabase()
  const handledJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  const response = await usageRecordsRoutes.withCostBreakdownAsync(usageRecordFixture())
  assert(response.costBreakdown, '成功使用记录应返回成本明细')
  assert.equal(response.costBreakdown.inputCostUsd, undefined, '缺少请求时价格快照的旧记录不得按当前目录重算输入成本')
  assert.equal(response.costBreakdown.serviceTierPricingSource, 'unknown', '缺少请求时价格快照的旧记录必须标记价格来源未知')
  assert.equal(
    readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs,
    handledJobsBefore,
    '历史费用拆分不得读取当前模型目录，避免未来价格污染旧记录'
  )

  console.log('使用记录费用拆分 read worker 回归通过：无请求时价格快照的旧记录不读取当前目录且价格来源保持未知')
} finally {
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function usageRecordFixture(): UsageRecordSummary {
  return {
    id: 'usage_cost_read_worker',
    traceId: 'trace_usage_cost_read_worker',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    groupId: 'group_usage_cost_read_worker',
    endpoint: '/v1/responses',
    stream: false,
    success: true,
    providerCode: GPT_VENDOR_CODE,
    model: 'gpt-5.5',
    inputTokens: 1_000_000,
    outputTokens: 1_000,
    createdAt: '2026-07-01T00:00:00.000Z'
  }
}
