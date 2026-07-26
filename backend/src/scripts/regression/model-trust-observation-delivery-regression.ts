import { strict as assert } from 'node:assert'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-trust-delivery-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.secret = 'model-trust-delivery-secret'
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'stats-worker'

const [modelChecks, trustRepository, security, database] = await Promise.all([
  import('../../storage/model-checks.repository.js'),
  import('../../storage/model-trust.repository.js'),
  import('../../modules/model-checks/model-checks-observation-security.js'),
  import('../../storage/database.js')
])

const run = modelChecks.createModelCheckRun({
  systemAccountId: 'sys_delivery',
  actorSystemAccountId: 'sys_delivery',
  providerCode: 'gpt',
  targetType: 'account',
  targetId: 'acct_delivery',
  accountId: 'acct_delivery',
  model: 'gpt-5.6-sol',
  profile: 'full',
  trustedComparison: false,
  probeSetVersion: 'delivery-regression'
})

const initial = observation('mco_delivery_initial', 'acct_delivery', '2026-07-26T00:00:00.000Z', 'observed')
await trustRepository.createModelCheckObservationsAsync([initial])
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 1)
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 0)

const lateId = 'mco_delivery_late'
const lateCreatedAt = '2000-01-01T00:00:00.000Z'
await trustRepository.createModelCheckObservationsAsync([
  observation(lateId, 'acct_delivery', lateCreatedAt, 'observed')
])
assert.equal(
  await trustRepository.aggregateModelTrustObservationsAsync(500),
  1,
  '任意旧 created_at 的晚提交 observation 必须从未完成集合被领取'
)
assert.equal(windowObservationCount('acct_delivery'), 2, '晚提交 observation 必须且只能累加一次')

database.getDatasetDatabase().prepare(`
  UPDATE model_check_observations SET aggregation_completed_at = NULL WHERE id = ?
`).run(lateId)
database.getStatsDatabase().prepare(`
  INSERT INTO model_trust_observation_receipts (observation_id, observation_created_at, processed_at)
  VALUES (?, ?, ?)
`).run(lateId, lateCreatedAt, new Date().toISOString())
assert.equal(
  await trustRepository.aggregateModelTrustObservationsAsync(500),
  0,
  'stats 已提交但 source marker 未确认时，durable receipt 必须阻止重复累加'
)
assert.equal(windowObservationCount('acct_delivery'), 2)
assert.equal(scalar(database.getDatasetDatabase(), `
  SELECT COUNT(*) AS count FROM model_check_observations
  WHERE id = ? AND aggregation_completed_at IS NOT NULL
`, lateId), 1, 'receipt 恢复必须补齐 source marker')
assert.equal(scalar(database.getStatsDatabase(), `
  SELECT COUNT(*) AS count FROM model_trust_observation_receipts WHERE observation_id = ?
`, lateId), 0, 'source marker 确认后必须清理临时 receipt')

const bounded = Array.from({ length: 101 }, (_item, index) => (
  observation(
    `mco_delivery_bounded_${String(index).padStart(3, '0')}`,
    `acct_delivery_bounded_${String(index).padStart(3, '0')}`,
    new Date(Date.UTC(1999, 0, 1, 0, 0, index)).toISOString(),
    'request_failed'
  )
))
await trustRepository.createModelCheckObservationsAsync(bounded)
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 100, '单事务 observation 批量必须硬限制为 100')
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 1, '剩余 observation 必须由下一短事务续跑')
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 0)

const pendingPlan = database.getDatasetDatabase().prepare(`
  EXPLAIN QUERY PLAN
  SELECT * FROM model_check_observations
  WHERE aggregation_completed_at IS NULL
  ORDER BY created_at, id
  LIMIT 100
`).all() as Array<{ detail: string }>
assert(
  pendingPlan.some((row) => row.detail.includes('idx_model_check_observations_pending_aggregation')),
  `未聚合候选查询必须命中部分索引，实际：${pendingPlan.map((row) => row.detail).join(' | ')}`
)

console.log('模型可信 observation 交付回归通过：晚提交不漏、receipt 防重、单事务 100 条和部分索引均符合预期')

function observation(
  id: string,
  accountId: string,
  createdAt: string,
  observationStatus: string
): import('../../storage/model-trust.repository.js').ModelCheckObservationInput {
  const suffix = id.replace(/[^a-z0-9]/gi, '-')
  return {
    id,
    runId: run.id,
    systemAccountId: 'sys_delivery',
    accountId,
    providerCode: 'gpt',
    providerProtocolProfileId: 'gpt-openai-v1',
    endpointFamily: 'responses',
    requestedModel: 'gpt-5.6-sol',
    mappedUpstreamModel: 'gpt-5.6-sol',
    observedModel: observationStatus === 'observed' ? 'gpt-5.6-sol' : undefined,
    mappingApplied: false,
    upstreamBucketHmac: security.modelCheckObservationHmac(`upstream:${suffix}`, 'upstream'),
    cohortKeyHmac: security.modelCheckObservationHmac(`cohort:${accountId}`, 'cohort'),
    populationKeyHmac: security.modelCheckObservationHmac(`population:${accountId}`, 'population'),
    probeKeyHmac: security.modelCheckObservationHmac(`probe:${suffix}`, 'probe'),
    probeFamily: 'token_input_differential',
    probeSetVersion: 'delivery-regression',
    tokenizerVersion: 'js-tiktoken@1.0.21:o200k_base',
    featureVersion: 'none',
    roundIndex: 0,
    paddingTokens: 0,
    localInputTokens: 100,
    reportedInputTokens: observationStatus === 'observed' ? 110 : undefined,
    observationStatus,
    identityStatus: 'consistent',
    mappingStatus: 'direct',
    protocolStatus: 'consistent',
    evidenceCoverage: 100,
    createdAt
  }
}

function windowObservationCount(accountId: string): number {
  return scalar(database.getStatsDatabase(), `
    SELECT observation_count AS count FROM model_token_integrity_windows WHERE account_id = ?
  `, accountId)
}

function scalar(
  client: ReturnType<typeof database.getStatsDatabase>,
  sql: string,
  value: string
): number {
  return Number((client.prepare(sql).get(value) as { count?: number } | undefined)?.count ?? 0)
}
