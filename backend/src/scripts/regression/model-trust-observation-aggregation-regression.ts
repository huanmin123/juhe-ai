import { strict as assert } from 'node:assert'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-trust-aggregation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.secret = 'model-trust-aggregation-secret'
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'stats-worker'

const [modelChecks, trustRepository, security, postgresSchema, database] = await Promise.all([
  import('../../storage/model-checks.repository.js'),
  import('../../storage/model-trust.repository.js'),
  import('../../modules/model-checks/model-checks-observation-security.js'),
  import('../../storage/postgres-schema.js'),
  import('../../storage/database.js')
])

const run = modelChecks.createModelCheckRun({
  systemAccountId: 'sys_model_trust',
  actorSystemAccountId: 'sys_model_trust',
  providerCode: 'gpt',
  targetType: 'account',
  targetId: 'acct_model_trust',
  accountId: 'acct_model_trust',
  model: 'gpt-5.6-sol',
  profile: 'full',
  trustedComparison: false,
  probeSetVersion: 'openai-model-check-v1'
})
const origin = 'https://relay.example.com/v1'
const upstreamBucketHmac = security.modelCheckObservationHmac(security.normalizedUpstreamOrigin(origin), 'upstream')
const cohortKeyHmac = security.modelCheckObservationHmac('gpt\u0000gpt-openai-v1\u0000responses\u0000gpt-5.6-sol', 'cohort')
assert(!upstreamBucketHmac.includes('relay.example.com'))
await assert.rejects(
  () => trustRepository.createModelCheckObservationsAsync([{
    runId: run.id, systemAccountId: 'sys_model_trust', accountId: 'acct_model_trust', providerCode: 'gpt',
    providerProtocolProfileId: 'gpt-openai-v1', endpointFamily: 'responses', requestedModel: 'gpt-5.6-sol',
    mappedUpstreamModel: 'gpt-5.6-sol', mappingApplied: false, upstreamBucketHmac: origin,
    cohortKeyHmac, populationKeyHmac: cohortKeyHmac, probeKeyHmac: cohortKeyHmac, probeFamily: 'token_input_differential',
    probeSetVersion: 'openai-model-check-v1', tokenizerVersion: 'js-tiktoken@1.0.21:o200k_base',
    featureVersion: 'none',
    roundIndex: 0, paddingTokens: 0, localInputTokens: 1, observationStatus: 'observed',
    identityStatus: 'consistent', mappingStatus: 'direct', protocolStatus: 'consistent', evidenceCoverage: 0
  }]),
  /HMAC 格式无效/,
  'repository 必须拒绝把明文 origin 写入 HMAC 字段'
)

const observations: import('../../storage/model-trust.repository.js').ModelCheckObservationInput[] = []
for (let roundIndex = 0; roundIndex < 3; roundIndex += 1) {
  for (const paddingTokens of [0, 512, 2048]) {
    const localInputTokens = 100 + paddingTokens + roundIndex
    observations.push({
      runId: run.id,
      systemAccountId: 'sys_model_trust',
      accountId: 'acct_model_trust',
      providerCode: 'gpt',
      providerProtocolProfileId: 'gpt-openai-v1',
      endpointFamily: 'responses',
      requestedModel: 'gpt-5.6-sol',
      mappedUpstreamModel: 'gpt-5.6-sol',
      observedModel: 'gpt-5.6-sol',
      mappingApplied: false,
      upstreamBucketHmac,
      cohortKeyHmac,
      populationKeyHmac: cohortKeyHmac,
      probeKeyHmac: security.modelCheckObservationHmac(`probe-${roundIndex}-${paddingTokens}`, 'probe'),
      probeFamily: 'token_input_differential',
      probeSetVersion: 'openai-model-check-v1',
      tokenizerVersion: 'js-tiktoken@1.0.21:o200k_base',
      featureVersion: 'none',
      roundIndex,
      paddingTokens,
      localInputTokens,
      reportedInputTokens: localInputTokens + 10,
      observationStatus: 'observed',
      identityStatus: 'consistent',
      mappingStatus: 'direct',
      protocolStatus: 'consistent',
      evidenceCoverage: 100,
      createdAt: new Date(Date.UTC(2026, 6, 14, 0, 0, observations.length)).toISOString()
    })
  }
}
assert.equal(await trustRepository.createModelCheckObservationsAsync(observations), 9)
const concurrentAggregation = await Promise.all([
  trustRepository.aggregateModelTrustObservationsAsync(4),
  trustRepository.aggregateModelTrustObservationsAsync(4)
])
assert.deepEqual([...concurrentAggregation].sort((left, right) => left - right), [0, 4], '并发聚合只能由一个数据库租约 owner 推进游标')
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 5)
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 0, '游标不能重复聚合已完成 observation')

