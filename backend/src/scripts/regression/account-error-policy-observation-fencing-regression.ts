import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-error-policy-observation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-error-policy-observation-fencing-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, accountErrorPolicy] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/policy/account-error-policy.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'super_admin' as const }

try {
  const group = repositories.createGroup({
    name: '显式策略观测顺序回归分组',
    providerCode: 'gpt'
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '显式策略观测顺序回归账户',
    type: 'api_key',
    status: 'active',
    schedulable: true,
    credentials: {
      api_key: 'sk-account-error-policy-observation-fencing',
      base_url: 'https://example.invalid/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-5.5']
  }, access)
  repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })

  const staleActive = gatewayAccount(group.id, account.id)
  assert(staleActive.dispatchRevision && staleActive.dispatchRevision > 0, '网关账户必须携带 dispatch revision')
  const baseMs = Date.now() + 1_000
  const failureObservedAt = iso(baseMs)
  const successObservedAt = iso(baseMs + 1_000)
  const delayedOlderSuccessObservedAt = iso(baseMs + 2_000)
  const laterFailureObservedAt = iso(baseMs + 3_000)
  const recoveryObservedAt = iso(baseMs + 4_000)

  const healthyWatermark = accountErrorPolicy.applyAccountErrorHandling(staleActive, {
    success: true,
    observedAt: successObservedAt,
    dispatchRevision: staleActive.dispatchRevision,
    trafficSource: 'gateway'
  })
  assert.equal(healthyWatermark.changed, false, 'active 健康快照成功不应伪报账户状态变化')
  assert.equal(accountState(account.id).lastHealthSuccessAt, successObservedAt, 'active 健康快照成功仍必须持久化 watermark')

  const lateFailure = applyCooldown(staleActive, failureObservedAt)
  assert.equal(lateFailure.changed, false, '迟到显式策略失败不得覆盖较新成功')
  assert.equal(accountState(account.id).status, 'active', '迟到失败后账户必须保持 active')

  const equalMillisecondFailure = applyCooldown(staleActive, successObservedAt)
  assert.equal(equalMillisecondFailure.changed, false, '同毫秒失败不得覆盖成功')
  assert.equal(accountState(account.id).status, 'active', '同毫秒失败后账户必须保持 active')

  const currentFailure = applyCooldown(staleActive, laterFailureObservedAt)
  assert.equal(currentFailure.changed, true, '较新显式策略失败仍必须按用户规则生效')
  assert.equal(accountState(account.id).status, 'temporary_unavailable', '较新显式策略失败应进入 temporary_unavailable')

  const delayedOlderSuccess = accountErrorPolicy.applyAccountErrorHandling(staleActive, {
    success: true,
    observedAt: delayedOlderSuccessObservedAt,
    dispatchRevision: staleActive.dispatchRevision,
    trafficSource: 'gateway'
  })
  assert.equal(delayedOlderSuccess.changed, false, '迟到的较旧成功不得恢复更新的显式策略失败')
  assert.equal(accountState(account.id).status, 'temporary_unavailable', '较旧成功晚到后仍应保持 temporary_unavailable')
  assert.equal(
    accountState(account.id).lastHealthSuccessAt,
    delayedOlderSuccessObservedAt,
    '较旧成功虽不能恢复状态，仍应单调推进成功 watermark'
  )

  const staleSnapshotRecovery = accountErrorPolicy.applyAccountErrorHandling(staleActive, {
    success: true,
    observedAt: recoveryObservedAt,
    dispatchRevision: staleActive.dispatchRevision,
    trafficSource: 'gateway'
  })
  assert.equal(staleSnapshotRecovery.changed, false, '旧在途请求即使较晚完成，也不得恢复用户显式 cooldown')
  assert.equal(accountState(account.id).status, 'temporary_unavailable', '普通协议成功不得恢复用户显式 temporary_unavailable')

  const newerBackgroundSuccess = accountErrorPolicy.applyAccountErrorHandling(staleActive, {
    success: true,
    observedAt: iso(baseMs + 4_500),
    dispatchRevision: staleActive.dispatchRevision,
    trafficSource: 'account_health_check'
  })
  assert.equal(newerBackgroundSuccess.changed, false, '较新后台成功不得越权恢复用户显式 cooldown')
  assert.equal(accountState(account.id).status, 'temporary_unavailable', '后台成功后用户显式 cooldown 必须保持')

  const currentExplicitCooldown = gatewayAccount(group.id, account.id)
  const backgroundSuccessWithoutObservation = accountErrorPolicy.applyAccountErrorHandling(currentExplicitCooldown, {
    success: true,
    trafficSource: 'account_health_check'
  })
  assert.equal(backgroundSuccessWithoutObservation.changed, false, '未携带观测时间的兼容调用也不得清理显式 cooldown')
  assert.equal(accountState(account.id).status, 'temporary_unavailable', '无 observation 后台成功后显式 cooldown 必须保持')

  const staleSnapshotWithoutObservation = accountErrorPolicy.applyAccountErrorHandling({
    ...staleActive,
    lastErrorMessage: '冷却前快照中的旧诊断'
  }, {
    success: true,
    trafficSource: 'account_health_check'
  })
  assert.equal(staleSnapshotWithoutObservation.changed, false, '冷却前旧快照且无 observedAt 的迟到成功不得清理库内显式 cooldown')
  assert.equal(accountState(account.id).status, 'temporary_unavailable', '库层必须拦截旧快照发起的默认 clear')

  const defaultClear = repositories.clearAccountFailureStateResult(account.id, access, { allowErrorRestore: false })
  assert.equal(defaultClear.changed, false, '默认 repository clear 不得清理用户显式 cooldown')

  const explicitCooldownState = accountState(account.id)
  assert.equal(explicitCooldownState.lastErrorCode, 'explicit_account_error_policy_cooldown', '用户显式 cooldown 必须持久化稳定来源标记')
  assert(explicitCooldownState.cooldownRetestObservationStartedAt, '用户显式 cooldown 必须持久化恢复代次')
  assert(explicitCooldownState.cooldownRetestGeneration, '用户显式 cooldown 必须持久化唯一 generation')
  const explicitCooldownGuard = cooldownRetestGuard(explicitCooldownState)
  assertCooldownRetestGuardRejectedAcrossOutcomes(account.id, {
    ...explicitCooldownGuard,
    expectedConfigRevision: explicitCooldownState.configRevision + 1
  }, '陈旧配置版本')
  assertCooldownRetestGuardRejectedAcrossOutcomes(account.id, {
    ...explicitCooldownGuard,
    expectedObservationStartedAt: new Date(Date.parse(explicitCooldownState.cooldownRetestObservationStartedAt) - 1).toISOString()
  }, '陈旧观察代次')
  assertCooldownRetestGuardRejectedAcrossOutcomes(account.id, {
    ...explicitCooldownGuard,
    expectedObservationStartedAt: ''
  }, '缺少观察代次')
  assertCooldownRetestGuardRejectedAcrossOutcomes(account.id, {
    ...explicitCooldownGuard,
    expectedGeneration: ''
  }, '缺少 generation')
  assertCooldownRetestGuardRejectedAcrossOutcomes(account.id, {
    ...explicitCooldownGuard,
    expectedDispatchRevision: explicitCooldownGuard.expectedDispatchRevision + 1
  }, '陈旧 dispatch revision')
  assertCooldownRetestGuardRejectedAcrossOutcomes(account.id, {
    ...explicitCooldownGuard,
    expectedGeneration: `${explicitCooldownGuard.expectedGeneration}-stale`
  }, '陈旧 generation')
  assert.equal(accountState(account.id).cooldownUntil, explicitCooldownState.cooldownUntil, '陈旧或缺失代次的 defer 不得缩短新的显式 cooldown')
  const explicitRetestFailure = repositories.recordCooldownAccountRetestFailure(account.id, {
    ...explicitCooldownGuard,
    errorCode: 'transport_timeout',
    errorMessage: '显式 cooldown 复测传输失败'
  })
  assert.equal(explicitRetestFailure.changed, true, '显式 cooldown 的非终态复测失败应更新退避')
  assert.equal(accountState(account.id).lastErrorCode, 'explicit_account_error_policy_cooldown', '复测诊断不得覆盖显式状态 provenance')
  const successAfterRetestFailure = accountErrorPolicy.applyAccountErrorHandling(staleActive, {
    success: true,
    observedAt: iso(baseMs + 4_750),
    dispatchRevision: staleActive.dispatchRevision,
    trafficSource: 'gateway'
  })
  assert.equal(successAfterRetestFailure.changed, false, '复测失败后的较新普通成功仍不得清理显式 cooldown')
  const sourceMatchedRecovery = repositories.recordCooldownAccountRetestSuccess(account.id, {
    ...explicitCooldownGuard
  })
  assert.equal(sourceMatchedRecovery.changed, true, '只有携带当前 cooldown 代次的复测成功才能恢复显式状态')
  assert.equal(accountState(account.id).status, 'active', '来源匹配的 cooldown 复测应恢复 active')

  const retriedOldFailure = applyCooldown(staleActive, laterFailureObservedAt)
  assert.equal(retriedOldFailure.changed, false, 'DB 重试的旧失败不得在恢复后复活')
  assert.equal(accountState(account.id).status, 'active', '旧失败 DB 重试后账户必须保持 active')

  const equalTimestamp = iso(baseMs + 5_000)
  const equalTimestampFailureFirst = applyCooldown(staleActive, equalTimestamp)
  assert.equal(equalTimestampFailureFirst.changed, true, '同毫秒成功之前的显式失败应先正常生效')
  const equalTimestampSuccessSecond = accountErrorPolicy.applyAccountErrorHandling(staleActive, {
    success: true,
    observedAt: equalTimestamp,
    dispatchRevision: staleActive.dispatchRevision,
    trafficSource: 'gateway'
  })
  assert.equal(equalTimestampSuccessSecond.changed, false, '同毫秒成功也不得绕过显式 cooldown provenance')
  assert.equal(accountState(account.id).status, 'temporary_unavailable', '同毫秒成功后显式 cooldown 必须保持')
  const equalTimestampCooldownState = accountState(account.id)
  assert(equalTimestampCooldownState.cooldownRetestGeneration, '同毫秒新 cooldown episode 必须刷新 generation')
  assert.equal(repositories.recordCooldownAccountRetestSuccess(account.id, {
    ...cooldownRetestGuard(equalTimestampCooldownState)
  }).changed, true, '同毫秒场景仍必须通过匹配当前代次的复测恢复')

  const edited = repositories.updateAccount(account.id, { priority: 7 }, access)
  assert(edited, '测试账户 priority 更新失败')
  const currentAfterPriority = gatewayAccount(group.id, account.id)
  assert.equal(currentAfterPriority.dispatchRevision, staleActive.dispatchRevision, 'priority 不属于传输身份，不得借管理操作清除活动电路')
  const transportEdited = repositories.updateAccount(account.id, {
    credentials: {
      api_key: 'sk-account-error-policy-observation-fencing',
      base_url: 'https://rotated.example.invalid/v1'
    }
  }, access)
  assert(transportEdited, '测试账户传输身份更新失败')
  repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  const currentAfterRevision = gatewayAccount(group.id, account.id)
  assert.notEqual(currentAfterRevision.dispatchRevision, staleActive.dispatchRevision, '凭据连接身份更新必须推进 dispatch revision')
  const oldRevisionFailure = applyCooldown(staleActive, iso(baseMs + 6_000))
  assert.equal(oldRevisionFailure.changed, false, '旧 dispatch revision 的失败不得修改新配置账户')
  assert.equal(accountState(account.id).status, 'active', '旧 revision 失败后账户必须保持 active')

  const beforeDisable = currentAfterRevision
  const disableWatermarkAt = iso(baseMs + 7_000)
  accountErrorPolicy.applyAccountErrorHandling(beforeDisable, {
    success: true,
    observedAt: disableWatermarkAt,
    dispatchRevision: beforeDisable.dispatchRevision,
    trafficSource: 'gateway'
  })
  const staleDisable = applyDisable(beforeDisable, iso(baseMs + 6_500))
  assert.equal(staleDisable.changed, false, '迟到 disable 策略不得覆盖较新成功')
  assert.equal(accountState(account.id).status, 'active', '迟到 disable 后账户必须保持 active')

  const currentDisable = applyDisable(beforeDisable, iso(baseMs + 8_000))
  assert.equal(currentDisable.changed, true, '较新 disable 策略仍必须按用户规则生效')
  assert.equal(accountState(account.id).status, 'error', '较新 disable 策略应进入 error')

  const successAfterError = accountErrorPolicy.applyAccountErrorHandling(beforeDisable, {
    success: true,
    observedAt: iso(baseMs + 9_000),
    dispatchRevision: beforeDisable.dispatchRevision,
    trafficSource: 'gateway'
  })
  assert.equal(successAfterError.changed, false, '成功 watermark 不得擅自恢复用户显式 error')
  assert.equal(accountState(account.id).status, 'error', '用户显式 error 必须保持')

  const transitionAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '显式策略较新失败升级账户',
    type: 'api_key',
    status: 'active',
    schedulable: true,
    credentials: {
      api_key: 'sk-account-error-policy-newer-failure',
      base_url: 'https://example.invalid/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-5.5']
  }, access)
  repositories.recordAccountHealthCheckSuccess(transitionAccount.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  const staleTransitionActive = gatewayAccount(group.id, transitionAccount.id)
  const transitionBaseMs = Date.now() + 60_000
  assert.equal(applyCooldown(staleTransitionActive, iso(transitionBaseMs)).changed, true)
  const newerDisableAfterCooldown = applyDisable(staleTransitionActive, iso(transitionBaseMs + 1_000))
  assert.equal(newerDisableAfterCooldown.changed, true, '较新 disable 必须覆盖较早 cooldown，不能被旧 active 快照吞掉')
  assert.equal(accountState(transitionAccount.id).status, 'error', '较新 disable 应把 cooldown 状态升级为 error')

  const rateLimitedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '显式 rate limited 并发恢复保护账户',
    type: 'api_key',
    status: 'active',
    schedulable: true,
    credentials: {
      api_key: 'sk-account-error-policy-rate-limited',
      base_url: 'https://example.invalid/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-5.5']
  }, access)
  repositories.recordAccountHealthCheckSuccess(rateLimitedAccount.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  const staleRateLimitedActive = gatewayAccount(group.id, rateLimitedAccount.id)
  const rateLimitedBaseMs = Date.now() + 60_000
  const rateLimitedAt = iso(rateLimitedBaseMs)
  const rateLimited = accountErrorPolicy.applyAccountErrorHandling(staleRateLimitedActive, {
    success: false,
    observedAt: rateLimitedAt,
    dispatchRevision: staleRateLimitedActive.dispatchRevision,
    trafficSource: 'gateway',
    policyDecision: {
      action: 'cooldown',
      cooldownStatus: 'rate_limited',
      cooldownUntil: iso(rateLimitedBaseMs + 60_000),
      ruleName: '用户显式 rate limited'
    }
  })
  assert.equal(rateLimited.changed, true, '用户显式 rate limited 应正常生效')
  const successAfterConcurrentRateLimited = accountErrorPolicy.applyAccountErrorHandling(staleRateLimitedActive, {
    success: true,
    observedAt: iso(rateLimitedBaseMs + 1_000),
    dispatchRevision: staleRateLimitedActive.dispatchRevision,
    trafficSource: 'gateway'
  })
  assert.equal(successAfterConcurrentRateLimited.changed, false, 'stale active 网关成功不得清除当前 rate_limited 用户策略')
  assert.equal(accountState(rateLimitedAccount.id).status, 'rate_limited', '用户显式 rate_limited 必须保持')

  const expiredRateLimitedAccount = createReadyAccount(group.id, '显式 TTL 恢复账户', 'explicit-ttl')
  const staleExpiredRateLimited = gatewayAccount(group.id, expiredRateLimitedAccount.id)
  const expiredRateLimited = accountErrorPolicy.applyAccountErrorHandling(staleExpiredRateLimited, {
    success: false,
    observedAt: iso(Date.now() + 1_000),
    dispatchRevision: staleExpiredRateLimited.dispatchRevision,
    trafficSource: 'gateway',
    policyDecision: {
      action: 'cooldown',
      cooldownStatus: 'rate_limited',
      cooldownUntil: iso(Date.now() - 1_000),
      ruleName: '用户显式 TTL'
    }
  })
  assert.equal(expiredRateLimited.changed, true, '已到 TTL 的显式 rate_limited 应先保持来源状态')
  const dueForRetest = repositories.findAccountForCooldownRetest(expiredRateLimitedAccount.id)
  assert(dueForRetest, 'TTL 到期应仅使账户进入带代次的 cooldown 复测通道')
  assert.equal(accountState(expiredRateLimitedAccount.id).status, 'rate_limited', 'TTL 到期本身不得直接清理显式状态')
  const ttlRecovery = repositories.recordCooldownAccountRetestSuccess(expiredRateLimitedAccount.id, {
    ...cooldownRetestGuard(dueForRetest)
  })
  assert.equal(ttlRecovery.changed, true, 'TTL 到期后带当前代次的复测成功应恢复账户')

  const manualRecoveryAccount = createReadyAccount(group.id, '显式人工恢复账户', 'explicit-manual')
  const staleManualRecovery = gatewayAccount(group.id, manualRecoveryAccount.id)
  assert.equal(applyCooldown(staleManualRecovery, iso(Date.now() + 10_000)).changed, true)
  const manualRecovery = repositories.clearAccountFailureStateResult(manualRecoveryAccount.id, access, {
    allowErrorRestore: false,
    allowExplicitPolicyRestore: true
  })
  assert.equal(manualRecovery.changed, true, '有权限的人工恢复路径应能清理显式 cooldown')
  assert.equal(accountState(manualRecoveryAccount.id).status, 'active', '人工恢复后账户应为 active')

  const automaticTransportAccount = createReadyAccount(group.id, '网关自动 transport 恢复账户', 'automatic-transport')
  const automaticCooldown = repositories.markAccountCooldown(
    automaticTransportAccount.id,
    iso(Date.now() + 30_000),
    '网关自动 transport 冷却',
    'rate_limited'
  )
  assert(automaticCooldown, '模拟网关自动 transport cooldown 失败')
  const automaticTransportSuccess = accountErrorPolicy.applyAccountErrorHandling(gatewayAccount(group.id, automaticTransportAccount.id), {
    success: true,
    observedAt: iso(Date.now() + 60_000),
    dispatchRevision: gatewayAccount(group.id, automaticTransportAccount.id).dispatchRevision,
    trafficSource: 'runtime_recovery_probe'
  })
  assert.equal(automaticTransportSuccess.changed, false, '无 cooldown generation 的通用成功不得清理自动 transport 持久状态')
  assert.equal(accountState(automaticTransportAccount.id).status, 'rate_limited', '自动 transport cooldown 必须保留到匹配代次的复测成功')
  const automaticTransportState = accountState(automaticTransportAccount.id)
  assert(automaticTransportState.cooldownRetestGeneration, '自动 transport cooldown 必须持久化 generation')
  assert.equal(repositories.recordCooldownAccountRetestSuccess(automaticTransportAccount.id, {
    ...cooldownRetestGuard(automaticTransportState)
  }).changed, true, '自动 transport 状态必须可由匹配 cooldown generation 的复测成功恢复')
  assert.equal(accountState(automaticTransportAccount.id).status, 'active', '匹配代次恢复不得被通用成功收紧破坏')

  const missingObservationAccount = createReadyAccount(group.id, '缺少观测水位恢复保护账户', 'missing-observation')
  const staleMissingObservationActive = gatewayAccount(group.id, missingObservationAccount.id)
  assert(repositories.markAccountTemporaryUnavailable(missingObservationAccount.id, '自动 transport 冷却'), '模拟自动 temporary_unavailable 失败')
  const missingObservationSuccess = accountErrorPolicy.applyAccountErrorHandling(staleMissingObservationActive, {
    success: true,
    trafficSource: 'gateway'
  })
  assert.equal(missingObservationSuccess.changed, false, '缺少 observedAt/revision 的旧成功不得清理任何持久冷却')
  assert.equal(accountState(missingObservationAccount.id).status, 'temporary_unavailable', '旧 active 快照不得绕过当前 cooldown generation')

  console.log('账户显式策略观测 fencing 回归通过：provenance、epoch、observedAt 和 dispatch revision 共同隔离迟到成功/失败')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function gatewayAccount(groupId: string, accountId: string) {
  const account = repositories.findOpenAIAccountForGroup(
    groupId,
    accountId,
    'sys_admin',
    { ignoreAvailability: true }
  )
  assert(account, `网关账户 ${accountId} 不存在`)
  return account
}

function applyCooldown(account: ReturnType<typeof gatewayAccount>, observedAt: string) {
  return accountErrorPolicy.applyAccountErrorHandling(account, {
    success: false,
    statusCode: 599,
    bodyText: '{"error":{"message":"configured cooldown"}}',
    observedAt,
    dispatchRevision: account.dispatchRevision,
    trafficSource: 'gateway',
    policyDecision: {
      action: 'cooldown',
      cooldownStatus: 'temporary_unavailable',
      ruleName: '用户显式临时避让'
    }
  })
}

function applyDisable(account: ReturnType<typeof gatewayAccount>, observedAt: string) {
  return accountErrorPolicy.applyAccountErrorHandling(account, {
    success: false,
    statusCode: 598,
    bodyText: '{"error":{"message":"configured disable"}}',
    observedAt,
    dispatchRevision: account.dispatchRevision,
    trafficSource: 'gateway',
    policyDecision: {
      action: 'disable',
      ruleName: '用户显式停用'
    }
  })
}

function createReadyAccount(groupId: string, name: string, keySuffix: string) {
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name,
    type: 'api_key',
    status: 'active',
    schedulable: true,
    credentials: {
      api_key: `sk-account-error-policy-${keySuffix}`,
      base_url: 'https://example.invalid/v1'
    },
    groupId,
    supportedModels: ['gpt-5.5']
  }, access)
  repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  return account
}

