import { strict as assert } from 'node:assert'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-identity-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.secret = 'model-identity-baseline-secret'
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'stats-worker'

const [modelChecks, repository, security, database, statistics] = await Promise.all([
  import('../../storage/model-checks.repository.js'),
  import('../../storage/model-trust.repository.js'),
  import('../../modules/model-checks/model-checks-observation-security.js'),
  import('../../storage/database.js'),
  import('../../storage/model-trust-statistics.js')
])

const run = modelChecks.createModelCheckRun({
  systemAccountId: 'sys_identity', actorSystemAccountId: 'sys_identity', providerCode: 'gpt',
  targetType: 'account', targetId: 'acct_same_source', accountId: 'acct_same_source',
  model: 'gpt-5.6-sol', trustedComparison: false, probeSetVersion: 'identity-mock-v1'
})
const populationKeyHmac = security.modelCheckObservationHmac('identity-mock-population', 'population')
const models = ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.5', 'gpt-5.4']
const modelBase = new Map([
  ['gpt-5.6-sol', 0.10], ['gpt-5.6-terra', 0.25], ['gpt-5.6-luna', 0.40],
  ['gpt-5.5', 0.48], ['gpt-5.4', 0.62]
])
const observations: import('../../storage/model-trust.repository.js').ModelCheckObservationInput[] = []

for (let sourceIndex = 0; sourceIndex < 5; sourceIndex += 1) {
  for (const model of models) {
    for (let probeIndex = 0; probeIndex < 3; probeIndex += 1) {
      for (let round = 0; round < 2; round += 1) {
        observations.push(identityObservation({
          accountId: `acct_source_${sourceIndex}`,
          upstream: `source-${sourceIndex}`,
          model,
          probeIndex,
          vector: featureVector(Number(modelBase.get(model)) + sourceIndex * 0.012 + probeIndex * 0.004),
          createdAt: new Date(Date.UTC(2026, 6, 10 + round * 2, 0, sourceIndex, probeIndex)).toISOString()
        }))
      }
    }
  }
}

for (const model of ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna']) {
  for (let probeIndex = 0; probeIndex < 3; probeIndex += 1) {
    observations.push(identityObservation({
      accountId: 'acct_same_source', upstream: 'same-source-target', model, probeIndex,
      vector: featureVector(0.22 + probeIndex * 0.004), createdAt: new Date(Date.UTC(2026, 6, 12, 1, 0, probeIndex)).toISOString()
    }))
  }
}

for (let probeIndex = 0; probeIndex < 3; probeIndex += 1) {
  observations.push(identityObservation({
    accountId: 'acct_downgrade', upstream: 'downgrade-target', model: 'gpt-5.5', probeIndex,
    vector: featureVector(0.644 + probeIndex * 0.004), createdAt: new Date(Date.UTC(2026, 6, 12, 2, 0, probeIndex)).toISOString()
  }))
  observations.push(identityObservation({
    accountId: 'acct_undeclared', upstream: 'undeclared-target', model: 'gpt-5.6-sol', probeIndex,
    vector: featureVector(0.12 + probeIndex * 0.004), createdAt: new Date(Date.UTC(2026, 6, 12, 3, 0, probeIndex)).toISOString(),
    observedModel: 'gpt-5.6-luna', mappingStatus: 'undeclared_mismatch'
  }))
}

assert.equal(await repository.createModelCheckObservationsAsync(observations), observations.length)
while (await repository.aggregateModelTrustObservationsAsync(47)) {
  // 验证多批游标增量，不允许一次性加载全量 observation。
}

const sameSource = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_same_source', 'gpt-5.6-sol')
assert(sameSource)
assert.equal(sameSource.identityStatus, 'suspected_same_source')
assert(sameSource.reasonCodes.includes('paired_models_collapsed'))
assert.equal(sameSource.independentSourceCount, 6, '基线来源数必须按六个独立上游桶计数，不按 observation 数量放大')
assert.equal(sameSource.pairedProbeCount, 2)
assert.equal(sameSource.baselineVersion, 1)

const downgrade = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_downgrade', 'gpt-5.5')
assert(downgrade)
assert.equal(downgrade.identityStatus, 'suspected_downgrade')
assert(downgrade.reasonCodes.includes('closer_to_lower_model_baseline'))

const undeclared = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_undeclared', 'gpt-5.6-sol')
assert(undeclared)
assert.equal(undeclared.mappingStatus, 'undeclared_mismatch')
assert(undeclared.reasonCodes.includes('undeclared_response_model_mismatch'))

const robust = statistics.robustVectorSummary([
  featureVector(0.1), featureVector(0.11), featureVector(0.12), featureVector(0.95)
])
assert.equal(robust.excludedCount, 1, '极端上游样本必须退出稳健基线')
assert(Math.abs(Number(robust.median[0]) - 0.11) < 0.02)