const latest = await trustRepository.findModelAccountTrustResultAsync('sys_model_trust', 'acct_model_trust', 'gpt-5.6-sol')
assert(latest)
assert.equal(latest.observationCount, 9)
assert.equal(latest.roundCount, 3)
assert.equal(latest.independentSourceCount, 1)
assert.equal(latest.usageIntegrityStatus, 'consistent')
assert(Math.abs((latest.slope ?? 0) - 1) < 0.001)
assert(Math.abs((latest.intercept ?? 0) - 10) < 0.001)
assert.equal(latest.interceptBaselineStatus, 'calibration_pending')
assert.equal(latest.interceptStrongGateEnabled, false, '单来源待校准基线不能开启固定灌水强判')
assert(latest.reasonCodes.includes('fixed_intercept_calibration_pending'))
assert.equal(latest.evidenceStatus, 'insufficient', '单一上游桶不能形成群体稳定证据')
const window = database.getStatsDatabase().prepare('SELECT round_count, slope, intercept, usage_integrity_status FROM model_token_integrity_windows LIMIT 1').get() as Record<string, unknown>
assert.equal(window.round_count, 3, '窗口本身必须保存已完成轮次')
assert.equal(window.usage_integrity_status, 'consistent')
assert(Math.abs(Number(window.slope) - 1) < 0.001)

modelChecks.finishModelCheckRun(run.id, {
  level: 'high_confidence', score: 100, status: 'completed', message: '模型检测完成',
  resultSummary: {
    trustReport: {
      identityStatus: 'consistent', mappingStatus: 'direct', usageIntegrityStatus: 'insufficient_evidence',
      protocolStatus: 'consistent', evidenceStatus: 'insufficient', evidenceCoverage: 100,
      observedModel: 'gpt-5.6-sol', reasonCodes: []
    }
  }
})
const { getModelCheckRun } = await import('../../modules/model-checks/model-checks.service.js')
const apiDetail = await getModelCheckRun(run.id, { systemAccountId: 'sys_model_trust', role: 'admin' })
const apiTrustReport = apiDetail?.resultSummary.trustReport as Record<string, unknown> | undefined
assert.equal(apiTrustReport?.usageIntegrityStatus, 'consistent', '详情 API 必须只读 stats latest 结果并合并到报告')
assert.equal(apiTrustReport?.observationCount, 9)
const userDetail = await getModelCheckRun(run.id, { systemAccountId: 'sys_model_trust', role: 'user' })
const userTrustReport = userDetail?.resultSummary.trustReport as Record<string, unknown> | undefined
assert.equal(userTrustReport?.observationCount, 9, '用户详情也必须按当前作用域读取预聚合结果')
const unavailableRun = modelChecks.createModelCheckRun({
  systemAccountId: 'sys_model_trust', actorSystemAccountId: 'sys_model_trust', providerCode: 'gpt',
  targetType: 'account', targetId: 'acct_model_trust', accountId: 'acct_model_trust', model: 'gpt-5.6-sol',
  trustedComparison: false, probeSetVersion: 'openai-model-check-v1'
})
modelChecks.finishModelCheckRun(unavailableRun.id, {
  level: 'unavailable', score: 0, maxScore: 0, status: 'completed', message: '探针不可用',
  resultSummary: {
    trustReport: {
      identityStatus: 'insufficient_evidence', mappingStatus: 'unknown', usageIntegrityStatus: 'insufficient_evidence',
      protocolStatus: 'insufficient_evidence', evidenceStatus: 'insufficient', evidenceCoverage: 0,
      reasonCodes: ['model_response_evidence_unavailable']
    }
  }
})
const unavailableDetail = await getModelCheckRun(unavailableRun.id, { systemAccountId: 'sys_model_trust', role: 'admin' })
const unavailableReport = unavailableDetail?.resultSummary.trustReport as Record<string, unknown> | undefined
assert.equal(unavailableReport?.identityStatus, 'insufficient_evidence', '历史 latest 不能覆盖当前不可用报告')
assert.equal(unavailableReport?.mappingStatus, 'unknown')
assert.equal(unavailableReport?.observationCount, undefined, '当前无有效响应时不能混入历史 observation 数')