function accountState(accountId: string): {
  status: string
  configRevision: number
  dispatchRevision: number
  cooldownRetestObservationStartedAt?: string
  cooldownRetestGeneration?: string
  cooldownUntil?: string
  lastErrorCode?: string
  lastHealthSuccessAt?: string
} {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT status, config_revision, dispatch_revision, cooldown_until, cooldown_retest_observation_started_at, cooldown_retest_generation, last_error_code, last_health_success_at FROM accounts WHERE id = ?')
    .get(accountId) as unknown as {
      status?: string
      config_revision?: number
      dispatch_revision?: number
      cooldown_until?: string | null
      cooldown_retest_observation_started_at?: string | null
      cooldown_retest_generation?: string | null
      last_error_code?: string | null
      last_health_success_at?: string | null
    } | undefined
  assert(row?.status, `账户 ${accountId} 状态不存在`)
  return {
    status: row.status,
    configRevision: row.config_revision ?? 1,
    dispatchRevision: row.dispatch_revision ?? 1,
    cooldownUntil: row.cooldown_until ?? undefined,
    cooldownRetestObservationStartedAt: row.cooldown_retest_observation_started_at ?? undefined,
    cooldownRetestGeneration: row.cooldown_retest_generation ?? undefined,
    lastErrorCode: row.last_error_code ?? undefined,
    lastHealthSuccessAt: row.last_health_success_at ?? undefined
  }
}

