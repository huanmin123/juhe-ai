import { strict as assert } from 'node:assert'
import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { encryptJson } from '../../storage/crypto.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  clearAccountFailureStateResultAsync,
  createAccountAsync,
  createGroupAsync,
  createResourceAuthorizationAsync,
  deferCooldownAccountRetestAsync,
  deleteAccountAsync,
  deleteGroupAsync,
  findAccountForCooldownRetestAsync,
  findOpenAIAccountForGroupAsync,
  listAccountsDueForCooldownRetestPageAsync,
  recordCooldownAccountRetestFailureAsync,
  recordCooldownAccountRetestSuccessAsync
} from '../../storage/repositories.js'
import { applyAccountErrorHandlingAsync } from '../../modules/gateway/policy/account-error-policy.service.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'account error policy observation fencing PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')
assert.equal(
  process.env.JUHE_AI_ALLOW_ACCOUNT_ERROR_POLICY_OBSERVATION_FENCING_POSTGRES_SMOKE,
  '1',
  'account error policy observation fencing PG smoke 会写隔离 fixture，必须显式设置 JUHE_AI_ALLOW_ACCOUNT_ERROR_POLICY_OBSERVATION_FENCING_POSTGRES_SMOKE=1'
)

const marker = `account_error_observation_pg_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []
const createdSystemAccountIds: string[] = []
const createdAuthorizationIds: string[] = []
const bulkCooldownScanAccountIds: string[] = []

try {
  const initialPool = await getPostgresPool()
  await assertCooldownRetestSchema(initialPool)
  await assertSeedPrerequisites(initialPool)
  const group = await createGroupAsync({
    name: `account error observation PG ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  createdGroupIds.push(group.id)
  const account = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `account error observation PG ${marker}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-account-error-observation-${marker}`,
      base_url: 'https://example.invalid/v1',
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
    },
    groupId: group.id,
    supportedModels: ['gpt-4o-mini'],
    status: 'active',
    schedulable: true
  }, access)
  createdAccountIds.push(account.id)

  const pool = await getPostgresPool()
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'active', schedulable = 1, cooldown_until = NULL,
        last_error_code = NULL, last_error_message = NULL,
        last_health_success_at = NULL, updated_at = $2
    WHERE id = $1
  `, [account.id, new Date(Date.now() - 60_000).toISOString()])

  const staleActive = await findOpenAIAccountForGroupAsync(group.id, account.id, 'sys_admin', { ignoreAvailability: true })
  assert(staleActive?.dispatchRevision && staleActive.dispatchRevision > 0, 'PG 网关账户必须携带 dispatch revision')
  const baseMs = Date.now() - 10_000
  const successAt = new Date(baseMs + 1_000).toISOString()
  const oldFailureAt = new Date(baseMs).toISOString()
  const laterFailureAt = new Date(baseMs + 2_000).toISOString()
  const recoveryAt = new Date(baseMs + 3_000).toISOString()

  const watermark = await applyAccountErrorHandlingAsync(staleActive, {
    success: true,
    observedAt: successAt,
    dispatchRevision: staleActive.dispatchRevision,
    trafficSource: 'gateway'
  })
  assert.equal(watermark.changed, false, 'PG active 成功 watermark 不应伪报状态变化')
  assert.equal((await state(account.id)).last_health_success_at, successAt, 'PG 必须持久化 active 成功 watermark')

  const staleFailure = await applyCooldown(staleActive, oldFailureAt)
  assert.equal(staleFailure.changed, false, 'PG 迟到失败不得覆盖较新成功')
  assert.equal((await state(account.id)).status, 'active')

  const currentFailure = await applyCooldown(staleActive, laterFailureAt)
  assert.equal(currentFailure.changed, true, 'PG 较新显式失败应生效')
  const explicitCooldownState = await state(account.id)
  assert.equal(explicitCooldownState.status, 'temporary_unavailable')
  assert.equal(explicitCooldownState.last_error_code, 'explicit_account_error_policy_cooldown', 'PG 显式 cooldown 必须持久化 provenance')
  assert(explicitCooldownState.cooldown_retest_observation_started_at, 'PG 显式 cooldown 必须持久化恢复代次')

  const recovery = await applyAccountErrorHandlingAsync(staleActive, {
    success: true,
    observedAt: recoveryAt,
    dispatchRevision: staleActive.dispatchRevision,
    trafficSource: 'gateway'
  })
  assert.equal(recovery.changed, false, 'PG 旧在途请求较晚完成也不得恢复显式 cooldown')
  assert.equal((await state(account.id)).status, 'temporary_unavailable')

  const noObservationRecovery = await applyAccountErrorHandlingAsync({
    ...staleActive,
    lastErrorMessage: 'PG 冷却前快照中的旧诊断'
  }, {
    success: true,
    trafficSource: 'account_health_check'
  })
  assert.equal(noObservationRecovery.changed, false, 'PG 旧快照且无 observedAt 的成功不得清理显式 cooldown')
  const defaultClear = await clearAccountFailureStateResultAsync(account.id, access, { allowErrorRestore: false })
  assert.equal(defaultClear.changed, false, 'PG 默认 repository clear 不得清理显式 cooldown')
  assert.equal((await state(account.id)).status, 'temporary_unavailable')

  const sourceMatchedRecovery = await recordCooldownAccountRetestSuccessAsync(account.id, {
    ...cooldownGuard(explicitCooldownState)
  })
  assert.equal(sourceMatchedRecovery.changed, true, 'PG 仅当复测携带当前 cooldown 代次时才能恢复')
  assert.equal((await state(account.id)).status, 'active')

  const automaticStateAt = new Date(Date.now() - 10_000).toISOString()
  const automaticCooldownUntil = new Date(Date.now() + 60_000).toISOString()
  const automaticGeneration = `cooldown:${randomUUID()}`
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'rate_limited', schedulable = 1,
        cooldown_until = $2,
        last_error_code = NULL,
        last_error_message = '自动 transport cooldown',
        cooldown_retest_observation_started_at = $3,
        cooldown_retest_generation = $4,
        cooldown_retest_failure_count = 0,
        updated_at = $3
    WHERE id = $1
  `, [account.id, automaticCooldownUntil, automaticStateAt, automaticGeneration])
  const automaticStateSnapshot = await findOpenAIAccountForGroupAsync(group.id, account.id, 'sys_admin', { ignoreAvailability: true })
  assert(automaticStateSnapshot, 'PG 自动 transport cooldown 快照读取失败')
  const automaticRecovery = await applyAccountErrorHandlingAsync(automaticStateSnapshot, {
    success: true,
    observedAt: new Date().toISOString(),
    dispatchRevision: automaticStateSnapshot.dispatchRevision,
    trafficSource: 'runtime_recovery_probe'
  })
  assert.equal(automaticRecovery.changed, false, 'PG 未携带 cooldown generation 的通用成功不得恢复自动 cooldown')
  const automaticState = await state(account.id)
  assert.equal(automaticState.status, 'rate_limited', 'PG 自动 cooldown 必须保留到匹配复测')
  assert.equal(automaticState.cooldown_retest_generation, automaticGeneration, 'PG 自动 cooldown 必须保留当前唯一代次')
  const automaticRetestRecovery = await recordCooldownAccountRetestSuccessAsync(account.id, {
    ...cooldownGuard(automaticState)
  })
  assert.equal(automaticRetestRecovery.changed, true, 'PG 匹配 cooldown generation 的复测成功应恢复自动 cooldown')
  assert.equal((await state(account.id)).status, 'active')

  const retriedFailure = await applyCooldown(staleActive, laterFailureAt)
  assert.equal(retriedFailure.changed, false, 'PG DB 重试的旧失败不得复活')
  assert.equal((await state(account.id)).status, 'active')

  const granteeId = `sysacc_${marker}`
  await insertSmokeSystemAccount(granteeId)
  createdSystemAccountIds.push(granteeId)
  const granteeAccess: AccessScope = { systemAccountId: granteeId, role: 'user' }
  const granteeGroup = await createGroupAsync({
    name: `account error observation PG grantee ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, granteeAccess)
  createdGroupIds.push(granteeGroup.id)
  await createResourceAuthorizationAsync({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId,
    targetGroupId: granteeGroup.id,
    remark: `cooldown source revision ${marker}`,
    limits: { total: { enabled: true, limit: 1 } }
  }, access)
  const authorizedInstance = await findAuthorizedInstance(account.id, granteeId)
  const authorizedInstanceId = authorizedInstance.id
  createdAccountIds.push(authorizedInstanceId)
  createdAuthorizationIds.push(authorizedInstance.authorizationId)

  const authorizedObservationStartedAt = new Date(Date.now() - 30_000).toISOString()
  const authorizedGeneration = `cooldown:${randomUUID()}`
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'temporary_unavailable', schedulable = 1,
        cooldown_until = $2,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = $3,
        cooldown_retest_generation = $4,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        updated_at = $2
    WHERE id = $1
  `, [authorizedInstanceId, new Date(Date.now() - 1_000).toISOString(), authorizedObservationStartedAt, authorizedGeneration])
  const authorizedCandidate = await findAccountForCooldownRetestAsync(authorizedInstanceId)
  assert(authorizedCandidate, 'PG 被授权冷却实例必须能进入复测候选')
  assert.equal(authorizedCandidate.accessType, 'authorized', 'PG 被授权冷却实例必须保留 authorized 访问类型')
  assert(authorizedCandidate.configRevision && authorizedCandidate.configRevision > 0, 'PG 被授权冷却候选必须携带配置版本')
  assert(authorizedCandidate.cooldownRetestDispatchRevision && authorizedCandidate.cooldownRetestDispatchRevision > 0, 'PG 被授权冷却候选必须携带派发版本')
  assert(authorizedCandidate.cooldownRetestObservationStartedAt, 'PG 被授权冷却候选必须携带观察窗口')
  assert(authorizedCandidate.cooldownRetestGeneration, 'PG 被授权冷却候选必须携带唯一代次')
  assert(authorizedCandidate.cooldownRetestSourceConfigRevision, 'PG 被授权冷却候选必须携带来源配置版本')
  const authorizedGuard = {
    expectedConfigRevision: authorizedCandidate.configRevision,
    expectedDispatchRevision: authorizedCandidate.cooldownRetestDispatchRevision,
    expectedObservationStartedAt: authorizedCandidate.cooldownRetestObservationStartedAt,
    expectedGeneration: authorizedCandidate.cooldownRetestGeneration,
    expectedSourceConfigRevision: authorizedCandidate.cooldownRetestSourceConfigRevision
  }
  const beforeSourceRevisionAdvance = await state(authorizedInstanceId)
  await pool.query(`
    UPDATE juhe_business.accounts
    SET config_revision = config_revision + 1, updated_at = $2
    WHERE id = $1
  `, [account.id, new Date().toISOString()])

  const staleSourceFailure = await recordCooldownAccountRetestFailureAsync(authorizedInstanceId, {
    statusCode: 503,
    errorMessage: 'stale authorized source failure',
    ...authorizedGuard,
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(staleSourceFailure.changed, false, 'PG 来源配置版本推进后旧 failure 不得写回授权实例')
  const staleSourceDefer = await deferCooldownAccountRetestAsync(authorizedInstanceId, {
    ...authorizedGuard,
    delaySeconds: 60
  })
  assert.equal(staleSourceDefer.changed, false, 'PG 来源配置版本推进后旧 defer 不得写回授权实例')
  const staleSourceSuccess = await recordCooldownAccountRetestSuccessAsync(authorizedInstanceId, authorizedGuard)
  assert.equal(staleSourceSuccess.changed, false, 'PG 来源配置版本推进后旧 success 不得恢复授权实例')
  assert.deepEqual(await state(authorizedInstanceId), beforeSourceRevisionAdvance, 'PG 来源配置版本 fence 应拒绝三类旧写回且不改变授权实例')

  const currentSourceRevision = (await state(account.id)).config_revision
  assert.notEqual(currentSourceRevision, authorizedGuard.expectedSourceConfigRevision, 'PG 来源配置版本必须已经推进')
  const currentAuthorizedCandidate = await findAccountForCooldownRetestAsync(authorizedInstanceId)
  assert(currentAuthorizedCandidate, 'PG 来源配置版本推进后授权实例仍应保持复测候选资格')
  assert.equal(currentAuthorizedCandidate.cooldownRetestSourceConfigRevision, currentSourceRevision, 'PG 后续候选必须携带推进后的来源配置版本')
  const currentSourceRecovery = await recordCooldownAccountRetestSuccessAsync(authorizedInstanceId, {
    ...authorizedGuard,
    expectedSourceConfigRevision: currentAuthorizedCandidate.cooldownRetestSourceConfigRevision
  })
  assert.equal(currentSourceRecovery.changed, true, 'PG 携带当前来源配置版本的复测成功应恢复授权实例')
  assert.equal((await state(authorizedInstanceId)).status, 'active', 'PG 当前来源配置版本只应恢复本地授权实例')
  assert.equal((await state(account.id)).status, 'active', 'PG 授权实例恢复不得改变来源账户状态')

  const quotaObservationStartedAt = new Date(Date.now() - 30_000).toISOString()
  const quotaGeneration = `cooldown:${randomUUID()}`
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'temporary_unavailable', schedulable = 1,
        cooldown_until = $2,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = $3,
        cooldown_retest_generation = $4,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        updated_at = $2
    WHERE id = $1
  `, [authorizedInstanceId, new Date(Date.now() - 1_000).toISOString(), quotaObservationStartedAt, quotaGeneration])
  const quotaEligibleCandidate = await findAccountForCooldownRetestAsync(authorizedInstanceId)
  assert(quotaEligibleCandidate, 'PG 授权额度未耗尽前，合法冷却实例必须仍能进入复测候选')
  assert.equal(quotaEligibleCandidate.cooldownRetestGeneration, quotaGeneration, 'PG 授权额度基线候选必须对应当前冷却代次')
  await seedAuthorizationQuotaExhaustion(granteeId, authorizedInstance.authorizationId)
  const quotaExhaustedCandidate = await findAccountForCooldownRetestAsync(authorizedInstanceId)
  assert.equal(quotaExhaustedCandidate, undefined, 'PG 授权额度耗尽的冷却实例不得进入复测候选')
  const quotaExhaustedState = await state(authorizedInstanceId)
  assert.equal(quotaExhaustedState.status, 'temporary_unavailable', 'PG 候选额度过滤不得擅自改写授权实例状态')
  assert.equal(quotaExhaustedState.cooldown_retest_generation, quotaGeneration, 'PG 候选额度过滤不得推进或清理当前冷却代次')

  const paginationBaseMs = Date.now() - 5 * 60_000
  const paginationBarrier = await seedCooldownRetestPaginationBarrierFixtures({
    groupId: granteeGroup.id,
    templateAccountId: authorizedInstanceId,
    baseMs: paginationBaseMs
  })
  bulkCooldownScanAccountIds.push(...paginationBarrier.accountIds)
  createdAuthorizationIds.push(...paginationBarrier.authorizationIds)
  const paginationOwnerObservationStartedAt = new Date(paginationBaseMs - 30_000).toISOString()
  const paginationOwnerGeneration = `cooldown:${randomUUID()}`
  const paginationOwnerCooldownUntil = new Date(paginationBaseMs + 201).toISOString()
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'temporary_unavailable', schedulable = 1,
        cooldown_until = $2,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = $3,
        cooldown_retest_generation = $4,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        updated_at = $2
    WHERE id = $1
  `, [account.id, paginationOwnerCooldownUntil, paginationOwnerObservationStartedAt, paginationOwnerGeneration])
  const paginationStartCursor = {
    cooldownUntil: new Date(paginationBaseMs - 1).toISOString(),
    priority: -2_147_483_648,
    createdAt: '1970-01-01T00:00:00.000Z',
    id: ''
  }
  const unavailableBarrierPage = await listAccountsDueForCooldownRetestPageAsync(1, paginationStartCursor)
  assert.equal(unavailableBarrierPage.accounts.length, 0, 'PG 前 200 条授权不可用冷却记录必须在后置过滤后不返回候选')
  assert(unavailableBarrierPage.nextCursor, 'PG 前 200 条授权不可用记录即使全部被后置过滤，也必须推进 raw cursor')
  const postBarrierPage = await listAccountsDueForCooldownRetestPageAsync(1, unavailableBarrierPage.nextCursor)
  assert.equal(postBarrierPage.accounts[0]?.id, account.id, 'PG 穿过授权不可用与配额耗尽屏障后，raw cursor 分页必须到达健康 owner')

  console.log(JSON.stringify({
    message: 'account error policy observation fencing PG smoke 通过',
    successWatermarkPersisted: true,
    staleFailureRejected: true,
    staleSnapshotRecoveryRejected: true,
    noObservationRecoveryRejected: true,
    defaultClearRejected: true,
    sourceMatchedRecoveryApplied: true,
    authorizedSourceRevisionFenced: true,
    authorizedQuotaExhaustedExcluded: true,
    authorizationPostFilterRawCursorProgresses: true,
    dbRetryRejected: true
  }))
} finally {
  let cleanupError: unknown
  try {
    await cleanupSmokeRows()
  } catch (error) {
    cleanupError = error
  }
  try {
    await closeRedisClients()
  } catch (error) {
    cleanupError ??= error
  }
  try {
    await closePostgresPool()
  } catch (error) {
    cleanupError ??= error
  }
  if (cleanupError) throw cleanupError
}

async function applyCooldown(account: NonNullable<Awaited<ReturnType<typeof findOpenAIAccountForGroupAsync>>>, observedAt: string) {
  return await applyAccountErrorHandlingAsync(account, {
    success: false,
    statusCode: 599,
    bodyText: '{"error":{"message":"configured cooldown"}}',
    observedAt,
    dispatchRevision: account.dispatchRevision,
    trafficSource: 'gateway',
    policyDecision: {
      action: 'cooldown',
      cooldownStatus: 'temporary_unavailable',
      ruleName: 'PG 用户显式临时避让'
    }
  })
}

async function state(accountId: string): Promise<{
  status: string
  config_revision: number
  dispatch_revision: number
  cooldown_retest_observation_started_at: string | null
  cooldown_retest_generation: string | null
  last_error_code: string | null
  last_health_success_at: string | null
}> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT status, config_revision, dispatch_revision,
      cooldown_retest_observation_started_at, cooldown_retest_generation,
      last_error_code, last_health_success_at
    FROM juhe_business.accounts
    WHERE id = $1
  `, [accountId])
  const row = result.rows[0] as {
    status?: string
    config_revision?: number | string
    dispatch_revision?: number | string
    cooldown_retest_observation_started_at?: string | Date | null
    cooldown_retest_generation?: string | null
    last_error_code?: string | null
    last_health_success_at?: string | Date | null
  } | undefined
  assert(row?.status, `PG 账户 ${accountId} 状态不存在`)
  return {
    status: row.status,
    config_revision: Number(row.config_revision ?? 1),
    dispatch_revision: Number(row.dispatch_revision ?? 1),
    cooldown_retest_observation_started_at: row.cooldown_retest_observation_started_at instanceof Date
      ? row.cooldown_retest_observation_started_at.toISOString()
      : row.cooldown_retest_observation_started_at ?? null,
    cooldown_retest_generation: row.cooldown_retest_generation ?? null,
    last_error_code: row.last_error_code ?? null,
    last_health_success_at: row.last_health_success_at instanceof Date
      ? row.last_health_success_at.toISOString()
      : row.last_health_success_at ?? null
  }
}

function cooldownGuard(value: Awaited<ReturnType<typeof state>>): {
  expectedConfigRevision: number
  expectedDispatchRevision: number
  expectedObservationStartedAt: string
  expectedGeneration: string
} {
  assert(value.cooldown_retest_observation_started_at, 'PG cooldown 状态必须包含观察窗口')
  assert(value.cooldown_retest_generation, 'PG cooldown 状态必须包含唯一代次')
  return {
    expectedConfigRevision: value.config_revision,
    expectedDispatchRevision: value.dispatch_revision,
    expectedObservationStartedAt: value.cooldown_retest_observation_started_at,
    expectedGeneration: value.cooldown_retest_generation
  }
}

async function assertCooldownRetestSchema(pool: Awaited<ReturnType<typeof getPostgresPool>>): Promise<void> {
  const requiredColumns = [
    'config_revision',
    'dispatch_revision',
    'cooldown_retest_observation_started_at',
    'cooldown_retest_generation'
  ]
  const result = await pool.query(`
    SELECT column_name
    FROM information_schema.columns
    WHERE table_schema = 'juhe_business'
      AND table_name = 'accounts'
      AND column_name = ANY($1::text[])
  `, [requiredColumns])
  const present = new Set(result.rows.map((row) => String(row.column_name)))
  const missing = requiredColumns.filter((column) => !present.has(column))
  assert.equal(missing.length, 0, `account error policy PG smoke schema 前置缺失：${missing.join(', ')}；请先应用 Node/PG accounts schema，再运行 smoke（不会自动修改共享数据库）`)
  const statsTable = await pool.query("SELECT to_regclass('juhe_stats.usage_stats_totals') AS table_name")
  assert(statsTable.rows[0]?.table_name, 'account error policy PG smoke schema 前置缺失：juhe_stats.usage_stats_totals；请先应用 Node/PG stats schema，再运行 smoke（不会自动修改共享数据库）')
}

async function assertSeedPrerequisites(pool: Awaited<ReturnType<typeof getPostgresPool>>): Promise<void> {
  const result = await pool.query(`
    SELECT
      EXISTS (
        SELECT 1
        FROM juhe_business.system_accounts
        WHERE id = 'sys_admin' AND status = 'active'
      ) AS admin_exists,
      EXISTS (
        SELECT 1
        FROM juhe_business.provider_protocol_profiles
        WHERE id = $1 AND provider_code = 'gpt'
      ) AS profile_exists
  `, [GPT_OPENAI_V1_PROFILE_ID])
  const row = result.rows[0] as { admin_exists?: boolean; profile_exists?: boolean } | undefined
  assert.equal(row?.admin_exists, true, 'account error policy PG smoke 需要已初始化且 active 的 sys_admin；脚本不会修改共享默认数据')
  assert.equal(row?.profile_exists, true, `account error policy PG smoke 需要已初始化的 ${GPT_OPENAI_V1_PROFILE_ID} 协议档案；脚本不会执行全局 seed`)
}

async function insertSmokeSystemAccount(id: string): Promise<void> {
  const pool = await getPostgresPool()
  const now = new Date().toISOString()
  await pool.query(`
    INSERT INTO juhe_business.system_accounts (
      id, username, display_name, role, status, password_hash, must_change_password,
      image_generation_enabled, created_at, updated_at
    ) VALUES ($1, $2, $3, 'user', 'active', $4, 0, 0, $5, $5)
  `, [id, `smoke_${marker}`, `account error observation PG ${marker}`, 'pg-smoke-password-hash', now])
}

async function findAuthorizedInstance(sourceAccountId: string, granteeSystemAccountId: string): Promise<{ id: string; authorizationId: string }> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT id, authorization_instance_authorization_id
    FROM juhe_business.accounts
    WHERE authorization_instance_source_account_id = $1
      AND system_account_id = $2
      AND deleted_at IS NULL
    ORDER BY created_at DESC, id DESC
    LIMIT 1
  `, [sourceAccountId, granteeSystemAccountId])
  const id = typeof result.rows[0]?.id === 'string' ? result.rows[0].id : undefined
  const authorizationId = typeof result.rows[0]?.authorization_instance_authorization_id === 'string'
    ? result.rows[0].authorization_instance_authorization_id
    : undefined
  assert(id, 'PG 账户授权必须创建隔离的被授权实例')
  assert(authorizationId, 'PG 被授权实例必须关联运行时授权记录')
  return { id, authorizationId }
}