const lateCommittedRun = modelChecks.createModelCheckRun({
  systemAccountId: 'sys_model_trust', actorSystemAccountId: 'sys_model_trust', providerCode: 'gpt',
  targetType: 'account', targetId: 'acct_late_commit', accountId: 'acct_late_commit', model: 'gpt-5.6-sol',
  trustedComparison: false, probeSetVersion: 'openai-model-check-v1'
})
const lateCommittedObservation = tokenObservation({
  accountId: 'acct_late_commit',
  upstream: 'late-commit-source',
  cohortKeyHmac: security.modelCheckObservationHmac('late-commit-cohort', 'cohort'),
  observationStatus: 'observed',
  reportedInputTokens: 110,
  createdAt: new Date(Date.UTC(2026, 6, 13, 23, 59, 59)).toISOString()
})
lateCommittedObservation.runId = lateCommittedRun.id
await trustRepository.createModelCheckObservationsAsync([lateCommittedObservation])
assert.equal(
  await trustRepository.aggregateModelTrustObservationsAsync(500),
  1,
  '晚于游标推进才提交的旧 created_at observation 仍必须最终聚合，不能被永久遗漏'
)
assert.equal(
  (database.getStatsDatabase().prepare('SELECT observation_count AS count FROM model_token_integrity_windows WHERE account_id = ?').get('acct_late_commit') as { count: number }).count,
  1,
  '晚提交 observation 只能聚合一次'
)
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 0, '晚提交 observation 完成后不得重复计数')
database.getDatasetDatabase().prepare('UPDATE model_check_observations SET aggregation_completed_at = NULL WHERE account_id = ?').run('acct_late_commit')
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 0, '源标记确认失败后的重试必须由 durable receipt 去重')
assert.equal(
  (database.getStatsDatabase().prepare('SELECT observation_count AS count FROM model_token_integrity_windows WHERE account_id = ?').get('acct_late_commit') as { count: number }).count,
  1,
  'receipt 重试不得重复累加派生窗口'
)
assert.equal(
  (database.getDatasetDatabase().prepare('SELECT COUNT(*) AS count FROM model_check_observations WHERE account_id = ? AND aggregation_completed_at IS NOT NULL').get('acct_late_commit') as { count: number }).count,
  1,
  'receipt 重试后必须补齐源表完成标记'
)

const boundedBatchObservations = Array.from({ length: 101 }, (_item, index) => {
  const observation = tokenObservation({
    accountId: `acct_bounded_${String(index).padStart(3, '0')}`,
    upstream: `bounded-source-${index}`,
    cohortKeyHmac: security.modelCheckObservationHmac(`bounded-cohort-${index}`, 'cohort'),
    observationStatus: 'request_failed',
    createdAt: new Date(Date.UTC(2026, 6, 12, 0, 0, index)).toISOString()
  })
  observation.runId = lateCommittedRun.id
  return observation
})
await trustRepository.createModelCheckObservationsAsync(boundedBatchObservations)
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 100, '调用方即使传入 500，单个聚合事务也只能领取 100 条 observation')
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 1, '超过单事务上限的 observation 必须留给下一短事务')
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 0)