function assertCooldownRetestGuardRejectedAcrossOutcomes(
  accountId: string,
  guard: {
    expectedConfigRevision: number
    expectedDispatchRevision: number
    expectedObservationStartedAt: string
    expectedGeneration: string
    expectedSourceConfigRevision?: number
  },
  label: string
): void {
  assert.equal(
    repositories.recordCooldownAccountRetestSuccess(accountId, guard).changed,
    false,
    `${label}的冷却复测 success 不得改写显式 cooldown`
  )
  assert.equal(
    repositories.deferCooldownAccountRetest(accountId, { ...guard, delaySeconds: 3 }).changed,
    false,
    `${label}的冷却复测 defer 不得改写显式 cooldown`
  )
  assert.equal(
    repositories.recordCooldownAccountRetestFailure(accountId, {
      ...guard,
      errorCode: 'stale_cooldown_retest_guard',
      errorMessage: `${label}的陈旧冷却复测失败`
    }).changed,
    false,
    `${label}的冷却复测 failure 不得改写显式 cooldown`
  )
}

function cooldownRetestGuard(state: {
  configRevision?: number
  dispatchRevision?: number
  cooldownRetestDispatchRevision?: number
  cooldownRetestObservationStartedAt?: string
  cooldownRetestGeneration?: string
  cooldownRetestSourceConfigRevision?: number
}) {
  const expectedConfigRevision = state.configRevision ?? 1
  const expectedDispatchRevision = state.cooldownRetestDispatchRevision ?? state.dispatchRevision ?? 1
  const expectedObservationStartedAt = state.cooldownRetestObservationStartedAt?.trim() ?? ''
  const expectedGeneration = state.cooldownRetestGeneration?.trim() ?? ''
  assert(expectedObservationStartedAt, 'cooldown guard 必须携带 observation')
  assert(expectedGeneration, 'cooldown guard 必须携带 generation')
  return {
    expectedConfigRevision,
    expectedDispatchRevision,
    expectedObservationStartedAt,
    expectedGeneration,
    ...(state.cooldownRetestSourceConfigRevision === undefined
      ? {}
      : { expectedSourceConfigRevision: state.cooldownRetestSourceConfigRevision })
  }
}

function iso(timestamp: number): string {
  return new Date(timestamp).toISOString()
}
