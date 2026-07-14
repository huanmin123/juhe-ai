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
assert.equal(sameSource.independentSourceCount, 5, '未声明模型冲突不得作为第六个独立上游桶污染基线')
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
const undeclaredFeatureCount = database.getStatsDatabase().prepare(`
  SELECT COUNT(*) AS count FROM model_identity_source_features WHERE account_id = ?
`).get('acct_undeclared') as { count: number }
assert.equal(undeclaredFeatureCount.count, 0, '未声明模型冲突 observation 只保留诊断事实，不得进入身份来源或群体基线')

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

const invalidIdentityObservations: import('../../storage/model-trust.repository.js').ModelCheckObservationInput[] = []
for (let sourceIndex = 0; sourceIndex < 10; sourceIndex += 1) {
  for (let probeIndex = 0; probeIndex < 3; probeIndex += 1) {
    const requestFailed = probeIndex % 2 === 0
    invalidIdentityObservations.push(identityObservation({
      accountId: 'acct_failed_identity',
      upstream: `failed-identity-source-${sourceIndex}`,
      model: 'gpt-5.6-sol',
      probeIndex,
      vector: featureVector(0.2 + sourceIndex * 0.01),
      createdAt: new Date(Date.UTC(2026, 6, 20 + (sourceIndex % 3), 0, sourceIndex, probeIndex)).toISOString(),
      observationStatus: requestFailed ? 'request_failed' : 'model_missing',
      observedModel: requestFailed ? 'gpt-5.6-sol' : null
    }))
  }
}
await repository.createModelCheckObservationsAsync(invalidIdentityObservations)
while (await repository.aggregateModelTrustObservationsAsync(13)) {
}
assert.equal(
  await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_failed_identity', 'gpt-5.6-sol'),
  undefined,
  '失败或无 response model 的身份 observation 不能生成 latest 结论'
)
const failedFeatureCount = database.getStatsDatabase().prepare(`
  SELECT COUNT(*) AS count FROM model_identity_source_features WHERE account_id = ?
`).get('acct_failed_identity') as { count: number }
assert.equal(failedFeatureCount.count, 0, '无效身份 observation 不能进入来源特征或群体基线')

const protocolConflict = identityObservation({
  accountId: 'acct_protocol_conflict', upstream: 'protocol-conflict-source', model: 'gpt-5.6-sol', probeIndex: 0,
  vector: featureVector(0.2), createdAt: new Date(Date.UTC(2026, 6, 24)).toISOString(), protocolStatus: 'failed'
})
await repository.createModelCheckObservationsAsync([protocolConflict])
while (await repository.aggregateModelTrustObservationsAsync(13)) {
}
const protocolConflictLatest = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_protocol_conflict', 'gpt-5.6-sol')
assert.equal(protocolConflictLatest?.protocolStatus, 'failed', '协议硬冲突必须保留 latest 诊断事实')
assert(protocolConflictLatest?.reasonCodes.includes('protocol_check_failed'))
assert.equal(protocolConflictLatest?.identityObservationCount, 0, '协议硬冲突不得计入身份 observation')
assert.equal(
  (database.getStatsDatabase().prepare('SELECT COUNT(*) AS count FROM model_identity_source_features WHERE account_id = ?').get('acct_protocol_conflict') as { count: number }).count,
  0,
  '协议硬冲突不得进入身份来源或群体基线'
)
assert.equal(
  (database.getDatasetDatabase().prepare('SELECT COUNT(*) AS count FROM model_check_observations WHERE account_id = ?').get('acct_protocol_conflict') as { count: number }).count,
  1,
  '协议硬冲突 observation 本身必须保留'
)