const invalidCohortKeyHmac = security.modelCheckObservationHmac('invalid-token-cohort', 'cohort')
const invalidObservations: import('../../storage/model-trust.repository.js').ModelCheckObservationInput[] = []
const validObservedAt = new Date(Date.UTC(2026, 6, 15, 0, 0, 0)).toISOString()
const lastValidObservedAt = new Date(Date.UTC(2026, 6, 15, 0, 0, 2)).toISOString()
for (let roundIndex = 0; roundIndex < 3; roundIndex += 1) {
  invalidObservations.push(tokenObservation({
    accountId: 'acct_invalid_evidence',
    upstream: 'valid-source',
    cohortKeyHmac: invalidCohortKeyHmac,
    observationStatus: 'observed',
    reportedInputTokens: 110 + roundIndex,
    createdAt: new Date(Date.UTC(2026, 6, 15, 0, 0, roundIndex)).toISOString(),
    roundIndex,
    paddingTokens: 0
  }))
}
for (let index = 0; index < 30; index += 1) {
  const requestFailed = index % 2 === 0
  invalidObservations.push(tokenObservation({
    accountId: 'acct_invalid_evidence',
    upstream: `invalid-source-${index % 3}`,
    cohortKeyHmac: invalidCohortKeyHmac,
    observationStatus: requestFailed ? 'request_failed' : 'usage_missing',
    reportedInputTokens: requestFailed ? 110 + index : undefined,
    createdAt: new Date(Date.UTC(2026, 6, 15 + (index % 3), 1, 0, index)).toISOString(),
    roundIndex: Math.floor(index / 3),
    paddingTokens: [0, 512, 2048][index % 3]
  }))
}
await trustRepository.createModelCheckObservationsAsync(invalidObservations)
while (await trustRepository.aggregateModelTrustObservationsAsync(7)) {
  // 多批聚合必须只推进游标，失败和 usage 缺失 observation 不得进入证据窗口。
}
const invalidLatest = await trustRepository.findModelAccountTrustResultAsync('sys_model_trust', 'acct_invalid_evidence', 'gpt-5.6-sol')
assert(invalidLatest)
assert.equal(invalidLatest.observationCount, 3, 'latest observation 数只能统计有效样本')
assert.equal(invalidLatest.roundCount, 0, '三个残缺轮次不能拼成一个完整轮次')
assert.equal(invalidLatest.independentSourceCount, 1, '失败来源不能放大独立来源数')
assert.equal(invalidLatest.evidenceStatus, 'insufficient', '失败和 usage 缺失样本不能把阶段推到 bootstrap')
assert.equal(invalidLatest.evidenceCoverage, 8, '覆盖率只能由一个有效来源贡献')
const invalidWindow = database.getStatsDatabase().prepare(`
  SELECT observation_count, valid_sample_count, round_count, first_observed_at, last_observed_at
  FROM model_token_integrity_windows WHERE account_id = ?
`).get('acct_invalid_evidence') as Record<string, unknown>
assert.equal(invalidWindow.observation_count, 3)
assert.equal(invalidWindow.valid_sample_count, 3)
assert.equal(invalidWindow.round_count, 0)
assert.equal(invalidWindow.first_observed_at, validObservedAt)
assert.equal(invalidWindow.last_observed_at, lastValidObservedAt, '失败样本时间不能扩大稳定证据时间跨度')
const completedRoundCount = database.getStatsDatabase().prepare(`
  SELECT COUNT(*) AS count FROM model_token_integrity_rounds WHERE account_id = ? AND (padding_mask & 7) = 7
`).get('acct_invalid_evidence') as { count: number }
assert.equal(completedRoundCount.count, 0)
const validSourceCount = database.getStatsDatabase().prepare(`
  SELECT COUNT(*) AS count FROM model_trust_window_sources WHERE account_id = ?
`).get('acct_invalid_evidence') as { count: number }
assert.equal(validSourceCount.count, 1)

