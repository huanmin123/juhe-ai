import { strict as assert } from 'node:assert'

import type { OpenAIAccountSecret } from '../../storage/openai-account-selector.types.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { createAccountAsync, createGroupAsync } from '../../storage/repositories.js'
import {
  deferAccountApiKeyRuntimeProbeAsync,
  listAccountApiKeyRuntimeStatesDueForProbeAsync,
  recordAccountApiKeyRuntimeFailureAsync,
  recordAccountApiKeyRuntimeSuccessAsync,
  loadAccountApiKeyRuntimeDetailsByAccountIdsAsync
} from '../../storage/account-api-key-runtime-state.repository.js'
import { accountApiKeyEntries } from '../../storage/account-api-key-rotation.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { closeRedisClients } from '../../shared/redis-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账户内 API Key runtime state PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `api_key_runtime_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []

try {
  const group = await createGroupAsync({
    name: `API Key runtime PG smoke 分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  createdGroupIds.push(group.id)

  const apiKeys = [
    `sk-api-key-runtime-pg-${marker}-a`,
    `sk-api-key-runtime-pg-${marker}-b`
  ]
  const credentials = {
    api_keys: apiKeys,
    api_key: apiKeys[0],
    base_url: 'https://example.invalid/v1'
  }
  const account = await createAccountAsync({
    name: `API Key runtime PG smoke 账号 ${marker}`,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'api_key',
    status: 'active',
    groupId: group.id,
    credentials,
    concurrencyLimit: 20,
    supportedModels: ['gpt-5-mini']
  }, access)
  createdAccountIds.push(account.id)
  await activateSmokeAccount(account.id)

  const entries = accountApiKeyEntries(credentials)
  assert.equal(entries.length, 2, '测试账号必须启用多 API Key runtime isolation')
  const selected = entries[0]
  const dispatchAccount: OpenAIAccountSecret = {
    id: account.id,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId ?? 'gpt-openai-v1',
    protocolCode: account.protocolCode ?? 'openai',
    protocolVersion: account.protocolVersion ?? 'v1',
    systemAccountId: access.systemAccountId,
    accountOwnerSystemAccountId: access.systemAccountId,
    groupOwnerSystemAccountId: access.systemAccountId,
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    boundGroupId: group.id,
    name: account.name,
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 20,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: account.clientCompatibility,
    supportedModels: ['gpt-5-mini'],
    healthCheckEndpointMode: 'responses_sse',
    baseUrl: 'https://example.invalid/v1',
    apiKey: selected.key,
    apiKeys,
    selectedApiKeyFingerprint: selected.fingerprint,
    selectedApiKeyIndex: selected.index,
    streamFailureCount: 0,
    credentials
  }

  const dueAt = new Date(Date.now() - 1000).toISOString()
  const failure = await recordAccountApiKeyRuntimeFailureAsync({
    account: dispatchAccount,
    status: 'rate_limited',
    statusCode: 429,
    errorCode: 'rate_limit_smoke',
    errorMessage: 'PG runtime state smoke',
    cooldownUntil: dueAt,
    traceId: 'pg-runtime-trace'
  })
  assert.equal(failure.changed, true, 'PG failure 写回应创建 runtime state')

  const initialPool = await getPostgresPool()
  const explicitResetKey = entries[1]
  assert(explicitResetKey, 'PG 显式 reset 回归需要第二个 API Key')
  const explicitResetAt = '2030-01-01T00:00:00.000Z'
  const explicitResetDispatchAccount: OpenAIAccountSecret = {
    ...dispatchAccount,
    apiKey: explicitResetKey.key,
    selectedApiKeyFingerprint: explicitResetKey.fingerprint,
    selectedApiKeyIndex: explicitResetKey.index
  }
  assert.equal((await recordAccountApiKeyRuntimeFailureAsync({
    account: explicitResetDispatchAccount,
    status: 'rate_limited',
    statusCode: 429,
    errorCode: 'system_quota_explicit_reset',
    errorMessage: 'PG 显式 reset 时间字段回归',
    quotaRecoveryMode: 'explicit_reset',
    cooldownUntil: explicitResetAt,
    observedAt: '2026-01-02T00:00:00.000Z'
  })).changed, true, 'PG 明确 reset_at 必须进入显式恢复模式')
  const explicitResetResult = await initialPool.query(`
    SELECT cooldown_until, next_probe_at
    FROM juhe_business.account_api_key_runtime_states
    WHERE account_id = $1 AND key_fingerprint = $2
  `, [account.id, explicitResetKey.fingerprint]) as { rows: Array<{
    cooldown_until: string | null
    next_probe_at: string | null
  }> }
  const explicitResetRow = explicitResetResult.rows[0]
  assert(explicitResetRow, 'PG 显式 reset 写回后必须存在 runtime state')
  assert.equal(explicitResetRow.cooldown_until, explicitResetAt, 'PG 明确 reset_at 必须原样保存为 cooldown_until')
  assert(explicitResetRow.next_probe_at, 'PG 明确 reset_at 必须保存被动复测时间')
  assert.ok(
    Date.parse(explicitResetRow.next_probe_at) > Date.parse(explicitResetAt)
      && Date.parse(explicitResetRow.next_probe_at) <= Date.parse('2030-01-01T08:00:00.000Z'),
    'PG 明确 reset_at 的 next_probe_at 必须在截止后错峰，且不超过周级最大 8 小时'
  )
  const recoveryParentStatusKey = entries[1]
  assert(recoveryParentStatusKey, 'PG 父账户状态回归需要第二个 API Key')
  const recoveryParentStatusDueAt = new Date(Date.now() - 3_000).toISOString()
  await initialPool.query(
    'DELETE FROM juhe_business.account_api_key_runtime_states WHERE account_id = $1 AND key_fingerprint = $2',
    [account.id, recoveryParentStatusKey.fingerprint]
  )
  await initialPool.query(`
    INSERT INTO juhe_business.account_api_key_runtime_states (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds,
      created_at, updated_at
    ) VALUES ($1, $2, $3, $4, $5, 'temporary_unavailable', 1, 1, 0, $6, $6, 3, $6, $6)
  `, [
    `${marker}-cooling-parent-state`,
    access.systemAccountId,
    account.id,
    recoveryParentStatusKey.fingerprint,
    recoveryParentStatusKey.index,
    recoveryParentStatusDueAt
  ])
  await initialPool.query(
    `UPDATE juhe_business.accounts
     SET status = 'temporary_unavailable', schedulable = 1, cooldown_until = $1, updated_at = NOW()
     WHERE id = $2`,
    [recoveryParentStatusDueAt, account.id]
  )
  await initialPool.query(`
    INSERT INTO juhe_business.account_api_key_runtime_states (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds,
      created_at, updated_at
    )
    SELECT $1 || '-stale-' || series::text, $2, $3, $1 || '-fingerprint-' || series::text, series + 10,
      'temporary_unavailable', 1, 1, 0,
      '2026-07-19T00:00:00.000Z', '2026-07-19T00:00:00.000Z', 3,
      '2026-07-19T00:00:00.000Z', '2026-07-19T00:00:00.000Z'
    FROM generate_series(1, 64) AS series
  `, [marker, access.systemAccountId, account.id])

  const candidates = await listAccountApiKeyRuntimeStatesDueForProbeAsync(20)
  const candidate = candidates.find((item) => item.accountId === account.id && item.keyFingerprint === selected.fingerprint)
  assert.ok(candidate, 'PG due-for-probe 读取应返回刚写入的 key')
  assert(
    candidates.some((item) => item.accountId === account.id && item.keyFingerprint === recoveryParentStatusKey.fingerprint),
    'PG temporary_unavailable 父账户的到期 Key 必须进入恢复探针候选'
  )
  await initialPool.query(
    `UPDATE juhe_business.accounts
     SET status = 'disabled', cooldown_until = NULL, updated_at = NOW()
     WHERE id = $1`,
    [account.id]
  )
  const disabledParentCandidates = await listAccountApiKeyRuntimeStatesDueForProbeAsync(20)
  assert.equal(
    disabledParentCandidates.some((item) => item.accountId === account.id && item.keyFingerprint === recoveryParentStatusKey.fingerprint),
    false,
    'PG disabled 父账户不得进入恢复探针候选'
  )
  await initialPool.query(
    `UPDATE juhe_business.accounts
     SET status = 'active', schedulable = 1, cooldown_until = NULL, updated_at = NOW()
     WHERE id = $1`,
    [account.id]
  )
  await initialPool.query(
    `UPDATE juhe_business.account_api_key_runtime_states
     SET status = 'active', next_probe_at = NULL, probe_claim_token = NULL,
         probe_claimed_until = NULL, updated_at = NOW()
     WHERE account_id = $1 AND key_fingerprint = $2`,
    [account.id, recoveryParentStatusKey.fingerprint]
  )
  assert.equal(candidate.apiKey, selected.key, 'PG due-for-probe 应能从加密凭据恢复目标 API Key')
  assert(candidate.probeClaimToken, 'PG due-for-probe 必须原子取得数据库 claim')
  assert(Number.isFinite(Date.parse(candidate.stateUpdatedAt)), 'PG due-for-probe 必须返回精确 state updated_at generation')
  const details = await loadAccountApiKeyRuntimeDetailsByAccountIdsAsync([account.id])
  const detailBeforeNeutralDefer = details.get(account.id)?.find((item) => item.keyFingerprintPrefix === selected.fingerprint.slice(0, 12))
  assert.equal(detailBeforeNeutralDefer?.lastTraceId, 'pg-runtime-trace', 'PG runtime state 应返回最近失败 traceId')

  const neutralDeferred = await deferAccountApiKeyRuntimeProbeAsync({
    account: dispatchAccount,
    expectedStatus: candidate.status,
    expectedNextProbeAt: candidate.nextProbeAt,
    expectedStateUpdatedAt: candidate.stateUpdatedAt,
    expectedProbeClaimToken: candidate.probeClaimToken,
    expectedAccountConfigRevision: candidate.accountConfigRevision,
    delaySeconds: 60,
    observedAt: new Date().toISOString()
  })
  assert.equal(neutralDeferred.changed, true, 'PG 中性探针应推进 next_probe_at')
  const detailAfterNeutralDefer = (await loadAccountApiKeyRuntimeDetailsByAccountIdsAsync([account.id]))
    .get(account.id)?.find((item) => item.keyFingerprintPrefix === selected.fingerprint.slice(0, 12))
  assert.equal(detailAfterNeutralDefer?.status, detailBeforeNeutralDefer?.status, 'PG 中性探针不得改变 Key 状态')
  assert.equal(detailAfterNeutralDefer?.failureCount, detailBeforeNeutralDefer?.failureCount, 'PG 中性探针不得增加 Key 失败次数')
  assert.equal(detailAfterNeutralDefer?.lastErrorCode, detailBeforeNeutralDefer?.lastErrorCode, 'PG 中性探针不得覆盖诊断')
  assert.equal((await listAccountApiKeyRuntimeStatesDueForProbeAsync(20)).some((item) => item.accountId === account.id && item.keyFingerprint === selected.fingerprint), false, 'PG 中性探针顺延后不得立即再次入队')
  const staleStateFence = {
    expectedStatus: candidate.status,
    expectedNextProbeAt: candidate.nextProbeAt,
    expectedStateUpdatedAt: candidate.stateUpdatedAt,
    expectedProbeClaimToken: candidate.probeClaimToken,
    expectedAccountConfigRevision: candidate.accountConfigRevision,
    observedAt: new Date().toISOString()
  }
  assert.equal((await recordAccountApiKeyRuntimeSuccessAsync(dispatchAccount, staleStateFence)).changed, false, 'PG state generation 更新后旧 success 不得恢复 Key')
  assert.equal((await recordAccountApiKeyRuntimeFailureAsync({
    account: dispatchAccount,
    status: 'temporary_unavailable',
    errorCode: 'pg_stale_state_generation_failure',
    ...staleStateFence
  })).changed, false, 'PG state generation 更新后旧 transport failure 不得覆盖 Key')
  assert.equal((await deferAccountApiKeyRuntimeProbeAsync({
    account: dispatchAccount,
    expectedStatus: candidate.status,
    expectedNextProbeAt: candidate.nextProbeAt,
    expectedStateUpdatedAt: candidate.stateUpdatedAt,
    expectedProbeClaimToken: candidate.probeClaimToken,
    expectedAccountConfigRevision: candidate.accountConfigRevision,
    delaySeconds: 60,
    observedAt: new Date().toISOString()
  })).changed, false, 'PG state generation 更新后旧 neutral defer 不得覆盖 Key')

  const pool = await getPostgresPool()
  const explicitPolicyProbeAt = '2030-07-24T00:00:00.000Z'
  const explicitPolicyUpdatedAt = '2030-07-24T00:00:01.000Z'
  const dueForConfigFence = new Date(Date.now() - 2_000).toISOString()
  await pool.query(`
    UPDATE juhe_business.account_api_key_runtime_states
    SET status = 'rate_limited', next_probe_at = $1,
        probe_claim_token = NULL, probe_claimed_until = NULL,
        last_attempt_at = '2026-07-24T00:00:00.000Z', updated_at = '2030-07-24T00:00:00.000Z'
    WHERE account_id = $2 AND key_fingerprint = $3
  `, [dueForConfigFence, account.id, selected.fingerprint])
  const staleCandidate = (await listAccountApiKeyRuntimeStatesDueForProbeAsync(20))
    .find((item) => item.accountId === account.id && item.keyFingerprint === selected.fingerprint)
  assert(staleCandidate, 'PG 配置代次 fencing 测试必须取得新候选')
  await pool.query(`
    UPDATE juhe_business.accounts
    SET config_revision = config_revision + 1, updated_at = $1
    WHERE id = $2
  `, [explicitPolicyUpdatedAt, account.id])
  const staleFence = {
    expectedStatus: staleCandidate.status,
    expectedNextProbeAt: staleCandidate.nextProbeAt,
    expectedStateUpdatedAt: staleCandidate.stateUpdatedAt,
    expectedProbeClaimToken: staleCandidate.probeClaimToken,
    expectedAccountConfigRevision: staleCandidate.accountConfigRevision,
    observedAt: new Date().toISOString()
  }
  assert.equal((await recordAccountApiKeyRuntimeSuccessAsync(dispatchAccount, staleFence)).changed, false, 'PG Key 配置代次更新后旧 success 不得恢复 Key')
  assert.equal((await recordAccountApiKeyRuntimeFailureAsync({
    account: dispatchAccount,
    status: 'temporary_unavailable',
    errorCode: 'pg_stale_account_config_failure',
    ...staleFence
  })).changed, false, 'PG Key 配置代次更新后旧 transport failure 不得覆盖 Key')
  assert.equal((await deferAccountApiKeyRuntimeProbeAsync({
    account: dispatchAccount,
    expectedStatus: staleCandidate.status,
    expectedNextProbeAt: staleCandidate.nextProbeAt,
    expectedStateUpdatedAt: staleCandidate.stateUpdatedAt,
    expectedProbeClaimToken: staleCandidate.probeClaimToken,
    expectedAccountConfigRevision: staleCandidate.accountConfigRevision,
    delaySeconds: 60,
    observedAt: new Date().toISOString()
  })).changed, false, 'PG Key 配置代次更新后旧 neutral defer 不得覆盖 Key')
  await pool.query(`
    UPDATE juhe_business.account_api_key_runtime_states
    SET status = 'rate_limited', next_probe_at = $1,
        probe_claim_token = NULL, probe_claimed_until = NULL, updated_at = $2
    WHERE account_id = $3 AND key_fingerprint = $4
  `, [explicitPolicyProbeAt, explicitPolicyUpdatedAt, account.id, selected.fingerprint])
  assert.equal((await recordAccountApiKeyRuntimeFailureAsync({
    account: dispatchAccount,
    status: 'temporary_unavailable',
    errorCode: 'pg_stale_probe_failure',
    ...staleFence
  })).changed, false, 'PG 显式策略更新后旧 transport failure 不得覆盖 Key')
  assert.equal((await deferAccountApiKeyRuntimeProbeAsync({
    account: dispatchAccount,
    expectedStatus: staleCandidate.status,
    expectedNextProbeAt: staleCandidate.nextProbeAt,
    expectedStateUpdatedAt: staleCandidate.stateUpdatedAt,
    expectedProbeClaimToken: staleCandidate.probeClaimToken,
    expectedAccountConfigRevision: staleCandidate.accountConfigRevision,
    delaySeconds: 60,
    observedAt: new Date().toISOString()
  })).changed, false, 'PG 显式策略更新后旧 neutral defer 不得覆盖 Key')
  const afterStaleProbe = (await loadAccountApiKeyRuntimeDetailsByAccountIdsAsync([account.id]))
    .get(account.id)?.find((item) => item.keyFingerprintPrefix === selected.fingerprint.slice(0, 12))
  assert.equal(afterStaleProbe?.status, 'rate_limited', 'PG 迟到探针不得改变显式策略状态')
  assert.equal(afterStaleProbe?.nextProbeAt, explicitPolicyProbeAt, 'PG 迟到探针不得改变显式策略计划')

  const dueForFencedSuccess = new Date(Date.now() - 2_000).toISOString()
  await pool.query(`
    UPDATE juhe_business.account_api_key_runtime_states
    SET status = 'rate_limited', next_probe_at = $1,
        probe_claim_token = NULL, probe_claimed_until = NULL,
        last_attempt_at = '2026-07-24T00:00:00.000Z', updated_at = '2030-07-24T00:00:02.000Z'
    WHERE account_id = $2 AND key_fingerprint = $3
  `, [dueForFencedSuccess, account.id, selected.fingerprint])
  const successCandidate = (await listAccountApiKeyRuntimeStatesDueForProbeAsync(20))
    .find((item) => item.accountId === account.id && item.keyFingerprint === selected.fingerprint)
  assert(successCandidate, 'PG 同代 success 测试必须取得新候选')
  assert.equal((await recordAccountApiKeyRuntimeSuccessAsync(dispatchAccount, {
    expectedStatus: successCandidate.status,
    expectedNextProbeAt: successCandidate.nextProbeAt,
    expectedStateUpdatedAt: successCandidate.stateUpdatedAt,
    expectedProbeClaimToken: successCandidate.probeClaimToken,
    expectedAccountConfigRevision: successCandidate.accountConfigRevision,
    observedAt: new Date().toISOString()
  })).changed, true, 'PG 同代 complete_success 必须恢复 Key')

  const dueForFencedFailure = new Date(Date.now() - 2_000).toISOString()
  await pool.query(`
    UPDATE juhe_business.account_api_key_runtime_states
    SET status = 'temporary_unavailable', next_probe_at = $1,
        probe_claim_token = NULL, probe_claimed_until = NULL,
        last_attempt_at = '2026-07-24T00:00:01.000Z', updated_at = '2030-07-24T00:00:03.000Z'
    WHERE account_id = $2 AND key_fingerprint = $3
  `, [dueForFencedFailure, account.id, selected.fingerprint])
  const failureCandidate = (await listAccountApiKeyRuntimeStatesDueForProbeAsync(20))
    .find((item) => item.accountId === account.id && item.keyFingerprint === selected.fingerprint)
  assert(failureCandidate, 'PG 同代 transport failure 测试必须取得新候选')
  assert.equal((await recordAccountApiKeyRuntimeFailureAsync({
    account: dispatchAccount,
    status: 'temporary_unavailable',
    errorCode: 'pg_same_generation_transport_failure',
    expectedStatus: failureCandidate.status,
    expectedNextProbeAt: failureCandidate.nextProbeAt,
    expectedStateUpdatedAt: failureCandidate.stateUpdatedAt,
    expectedProbeClaimToken: failureCandidate.probeClaimToken,
    expectedAccountConfigRevision: failureCandidate.accountConfigRevision,
    observedAt: new Date().toISOString()
  })).changed, true, 'PG 同代 transport failure 必须继续退避')

  const fencedGenericState = (await pool.query(`
    SELECT cooldown_until, next_probe_at
    FROM juhe_business.account_api_key_runtime_states
    WHERE account_id = $1 AND key_fingerprint = $2
  `, [account.id, selected.fingerprint])).rows[0] as { cooldown_until: string | null; next_probe_at: string | null }
  assert.equal(fencedGenericState.cooldown_until, fencedGenericState.next_probe_at, 'PG fenced generic failure 必须让 cooldown_until 与 next_probe_at 使用同一调度时间')

  assert.equal((await recordAccountApiKeyRuntimeFailureAsync({
    account: dispatchAccount,
    status: 'temporary_unavailable',
    errorCode: 'pg_generic_upsert_failure',
    observedAt: new Date(Date.now() + 1_000).toISOString()
  })).changed, true, 'PG 无 fence generic failure 必须继续写入既有 runtime state')
  const upsertGenericState = (await pool.query(`
    SELECT cooldown_until, next_probe_at
    FROM juhe_business.account_api_key_runtime_states
    WHERE account_id = $1 AND key_fingerprint = $2
  `, [account.id, selected.fingerprint])).rows[0] as { cooldown_until: string | null; next_probe_at: string | null }
  assert.equal(upsertGenericState.cooldown_until, upsertGenericState.next_probe_at, 'PG upsert generic failure 必须让 cooldown_until 与 next_probe_at 使用同一调度时间')

  const claimRaceDueAt = new Date(Date.now() - 5_000).toISOString()
  await pool.query(`
    UPDATE juhe_business.account_api_key_runtime_states
    SET status = 'temporary_unavailable', next_probe_at = $1,
        last_attempt_at = '2026-07-24T00:00:02.000Z',
        probe_claim_token = NULL, probe_claimed_until = NULL,
        updated_at = $2
    WHERE account_id = $3 AND key_fingerprint = $4
  `, [claimRaceDueAt, new Date().toISOString(), account.id, selected.fingerprint])
  const firstWorkerCandidate = (await listAccountApiKeyRuntimeStatesDueForProbeAsync(20))
    .find((item) => item.accountId === account.id && item.keyFingerprint === selected.fingerprint)
  assert(firstWorkerCandidate, 'PG 第一个 worker 必须取得 claim')
  assert.equal(
    (await listAccountApiKeyRuntimeStatesDueForProbeAsync(20))
      .some((item) => item.accountId === account.id && item.keyFingerprint === selected.fingerprint),
    false,
    'PG 有效 claim 期间第二个 worker 不得重复取得同一 Key'
  )
  await pool.query(`
    UPDATE juhe_business.account_api_key_runtime_states
    SET probe_claimed_until = $1
    WHERE account_id = $2 AND key_fingerprint = $3
  `, [new Date(Date.now() - 1_000).toISOString(), account.id, selected.fingerprint])
  const secondWorkerCandidate = (await listAccountApiKeyRuntimeStatesDueForProbeAsync(20))
    .find((item) => item.accountId === account.id && item.keyFingerprint === selected.fingerprint)
  assert(secondWorkerCandidate, 'PG claim 租约过期后第二个 worker 必须接管')
  assert.notEqual(secondWorkerCandidate.probeClaimToken, firstWorkerCandidate.probeClaimToken)
  assert.equal((await recordAccountApiKeyRuntimeFailureAsync({
    account: dispatchAccount,
    status: 'temporary_unavailable',
    errorCode: 'pg_expired_worker_failure',
    expectedStatus: firstWorkerCandidate.status,
    expectedNextProbeAt: firstWorkerCandidate.nextProbeAt,
    expectedStateUpdatedAt: firstWorkerCandidate.stateUpdatedAt,
    expectedAccountConfigRevision: firstWorkerCandidate.accountConfigRevision,
    expectedProbeClaimToken: firstWorkerCandidate.probeClaimToken,
    observedAt: new Date().toISOString()
  })).changed, false, 'PG 被接管的旧 worker failure 不得落库')
  assert.equal((await recordAccountApiKeyRuntimeSuccessAsync(dispatchAccount, {
    expectedStatus: secondWorkerCandidate.status,
    expectedNextProbeAt: secondWorkerCandidate.nextProbeAt,
    expectedStateUpdatedAt: secondWorkerCandidate.stateUpdatedAt,
    expectedAccountConfigRevision: secondWorkerCandidate.accountConfigRevision,
    expectedProbeClaimToken: secondWorkerCandidate.probeClaimToken,
    observedAt: new Date().toISOString()
  })).changed, true, 'PG 接管 worker 的 success 必须恢复 Key')

  const dirty = await readDirtyReason(group.id)
  assert.equal(dirty, 'account_api_key_runtime', 'PG runtime state 写回应标记分组账号统计 dirty')

  const success = await recordAccountApiKeyRuntimeSuccessAsync(dispatchAccount)
  assert.equal(success.changed, true, 'PG success 写回应恢复 key 到 active')
  const afterSuccess = await listAccountApiKeyRuntimeStatesDueForProbeAsync(20)
  assert.equal(afterSuccess.some((item) => item.accountId === account.id && item.keyFingerprint === selected.fingerprint), false, 'PG success 后 key 不应继续进入 probe 候选')
  const detailsAfterSuccess = await loadAccountApiKeyRuntimeDetailsByAccountIdsAsync([account.id])
  assert.equal(detailsAfterSuccess.get(account.id)?.find((item) => item.keyFingerprintPrefix === selected.fingerprint.slice(0, 12))?.lastTraceId, undefined, 'PG success 后应清空最近失败 traceId')

  await new Promise((resolve) => setTimeout(resolve, 2))
  const sameMillisecond = new Date().toISOString()
  assert.equal((await recordAccountApiKeyRuntimeFailureAsync({
    account: dispatchAccount,
    status: 'temporary_unavailable',
    errorCode: 'pg_same_millisecond_failure',
    observedAt: sameMillisecond
  })).changed, true)
  assert.equal((await recordAccountApiKeyRuntimeSuccessAsync(dispatchAccount, {
    observedAt: sameMillisecond
  })).changed, true, 'PG 同毫秒 success 必须覆盖先写入的 failure')
  assert.equal((await recordAccountApiKeyRuntimeFailureAsync({
    account: dispatchAccount,
    status: 'temporary_unavailable',
    errorCode: 'pg_late_same_millisecond_failure',
    observedAt: sameMillisecond
  })).changed, false, 'PG 同毫秒迟到 failure 不得覆盖 success')
  const detailsAfterRace = await loadAccountApiKeyRuntimeDetailsByAccountIdsAsync([account.id])
  const racedState = detailsAfterRace.get(account.id)?.find((item) => item.keyFingerprintPrefix === selected.fingerprint.slice(0, 12))
  assert.equal(racedState?.status, 'active', 'PG 同毫秒竞态必须稳定收敛为 active')
  assert.equal(racedState?.lastErrorCode, undefined, 'PG 同毫秒迟到 failure 不得复活错误详情')

  await assertProbeExplainUsesIndex(dueAt)

  console.log(JSON.stringify({
    message: '账户内 API Key runtime state PG smoke 通过',
    candidatesBeforeSuccess: candidates.length,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function readDirtyReason(groupId: string): Promise<string | undefined> {
  const pool = await getPostgresPool()
  const result = await pool.query(
    'SELECT reason FROM juhe_business.group_account_stats_dirty WHERE group_id = $1 LIMIT 1',
    [groupId]
  )
  const row = result.rows[0] as { reason?: unknown } | undefined
  return typeof row?.reason === 'string' ? row.reason : undefined
}

async function activateSmokeAccount(accountId: string): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(
    `UPDATE juhe_business.accounts
     SET status = 'active', schedulable = 1, cooldown_until = NULL,
         last_error_code = NULL, last_error_message = NULL, updated_at = NOW()
     WHERE id = $1`,
    [accountId]
  )
}

async function assertProbeExplainUsesIndex(dueAt: string): Promise<void> {
  const pool = await getPostgresPool()
  const client = await pool.connect()
  try {
    await client.query('BEGIN')
    await client.query('SET LOCAL enable_seqscan = off')
    const result = await client.query(
      `EXPLAIN (COSTS OFF)
       SELECT states.account_id, states.key_fingerprint
       FROM juhe_business.account_api_key_runtime_states states
       JOIN juhe_business.accounts accounts ON accounts.id = states.account_id
       WHERE states.status IN ('unverified', 'temporary_unavailable', 'rate_limited')
         AND states.next_probe_at IS NOT NULL
         AND states.next_probe_at <= $1
       AND accounts.deleted_at IS NULL
         AND accounts.status IN ('active', 'rate_limited', 'temporary_unavailable')
         AND accounts.schedulable = 1
         AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > $2)
       ORDER BY states.next_probe_at ASC, states.updated_at ASC, states.account_id ASC, states.key_index ASC
       LIMIT 20`,
      [dueAt, dueAt]
    )
    const plan = result.rows.map((row) => String(row['QUERY PLAN'] ?? '')).join('\n')
    assert.match(plan, /idx_account_api_key_runtime_probe/, 'PG probe 查询应使用 next_probe_at/status 索引')
    assert.doesNotMatch(plan, /\bSeq Scan\b/, 'PG probe 查询不应出现 Seq Scan')
  } finally {
    await client.query('ROLLBACK').catch(() => undefined)
    client.release()
  }
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  if (createdAccountIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.account_api_key_runtime_states WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.account_supported_models WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.account_model_mappings WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.account_tag_bindings WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_terms WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_documents WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [createdAccountIds])
  }
  if (createdGroupIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = ANY($1::text[])', [createdGroupIds])
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE group_id = ANY($1::text[])', [createdGroupIds])
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [createdGroupIds])
  }
}