const shifted: import('../../storage/model-trust.repository.js').ModelCheckObservationInput[] = []
for (let sourceIndex = 0; sourceIndex < 5; sourceIndex += 1) {
  for (let probeIndex = 0; probeIndex < 3; probeIndex += 1) {
    shifted.push(identityObservation({
      accountId: `acct_source_${sourceIndex}`, upstream: `source-${sourceIndex}`, model: 'gpt-5.6-sol', probeIndex,
      vector: featureVector(0.68 + sourceIndex * 0.012 + probeIndex * 0.004),
      createdAt: new Date(Date.UTC(2026, 6, 15, 0, sourceIndex, probeIndex)).toISOString()
    }))
  }
}
await repository.createModelCheckObservationsAsync(shifted)
while (await repository.aggregateModelTrustObservationsAsync(11)) {
}
const driftProtected = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_source_0', 'gpt-5.6-sol')
assert(driftProtected)
assert.equal(driftProtected.baselineVersionStatus, 'drift_protected')
assert.equal(driftProtected.identityStatus, 'insufficient_evidence', '群体共同漂移期间不得输出强身份异常结论')
assert(driftProtected.reasonCodes.includes('population_drift_protected'))
const protectedExistingTarget = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_same_source', 'gpt-5.6-sol')
assert.equal(protectedExistingTarget?.baselineVersionStatus, 'drift_protected', '群体漂移必须刷新未出现在当前批次中的同 population latest')
assert.equal(protectedExistingTarget?.identityStatus, 'insufficient_evidence')

const stored = database.getDatasetDatabase().prepare("SELECT * FROM model_check_observations WHERE probe_family LIKE 'identity_%' LIMIT 1").get() as Record<string, unknown>
assert(!('prompt' in stored) && !('response_body' in stored) && !('output_text' in stored), '身份 observation 不得持久化题面或回答正文')
assert.equal(typeof stored.feature_1, 'number')
const baselineRows = database.getStatsDatabase().prepare("SELECT baseline_version, version_status FROM model_identity_baseline_versions WHERE requested_model = 'gpt-5.6-sol' ORDER BY baseline_version").all() as Array<Record<string, unknown>>
assert.deepEqual(baselineRows.map((row) => row.version_status), ['active', 'drift_protected'])
const { buildPostgresSchemaSql } = await import('../../storage/postgres-schema.js')
const postgresSql = buildPostgresSchemaSql()
assert(postgresSql.includes('CREATE TABLE IF NOT EXISTS model_identity_source_features'))
assert(postgresSql.includes('CREATE TABLE IF NOT EXISTS model_identity_baseline_versions'))
assert(postgresSql.includes('CREATE TABLE IF NOT EXISTS model_paired_similarity_windows'))

console.log('模型身份稳健基线回归通过：paired 同源、降级、LOO 限权、异常退出与群体漂移保护符合预期')

function identityObservation(input: {
  accountId: string
  upstream: string
  model: string
  probeIndex: number
  vector: number[]
  createdAt: string
  observedModel?: string
  mappingStatus?: string
}): import('../../storage/model-trust.repository.js').ModelCheckObservationInput {
  const cohort = security.modelCheckObservationHmac(`cohort:${input.model}`, 'cohort')
  return {
    runId: run.id,
    systemAccountId: 'sys_identity',
    accountId: input.accountId,
    providerCode: 'gpt',
    providerProtocolProfileId: 'gpt-openai-v1',
    endpointFamily: 'responses',
    requestedModel: input.model,
    mappedUpstreamModel: input.model,
    observedModel: input.observedModel ?? input.model,
    mappingApplied: false,
    upstreamBucketHmac: security.modelCheckObservationHmac(input.upstream, 'upstream'),
    cohortKeyHmac: cohort,
    populationKeyHmac,
    probeKeyHmac: security.modelCheckObservationHmac(`probe-${input.probeIndex}`, 'probe'),
    probeFamily: 'identity_generated_canary',
    probeSetVersion: 'identity-mock-v1',
    tokenizerVersion: 'js-tiktoken@1.0.21:o200k_base',
    featureVersion: 'identity-features-v1',
    roundIndex: 0,
    paddingTokens: 0,
    localInputTokens: 32,
    reportedInputTokens: 32,
    constraintPassed: true,
    featureVector: input.vector,
    observationStatus: 'observed',
    identityStatus: 'insufficient_evidence',
    mappingStatus: input.mappingStatus ?? 'direct',
    protocolStatus: 'consistent',
    evidenceCoverage: 0,
    createdAt: input.createdAt
  }
}

function featureVector(base: number): number[] {
  return Array.from({ length: 8 }, (_, index) => Math.min(0.99, base + index * 0.003))
}