const hardConflictObservedAt = new Date(Date.UTC(2026, 6, 18)).toISOString()
await trustRepository.createModelCheckObservationsAsync([tokenObservation({
  accountId: 'acct_protocol_conflict_token',
  upstream: 'protocol-conflict-token-source',
  cohortKeyHmac: security.modelCheckObservationHmac('protocol-conflict-token-cohort', 'cohort'),
  observationStatus: 'observed',
  reportedInputTokens: 100,
  createdAt: hardConflictObservedAt,
  protocolStatus: 'failed'
})])
while (await trustRepository.aggregateModelTrustObservationsAsync(7)) {
}
const hardConflictLatest = await trustRepository.findModelAccountTrustResultAsync('sys_model_trust', 'acct_protocol_conflict_token', 'gpt-5.6-sol')
assert.equal(hardConflictLatest?.protocolStatus, 'failed', 'Token 协议硬冲突必须保留 latest 诊断事实')
assert.equal(hardConflictLatest?.observationCount, 0, '协议硬冲突不得增加 Token 有效样本')
assert(hardConflictLatest?.reasonCodes.includes('protocol_check_failed'))
assert.equal(
  (database.getStatsDatabase().prepare('SELECT COUNT(*) AS count FROM model_token_integrity_windows WHERE account_id = ?').get('acct_protocol_conflict_token') as { count: number }).count,
  0,
  '协议硬冲突不得进入 Token 窗口'
)
assert.equal(
  (database.getStatsDatabase().prepare('SELECT COUNT(*) AS count FROM model_trust_window_sources WHERE account_id = ?').get('acct_protocol_conflict_token') as { count: number }).count,
  0,
  '协议硬冲突不得进入 cohort 来源'
)
assert.equal(
  (database.getDatasetDatabase().prepare('SELECT COUNT(*) AS count FROM model_check_observations WHERE account_id = ?').get('acct_protocol_conflict_token') as { count: number }).count,
  1,
  '协议硬冲突 observation 本身必须保留'
)

const sourceIndexColumns = database.getStatsDatabase().prepare("PRAGMA index_info('idx_model_trust_window_sources_cohort')").all() as Array<{ seqno: number; name: string }>
assert.deepEqual(
  sourceIndexColumns.sort((left, right) => left.seqno - right.seqno).map((column) => column.name),
  ['cohort_key_hmac', 'upstream_bucket_hmac'],
  'cohort 来源 COUNT 索引必须以前导等值列开始，并覆盖 DISTINCT 来源桶'
)
const sourceCountPlan = database.getStatsDatabase().prepare(`
  EXPLAIN QUERY PLAN
  SELECT COUNT(DISTINCT upstream_bucket_hmac) FROM model_trust_window_sources WHERE cohort_key_hmac = ?
`).all(cohortKeyHmac) as Array<{ detail: string }>
assert(
  sourceCountPlan.some((row) => row.detail.includes('idx_model_trust_window_sources_cohort')),
  `cohort 来源 COUNT 必须命中新索引，实际计划：${sourceCountPlan.map((row) => row.detail).join(' | ')}`
)

const stored = database.getDatasetDatabase().prepare('SELECT * FROM model_check_observations LIMIT 1').get() as Record<string, unknown>
const serialized = JSON.stringify(stored)
assert(!serialized.includes(origin), 'observation 不得保存明文上游 origin')
assert(!serialized.includes('Controlled token integrity probe'), 'observation 不得保存受控题面')
assert(!('prompt' in stored) && !('request_body' in stored) && !('response_body' in stored), 'observation schema 不得包含正文列')
const pendingAggregationPlan = database.getDatasetDatabase().prepare(`
  EXPLAIN QUERY PLAN
  SELECT * FROM model_check_observations
  WHERE aggregation_completed_at IS NULL
  ORDER BY created_at, id
  LIMIT 100
`).all() as Array<{ detail: string }>
assert(
  pendingAggregationPlan.some((row) => row.detail.includes('idx_model_check_observations_pending_aggregation')),
  `未聚合 observation 候选查询必须命中部分索引，实际计划：${pendingAggregationPlan.map((row) => row.detail).join(' | ')}`
)

const dirtyInsert = database.getStatsDatabase().prepare(`
  INSERT INTO model_trust_latest_dirty_accounts (
    system_account_id, account_id, requested_model, dirty_reason, updated_at
  ) VALUES (?, ?, 'gpt-5.6-sol', 'regression', ?)
`)
database.getStatsDatabase().exec('BEGIN')
try {
  for (let index = 0; index < 501; index += 1) {
    dirtyInsert.run('sys_model_trust', `acct_dirty_${String(index).padStart(4, '0')}`, '2026-07-20T00:00:00.000Z')
  }
  database.getStatsDatabase().exec('COMMIT')
} catch (error) {
  database.getStatsDatabase().exec('ROLLBACK')
  throw error
}
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 0)
assert.equal(
  (database.getStatsDatabase().prepare('SELECT COUNT(*) AS count FROM model_trust_latest_dirty_accounts').get() as { count: number }).count,
  401,
  '无新 observation 时也必须按单事务 100 个账户的边界续跑 dirty queue'
)
for (let index = 0; index < 5; index += 1) {
  assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(500), 0)
}
assert.equal(
  (database.getStatsDatabase().prepare('SELECT COUNT(*) AS count FROM model_trust_latest_dirty_accounts').get() as { count: number }).count,
  0,
  'dirty queue continuation 必须最终清空剩余账号'
)

