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
let cohortScanCount = 0
const countingClient: import('../../storage/database-client.js').DatabaseClient = {
  ...client,
  async query<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T[]> {
    if (sql.includes('model_token_integrity_windows') && sql.includes('INNER JOIN') && sql.includes('model_trust_window_sources')) {
      cohortScanCount += 1
    }
    return await client.query<T>(sql, params)
  }
}
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
    ) VALUES (?, ?, ?, ?, ?, ?, 30, 30, 10, 1, 1, 1, 1, 1, 0, 1, ?, 'consistent', ?, ?, ?)
  `).run('sys_intercept', accountId, scope.requestedModel, scope.cohortKeyHmac, scope.tokenizerVersion, scope.probeSetVersion,
    intercept, firstObservedAt, lastObservedAt, lastObservedAt)
  database.prepare(`
    INSERT INTO model_trust_window_sources (
      system_account_id, account_id, cohort_key_hmac, mapped_upstream_model, upstream_bucket_hmac,
      first_observed_at, last_observed_at, observation_count, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 30, ?)
  `).run('sys_intercept', accountId, scope.cohortKeyHmac, 'gpt-5.6-luna',
    `hmac-sha256-v1:${String(index).padStart(64, '0')}`, firstObservedAt, lastObservedAt, lastObservedAt)
}
database.prepare(`
  UPDATE model_trust_window_sources SET observation_count = 60
  WHERE account_id = 'acct_intercept_0'
`).run()
database.prepare(`
  INSERT INTO model_account_trust_results (
    system_account_id, account_id, requested_model, reason_codes_json, updated_at
  ) VALUES (?, ?, ?, '[]', ?)
`).run('sys_intercept', 'acct_intercept_9', scope.requestedModel, lastObservedAt)
database.prepare(`
  INSERT INTO model_token_integrity_windows (
    system_account_id, account_id, requested_model, cohort_key_hmac, tokenizer_version, probe_set_version,
    observation_count, valid_sample_count, round_count, sum_local, sum_reported, sum_local_squared,
    sum_local_reported, sum_reported_squared, bucket_aligned_count, slope, intercept,
    usage_integrity_status, first_observed_at, last_observed_at, updated_at
  ) VALUES (?, ?, ?, ?, ?, ?, 1, 1, 0, 1, 1, 1, 1, 1, 0, NULL, NULL, 'unsupported', ?, ?, ?)
`).run('sys_intercept', 'acct_intercept_empty', scope.requestedModel, scope.cohortKeyHmac,
  scope.tokenizerVersion, scope.probeSetVersion, lastObservedAt, lastObservedAt, lastObservedAt)
database.prepare(`
  INSERT INTO model_account_trust_results (
    system_account_id, account_id, requested_model, intercept_baseline_status,
    reason_codes_json, updated_at
  ) VALUES (?, ?, ?, 'calibration_pending', '["fixed_intercept_calibration_pending"]', ?)
`).run('sys_intercept', 'acct_intercept_empty', scope.requestedModel, lastObservedAt)

const contexts = await baseline.refreshTokenInterceptBaselines(countingClient, [scope])
assert.equal(cohortScanCount, 1, '同一 cohort 每批只能加载一次固定截距来源快照')
const context = contexts.get(baseline.tokenInterceptScopeKey(scope))
assert(context)
const pending = database.prepare(`
  SELECT * FROM model_token_intercept_baseline_versions WHERE version_status = 'calibration_pending'