const meanPopulationKeyHmac = security.modelCheckObservationHmac('identity-cumulative-mean-population', 'population')
const cumulativeMeanObservations = [
  identityObservation({ accountId: 'acct_cumulative_mean', upstream: 'cumulative-mean-source', model: 'gpt-5.6-sol', probeIndex: 0, vector: [1, ...Array(7).fill(0.1)], constraintPassed: true, populationKeyHmac: meanPopulationKeyHmac, createdAt: new Date(Date.UTC(2026, 6, 25, 0, 0, 0)).toISOString() }),
  identityObservation({ accountId: 'acct_cumulative_mean', upstream: 'cumulative-mean-source', model: 'gpt-5.6-terra', probeIndex: 0, vector: [0, ...Array(7).fill(0.9)], constraintPassed: false, populationKeyHmac: meanPopulationKeyHmac, createdAt: new Date(Date.UTC(2026, 6, 25, 0, 0, 1)).toISOString() }),
  identityObservation({ accountId: 'acct_cumulative_mean', upstream: 'cumulative-mean-source', model: 'gpt-5.6-sol', probeIndex: 0, vector: [0, ...Array(7).fill(0.9)], constraintPassed: false, populationKeyHmac: meanPopulationKeyHmac, createdAt: new Date(Date.UTC(2026, 6, 25, 0, 0, 2)).toISOString() }),
  identityObservation({ accountId: 'acct_cumulative_mean', upstream: 'cumulative-mean-source', model: 'gpt-5.6-terra', probeIndex: 0, vector: [1, ...Array(7).fill(0.1)], constraintPassed: true, populationKeyHmac: meanPopulationKeyHmac, createdAt: new Date(Date.UTC(2026, 6, 25, 0, 0, 3)).toISOString() })
]
await repository.createModelCheckObservationsAsync(cumulativeMeanObservations)
while (await repository.aggregateModelTrustObservationsAsync(13)) {
}
const cumulativeMean = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_cumulative_mean', 'gpt-5.6-sol')
assert(cumulativeMean)
assert(Math.abs(Number(cumulativeMean.pairedDistance)) < 0.000001, 'paired 来源向量必须使用累计均值，不能由两个不同的 latest_feature 制造距离')
const cumulativeSource = database.getStatsDatabase().prepare(`
  SELECT sample_count, constraint_pass_count, sum_feature_2, latest_feature_2
  FROM model_identity_source_features WHERE account_id = ? AND requested_model = ?
`).get('acct_cumulative_mean', 'gpt-5.6-sol') as Record<string, number>
assert.equal(cumulativeSource.sample_count, 2)
assert.equal(cumulativeSource.constraint_pass_count / cumulativeSource.sample_count, 0.5, '约束维度必须按累计通过率计算')
assert.equal(cumulativeSource.sum_feature_2 / cumulativeSource.sample_count, 0.5)
assert.equal(cumulativeSource.latest_feature_2, 0.9, '回归样本必须确保 latest 与累计均值不同')