const pgSql = postgresSchema.buildPostgresSchemaSql()
assert(pgSql.includes('CREATE TABLE IF NOT EXISTS model_check_observations'))
assert(pgSql.includes('CREATE TABLE IF NOT EXISTS model_token_integrity_windows'))
assert(pgSql.includes('CREATE TABLE IF NOT EXISTS model_token_integrity_rounds'))
assert(pgSql.includes('CREATE TABLE IF NOT EXISTS model_token_intercept_baseline_versions'))
assert(pgSql.includes('CREATE TABLE IF NOT EXISTS model_account_trust_results'))
assert(pgSql.includes('CREATE TABLE IF NOT EXISTS model_trust_latest_dirty_accounts'))
assert(pgSql.includes('CREATE TABLE IF NOT EXISTS model_trust_observation_receipts'))
assert(pgSql.includes('CREATE INDEX IF NOT EXISTS idx_model_token_intercept_baseline_active'))
assert(pgSql.includes('CREATE INDEX IF NOT EXISTS idx_model_trust_window_sources_cohort ON model_trust_window_sources(cohort_key_hmac, upstream_bucket_hmac)'))
assert(pgSql.includes('CREATE INDEX IF NOT EXISTS idx_model_trust_latest_dirty_updated'))
assert(pgSql.includes('ALTER TABLE model_check_observations ADD COLUMN IF NOT EXISTS aggregation_completed_at text'))
assert(pgSql.includes('CREATE INDEX IF NOT EXISTS idx_model_check_observations_pending_aggregation ON model_check_observations(created_at, id) WHERE aggregation_completed_at IS NULL'))

console.log('模型可信 observation 聚合回归通过：脱敏事实、游标增量、窗口结果和 PostgreSQL schema 同步符合预期')

function tokenObservation(input: {
  accountId: string
  upstream: string
  cohortKeyHmac: string
  observationStatus: string
  reportedInputTokens?: number
  createdAt: string
  roundIndex?: number
  paddingTokens?: number
  protocolStatus?: string
}): import('../../storage/model-trust.repository.js').ModelCheckObservationInput {
  return {
    runId: run.id,
    systemAccountId: 'sys_model_trust',
    accountId: input.accountId,
    providerCode: 'gpt',
    providerProtocolProfileId: 'gpt-openai-v1',
    endpointFamily: 'responses',
    requestedModel: 'gpt-5.6-sol',
    mappedUpstreamModel: 'gpt-5.6-sol',
    observedModel: 'gpt-5.6-sol',
    mappingApplied: false,
    upstreamBucketHmac: security.modelCheckObservationHmac(input.upstream, 'upstream'),
    cohortKeyHmac: input.cohortKeyHmac,
    populationKeyHmac: input.cohortKeyHmac,
    probeKeyHmac: security.modelCheckObservationHmac(`${input.accountId}:${input.createdAt}`, 'probe'),
    probeFamily: 'token_input_differential',
    probeSetVersion: 'openai-model-check-v1',
    tokenizerVersion: 'js-tiktoken@1.0.21:o200k_base',
    featureVersion: 'none',
    roundIndex: input.roundIndex ?? 0,
    paddingTokens: input.paddingTokens ?? 0,
    localInputTokens: 100 + (input.paddingTokens ?? 0),
    reportedInputTokens: input.reportedInputTokens,
    observationStatus: input.observationStatus,
    identityStatus: 'consistent',
    mappingStatus: 'direct',
    protocolStatus: input.protocolStatus ?? 'consistent',
    evidenceCoverage: 100,
    createdAt: input.createdAt
  }
}
