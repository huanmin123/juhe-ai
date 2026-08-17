import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-runtime-observation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-api-key-runtime-observation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, rotation, runtimeStates] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-api-key-rotation.js'),
  import('../../storage/account-api-key-runtime-state.repository.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const group = repositories.createGroup({ name: 'Key 探测摘要回归分组', providerCode: 'gpt' }, access)
  const apiKeys = ['sk-runtime-observation-a', 'sk-runtime-observation-b']
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'Key 探测摘要回归账户',
    type: 'api_key',
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5'],
    credentials: {
      api_key: apiKeys[0],
      api_keys: apiKeys,
      api_key_strategy: 'round_robin',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'active', schedulable = 1, account_expires_at = NULL
    WHERE id = ?
  `).run(account.id)
  const gatewayAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId, { ignoreAvailability: true })
  assert(gatewayAccount, '应能读取测试网关账户')

  const entries = rotation.accountApiKeyEntries({ api_keys: apiKeys })
  assert.equal(entries.length, 2)
  const observedAt = '2026-07-20T00:30:00.000Z'
  const firstProbeAt = '2026-07-20T01:00:00.000Z'
  const secondProbeAt = '2026-07-20T01:10:00.000Z'
  const selected = entries.map((entry) => ({
    ...gatewayAccount,
    apiKey: entry.key,
    selectedApiKeyFingerprint: entry.fingerprint,
    selectedApiKeyIndex: entry.index
  }))

  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[1],
    status: 'rate_limited',
    statusCode: 429,
    errorCode: 'later_key_failure',
    errorMessage: '第二个 Key 失败',
    cooldownUntil: secondProbeAt,
    observedAt,
    traceId: 'trace-key-1'
  }).changed, true)
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'rate_limited',
    statusCode: 503,
    errorCode: 'stable_tie_winner',
    errorMessage: '第一个 Key 失败',
    cooldownUntil: firstProbeAt,
    observedAt,
    traceId: 'trace-key-0'
  }).changed, true)

  const summary = runtimeStates.loadAccountApiKeyRuntimeSummariesByAccountIds([account.id]).get(account.id)
  assert(summary, '应生成 Key 池运行态摘要')
  assert.equal(summary.lastFailureAt, observedAt, '摘要应选择最近的非空失败时间')
  assert.equal(summary.lastErrorCode, 'stable_tie_winner', '同一失败时间应按 key_index 稳定选择')
  assert.equal(summary.lastTraceId, 'trace-key-0', 'traceId 必须与选中的最近失败属于同一 observation')
  assert.equal(summary.nextProbeAt, firstProbeAt, '下次检查应选择不可用 Key 中最早的非空计划')

  const details = runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)
  assert.equal(details?.[0]?.lastTraceId, 'trace-key-0', 'Key 明细应返回最近失败 traceId')

  const staleCreatedAt = '2026-07-19T00:00:00.000Z'
  const insertStaleFingerprint = databaseModule.getBusinessDatabase().prepare(`
    INSERT INTO account_api_key_runtime_states (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds,
      created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, 'temporary_unavailable', 1, 1, 0, ?, ?, 3, ?, ?)
  `)
  const insertStaleFingerprints = (startIndex: number, count: number): void => {
    const database = databaseModule.getBusinessDatabase()
    database.exec('BEGIN IMMEDIATE')
    try {
      for (let offset = 0; offset < count; offset += 1) {
        const index = startIndex + offset
        insertStaleFingerprint.run(
          `stale-runtime-${index}`,
          access.systemAccountId,
          account.id,
          `stale-fingerprint-${String(index).padStart(5, '0')}`,
          index + 10,
          staleCreatedAt,
          staleCreatedAt,
          staleCreatedAt,
          staleCreatedAt
        )
      }
      database.exec('COMMIT')
    } catch (error) {
      database.exec('ROLLBACK')
      throw error
    }
  }
  insertStaleFingerprints(0, 101)

  const neutralCandidate = runtimeStates.listAccountApiKeyRuntimeStatesDueForProbe(1)
    .find((item) => item.accountId === account.id && item.keyFingerprint === entries[0].fingerprint)
  assert(neutralCandidate, 'SQLite 探针候选必须越过 101 个旧 fingerprint，不能沿用 100 行扫描截断')
  assert(Number.isFinite(Date.parse(neutralCandidate.stateUpdatedAt)), 'SQLite 探针候选必须携带可比较的 state updated_at generation')
  const beforeNeutralDefer = details?.[0]
  assert.equal(runtimeStates.deferAccountApiKeyRuntimeProbe({
    account: selected[0],
    expectedStatus: neutralCandidate.status,
    expectedNextProbeAt: neutralCandidate.nextProbeAt,
    expectedStateUpdatedAt: neutralCandidate.stateUpdatedAt,
    expectedProbeClaimToken: neutralCandidate.probeClaimToken,
    expectedAccountConfigRevision: neutralCandidate.accountConfigRevision,
    delaySeconds: 60,
    observedAt: '2026-07-20T00:31:00.000Z'
  }).changed, true, '中性探针应只顺延当前 generation 的下次复测')
  const afterNeutralDefer = runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[0]
  assert.equal(afterNeutralDefer?.status, beforeNeutralDefer?.status, '中性探针不得改变 Key 状态')
  assert.equal(afterNeutralDefer?.failureCount, beforeNeutralDefer?.failureCount, '中性探针不得增加 Key 失败次数')
  assert.equal(afterNeutralDefer?.lastErrorCode, beforeNeutralDefer?.lastErrorCode, '中性探针不得覆盖 Key 错误诊断')
  assert.notEqual(afterNeutralDefer?.nextProbeAt, firstProbeAt, '中性探针必须推进已到期的 next_probe_at')
  assert.equal(runtimeStates.deferAccountApiKeyRuntimeProbe({
    account: selected[0],
    expectedStatus: neutralCandidate.status,
    expectedNextProbeAt: neutralCandidate.nextProbeAt,
    expectedStateUpdatedAt: neutralCandidate.stateUpdatedAt,
    expectedProbeClaimToken: neutralCandidate.probeClaimToken,
    expectedAccountConfigRevision: neutralCandidate.accountConfigRevision,
    delaySeconds: 60,
    observedAt: '2026-07-20T00:31:01.000Z'
  }).changed, false, '旧 next_probe_at 的迟到 defer 不得覆盖新一轮计划')

  insertStaleFingerprints(101, 9_898)
  const staleFingerprintCount = databaseModule.getBusinessDatabase().prepare(`
    SELECT COUNT(*) AS count
    FROM account_api_key_runtime_states
    WHERE account_id = ?
      AND key_fingerprint LIKE 'stale-fingerprint-%'
  `).get(account.id) as { count: number }
  assert.equal(staleFingerprintCount.count, 9_999, '第 10,000 行候选测试必须精确准备 9,999 个旧 fingerprint')

  const staleCandidate = runtimeStates.listAccountApiKeyRuntimeStatesDueForProbe(1)
    .find((item) => item.accountId === account.id && item.keyFingerprint === entries[1].fingerprint)
  assert(staleCandidate, '当前 Key 位于扫描窗口第 10,000 行时仍必须进入 SQLite 探针候选')
  const staleCleanup = databaseModule.getBusinessDatabase().prepare(`
    DELETE FROM account_api_key_runtime_states
    WHERE account_id = ?
      AND key_fingerprint LIKE 'stale-fingerprint-%'
  `).run(account.id)
  assert.equal(Number(staleCleanup.changes ?? 0), 9_999, '窗口边界断言完成后必须清理旧 fingerprint，避免污染后续状态机用例')
  const explicitPolicyProbeAt = '2030-07-20T01:20:00.000Z'
  const explicitPolicyUpdatedAt = '2030-07-20T00:32:00.000Z'
  const staleStateFence = {
    expectedStatus: staleCandidate.status,
    expectedNextProbeAt: staleCandidate.nextProbeAt,
    expectedStateUpdatedAt: staleCandidate.stateUpdatedAt,
    expectedProbeClaimToken: staleCandidate.probeClaimToken,
    expectedAccountConfigRevision: staleCandidate.accountConfigRevision,
    observedAt: new Date().toISOString()
  }
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE account_api_key_runtime_states
    SET updated_at = ?, probe_claim_token = NULL, probe_claimed_until = NULL
    WHERE account_id = ?
      AND key_fingerprint = ?
  `).run(explicitPolicyUpdatedAt, account.id, entries[1].fingerprint)
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeSuccess(selected[1], staleStateFence).changed, false, '同状态同计划但 state updated_at 已变化时旧 success 不得恢复 Key')
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[1],
    status: 'temporary_unavailable',
    errorCode: 'stale_state_generation_failure',
    ...staleStateFence
  }).changed, false, '同状态同计划但 state updated_at 已变化时旧 transport failure 不得覆盖 Key')
  assert.equal(runtimeStates.deferAccountApiKeyRuntimeProbe({
    account: selected[1],
    expectedStatus: staleCandidate.status,
    expectedNextProbeAt: staleCandidate.nextProbeAt,
    expectedStateUpdatedAt: staleCandidate.stateUpdatedAt,
    expectedProbeClaimToken: staleCandidate.probeClaimToken,
    expectedAccountConfigRevision: staleCandidate.accountConfigRevision,
    delaySeconds: 60,
    observedAt: new Date().toISOString()
  }).changed, false, '同状态同计划但 state updated_at 已变化时旧 neutral defer 不得覆盖 Key')
  const staleConfigCandidate = runtimeStates.listAccountApiKeyRuntimeStatesDueForProbe(1)
    .find((item) => item.accountId === account.id && item.keyFingerprint === entries[1].fingerprint)
  assert(staleConfigCandidate, '账户配置代次 fencing 测试必须取得新候选')
  const staleFence = {
    expectedStatus: staleConfigCandidate.status,
    expectedNextProbeAt: staleConfigCandidate.nextProbeAt,
    expectedStateUpdatedAt: staleConfigCandidate.stateUpdatedAt,
    expectedProbeClaimToken: staleConfigCandidate.probeClaimToken,
    expectedAccountConfigRevision: staleConfigCandidate.accountConfigRevision,
    observedAt: new Date().toISOString()
  }
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET config_revision = config_revision + 1,
        updated_at = ?
    WHERE id = ?
  `).run(explicitPolicyUpdatedAt, account.id)
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeSuccess(selected[1], staleFence).changed, false, 'Key 配置代次更新后旧 success 不得恢复 Key')
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[1],
    status: 'temporary_unavailable',
    errorCode: 'stale_account_config_failure',
    ...staleFence
  }).changed, false, 'Key 配置代次更新后旧 transport failure 不得覆盖 Key')
  assert.equal(runtimeStates.deferAccountApiKeyRuntimeProbe({
    account: selected[1],
    expectedStatus: staleConfigCandidate.status,
    expectedNextProbeAt: staleConfigCandidate.nextProbeAt,
    expectedStateUpdatedAt: staleConfigCandidate.stateUpdatedAt,
    expectedProbeClaimToken: staleConfigCandidate.probeClaimToken,
    expectedAccountConfigRevision: staleConfigCandidate.accountConfigRevision,
    delaySeconds: 60,
    observedAt: new Date().toISOString()
  }).changed, false, 'Key 配置代次更新后旧 neutral defer 不得覆盖 Key')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE account_api_key_runtime_states
    SET status = 'rate_limited',
        next_probe_at = ?,
        probe_claim_token = NULL,
        probe_claimed_until = NULL,
        updated_at = ?
    WHERE account_id = ?
      AND key_fingerprint = ?
  `).run(explicitPolicyProbeAt, explicitPolicyUpdatedAt, account.id, entries[1].fingerprint)
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[1],
    status: 'temporary_unavailable',
    errorCode: 'stale_probe_failure',
    ...staleFence
  }).changed, false, '显式策略更新后旧 transport failure 不得覆盖 Key')
  assert.equal(runtimeStates.deferAccountApiKeyRuntimeProbe({
    account: selected[1],
    expectedStatus: staleConfigCandidate.status,
    expectedNextProbeAt: staleConfigCandidate.nextProbeAt,
    expectedStateUpdatedAt: staleConfigCandidate.stateUpdatedAt,
    expectedProbeClaimToken: staleConfigCandidate.probeClaimToken,
    expectedAccountConfigRevision: staleConfigCandidate.accountConfigRevision,
    delaySeconds: 60,
    observedAt: new Date().toISOString()
  }).changed, false, '显式策略更新后旧 neutral defer 不得覆盖 Key')
  const afterExplicitPolicy = runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[1]
  assert.equal(afterExplicitPolicy?.status, 'rate_limited', '迟到探针不得改变显式策略状态')
  assert.equal(afterExplicitPolicy?.nextProbeAt, explicitPolicyProbeAt, '迟到探针不得改变显式策略探测计划')
  assert.equal(afterExplicitPolicy?.lastErrorCode, 'later_key_failure', '迟到探针不得覆盖显式策略之前的诊断字段')

  const dueForFencedSuccess = new Date(Date.now() - 2_000).toISOString()
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE account_api_key_runtime_states
    SET status = 'rate_limited',
        next_probe_at = ?,
        last_attempt_at = '2026-07-20T00:33:00.000Z',
        probe_claim_token = NULL,
        probe_claimed_until = NULL,
        updated_at = ?
    WHERE account_id = ?
      AND key_fingerprint = ?
  `).run(dueForFencedSuccess, new Date().toISOString(), account.id, entries[1].fingerprint)
  const successCandidate = runtimeStates.listAccountApiKeyRuntimeStatesDueForProbe(1)
    .find((item) => item.accountId === account.id && item.keyFingerprint === entries[1].fingerprint)
  assert(successCandidate, '同代 success 测试必须取得新候选')
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeSuccess(selected[1], {
    expectedStatus: successCandidate.status,
    expectedNextProbeAt: successCandidate.nextProbeAt,
    expectedStateUpdatedAt: successCandidate.stateUpdatedAt,
    expectedProbeClaimToken: successCandidate.probeClaimToken,
    expectedAccountConfigRevision: successCandidate.accountConfigRevision,
    observedAt: new Date().toISOString()
  }).changed, true, '同代 complete_success 必须恢复 Key')
  assert.equal(runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[1]?.status, 'active')

  const dueForFencedFailure = new Date(Date.now() - 2_000).toISOString()
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE account_api_key_runtime_states
    SET status = 'temporary_unavailable',
        next_probe_at = ?,
        last_attempt_at = '2026-07-20T00:34:00.000Z',
        probe_claim_token = NULL,
        probe_claimed_until = NULL,
        updated_at = ?
    WHERE account_id = ?
      AND key_fingerprint = ?
  `).run(dueForFencedFailure, new Date().toISOString(), account.id, entries[1].fingerprint)
  const failureCandidate = runtimeStates.listAccountApiKeyRuntimeStatesDueForProbe(1)
    .find((item) => item.accountId === account.id && item.keyFingerprint === entries[1].fingerprint)
  assert(failureCandidate, '同代 transport failure 测试必须取得新候选')
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[1],
    status: 'temporary_unavailable',
    errorCode: 'same_generation_transport_failure',
    expectedStatus: failureCandidate.status,
    expectedNextProbeAt: failureCandidate.nextProbeAt,
    expectedStateUpdatedAt: failureCandidate.stateUpdatedAt,
    expectedProbeClaimToken: failureCandidate.probeClaimToken,
    expectedAccountConfigRevision: failureCandidate.accountConfigRevision,
    observedAt: new Date().toISOString()
  }).changed, true, '同代 transport failure 必须继续退避')
  const afterFencedFailure = runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[1]
  assert.equal(afterFencedFailure?.status, 'temporary_unavailable')
  assert.equal(afterFencedFailure?.lastErrorCode, 'same_generation_transport_failure')
  assert.notEqual(afterFencedFailure?.nextProbeAt, dueForFencedFailure, '同代 transport failure 必须生成新退避计划')

  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'temporary_unavailable',
    errorCode: 'stale_failure',
    errorMessage: '过期失败不得覆盖',
    observedAt: '2026-07-19T23:30:00.000Z',
    traceId: 'trace-stale'
  }).changed, false, '更早的失败 observation 不得覆盖新状态')
  const afterStaleFailure = runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[0]
  assert.equal(afterStaleFailure?.lastTraceId, 'trace-key-0', '过期失败不得覆盖同一 Key 的最近 traceId')
  assert.equal(afterStaleFailure?.lastErrorCode, 'stable_tie_winner', '过期失败不得覆盖同一 Key 的最近错误')

  assert.equal(runtimeStates.recordAccountApiKeyRuntimeSuccess(selected[0], {
    observedAt: '2026-07-20T00:40:00.000Z'
  }).changed, true)
  const restored = runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[0]
  assert.equal(restored?.lastTraceId, undefined, '成功后必须清空旧失败 traceId')
  assert.equal(restored?.lastErrorCode, undefined, '成功后必须清空旧错误')

  const sameMillisecond = '2026-07-20T00:50:00.000Z'
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'temporary_unavailable',
    errorCode: 'same_millisecond_failure',
    observedAt: sameMillisecond
  }).changed, true)
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeSuccess(selected[0], {
    observedAt: sameMillisecond
  }).changed, true, '同毫秒 success 必须覆盖先写入的 failure')
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'temporary_unavailable',
    errorCode: 'late_same_millisecond_failure',
    observedAt: sameMillisecond
  }).changed, false, '同毫秒迟到 failure 不得覆盖 success')
  const afterSameMillisecondRace = runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[0]
  assert.equal(afterSameMillisecondRace?.status, 'active', '同毫秒 success/failure 竞态必须稳定收敛为 active')
  assert.equal(afterSameMillisecondRace?.lastErrorCode, undefined, '同毫秒迟到 failure 不得复活错误详情')

  const staleActiveSnapshot = { ...selected[0], apiKeyRuntimeStates: [] }
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'temporary_unavailable',
    errorCode: 'failure_after_active_snapshot',
    observedAt: '2026-07-20T00:51:00.000Z'
  }).changed, true)
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeSuccess(staleActiveSnapshot, {
    observedAt: '2026-07-20T00:51:00.001Z'
  }).changed, true, '旧 active 调度快照观察到的新成功仍必须执行恢复写入')
  assert.equal(runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[0]?.status, 'active')

  const claimRaceDueAt = new Date(Date.now() - 5_000).toISOString()
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE account_api_key_runtime_states
    SET status = 'temporary_unavailable',
        next_probe_at = ?,
        last_attempt_at = '2026-07-20T00:52:00.000Z',
        probe_claim_token = NULL,
        probe_claimed_until = NULL,
        updated_at = ?
    WHERE account_id = ? AND key_fingerprint = ?
  `).run(claimRaceDueAt, new Date().toISOString(), account.id, entries[0].fingerprint)
  const firstWorkerCandidate = runtimeStates.listAccountApiKeyRuntimeStatesDueForProbe(20)
    .find((item) => item.accountId === account.id && item.keyFingerprint === entries[0].fingerprint)
  assert(firstWorkerCandidate, '第一个 worker 必须取得当前 Key claim')
  assert.equal(
    runtimeStates.listAccountApiKeyRuntimeStatesDueForProbe(20)
      .some((item) => item.accountId === account.id && item.keyFingerprint === entries[0].fingerprint),
    false,
    '有效 claim 期间第二个 worker 不得重复取得同一 Key'
  )
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE account_api_key_runtime_states
    SET probe_claimed_until = ?
    WHERE account_id = ? AND key_fingerprint = ?
  `).run(new Date(Date.now() - 1_000).toISOString(), account.id, entries[0].fingerprint)
  const secondWorkerCandidate = runtimeStates.listAccountApiKeyRuntimeStatesDueForProbe(20)
    .find((item) => item.accountId === account.id && item.keyFingerprint === entries[0].fingerprint)
  assert(secondWorkerCandidate, 'claim 租约过期后第二个 worker 必须能接管')
  assert.notEqual(secondWorkerCandidate.probeClaimToken, firstWorkerCandidate.probeClaimToken, '接管必须生成新 claim token')
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'temporary_unavailable',
    errorCode: 'expired_worker_failure',
    expectedStatus: firstWorkerCandidate.status,
    expectedNextProbeAt: firstWorkerCandidate.nextProbeAt,
    expectedStateUpdatedAt: firstWorkerCandidate.stateUpdatedAt,
    expectedAccountConfigRevision: firstWorkerCandidate.accountConfigRevision,
    expectedProbeClaimToken: firstWorkerCandidate.probeClaimToken,
    observedAt: new Date().toISOString()
  }).changed, false, '租约已被接管的旧 worker failure 不得落库')
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeSuccess(selected[0], {
    expectedStatus: secondWorkerCandidate.status,
    expectedNextProbeAt: secondWorkerCandidate.nextProbeAt,
    expectedStateUpdatedAt: secondWorkerCandidate.stateUpdatedAt,
    expectedAccountConfigRevision: secondWorkerCandidate.accountConfigRevision,
    expectedProbeClaimToken: secondWorkerCandidate.probeClaimToken,
    observedAt: new Date().toISOString()
  }).changed, true, '接管 worker 的同代 success 必须恢复 Key')
  assert.equal(runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[0]?.status, 'active')

  assert.throws(() => runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'temporary_unavailable',
    observedAt: 'not-an-iso-time'
  }), /observedAt/, '非法显式 observation 不得静默提升为当前时间')
  for (const observedAt of ['', '2026-07-20T00:55:00.000']) {
    assert.throws(() => runtimeStates.recordAccountApiKeyRuntimeFailure({
      account: selected[0],
      status: 'temporary_unavailable',
      observedAt
    }), /observedAt必须是带 Z 或数值 offset 的 RFC3339 时间/, `裸或空 observedAt 必须可见失败：${JSON.stringify(observedAt)}`)
  }
  assert.throws(() => runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'rate_limited',
    cooldownUntil: '2026-07-20T01:00:00.000',
    observedAt: '2026-07-20T00:55:00.000Z'
  }), /cooldownUntil必须是带 Z 或数值 offset 的 RFC3339 时间/, '裸 cooldownUntil 不得写入运行态')
  assert.deepEqual(
    runtimeStates.deferAccountApiKeyRuntimeProbe({
      account: selected[0],
      expectedStatus: 'temporary_unavailable',
      expectedNextProbeAt: '2026-07-20T01:00:00.000',
      delaySeconds: 60,
      observedAt: '2026-07-20T00:55:00.000Z'
    }),
    { changed: false, skippedReason: 'invalid_expected_probe_at' },
    '裸 expectedNextProbeAt 必须作为无效 fence，而不是按宿主时区解释'
  )
  const offsetObservedAt = '2099-07-20T08:55:00.000+08:00'
  const offsetCooldownUntil = '2099-07-20T09:00:00.000+08:00'
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'rate_limited',
    cooldownUntil: offsetCooldownUntil,
    observedAt: offsetObservedAt
  }).changed, true, '带数字 offset 的运行态时间必须保留为有效瞬时值')
  const canonicalOffsetState = runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[0]
  assert.equal(canonicalOffsetState?.cooldownUntil, '2099-07-20T01:00:00.000Z', 'cooldownUntil 必须 canonical 为 UTC Z')
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeFailure({
    account: selected[0],
    status: 'temporary_unavailable',
    errorCode: 'future_clock_failure',
    observedAt: '2099-01-01T00:00:00.000Z'
  }).changed, true)
  assert.equal(runtimeStates.recordAccountApiKeyRuntimeSuccess(selected[0], {
    observedAt: new Date().toISOString()
  }).changed, true, '未来漂移 failure 必须限幅，校时后的真实 success 应立即恢复')
  assert.equal(runtimeStates.loadAccountApiKeyRuntimeDetailsByAccountIds([account.id]).get(account.id)?.[0]?.status, 'active')

  const schemaColumns = databaseModule.getBusinessDatabase()
    .prepare("PRAGMA table_info('account_api_key_runtime_states')")
    .all() as Array<{ name: string }>
  assert(schemaColumns.some((column) => column.name === 'last_trace_id'), '当前 SQLite schema 必须包含 last_trace_id')
  assert(schemaColumns.some((column) => column.name === 'probe_claim_token'), '当前 SQLite schema 必须包含 probe claim token')
  assert(schemaColumns.some((column) => column.name === 'probe_claimed_until'), '当前 SQLite schema 必须包含 probe claim lease')

  // 101 个合法候选必须跨多个符合账户配置上限的 Key 池构造；这里仍保留
  // 第 101 个候选，用于验证前 100 个 claim 冲突时的窗口补位。
  const validClaimPoolSizes = [...Array.from({ length: 9 }, () => 10), 9, 2]
  const validClaimRows: Array<{ accountId: string; keyFingerprint: string; keyIndex: number }> = []
  let validClaimGlobalIndex = 0
  for (const [accountIndex, poolSize] of validClaimPoolSizes.entries()) {
    const poolKeys = Array.from(
      { length: poolSize },
      (_, keyIndex) => `sk-probe-window-${accountIndex}-${String(keyIndex).padStart(2, '0')}`
    )
    const poolAccount = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `Key 探针合法候选窗口账户 ${accountIndex + 1}`,
      type: 'api_key',
      status: 'active',
      schedulable: true,
      supportedModels: ['gpt-5.5'],
      credentials: {
        api_key: poolKeys[0],
        api_keys: poolKeys,
        api_key_strategy: 'round_robin',
        base_url: 'https://api.openai.com/v1'
      },
      groupId: group.id
    }, access)
    databaseModule.getBusinessDatabase().prepare(`
      UPDATE accounts
      SET status = 'active', schedulable = 1, account_expires_at = NULL
      WHERE id = ?
    `).run(poolAccount.id)
    for (const entry of rotation.accountApiKeyEntries({ api_keys: poolKeys })) {
      validClaimRows.push({
        accountId: poolAccount.id,
        keyFingerprint: entry.fingerprint,
        keyIndex: entry.index
      })
    }
  }
  assert.equal(validClaimRows.length, 101, '合法候选窗口必须精确包含 101 个当前 Key')

  const insertValidClaimCandidate = databaseModule.getBusinessDatabase().prepare(`
    INSERT INTO account_api_key_runtime_states (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds,
      created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, 'temporary_unavailable', 1, 1, 0, ?, ?, 3, ?, ?)
  `)
  const validClaimBaseAtMs = Date.parse('2026-07-18T00:00:00.000Z')
  const database = databaseModule.getBusinessDatabase()
  database.exec('BEGIN IMMEDIATE')
  try {
    for (const row of validClaimRows) {
      const dueAt = new Date(validClaimBaseAtMs + validClaimGlobalIndex).toISOString()
      insertValidClaimCandidate.run(
        `valid-claim-runtime-${validClaimGlobalIndex}`,
        access.systemAccountId,
        row.accountId,
        row.keyFingerprint,
        row.keyIndex,
        dueAt,
        dueAt,
        dueAt,
        dueAt
      )
      validClaimGlobalIndex += 1
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }

  const hundredAndFirstCandidate = validClaimRows[100]
  assert(hundredAndFirstCandidate, '第 101 个合法候选必须存在')
  database.exec(`
    CREATE TRIGGER ignore_first_hundred_api_key_probe_claims
    BEFORE UPDATE OF probe_claim_token ON account_api_key_runtime_states
    WHEN OLD.key_fingerprint <> '${hundredAndFirstCandidate.keyFingerprint}'
    BEGIN
      SELECT RAISE(IGNORE);
    END
  `)
  try {
    const claimedAfterFirstHundredConflicts = runtimeStates.listAccountApiKeyRuntimeStatesDueForProbe(1)
    assert.equal(claimedAfterFirstHundredConflicts.length, 1, '前 100 个合法候选 claim 冲突后仍必须补足一个 claim')
    assert.equal(
      claimedAfterFirstHundredConflicts[0]?.keyFingerprint,
      hundredAndFirstCandidate.keyFingerprint,
      '候选转换不得在 100 个合法 Key 处截断，必须继续选择第 101 个 Key'
    )
    const validClaimCount = database.prepare(`
      SELECT COUNT(*) AS count
      FROM account_api_key_runtime_states
      WHERE id LIKE 'valid-claim-runtime-%'
        AND probe_claim_token IS NOT NULL
    `).get() as { count: number }
    assert.equal(validClaimCount.count, 1, 'claim 冲突补位只能租用第 101 个 Key，不得超出请求空槽')
  } finally {
    database.exec('DROP TRIGGER IF EXISTS ignore_first_hundred_api_key_probe_claims')
  }

  console.log('账户内 API Key 探测摘要回归通过')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
