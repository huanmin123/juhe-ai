import { strict as assert } from 'node:assert'
import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { notifyGatewayRuntimeCacheInvalidationAsync } from '../../shared/gateway-cache-invalidation.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { encryptJson } from '../../storage/crypto.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createGroupAsync,
  deferCooldownAccountRetestAsync,
  deleteGroupAsync,
  findAccountForCooldownRetestAsync,
  recordCooldownAccountRetestFailureAsync,
  recordCooldownAccountRetestSuccessAsync,
  setAccountGroupAsync
} from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '冷却复测 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')
assert.equal(
  process.env.JUHE_AI_ALLOW_COOLDOWN_RETEST_POSTGRES_SMOKE,
  '1',
  '冷却复测 PG smoke 会写测试 fixture，必须显式设置 JUHE_AI_ALLOW_COOLDOWN_RETEST_POSTGRES_SMOKE=1'
)

const marker = `cooldown_retest_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const accountId = `acc_${marker}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
let fixtureCreated = false
let fixtureGroupId: string | undefined

type RuntimeState = {
  status: string
  schedulable: number
  config_revision: number
  dispatch_revision: number
  cooldown_until: string | null
  cooldown_retest_failure_count: number
  cooldown_retest_observation_started_at: string | null
  cooldown_retest_generation: string | null
  last_error_code: string | null
}

type CooldownGuard = {
  expectedConfigRevision: number
  expectedDispatchRevision: number
  expectedObservationStartedAt: string
  expectedGeneration: string
}