`).get() as Record<string, unknown>
assert.equal(pending.evidence_status, 'stable')
assert.equal(pending.independent_source_count, 10)
assert.equal(pending.retained_source_count, 10, 'mapped source 可累计多个 requested model，但当前 requested window 的样本资格必须独立计算')
assert.equal(pending.strong_gate_enabled, 0, '真实样本校准前强判门必须默认关闭')

const beforeActivation = baseline.evaluateTokenInterceptBaseline(context, {
  systemAccountId: 'sys_intercept', accountId: 'acct_intercept_9', requestedModel: scope.requestedModel
}, 40)
assert.equal(beforeActivation.baselineStatus, 'calibration_pending')
assert.equal(beforeActivation.strongGateEnabled, false)
assert.equal(beforeActivation.suspectedFixedPadding, false, '待校准基线不能强判固定灌水')
assert((beforeActivation.looMedian ?? 0) < 20, 'LOO 必须排除候选上游桶的异常截距')
assert(context.bucketsByAccount.size === 10, 'mapped upstream model 与 requested model 不同也必须按 cohort 找到来源桶')
for (let index = 0; index < 100; index += 1) {
  baseline.evaluateTokenInterceptBaseline(context, {
    systemAccountId: 'sys_intercept', accountId: `acct_intercept_${index % 10}`, requestedModel: scope.requestedModel
  }, 12)
}
assert.equal(cohortScanCount, 1, '批量账号 LOO 评估必须复用 cohort 快照，不能逐账号重扫窗口')

await baseline.activateTokenInterceptBaselineVersion(client, {
  ...scope,
  baselineVersion: Number(pending.baseline_version),
  strongThresholdIntercept: 30,
  calibrationNote: 'isolated regression calibration record'
})
const activeContexts = await baseline.refreshTokenInterceptBaselines(client, [scope])
const activeContext = activeContexts.get(baseline.tokenInterceptScopeKey(scope))
assert(activeContext)
const afterActivation = baseline.evaluateTokenInterceptBaseline(activeContext, {
  systemAccountId: 'sys_intercept', accountId: 'acct_intercept_9', requestedModel: scope.requestedModel
}, 40)
assert.equal(afterActivation.baselineStatus, 'active')
assert.equal(afterActivation.strongGateEnabled, true)
assert.equal(afterActivation.suspectedFixedPadding, true)
const rematerialized = database.prepare(`
  SELECT usage_integrity_status, intercept_baseline_version, intercept_baseline_status,
    intercept_strong_gate_enabled, reason_codes_json
  FROM model_account_trust_results WHERE account_id = 'acct_intercept_9'
`).get() as Record<string, unknown>
assert.equal(rematerialized.usage_integrity_status, 'suspected_padding', '激活事务必须同步重物化 latest')
assert.equal(rematerialized.intercept_baseline_status, 'active')
assert.equal(rematerialized.intercept_strong_gate_enabled, 1)
assert(String(rematerialized.reason_codes_json).includes('fixed_intercept_padding'))
const emptyInterceptLatest = database.prepare(`
  SELECT usage_integrity_status, intercept_baseline_status, intercept_strong_gate_enabled, reason_codes_json
  FROM model_account_trust_results WHERE account_id = 'acct_intercept_empty'
`).get() as Record<string, unknown>
assert.equal(emptyInterceptLatest.usage_integrity_status, 'unsupported')
assert.equal(emptyInterceptLatest.intercept_baseline_status, 'active', '空 intercept 账号也必须清理旧基线状态')
assert.equal(emptyInterceptLatest.intercept_strong_gate_enabled, 0)
assert(!String(emptyInterceptLatest.reason_codes_json).includes('fixed_intercept_calibration_pending'))

const stitchedScope = { ...scope, cohortKeyHmac: `hmac-sha256-v1:${'b'.repeat(64)}` }
for (let index = 0; index < 10; index += 1) {
  const accountId = `acct_stitched_${index}`
  const sourceFirst = index === 0 ? firstObservedAt : lastObservedAt
  database.prepare(`
    INSERT INTO model_token_integrity_windows (
      system_account_id, account_id, requested_model, cohort_key_hmac, tokenizer_version, probe_set_version,
      observation_count, valid_sample_count, round_count, sum_local, sum_reported, sum_local_squared,
      sum_local_reported, sum_reported_squared, bucket_aligned_count, slope, intercept,
      usage_integrity_status, first_observed_at, last_observed_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, 30, 30, 10, 1, 1, 1, 1, 1, 0, 1, 10, 'consistent', ?, ?, ?)
  `).run('sys_intercept', accountId, stitchedScope.requestedModel, stitchedScope.cohortKeyHmac,
    stitchedScope.tokenizerVersion, stitchedScope.probeSetVersion, sourceFirst, lastObservedAt, lastObservedAt)
  database.prepare(`
    INSERT INTO model_trust_window_sources (
      system_account_id, account_id, cohort_key_hmac, mapped_upstream_model, upstream_bucket_hmac,
      first_observed_at, last_observed_at, observation_count, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 30, ?)
  `).run('sys_intercept', accountId, stitchedScope.cohortKeyHmac, stitchedScope.requestedModel,
    `hmac-sha256-v1:${String(index + 20).padStart(64, '0')}`, sourceFirst, lastObservedAt, lastObservedAt)
}
await baseline.refreshTokenInterceptBaselines(client, [stitchedScope])
const stitched = database.prepare(`
  SELECT evidence_status FROM model_token_intercept_baseline_versions WHERE cohort_key_hmac = ?
`).get(stitchedScope.cohortKeyHmac) as Record<string, unknown>
assert.equal(stitched.evidence_status, 'insufficient', '不同来源的首尾时间不能拼接成 stable 资格')

console.log('固定 intercept cohort 回归通过：预聚合、版本、LOO、显式校准和默认关闭强判门符合预期')