const obsoletePopulationKeyHmac = security.modelCheckObservationHmac('identity-obsolete-population', 'population')
const currentPopulationKeyHmac = security.modelCheckObservationHmac('identity-current-population', 'population')
const isolationAccountId = 'acct_population_feature_isolation'
const isolationObservations: import('../../storage/model-trust.repository.js').ModelCheckObservationInput[] = [
  identityObservation({
    accountId: isolationAccountId,
    upstream: 'isolation-reference-0',
    model: 'gpt-5.6-sol',
    probeIndex: 0,
    vector: featureVector(0.95),
    populationKeyHmac: obsoletePopulationKeyHmac,
    featureVersion: 'identity-features-v2',
    createdAt: new Date(Date.UTC(2026, 6, 25, 1, 0, 0)).toISOString()
  }),
  identityObservation({
    accountId: isolationAccountId,
    upstream: 'isolation-reference-1',
    model: 'gpt-5.6-sol',
    probeIndex: 0,
    vector: featureVector(0.95),
    populationKeyHmac: currentPopulationKeyHmac,
    featureVersion: 'identity-features-v1',
    createdAt: new Date(Date.UTC(2026, 6, 25, 1, 0, 1)).toISOString()
  })
]
for (let sourceIndex = 0; sourceIndex < 5; sourceIndex += 1) {
  isolationObservations.push(identityObservation({
    accountId: `acct_isolation_reference_${sourceIndex}`,
    upstream: `isolation-reference-${sourceIndex}`,
    model: 'gpt-5.6-sol',
    probeIndex: 0,
    vector: featureVector(0.2 + sourceIndex * 0.002),
    populationKeyHmac: currentPopulationKeyHmac,
    featureVersion: 'identity-features-v2',
    createdAt: new Date(Date.UTC(2026, 6, 25, 2, 0, sourceIndex)).toISOString()
  }))
}
isolationObservations.push(identityObservation({
  accountId: isolationAccountId,
  upstream: 'isolation-current-target',
  model: 'gpt-5.6-sol',
  probeIndex: 0,
  vector: featureVector(0.204),
  populationKeyHmac: currentPopulationKeyHmac,
  featureVersion: 'identity-features-v2',
  createdAt: new Date(Date.UTC(2026, 6, 25, 2, 1, 0)).toISOString()
}))
await aggregateIdentityBatch(isolationObservations)
const isolatedSources = database.getStatsDatabase().prepare(`
  SELECT population_key_hmac, feature_version
  FROM model_identity_source_features
  WHERE system_account_id = ? AND account_id = ? AND requested_model = ?
`).all('sys_identity', isolationAccountId, 'gpt-5.6-sol') as Array<{ population_key_hmac: string; feature_version: string }>
assert.equal(isolatedSources.length, 3, '回归样本必须同时保留旧 population、旧 feature 和当前来源事实')
assert(isolatedSources.some((row) => row.population_key_hmac === obsoletePopulationKeyHmac && row.feature_version === 'identity-features-v2'))
assert(isolatedSources.some((row) => row.population_key_hmac === currentPopulationKeyHmac && row.feature_version === 'identity-features-v1'))
assert(isolatedSources.some((row) => row.population_key_hmac === currentPopulationKeyHmac && row.feature_version === 'identity-features-v2'))
const isolatedTrust = await repository.findModelAccountTrustResultAsync('sys_identity', isolationAccountId, 'gpt-5.6-sol')
assert(isolatedTrust)
assert.equal(isolatedTrust.featureVersion, 'identity-features-v2', 'latest 必须使用当前 feature version')
assert.equal(isolatedTrust.identityObservationCount, 1, '当前身份观察数不得累计旧 population 或旧 feature 样本')
assert.equal(isolatedTrust.independentSourceCount, 5, '旧 population / feature 的来源桶不得污染当前 LOO 排除集合')
assert(Math.abs(Number(isolatedTrust.identityDistance)) < 0.000001, '旧 v1 target 向量不得污染当前 v2 身份距离')