try {
  const pool = await getPostgresPool()
  await assertCooldownRetestSchema(pool)
  const profileResult = await pool.query(`
    SELECT id, protocol_code, protocol_version
    FROM juhe_business.provider_protocol_profiles
    WHERE provider_code = 'gpt'
    ORDER BY id ASC
    LIMIT 1
  `)
  const profile = profileResult.rows[0] as {
    id: string
    protocol_code: string
    protocol_version: string
  } | undefined
  assert(profile, '冷却复测 PG smoke 需要已初始化的 GPT 协议档案')

  const fixtureGroup = await createGroupAsync({
    name: `冷却复测 PG smoke 分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  fixtureGroupId = fixtureGroup.id

  const now = new Date().toISOString()
  const observationStartedAt = new Date(Date.now() - 60_000).toISOString()
  const generation = newCooldownGeneration()
  await pool.query(`
    INSERT INTO juhe_business.accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id,
      protocol_code, protocol_version, name, type, status, schedulable,
      credentials_encrypted, health_check_model, health_check_endpoint_mode,
      config_revision, dispatch_revision, cooldown_until,
      cooldown_retest_observation_started_at, cooldown_retest_generation,
      created_at, updated_at
    ) VALUES ($1, 'sys_admin', 'gpt', $2, $3, $4, $5, 'api_key',
      'temporary_unavailable', 1, $6, 'gpt-5-mini', 'responses_json',
      1, 1, $7, $8, $9, $10, $10)
  `, [
    accountId,
    profile.id,
    profile.protocol_code,
    profile.protocol_version,
    `冷却复测PG写回烟测${marker}`,
    encryptJson({ api_key: `sk-${marker}`, base_url: 'https://example.invalid/v1' }),
    new Date(Date.now() - 1_000).toISOString(),
    observationStartedAt,
    generation,
    now
  ])
  fixtureCreated = true
  const boundAccount = await setAccountGroupAsync(accountId, fixtureGroup.id, access)
  assert.equal(boundAccount?.boundGroupId, fixtureGroup.id, '冷却复测 PG fixture 必须绑定隔离分组，候选读取才可验证原子修复结果')

  const initialGuard = await readCooldownGuard(accountId)
  const currentFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 403,
    errorCode: 'insufficient_user_quota',
    errorMessage: 'PG cooldown writeback smoke',
    ...initialGuard,
    initialBackoffSeconds: 1,
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(currentFailure.changed, true, 'PG 当前冷却失败应写回')
  assert.equal(currentFailure.failureCount, 1, 'PG 当前冷却失败应累加计数')

  const beforeStaleFailure = await readRuntimeState(accountId)
  const staleConfigFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 403,
    errorMessage: 'stale config failure',
    ...initialGuard,
    expectedConfigRevision: initialGuard.expectedConfigRevision + 1,
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(staleConfigFailure.changed, false, 'PG 陈旧配置版本的失败不得写回')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleFailure, 'PG 陈旧配置版本不得改变运行态')

  const staleObservationFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 403,
    errorMessage: 'stale observation failure',
    ...initialGuard,
    expectedObservationStartedAt: new Date(Date.now() - 120_000).toISOString(),
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(staleObservationFailure.changed, false, 'PG 陈旧观察窗口的失败不得写回')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleFailure, 'PG 陈旧观察窗口不得改变运行态')

  const staleDispatchFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 403,
    errorMessage: 'stale dispatch failure',
    ...initialGuard,
    expectedDispatchRevision: initialGuard.expectedDispatchRevision + 1,
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(staleDispatchFailure.changed, false, 'PG 陈旧派发版本的失败不得写回')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleFailure, 'PG 陈旧派发版本不得改变运行态')

  const staleGenerationFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 403,
    errorMessage: 'stale generation failure',
    ...initialGuard,
    expectedGeneration: newCooldownGeneration(),
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(staleGenerationFailure.changed, false, 'PG 陈旧冷却代次的失败不得写回')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleFailure, 'PG 陈旧冷却代次不得改变运行态')

  const ownerSourceRevisionFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 403,
    errorMessage: 'owner source revision mismatch',
    ...initialGuard,
    expectedSourceConfigRevision: 1,
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(ownerSourceRevisionFailure.changed, false, 'PG owner 账户不得接受伪造来源配置版本')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleFailure, 'PG owner 来源版本失配不得改变运行态')

  const staleConfigDefer = await deferCooldownAccountRetestAsync(accountId, {
    ...initialGuard,
    expectedConfigRevision: initialGuard.expectedConfigRevision + 1,
    delaySeconds: 3
  })
  assert.equal(staleConfigDefer.changed, false, 'PG 陈旧配置版本的 defer 不得改写冷却时间')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleFailure, 'PG 陈旧配置版本 defer 不得改变运行态')

  const staleObservationDefer = await deferCooldownAccountRetestAsync(accountId, {
    ...initialGuard,
    expectedObservationStartedAt: new Date(Date.now() - 120_000).toISOString(),
    delaySeconds: 3
  })
  assert.equal(staleObservationDefer.changed, false, 'PG 陈旧观察窗口的 defer 不得改写冷却时间')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleFailure, 'PG 陈旧观察窗口 defer 不得改变运行态')

  const staleDispatchDefer = await deferCooldownAccountRetestAsync(accountId, {
    ...initialGuard,
    expectedDispatchRevision: initialGuard.expectedDispatchRevision + 1,
    delaySeconds: 3
  })
  assert.equal(staleDispatchDefer.changed, false, 'PG 陈旧派发版本的 defer 不得改写冷却时间')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleFailure, 'PG 陈旧派发版本 defer 不得改变运行态')

  const staleGenerationDefer = await deferCooldownAccountRetestAsync(accountId, {
    ...initialGuard,
    expectedGeneration: newCooldownGeneration(),
    delaySeconds: 3
  })
  assert.equal(staleGenerationDefer.changed, false, 'PG 陈旧冷却代次的 defer 不得改写冷却时间')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleFailure, 'PG 陈旧冷却代次 defer 不得改变运行态')

  const successObservationStartedAt = new Date(Date.now() - 30_000).toISOString()
  await resetCoolingState(accountId, successObservationStartedAt, newCooldownGeneration())
  const successGuard = await readCooldownGuard(accountId)
  const beforeStaleSuccess = await readRuntimeState(accountId)
  const staleConfigSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, {
    ...successGuard,
    expectedConfigRevision: successGuard.expectedConfigRevision + 1
  })
  assert.equal(staleConfigSuccess.changed, false, 'PG 陈旧配置版本的成功不得恢复账户')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleSuccess, 'PG 陈旧配置版本的成功不得改变运行态')

  const staleObservationSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, {
    ...successGuard,
    expectedObservationStartedAt: new Date(Date.now() - 120_000).toISOString()
  })
  assert.equal(staleObservationSuccess.changed, false, 'PG 陈旧观察窗口的成功不得恢复账户')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleSuccess, 'PG 陈旧观察窗口的成功不得改变运行态')

  const staleDispatchSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, {
    ...successGuard,
    expectedDispatchRevision: successGuard.expectedDispatchRevision + 1
  })
  assert.equal(staleDispatchSuccess.changed, false, 'PG 陈旧派发版本的成功不得恢复账户')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleSuccess, 'PG 陈旧派发版本不得改变运行态')

  const staleGenerationSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, {
    ...successGuard,
    expectedGeneration: newCooldownGeneration()
  })
  assert.equal(staleGenerationSuccess.changed, false, 'PG 陈旧冷却代次的成功不得恢复账户')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleSuccess, 'PG 陈旧冷却代次不得改变运行态')

  const ownerSourceRevisionSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, {
    ...successGuard,
    expectedSourceConfigRevision: 1
  })
  assert.equal(ownerSourceRevisionSuccess.changed, false, 'PG owner 账户不得接受伪造来源配置版本的成功')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleSuccess, 'PG owner 来源版本失配成功不得改变运行态')

  const currentSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, {
    ...successGuard
  })
  assert.equal(currentSuccess.changed, true, 'PG 当前冷却成功应恢复账户')
  const restoredState = await readRuntimeState(accountId)
  assert.equal(restoredState.status, 'active', 'PG 当前冷却成功应恢复 active')
  assert.equal(restoredState.schedulable, 1, 'PG 当前冷却成功应恢复调度')
  assert.equal(restoredState.cooldown_until, null, 'PG 当前冷却成功应清理冷却时间')
  assert.equal(restoredState.cooldown_retest_failure_count, 0, 'PG 当前冷却成功应清零失败计数')
  assert.equal(restoredState.cooldown_retest_observation_started_at, null, 'PG 当前冷却成功应清理观察窗口')
  assert.equal(restoredState.cooldown_retest_generation, null, 'PG 当前冷却成功应清理冷却代次')
  assert.equal(restoredState.last_error_code, null, 'PG 当前冷却成功应清理错误码')

  const sharedObservationStartedAt = new Date(Date.now() - 30_000).toISOString()
  await resetCoolingState(accountId, sharedObservationStartedAt, newCooldownGeneration())
  const oldSameTimestampGuard = await readCooldownGuard(accountId)
  await resetCoolingState(accountId, sharedObservationStartedAt, newCooldownGeneration())
  const currentSameTimestampGuard = await readCooldownGuard(accountId)
  assert.equal(currentSameTimestampGuard.expectedObservationStartedAt, oldSameTimestampGuard.expectedObservationStartedAt, 'PG 两个冷却 episode 可以拥有相同观察时间')
  assert.notEqual(currentSameTimestampGuard.expectedGeneration, oldSameTimestampGuard.expectedGeneration, 'PG 相同观察时间的两个冷却 episode 必须拥有不同代次')
  const beforeSameTimestampReplay = await readRuntimeState(accountId)
  const replayedSameTimestampFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 503,
    errorMessage: 'same timestamp stale generation failure',
    ...oldSameTimestampGuard,
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(replayedSameTimestampFailure.changed, false, 'PG 相同观察时间下旧 generation 的 failure 不得命中新 episode')
  const replayedSameTimestampDefer = await deferCooldownAccountRetestAsync(accountId, {
    ...oldSameTimestampGuard,
    delaySeconds: 60
  })
  assert.equal(replayedSameTimestampDefer.changed, false, 'PG 相同观察时间下旧 generation 的 defer 不得命中新 episode')
  const replayedSameTimestampSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, oldSameTimestampGuard)
  assert.equal(replayedSameTimestampSuccess.changed, false, 'PG 相同观察时间下旧 generation 的 success 不得命中新 episode')
  assert.deepEqual(await readRuntimeState(accountId), beforeSameTimestampReplay, 'PG 相同观察时间下三类旧结果都不得改变新 episode')
  const currentSameTimestampSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, currentSameTimestampGuard)
  assert.equal(currentSameTimestampSuccess.changed, true, 'PG 相同观察时间下只有当前 generation 可以恢复账户')

  await resetCoolingState(accountId, null, null)
  const repairedNullCandidate = await findAccountForCooldownRetestAsync(accountId)
  assert(repairedNullCandidate, 'PG NULL legacy 状态修复后应返回当前复测候选')
  const repairedNullState = await readRuntimeState(accountId)
  assert(repairedNullState.cooldown_retest_observation_started_at, 'PG NULL 观察窗口应在候选扫描前原子自愈')
  assert(repairedNullState.cooldown_retest_generation, 'PG NULL 冷却代次应在候选扫描前原子自愈')
  assert.equal(repairedNullCandidate.cooldownRetestObservationStartedAt, repairedNullState.cooldown_retest_observation_started_at, 'PG NULL legacy 候选必须携带数据库最终观察窗口')
  assert.equal(repairedNullCandidate.cooldownRetestGeneration, repairedNullState.cooldown_retest_generation, 'PG NULL legacy 候选必须携带数据库最终冷却代次')
  const repairedNullGeneration = repairedNullState.cooldown_retest_generation

  const poolForLegacy = await getPostgresPool()
  await poolForLegacy.query(`
    UPDATE juhe_business.accounts
    SET cooldown_retest_observation_started_at = '', cooldown_retest_generation = '',
        cooldown_until = $1, updated_at = $1
    WHERE id = $2
  `, [new Date(Date.now() - 1_000).toISOString(), accountId])
  const concurrentEmptyCandidates = await Promise.all([
    findAccountForCooldownRetestAsync(accountId),
    findAccountForCooldownRetestAsync(accountId)
  ])
  const repairedEmptyState = await readRuntimeState(accountId)
  assert(repairedEmptyState.cooldown_retest_observation_started_at, 'PG 空观察窗口应在并发候选扫描前原子自愈')
  assert(repairedEmptyState.cooldown_retest_generation, 'PG 空冷却代次应在并发候选扫描前原子自愈')
  assert.notEqual(repairedEmptyState.cooldown_retest_generation, repairedNullGeneration, 'PG 每次 legacy 冷却 episode 自愈都应生成新代次')
  assert(concurrentEmptyCandidates.some(Boolean), 'PG 并发 legacy 修复至少应有一个调用读到修复后的候选')
  for (const candidate of concurrentEmptyCandidates.filter((item) => item !== undefined)) {
    assert.equal(candidate.cooldownRetestObservationStartedAt, repairedEmptyState.cooldown_retest_observation_started_at, 'PG 并发 legacy 候选不得返回分裂的观察窗口')
    assert.equal(candidate.cooldownRetestGeneration, repairedEmptyState.cooldown_retest_generation, 'PG 并发 legacy 候选不得返回分裂的冷却代次')
  }
  const stableEmptyCandidate = await findAccountForCooldownRetestAsync(accountId)
  assert(stableEmptyCandidate, 'PG 并发 legacy 修复后稳定复读必须仍返回候选')
  assert.equal(stableEmptyCandidate.cooldownRetestObservationStartedAt, repairedEmptyState.cooldown_retest_observation_started_at, 'PG 并发 legacy 修复后的稳定复读必须保留同一观察窗口')
  assert.equal(stableEmptyCandidate.cooldownRetestGeneration, repairedEmptyState.cooldown_retest_generation, 'PG 并发 legacy 修复后的稳定复读必须保留同一冷却代次')

  const semanticInvalidObservation = '2026-13-01T00:00:00.000Z'
  const semanticInvalidGeneration = newCooldownGeneration()
  await poolForLegacy.query(`
    UPDATE juhe_business.accounts
    SET cooldown_retest_observation_started_at = $1, cooldown_retest_generation = $2,
        cooldown_until = $3, updated_at = $3
    WHERE id = $4
  `, [semanticInvalidObservation, semanticInvalidGeneration, new Date(Date.now() - 1_000).toISOString(), accountId])
  const repairedSemanticCandidate = await findAccountForCooldownRetestAsync(accountId)
  assert(repairedSemanticCandidate, 'PG 语义非法 legacy 时间修复后应返回当前复测候选')
  const repairedSemanticState = await readRuntimeState(accountId)
  assert(repairedSemanticState.cooldown_retest_observation_started_at, 'PG 语义非法 legacy 时间必须自愈为有效观察窗口')
  assert(Number.isFinite(Date.parse(repairedSemanticState.cooldown_retest_observation_started_at)), 'PG 语义非法 legacy 时间自愈结果必须可解析')
  assert.notEqual(repairedSemanticState.cooldown_retest_observation_started_at, semanticInvalidObservation, 'PG 月份越界但外形 canonical 的 legacy 时间不得原样保留')
  assert(repairedSemanticState.cooldown_retest_generation, 'PG 语义非法 legacy 时间自愈必须生成有效冷却代次')
  assert.notEqual(repairedSemanticState.cooldown_retest_generation, semanticInvalidGeneration, 'PG 语义非法 legacy 时间自愈必须推进冷却代次')
  assert.equal(repairedSemanticCandidate.cooldownRetestObservationStartedAt, repairedSemanticState.cooldown_retest_observation_started_at, 'PG 语义非法 legacy 候选必须携带修复后的观察窗口')
  assert.equal(repairedSemanticCandidate.cooldownRetestGeneration, repairedSemanticState.cooldown_retest_generation, 'PG 语义非法 legacy 候选必须携带修复后的冷却代次')

  const legacyGenerationWhitespaceCases = [
    { label: 'tab', value: '\t' },
    { label: 'LF', value: '\n' },
    { label: 'CRLF', value: '\r\n' },
    { label: '混合前后空白', value: ` \t${newCooldownGeneration()}\r\n ` }
  ]
  for (const legacyCase of legacyGenerationWhitespaceCases) {
    const validObservationStartedAt = new Date(Date.now() - 30_000).toISOString()
    await poolForLegacy.query(`
      UPDATE juhe_business.accounts
      SET cooldown_retest_observation_started_at = $1, cooldown_retest_generation = $2,
          cooldown_until = $3, updated_at = $3
      WHERE id = $4
    `, [validObservationStartedAt, legacyCase.value, new Date(Date.now() - 1_000).toISOString(), accountId])
    const repairedCandidate = await findAccountForCooldownRetestAsync(accountId)
    assert(repairedCandidate, `PG ${legacyCase.label} generation 污染修复后应返回当前复测候选`)
    const repairedState = await readRuntimeState(accountId)
    assert.equal(repairedState.cooldown_retest_observation_started_at, validObservationStartedAt, `PG ${legacyCase.label} generation 污染修复不得改写有效观察窗口`)
    assert(repairedState.cooldown_retest_generation, `PG ${legacyCase.label} generation 污染必须生成有效冷却代次`)
    assert.notEqual(repairedState.cooldown_retest_generation, legacyCase.value, `PG ${legacyCase.label} generation 污染不得原样保留`)
    assert.equal(repairedState.cooldown_retest_generation, repairedState.cooldown_retest_generation.trim(), `PG ${legacyCase.label} generation 自愈结果不得带首尾空白`)
    assert.equal(repairedCandidate.cooldownRetestObservationStartedAt, repairedState.cooldown_retest_observation_started_at, `PG ${legacyCase.label} generation 修复候选必须携带最终观察窗口`)
    assert.equal(repairedCandidate.cooldownRetestGeneration, repairedState.cooldown_retest_generation, `PG ${legacyCase.label} generation 修复候选必须携带最终持久 token`)
  }

  const legacyGuard = await readCooldownGuard(accountId)
  const missingGenerationFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 503,
    errorMessage: 'missing generation failure',
    ...legacyGuard,
    expectedGeneration: '',
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(missingGenerationFailure.changed, false, 'PG 缺少有效观察代次的失败不得写回账户')

  const deferWithoutObservation = await deferCooldownAccountRetestAsync(accountId, {
    ...legacyGuard,
    expectedObservationStartedAt: '',
    delaySeconds: 3
  })
  assert.equal(deferWithoutObservation.changed, false, 'PG 缺少有效观察代次的 defer 不得改写账户')

  const successWithoutObservation = await recordCooldownAccountRetestSuccessAsync(accountId, {
    ...legacyGuard,
    expectedObservationStartedAt: ''
  })
  assert.equal(successWithoutObservation.changed, false, 'PG 缺少有效观察代次的成功不得恢复账户')

  const backoffGuard = await resetCoolingStateAndReadGuard(accountId)
  const expectedBackoffSequence = [3, 6, 12, 24, 48, 60]
  let firstCappedFailure: Awaited<ReturnType<typeof recordCooldownAccountRetestFailureAsync>> | undefined
  for (const [index, expectedBackoffSeconds] of expectedBackoffSequence.entries()) {
    const failure = await recordCooldownAccountRetestFailureAsync(accountId, {
      statusCode: 503,
      errorMessage: `backoff sequence failure ${index + 1}`,
      ...backoffGuard,
      initialBackoffSeconds: 3,
      backoffMultiplier: 2,
      maxPauseMinutes: 1,
      maxRecoveryHours: 12
    })
    assert.equal(failure.changed, true, `PG 第 ${index + 1} 次连续失败应写回`)
    assert.equal(failure.failureCount, index + 1, `PG 第 ${index + 1} 次连续失败计数必须准确`)
    assert.equal(failure.backoffSeconds, expectedBackoffSeconds, `PG 第 ${index + 1} 次连续失败退避应为 ${expectedBackoffSeconds} 秒`)
    if (expectedBackoffSeconds < 60) {
      assert.equal(failure.maxedFailureCount, 0, `PG 第 ${index + 1} 次未封顶失败不得累计 maxedFailureCount`)
    } else if (!firstCappedFailure) {
      firstCappedFailure = failure
    }
  }
  assert(firstCappedFailure, 'PG 连续失败序列必须到达首个退避封顶点')
  assert.equal(firstCappedFailure.failureCount, 6, 'PG initial=3、multiplier=2、max=60 的首个封顶 failureCount 必须为 6')
  assert.equal(firstCappedFailure.maxedFailureCount, 1, 'PG 首次退避封顶时 maxedFailureCount 必须从 1 开始')

  const deferFirstGuard = await resetCoolingStateAndReadGuard(accountId)
  const deferFirst = await deferCooldownAccountRetestAsync(accountId, {
    ...deferFirstGuard,
    delaySeconds: 60
  })
  assert.equal(deferFirst.changed, true, 'PG defer-first 应写入较长冷却时间')
  const deferredUntil = (await readRuntimeState(accountId)).cooldown_until
  assert(deferredUntil, 'PG defer-first 应保留冷却时间')
  const failureAfterDefer = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 503,
    errorMessage: 'failure after defer',
    ...deferFirstGuard,
    initialBackoffSeconds: 3,
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(failureAfterDefer.changed, true, 'PG defer-first 后失败仍应写回诊断')
  const afterFailureAfterDefer = await readRuntimeState(accountId)
  assert(afterFailureAfterDefer.cooldown_until && afterFailureAfterDefer.cooldown_until >= deferredUntil, 'PG failure-after-defer 不得缩短已有冷却 TTL')

  const failureFirstGuard = await resetCoolingStateAndReadGuard(accountId)
  const failureFirst = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 503,
    errorMessage: 'failure before defer',
    ...failureFirstGuard,
    initialBackoffSeconds: 30,
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(failureFirst.changed, true, 'PG failure-first 应写入冷却时间')
  const failureFirstState = await readRuntimeState(accountId)
  assert(failureFirstState.cooldown_until, 'PG failure-first 应保留冷却时间')
  const deferAfterFailure = await deferCooldownAccountRetestAsync(accountId, {
    ...failureFirstGuard,
    delaySeconds: 3
  })
  assert.equal(deferAfterFailure.changed, false, 'PG failure-first 后较短 defer 不得覆盖冷却时间')
  assert.equal((await readRuntimeState(accountId)).cooldown_until, failureFirstState.cooldown_until, 'PG defer-after-failure 不得缩短已有冷却 TTL')

  const terminalGuard = await resetCoolingStateAndReadGuard(accountId, new Date(Date.now() - 8 * 24 * 60 * 60_000).toISOString())
  const terminalFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 503,
    errorMessage: 'terminal cooldown timeout',
    ...terminalGuard,
    maxPauseMinutes: 1,
    maxRecoveryHours: 1
  })
  assert.equal(terminalFailure.changed, true, 'PG 超过最大恢复窗口的失败应进入 error')
  assert.equal(terminalFailure.recoveryStage, 'terminal', 'PG 超过最大恢复窗口应标记终态')
  const terminalState = await readRuntimeState(accountId)
  assert.equal(terminalState.status, 'error', 'PG 终态冷却账户应为 error')
  assert.equal(terminalState.schedulable, 0, 'PG 终态冷却账户不得继续调度')

  const lateFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 503,
    errorMessage: 'late failure after terminal',
    ...terminalGuard,
    maxPauseMinutes: 1,
    maxRecoveryHours: 1
  })
  assert.equal(lateFailure.changed, false, 'PG 终态后的迟到 failure 不得再次写入')
  const lateDefer = await deferCooldownAccountRetestAsync(accountId, {
    ...terminalGuard,
    delaySeconds: 60
  })
  assert.equal(lateDefer.changed, false, 'PG 终态后的迟到 defer 不得改写冷却')
  const lateSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, terminalGuard)
  assert.equal(lateSuccess.changed, false, 'PG 终态后的迟到 success 不得恢复账户')
  assert.deepEqual(await readRuntimeState(accountId), terminalState, 'PG 终态后的三类迟到写都不得改变账户')

  console.log(JSON.stringify({
    message: '冷却复测 PostgreSQL 写回 smoke 通过',
    currentFailure: true,
    currentSuccess: true,
    staleConfigGuard: true,
    staleObservationGuard: true,
    staleDispatchGuard: true,
    staleGenerationGuard: true,
    ownerSourceRevisionGuard: true,
    sameTimestampGenerationFence: true,
    legacyNullEmptyAndSemanticRepair: true,
    legacyGenerationWhitespaceRepair: true,
    initialAndCappedBackoffSequence: true,
    monotonicDeferFailureTtl: true,
    terminalLateWritesRejected: true,
    staleDeferGuard: true,
    requiredFailureObservation: true,
    requiredDeferObservation: true,
    requiredSuccessObservation: true
  }))
} finally {
  let cleanupError: unknown
  try {
    if (fixtureCreated) {
      const pool = await getPostgresPool()
      await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = $1', [accountId])
      await pool.query('DELETE FROM juhe_business.accounts WHERE id = $1', [accountId])
      await notifyGatewayRuntimeCacheInvalidationAsync('cooldown_retest_postgres_smoke_cleanup').catch(() => undefined)
    }
    if (fixtureGroupId) {
      await deleteGroupAsync(fixtureGroupId, access).catch(() => undefined)
      const pool = await getPostgresPool()
      await pool.query('DELETE FROM juhe_business.group_accounts WHERE group_id = $1', [fixtureGroupId])
      await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = $1', [fixtureGroupId])
      await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE group_id = $1', [fixtureGroupId])
      await pool.query('DELETE FROM juhe_business.groups WHERE id = $1', [fixtureGroupId])
    }
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

async function resetCoolingState(id: string, observationStartedAt: string | null = new Date(Date.now() - 30_000).toISOString(), generation: string | null = newCooldownGeneration()): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'temporary_unavailable', schedulable = 1,
        cooldown_until = $1, cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = $2,
        cooldown_retest_generation = $3,
        cooldown_retest_last_at = NULL, cooldown_retest_last_status_code = NULL,
        last_error_code = NULL, last_error_message = NULL, last_error_trace_id = NULL,
        updated_at = $1
    WHERE id = $4
  `, [new Date(Date.now() - 1_000).toISOString(), observationStartedAt, generation, id])
}

