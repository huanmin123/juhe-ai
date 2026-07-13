import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-request-time-pricing-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.queueDriver = 'memory'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'usage-request-time-pricing-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, catalogService, repositories, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/model-pricing/model-catalog.service.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-record-shards.js')
])

try {
  assertRedisStreamFreezesBeforeEnqueue()

  const model = 'gpt-request-time-pricing-regression'
  const saved = catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model,
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: 'sys_admin'
  })
  const createdAt = '2026-07-14T01:02:03.000Z'
  const input = {
    id: usageRecordShards.generateUsageRecordId(createdAt, 'request-time-pricing-old'),
    traceId: 'trace-request-time-pricing-old',
    trafficSource: 'gateway' as const,
    systemAccountId: 'sys_admin',
    endpoint: '/v1/responses',
    providerCode: 'gpt',
    model,
    upstreamModel: model,
    billedServiceTier: 'default' as const,
    success: true,
    inputTokens: 1_000_000,
    outputTokens: 1_000_000,
    createdAt
  }

  const frozenBeforeCatalogChange = await repositories.freezeUsageRecordPricingFactsAsync(input)
  assert.equal(frozenBeforeCatalogChange.pricingModel, model, '入 Redis 前应固化请求时 pricingModel')
  assert.equal(frozenBeforeCatalogChange.pricingSnapshot?.inputUsdPer1M, 1, '入 Redis 前应固化请求时输入单价')
  assert.equal(frozenBeforeCatalogChange.pricingSnapshot?.outputUsdPer1M, 2, '入 Redis 前应固化请求时输出单价')
  assert.equal(frozenBeforeCatalogChange.pricingSnapshot?.accountChargeUsd, 3, '入 Redis 前应固化请求时完整成本')

  catalogService.saveCustomProviderModel({
    id: saved.id,
    providerCode: 'gpt',
    model,
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 10,
    outputUsdPer1M: 20,
    actorSystemAccountId: 'sys_admin'
  })
  const frozenAfterCatalogChange = await repositories.freezeUsageRecordPricingFactsAsync({
    ...input,
    id: usageRecordShards.generateUsageRecordId(createdAt, 'request-time-pricing-new'),
    traceId: 'trace-request-time-pricing-new'
  })
  assert.equal(frozenAfterCatalogChange.pricingSnapshot?.accountChargeUsd, 30, '目录变更后的新请求应使用新价格')

  repositories.createUsageRecordsBatch([frozenBeforeCatalogChange])
  const persisted = repositories.getUsageRecordDetail(input.id, { systemAccountId: 'sys_admin', role: 'admin' })
  assert.equal(persisted?.pricingSnapshot?.inputUsdPer1M, 1, 'consumer backlog 落库不得用未来目录覆盖已固化输入单价')
  assert.equal(persisted?.pricingSnapshot?.outputUsdPer1M, 2, 'consumer backlog 落库不得用未来目录覆盖已固化输出单价')
  assert.equal(persisted?.pricingSnapshot?.accountChargeUsd, 3, 'consumer backlog 落库不得重算已固化请求成本')

  console.log('使用记录请求时计价回归通过：Redis Stream 入队前固化完整快照，目录变更不重解释 backlog')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertRedisStreamFreezesBeforeEnqueue(): void {
  const queueSource = readFileSync(join(backendRoot, 'src', 'modules', 'gateway', 'usage', 'record-queue.service.ts'), 'utf8')
  const repositorySource = readFileSync(join(backendRoot, 'src', 'storage', 'usage-records.repository.ts'), 'utf8')
  assert.match(
    queueSource,
    /if \(shouldEnqueueUsageRecordToRedisStream\(\)\) \{[\s\S]*await freezeUsageRecordPricingFactsAsync\(queuedInput\)[\s\S]*enqueueUsageRecordToRedisStream\(frozenInput\)/,
    '高性能 Redis Stream 必须先异步固化请求时计价事实再入队'
  )
  assert.doesNotMatch(
    queueSource,
    /if \(shouldEnqueueUsageRecordToRedisStream\(\)\) \{[\s\S]{0,300}buildCatalogCostBreakdown\(/,
    '高性能请求路径不得同步扫描目录生成计价快照'
  )
  assert.match(
    repositorySource,
    /async function enrichSingleUsageRecordPricingAsync\([\s\S]*if \(input\.pricingSnapshot !== undefined\) return input/,
    'consumer 必须把已固化 pricing snapshot 当作不可变事实'
  )
}