const peerRefreshPopulationKeyHmac = security.modelCheckObservationHmac('identity-peer-refresh-population', 'population')
const peerRefreshInitialBases = [0.20, 0.21, 0.22, 0.23, 0.24]
await aggregateIdentityBatch(peerRefreshInitialBases.map((base, sourceIndex) => identityObservation({
  accountId: `acct_peer_refresh_${sourceIndex}`,
  upstream: `peer-refresh-source-${sourceIndex}`,
  model: 'gpt-5.6-sol',
  probeIndex: 0,
  vector: featureVector(base),
  populationKeyHmac: peerRefreshPopulationKeyHmac,
  createdAt: new Date(Date.UTC(2026, 6, 25, 3, 0, sourceIndex)).toISOString()
})))
const peerRefreshBefore = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_peer_refresh_1', 'gpt-5.6-sol')
assert(peerRefreshBefore?.identityDistance !== undefined)
await aggregateIdentityBatch([
  identityObservation({
    accountId: 'acct_peer_refresh_0', upstream: 'peer-refresh-source-0', model: 'gpt-5.6-sol', probeIndex: 0,
    vector: featureVector(0.23), populationKeyHmac: peerRefreshPopulationKeyHmac,
    createdAt: new Date(Date.UTC(2026, 6, 25, 4, 0, 0)).toISOString()
  }),
  identityObservation({
    accountId: 'acct_peer_refresh_2', upstream: 'peer-refresh-source-2', model: 'gpt-5.6-sol', probeIndex: 0,
    vector: featureVector(0.25), populationKeyHmac: peerRefreshPopulationKeyHmac,
    createdAt: new Date(Date.UTC(2026, 6, 25, 4, 0, 1)).toISOString()
  }),
  identityObservation({
    accountId: 'acct_peer_refresh_3', upstream: 'peer-refresh-source-3', model: 'gpt-5.6-sol', probeIndex: 0,
    vector: featureVector(0.26), populationKeyHmac: peerRefreshPopulationKeyHmac,
    createdAt: new Date(Date.UTC(2026, 6, 25, 4, 0, 2)).toISOString()
  })
])
const peerRefreshAfter = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_peer_refresh_1', 'gpt-5.6-sol')
assert(peerRefreshAfter?.identityDistance !== undefined)
assert(
  Number(peerRefreshAfter.identityDistance) > Number(peerRefreshBefore.identityDistance) + 1,
  '同 scope 其他来源累计向量变化时，即使 baseline 版本和来源数未变，也必须刷新未参与本批的 peer LOO latest'
)
assert(Math.abs(Number(peerRefreshAfter.identityDistance) - 2.75) < 0.000001)

const tiedPopulationA = security.modelCheckObservationHmac('identity-tied-population-a', 'population')
const tiedPopulationB = security.modelCheckObservationHmac('identity-tied-population-b', 'population')
const tiedAccountId = 'acct_tied_identity_scope'
const tiedObservedAt = new Date(Date.UTC(2026, 6, 25, 5, 0, 0)).toISOString()
await aggregateIdentityBatch([
  identityObservation({
    accountId: tiedAccountId, upstream: 'tied-scope-a', model: 'gpt-5.6-sol', probeIndex: 0,
    vector: featureVector(0.2), populationKeyHmac: tiedPopulationA, featureVersion: 'identity-features-tied-a',
    createdAt: tiedObservedAt
  }),
  identityObservation({
    accountId: tiedAccountId, upstream: 'tied-scope-b', model: 'gpt-5.6-sol', probeIndex: 0,
    vector: featureVector(0.8), populationKeyHmac: tiedPopulationB, featureVersion: 'identity-features-tied-b',
    createdAt: tiedObservedAt
  })
])
const expectedTiedFeature = tiedPopulationA > tiedPopulationB
  ? 'identity-features-tied-a'
  : 'identity-features-tied-b'
const tiedTrustBefore = await repository.findModelAccountTrustResultAsync('sys_identity', tiedAccountId, 'gpt-5.6-sol')
assert.equal(tiedTrustBefore?.featureVersion, expectedTiedFeature, '同毫秒 scope 必须按稳定的 scope 级排序选择 current')
await aggregateIdentityBatch([
  identityObservation({
    accountId: 'acct_tied_scope_refresh', upstream: 'tied-scope-refresh', model: 'gpt-5.6-sol', probeIndex: 0,
    vector: featureVector(0.3), populationKeyHmac: tiedPopulationA, featureVersion: 'identity-features-tied-a',
    createdAt: new Date(Date.UTC(2026, 6, 25, 6, 0, 0)).toISOString()
  })
])
const tiedTrustAfter = await repository.findModelAccountTrustResultAsync('sys_identity', tiedAccountId, 'gpt-5.6-sol')
assert.equal(tiedTrustAfter?.featureVersion, expectedTiedFeature, 'peer scope 刷新触发重复 evaluate 后 current 选择不得翻转')
const tiedPairedWindows = database.getStatsDatabase().prepare(`
  SELECT population_key_hmac, feature_version FROM model_paired_similarity_windows
  WHERE system_account_id = ? AND account_id = ?
`).all('sys_identity', tiedAccountId) as Array<{ population_key_hmac: string; feature_version: string }>
assert.equal(tiedPairedWindows.length, 1, '重复 evaluate 只能写一个 current paired window')
assert.equal(
  tiedPairedWindows[0]?.population_key_hmac,
  expectedTiedFeature === 'identity-features-tied-a' ? tiedPopulationA : tiedPopulationB
)
assert.equal(tiedPairedWindows[0]?.feature_version, expectedTiedFeature)