async function seedAuthorizationQuotaExhaustion(systemAccountId: string, authorizationId: string): Promise<void> {
  const pool = await getPostgresPool()
  const now = new Date().toISOString()
  await pool.query(`
    INSERT INTO juhe_stats.usage_stats_totals (
      system_account_id, scope_type, scope_id, request_count, total_cost_usd, last_used_at, updated_at
    ) VALUES ($1, 'account_authorization', $2, 1, 1, $3, $3)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      request_count = GREATEST(usage_stats_totals.request_count, EXCLUDED.request_count),
      total_cost_usd = GREATEST(usage_stats_totals.total_cost_usd, EXCLUDED.total_cost_usd),
      last_used_at = EXCLUDED.last_used_at,
      updated_at = EXCLUDED.updated_at
  `, [systemAccountId, authorizationId, now])
}

async function seedCooldownRetestPaginationBarrierFixtures(input: {
  groupId: string
  templateAccountId: string
  baseMs: number
}): Promise<{ accountIds: string[]; authorizationIds: string[] }> {
  const pool = await getPostgresPool()
  const accountIds = Array.from(
    { length: 201 },
    (_, index) => `account_${marker}_cooldown_barrier_${index}`
  )
  const cooldownUntilValues = accountIds.map((_, index) => new Date(input.baseMs + index).toISOString())
  const observationStartedAt = new Date(input.baseMs - 30_000).toISOString()
  const inserted = await pool.query(`
    WITH template AS (
      SELECT account_row
      FROM juhe_business.accounts AS account_row
      WHERE account_row.id = $1
    ), generated AS (
      SELECT generated.id, generated.cooldown_until
      FROM unnest($3::text[], $4::timestamptz[]) AS generated(id, cooldown_until)
    )
    INSERT INTO juhe_business.accounts
    SELECT (jsonb_populate_record(
      NULL::juhe_business.accounts,
      to_jsonb(template.account_row) || jsonb_build_object(
        'id', generated.id,
        'name', 'cooldown pagination barrier ' || generated.id,
        'status', 'temporary_unavailable',
        'schedulable', 1,
        'cooldown_until', generated.cooldown_until,
        'cooldown_retest_failure_count', 0,
        'cooldown_retest_observation_started_at', $2::timestamptz,
        'cooldown_retest_generation', 'cooldown:' || generated.id,
        'cooldown_retest_last_at', NULL,
        'cooldown_retest_last_status_code', NULL,
        'created_at', generated.cooldown_until,
        'updated_at', generated.cooldown_until
      )
    )).*
    FROM template
    CROSS JOIN generated
    RETURNING id
  `, [input.templateAccountId, observationStartedAt, accountIds, cooldownUntilValues])
  const insertedIds = inserted.rows
    .map((row) => typeof row.id === 'string' ? row.id : undefined)
    .filter((id): id is string => Boolean(id))
  assert.equal(insertedIds.length, accountIds.length, 'PG cooldown raw cursor 屏障账户必须完整创建')
  await pool.query(`
    WITH template AS (
      SELECT binding_row
      FROM juhe_business.group_accounts AS binding_row
      WHERE binding_row.group_id = $1 AND binding_row.account_id = $2
    ), generated AS (
      SELECT generated.id, generated.created_at
      FROM unnest($3::text[], $4::timestamptz[]) AS generated(id, created_at)
    )
    INSERT INTO juhe_business.group_accounts
    SELECT (jsonb_populate_record(
      NULL::juhe_business.group_accounts,
      to_jsonb(template.binding_row) || jsonb_build_object(
        'account_id', generated.id,
        'created_at', generated.created_at,
        'updated_at', generated.created_at
      )
    )).*
    FROM template
    CROSS JOIN generated
  `, [input.groupId, input.templateAccountId, accountIds, cooldownUntilValues])
  return { accountIds, authorizationIds: [] }
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  const cleanupAccountIds = new Set([...createdAccountIds, ...bulkCooldownScanAccountIds])
  if (createdSystemAccountIds.length > 0) {
    const discovered = await pool.query(`
      SELECT id
      FROM juhe_business.accounts
      WHERE system_account_id = ANY($1::text[])
    `, [createdSystemAccountIds])
    for (const row of discovered.rows) {
      if (typeof row.id === 'string') cleanupAccountIds.add(row.id)
    }
  }
  const accountIds = [...cleanupAccountIds]
  const cleanupAuthorizationIds = new Set(createdAuthorizationIds)
  if (accountIds.length > 0 || createdSystemAccountIds.length > 0) {
    const discoveredAuthorizations = await pool.query(`
      SELECT id
      FROM juhe_business.resource_authorizations
      WHERE resource_id = ANY($1::text[])
        OR grantee_system_account_id = ANY($2::text[])
    `, [accountIds, createdSystemAccountIds])
    for (const row of discoveredAuthorizations.rows) {
      if (typeof row.id === 'string') cleanupAuthorizationIds.add(row.id)
    }
  }
  const authorizationIds = [...cleanupAuthorizationIds]
  if (authorizationIds.length > 0) {
    await pool.query("DELETE FROM juhe_stats.usage_stats_totals WHERE scope_type = 'account_authorization' AND scope_id = ANY($1::text[])", [authorizationIds])
  }
  if (accountIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = ANY($1::text[])', [accountIds])
    const orderedAccounts = await pool.query(`
      SELECT id
      FROM juhe_business.accounts
      WHERE id = ANY($1::text[])
      ORDER BY (authorization_instance_source_account_id IS NULL) ASC, id ASC
    `, [accountIds])
    const orderedAccountIds = orderedAccounts.rows
      .map((row) => typeof row.id === 'string' ? row.id : undefined)
      .filter((id): id is string => Boolean(id))
    for (const accountId of orderedAccountIds) {
      await deleteAccountAsync(accountId, access).catch(() => false)
    }
    await pool.query('DELETE FROM juhe_business.account_api_key_runtime_states WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_supported_models WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_model_mappings WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_tag_bindings WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_terms WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_documents WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [accountIds])
  }
  if (accountIds.length > 0 || createdSystemAccountIds.length > 0) {
    await pool.query(`
      DELETE FROM juhe_business.resource_authorization_sources
      WHERE authorization_id IN (
        SELECT id
        FROM juhe_business.resource_authorizations
        WHERE resource_id = ANY($1::text[])
          OR grantee_system_account_id = ANY($2::text[])
      )
    `, [accountIds, createdSystemAccountIds])
    await pool.query(`
      DELETE FROM juhe_business.resource_authorizations
      WHERE resource_id = ANY($1::text[])
        OR grantee_system_account_id = ANY($2::text[])
    `, [accountIds, createdSystemAccountIds])
    await pool.query(`
      DELETE FROM juhe_business.resource_authorization_grants
      WHERE resource_id = ANY($1::text[])
        OR grantee_system_account_id = ANY($2::text[])
    `, [accountIds, createdSystemAccountIds])
  }
  for (const groupId of createdGroupIds) {
    await deleteGroupAsync(groupId, access).catch(() => undefined)
  }
  if (createdGroupIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = ANY($1::text[])', [createdGroupIds])
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE group_id = ANY($1::text[])', [createdGroupIds])
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [createdGroupIds])
  }
  if (createdSystemAccountIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = ANY($1::text[])', [createdSystemAccountIds])
  }
}