async function resetCoolingStateAndReadGuard(id: string, observationStartedAt = new Date(Date.now() - 30_000).toISOString()): Promise<CooldownGuard> {
  await resetCoolingState(id, observationStartedAt, newCooldownGeneration())
  return await readCooldownGuard(id)
}

async function readRuntimeState(id: string): Promise<RuntimeState> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT status, schedulable, config_revision, dispatch_revision, cooldown_until,
      cooldown_retest_failure_count, cooldown_retest_observation_started_at,
      cooldown_retest_generation, last_error_code
    FROM juhe_business.accounts
    WHERE id = $1
  `, [id])
  const row = result.rows[0] as Record<string, string | number | Date | null> | undefined
  assert(row, '冷却复测 PG smoke 应读回测试账户')
  return {
    status: String(row.status),
    schedulable: Number(row.schedulable ?? 0),
    config_revision: Number(row.config_revision ?? 1),
    dispatch_revision: Number(row.dispatch_revision ?? 1),
    cooldown_until: timestampString(row.cooldown_until),
    cooldown_retest_failure_count: Number(row.cooldown_retest_failure_count ?? 0),
    cooldown_retest_observation_started_at: timestampString(row.cooldown_retest_observation_started_at),
    cooldown_retest_generation: typeof row.cooldown_retest_generation === 'string' ? row.cooldown_retest_generation : null,
    last_error_code: typeof row.last_error_code === 'string' ? row.last_error_code : null
  }
}

async function readCooldownGuard(id: string): Promise<CooldownGuard> {
  const state = await readRuntimeState(id)
  assert(state.cooldown_retest_observation_started_at, 'PG cooldown fixture 必须有有效观察窗口')
  assert(state.cooldown_retest_generation, 'PG cooldown fixture 必须有有效冷却代次')
  return {
    expectedConfigRevision: state.config_revision,
    expectedDispatchRevision: state.dispatch_revision,
    expectedObservationStartedAt: state.cooldown_retest_observation_started_at,
    expectedGeneration: state.cooldown_retest_generation
  }
}

function newCooldownGeneration(): string {
  return `cooldown:${randomUUID()}`
}

function timestampString(value: string | number | Date | null | undefined): string | null {
  if (value instanceof Date) return value.toISOString()
  return typeof value === 'string' ? value : null
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
  assert.equal(missing.length, 0, `冷却复测 PG smoke schema 前置缺失：${missing.join(', ')}；请先应用 Node/PG accounts schema，再运行 smoke（不会自动修改共享数据库）`)
}