const recoveryObservations: import('../../storage/model-trust.repository.js').ModelCheckObservationInput[] = []
for (let repeat = 0; repeat < 40; repeat += 1) {
  for (let sourceIndex = 0; sourceIndex < 5; sourceIndex += 1) {
    for (let probeIndex = 0; probeIndex < 3; probeIndex += 1) {
      recoveryObservations.push(identityObservation({
        accountId: `acct_source_${sourceIndex}`, upstream: `source-${sourceIndex}`, model: 'gpt-5.6-sol', probeIndex,
        vector: featureVector(Number(modelBase.get('gpt-5.6-sol')) + sourceIndex * 0.012 + probeIndex * 0.004),
        createdAt: new Date(Date.UTC(2026, 6, 26 + Math.floor(repeat / 10), repeat % 10, sourceIndex, probeIndex)).toISOString()
      }))
    }
  }
}
await repository.createModelCheckObservationsAsync(recoveryObservations)
while (await repository.aggregateModelTrustObservationsAsync(5000)) {
}
const recovered = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_source_0', 'gpt-5.6-sol')
assert.equal(recovered?.baselineVersionStatus, 'active', '群体恢复后必须重新使用 active 基线，不能永久优先旧候选')
assert(!recovered?.reasonCodes.includes('population_drift_protected'))

