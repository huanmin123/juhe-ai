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
    cohortKeyHmac, probeKeyHmac: cohortKeyHmac, probeFamily: 'token_input_differential',
    probeSetVersion: 'openai-model-check-v1', tokenizerVersion: 'js-tiktoken@1.0.21:o200k_base',
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
      probeKeyHmac: security.modelCheckObservationHmac(`probe-${roundIndex}-${paddingTokens}`, 'probe'),
      probeFamily: 'token_input_differential',
      probeSetVersion: 'openai-model-check-v1',
      tokenizerVersion: 'js-tiktoken@1.0.21:o200k_base',
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
assert.equal(await trustRepository.aggregateModelTrustObservationsAsync(4), 4)
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
assert.equal(latest.evidenceStatus, 'insufficient', '单一上游桶不能形成群体稳定证据')
const window = database.getStatsDatabase().prepare('SELECT round_count, slope, intercept, usage_integrity_status FROM model_token_integrity_windows LIMIT 1').get() as Record<string, unknown>
assert.equal(window.round_count, 3, '窗口本身必须保存已完成轮次')
assert.equal(window.usage_integrity_status, 'consistent')
assert(Math.abs(Number(window.slope) - 1) < 0.001)

const { getModelCheckRun } = await import('../../modules/model-checks/model-checks.service.js')
const apiDetail = await getModelCheckRun(run.id, { systemAccountId: 'sys_model_trust', role: 'admin' })
const apiTrustReport = apiDetail?.resultSummary.trustReport as Record<string, unknown> | undefined
assert.equal(apiTrustReport?.usageIntegrityStatus, 'consistent', '详情 API 必须只读 stats latest 结果并合并到报告')
assert.equal(apiTrustReport?.observationCount, 9)
const userDetail = await getModelCheckRun(run.id, { systemAccountId: 'sys_model_trust', role: 'user' })
const userTrustReport = userDetail?.resultSummary.trustReport as Record<string, unknown> | undefined
assert.equal(userTrustReport?.observationCount, 9, '用户详情也必须按当前作用域读取预聚合结果')

const stored = database.getDatasetDatabase().prepare('SELECT * FROM model_check_observations LIMIT 1').get() as Record<string, unknown>
const serialized = JSON.stringify(stored)
assert(!serialized.includes(origin), 'observation 不得保存明文上游 origin')
assert(!serialized.includes('Controlled token integrity probe'), 'observation 不得保存受控题面')
assert(!('prompt' in stored) && !('request_body' in stored) && !('response_body' in stored), 'observation schema 不得包含正文列')

const pgSql = postgresSchema.buildPostgresSchemaSql()
assert(pgSql.includes('CREATE TABLE IF NOT EXISTS model_check_observations'))
assert(pgSql.includes('CREATE TABLE IF NOT EXISTS model_token_integrity_windows'))
assert(pgSql.includes('CREATE TABLE IF NOT EXISTS model_account_trust_results'))

console.log('模型可信 observation 聚合回归通过：脱敏事实、游标增量、窗口结果和 PostgreSQL schema 同步符合预期')
