import { strict as assert } from 'node:assert'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-token-intercept-baseline-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.databaseDriver = 'sqlite'

const [{ getStatsDatabase }, { createSqliteDatabaseClient }, baseline] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/database-client.js'),
  import('../../storage/model-trust-token-baseline.repository.js')
])

const database = getStatsDatabase()
const client = createSqliteDatabaseClient(database)
const scope = {
  cohortKeyHmac: `hmac-sha256-v1:${'a'.repeat(64)}`,
  requestedModel: 'gpt-5.6-sol',
  tokenizerVersion: 'js-tiktoken@1.0.21:o200k_base',
  probeSetVersion: 'probe-v1'
}
const firstObservedAt = '2026-07-01T00:00:00.000Z'
const lastObservedAt = '2026-07-15T00:00:00.000Z'
for (let index = 0; index < 10; index += 1) {
  const accountId = `acct_intercept_${index}`
  const intercept = index === 9 ? 40 : 10 + (index % 3)
  database.prepare(`
    INSERT INTO model_token_integrity_windows (
      system_account_id, account_id, requested_model, cohort_key_hmac, tokenizer_version, probe_set_version,
      observation_count, valid_sample_count, round_count, sum_local, sum_reported, sum_local_squared,
      sum_local_reported, sum_reported_squared, bucket_aligned_count, slope, intercept,
      usage_integrity_status, first_observed_at, last_observed_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, 9, 9, 3, 1, 1, 1, 1, 1, 0, 1, ?, 'consistent', ?, ?, ?)
  `).run('sys_intercept', accountId, scope.requestedModel, scope.cohortKeyHmac, scope.tokenizerVersion, scope.probeSetVersion,
    intercept, firstObservedAt, lastObservedAt, lastObservedAt)
  database.prepare(`
    INSERT INTO model_trust_window_sources (
      system_account_id, account_id, cohort_key_hmac, mapped_upstream_model, upstream_bucket_hmac,
      first_observed_at, last_observed_at, observation_count, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 9, ?)
  `).run('sys_intercept', accountId, scope.cohortKeyHmac, scope.requestedModel,
    `hmac-sha256-v1:${String(index).padStart(64, '0')}`, firstObservedAt, lastObservedAt, lastObservedAt)
}

await baseline.refreshTokenInterceptBaselines(client, [scope])
const pending = database.prepare(`
  SELECT * FROM model_token_intercept_baseline_versions WHERE version_status = 'calibration_pending'
`).get() as Record<string, unknown>
assert.equal(pending.evidence_status, 'stable')
assert.equal(pending.independent_source_count, 10)
assert.equal(pending.strong_gate_enabled, 0, '真实样本校准前强判门必须默认关闭')

const beforeActivation = await baseline.evaluateTokenInterceptBaseline(client, {
  systemAccountId: 'sys_intercept', accountId: 'acct_intercept_9', requestedModel: scope.requestedModel
}, scope, 40)
assert.equal(beforeActivation.baselineStatus, 'calibration_pending')
assert.equal(beforeActivation.strongGateEnabled, false)
assert.equal(beforeActivation.suspectedFixedPadding, false, '待校准基线不能强判固定灌水')
assert((beforeActivation.looMedian ?? 0) < 20, 'LOO 必须排除候选上游桶的异常截距')

await baseline.activateTokenInterceptBaselineVersion(client, {
  ...scope,
  baselineVersion: Number(pending.baseline_version),
  strongThresholdIntercept: 30,
  calibrationNote: 'isolated regression calibration record'
})
const afterActivation = await baseline.evaluateTokenInterceptBaseline(client, {
  systemAccountId: 'sys_intercept', accountId: 'acct_intercept_9', requestedModel: scope.requestedModel
}, scope, 40)
assert.equal(afterActivation.baselineStatus, 'active')
assert.equal(afterActivation.strongGateEnabled, true)
assert.equal(afterActivation.suspectedFixedPadding, true)

console.log('固定 intercept cohort 回归通过：预聚合、版本、LOO、显式校准和默认关闭强判门符合预期')