const rejectedPopulationKeyHmac = security.modelCheckObservationHmac('identity-rejected-candidate-population', 'population')
await aggregateIdentityBatch([
  ...populationObservationBatch({ label: 'rejected', populationKeyHmac: rejectedPopulationKeyHmac, base: 0.1, observedAt: new Date(Date.UTC(2026, 6, 31)).toISOString() }),
  ...populationObservationBatch({ label: 'rejected', populationKeyHmac: rejectedPopulationKeyHmac, base: 0.1, observedAt: new Date(Date.UTC(2026, 7, 2)).toISOString() })
])
await aggregateIdentityBatch(populationObservationBatch({
  label: 'rejected', populationKeyHmac: rejectedPopulationKeyHmac, base: 0.9,
  observedAt: new Date(Date.UTC(2026, 7, 3)).toISOString()
}))
const rejectedProtected = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_rejected_0', 'gpt-5.6-sol')
assert.equal(rejectedProtected?.baselineVersionStatus, 'drift_protected')
await aggregateIdentityBatch(populationObservationBatch({
  label: 'rejected', populationKeyHmac: rejectedPopulationKeyHmac, base: 0.99,
  observedAt: new Date(Date.UTC(2026, 7, 5)).toISOString()
}))
const rejectedRows = baselineStatuses(rejectedPopulationKeyHmac)
assert.deepEqual(rejectedRows, ['active', 'rejected'], '候选分布在固定验证窗内继续失稳时必须拒绝，不能永久保持 drift_protected')
assert.equal(
  (await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_rejected_0', 'gpt-5.6-sol'))?.baselineVersionStatus,
  'active',
  '拒绝候选后 latest 必须回到 active 基线'
)

const expiredPopulationKeyHmac = security.modelCheckObservationHmac('identity-expired-candidate-population', 'population')
await aggregateIdentityBatch(populationObservationBatch({
  label: 'expired', populationKeyHmac: expiredPopulationKeyHmac, base: 0.1, probeCount: 1,
  observedAt: new Date(Date.UTC(2026, 7, 10)).toISOString()
}))
await aggregateIdentityBatch(populationObservationBatch({
  label: 'expired', populationKeyHmac: expiredPopulationKeyHmac, base: 0.9, probeCount: 1,
  observedAt: new Date(Date.UTC(2026, 7, 11)).toISOString()
}))
const expiredProtected = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_expired_0', 'gpt-5.6-sol')
assert.equal(expiredProtected?.baselineVersionStatus, 'drift_protected')
await aggregateIdentityBatch(populationObservationBatch({
  label: 'expired', populationKeyHmac: expiredPopulationKeyHmac, base: 0.9, probeCount: 1,
  observedAt: new Date(Date.UTC(2026, 7, 18)).toISOString()
}))
assert.deepEqual(baselineStatuses(expiredPopulationKeyHmac), ['active', 'expired'], '证据不足的候选超过固定生命周期后必须过期')
assert.equal(
  (await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_expired_0', 'gpt-5.6-sol'))?.baselineVersionStatus,
  'active',
  '候选过期后 latest 必须回到 active 基线'
)

const promotedPopulationKeyHmac = security.modelCheckObservationHmac('identity-promoted-candidate-population', 'population')
await aggregateIdentityBatch([
  ...populationObservationBatch({ label: 'promoted', populationKeyHmac: promotedPopulationKeyHmac, base: 0.1, observedAt: new Date(Date.UTC(2026, 7, 20)).toISOString() }),
  ...populationObservationBatch({ label: 'promoted', populationKeyHmac: promotedPopulationKeyHmac, base: 0.1, observedAt: new Date(Date.UTC(2026, 7, 22)).toISOString() })
])
await aggregateIdentityBatch(populationObservationBatch({
  label: 'promoted', populationKeyHmac: promotedPopulationKeyHmac, base: 0.9,
  observedAt: new Date(Date.UTC(2026, 7, 23)).toISOString()
}))
assert.equal(
  (await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_promoted_0', 'gpt-5.6-sol'))?.baselineVersionStatus,
  'drift_protected'
)
await aggregateIdentityBatch(populationObservationBatch({
  label: 'promoted', populationKeyHmac: promotedPopulationKeyHmac, base: (0.1 + 0.1 + 0.9) / 3,
  observedAt: new Date(Date.UTC(2026, 7, 25)).toISOString()
}))
assert.deepEqual(baselineStatuses(promotedPopulationKeyHmac), ['retired', 'active'], '候选稳定满 3 天且证据充分时必须晋升为 active')
const promoted = await repository.findModelAccountTrustResultAsync('sys_identity', 'acct_promoted_0', 'gpt-5.6-sol')
assert.equal(promoted?.baselineVersion, 2)
assert.equal(promoted?.baselineVersionStatus, 'active')

const stored = database.getDatasetDatabase().prepare("SELECT * FROM model_check_observations WHERE probe_family LIKE 'identity_%' LIMIT 1").get() as Record<string, unknown>
assert(!('prompt' in stored) && !('response_body' in stored) && !('output_text' in stored), '身份 observation 不得持久化题面或回答正文')
assert.equal(typeof stored.feature_1, 'number')
const baselineRows = database.getStatsDatabase().prepare("SELECT baseline_version, version_status FROM model_identity_baseline_versions WHERE population_key_hmac = ? AND requested_model = 'gpt-5.6-sol' ORDER BY baseline_version").all(populationKeyHmac) as Array<Record<string, unknown>>
assert.deepEqual(baselineRows.map((row) => row.version_status), ['active', 'recovered'])
const { buildPostgresSchemaSql } = await import('../../storage/postgres-schema.js')
const postgresSql = buildPostgresSchemaSql()
assert(postgresSql.includes('CREATE TABLE IF NOT EXISTS model_identity_source_features'))
assert(postgresSql.includes('CREATE TABLE IF NOT EXISTS model_identity_baseline_versions'))
assert(postgresSql.includes('CREATE TABLE IF NOT EXISTS model_paired_similarity_windows'))

console.log('模型身份稳健基线回归通过：累计均值、population/feature 隔离、硬冲突门禁、LOO 限权与群体漂移恢复/拒绝/过期符合预期')

async function aggregateIdentityBatch(inputs: import('../../storage/model-trust.repository.js').ModelCheckObservationInput[]): Promise<void> {
  await repository.createModelCheckObservationsAsync(inputs)
  while (await repository.aggregateModelTrustObservationsAsync(5000)) {
  }
}

function populationObservationBatch(input: {
  label: string
  populationKeyHmac: string
  base: number
  observedAt: string
  probeCount?: number
}): import('../../storage/model-trust.repository.js').ModelCheckObservationInput[] {
  const observations: import('../../storage/model-trust.repository.js').ModelCheckObservationInput[] = []
  for (let sourceIndex = 0; sourceIndex < 5; sourceIndex += 1) {
    for (let probeIndex = 0; probeIndex < (input.probeCount ?? 3); probeIndex += 1) {
      observations.push(identityObservation({
        accountId: `acct_${input.label}_${sourceIndex}`,
        upstream: `${input.label}-source-${sourceIndex}`,
        model: 'gpt-5.6-sol',
        probeIndex,
        vector: featureVector(Math.min(0.99, input.base + sourceIndex * 0.001 + probeIndex * 0.0001)),
        populationKeyHmac: input.populationKeyHmac,
        createdAt: new Date(Date.parse(input.observedAt) + sourceIndex * 1_000 + probeIndex).toISOString()
      }))
    }
  }
  return observations
}

function baselineStatuses(population: string): string[] {
  const rows = database.getStatsDatabase().prepare(`
    SELECT version_status FROM model_identity_baseline_versions
    WHERE population_key_hmac = ? AND requested_model = 'gpt-5.6-sol'
    ORDER BY baseline_version
  `).all(population) as Array<{ version_status: string }>
  return rows.map((row) => row.version_status)
}

function identityObservation(input: {
  accountId: string
  upstream: string
  model: string
  probeIndex: number
  vector: number[]
  createdAt: string
  observedModel?: string | null
  mappingStatus?: string
  protocolStatus?: string
  observationStatus?: string
  constraintPassed?: boolean
  populationKeyHmac?: string
  featureVersion?: string
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
    observedModel: input.observedModel === null ? undefined : input.observedModel ?? input.model,
    mappingApplied: false,
    upstreamBucketHmac: security.modelCheckObservationHmac(input.upstream, 'upstream'),
    cohortKeyHmac: cohort,
    populationKeyHmac: input.populationKeyHmac ?? populationKeyHmac,
    probeKeyHmac: security.modelCheckObservationHmac(`probe-${input.probeIndex}`, 'probe'),
    probeFamily: 'identity_generated_canary',
    probeSetVersion: 'identity-mock-v1',
    tokenizerVersion: 'js-tiktoken@1.0.21:o200k_base',
    featureVersion: input.featureVersion ?? 'identity-features-v1',
    roundIndex: 0,
    paddingTokens: 0,
    localInputTokens: 32,
    reportedInputTokens: 32,
    constraintPassed: input.constraintPassed ?? true,
    featureVector: input.vector,
    observationStatus: input.observationStatus ?? 'observed',
    identityStatus: 'insufficient_evidence',
    mappingStatus: input.mappingStatus ?? 'direct',
    protocolStatus: input.protocolStatus ?? 'consistent',
    evidenceCoverage: 0,
    createdAt: input.createdAt
  }
}

function featureVector(base: number): number[] {
  return Array.from({ length: 8 }, (_, index) => Math.min(0.99, base + index * 0.003))
}
