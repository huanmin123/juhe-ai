import assert from 'node:assert/strict'

import { accountAvailabilityPresentation } from '../../domain/account-status-presentation.js'

const now = new Date('2026-07-20T01:05:00.000Z')

const active = {
  id: 'account-active',
  status: 'active' as const,
  effectiveAvailability: {
    available: true,
    status: 'available' as const,
    label: '正常',
    color: 'green'
  },
  lastHealthCheckAt: '2026-07-20T01:02:03.000Z',
  nextHealthCheckAt: '2026-07-20T01:12:03.000Z',
  lastHealthSuccessAt: '2026-07-20T01:02:03.000Z',
  lastErrorCode: 'stale_error',
  lastErrorMessage: '历史错误不应泄漏',
  lastErrorTraceId: 'trace-stale'
}
const activePresentation = accountAvailabilityPresentation(active, now)
assert.equal(activePresentation.status, 'available')
assert.equal(activePresentation.probe?.kind, 'health_check')
assert.equal(activePresentation.probe?.lastObservation?.attemptedAt, active.lastHealthCheckAt)
assert.equal(activePresentation.probe?.lastObservation?.result, 'success')
assert.equal(activePresentation.probe?.lastObservation?.reason, undefined, '正常状态不应展示历史失败原因')
assert.equal(activePresentation.probe?.lastObservation?.traceId, undefined, '正常状态不应展示历史 traceId')
assert.equal(activePresentation.probe?.schedule.nextAttemptAt, active.nextHealthCheckAt)
assert.equal(activePresentation.probe?.schedule.state, 'scheduled')
assert.equal(activePresentation.statusBoundary, undefined)

const pending = accountAvailabilityPresentation({
  id: 'account-pending',
  status: 'pending_test',
  effectiveAvailability: {
    available: false,
    status: 'instance_pending_test',
    label: '账户待检查',
    color: 'orange',
    reason: '等待首次激活检查'
  },
  nextHealthCheckAt: '2026-07-20T01:04:00.000Z'
}, now)
assert.equal(pending.status, 'pending_check')
assert.equal(pending.probe?.kind, 'activation_check')
assert.equal(pending.probe?.lastObservation, undefined)
assert.equal(pending.probe?.schedule.state, 'due_waiting')

assert.throws(
  () => accountAvailabilityPresentation({
    id: 'account-pending-invalid-schedule',
    status: 'pending_test',
    effectiveAvailability: {
      available: false,
      status: 'instance_pending_test',
      label: '账户待检查',
      color: 'orange'
    },
    nextHealthCheckAt: 'not-a-date'
  }, now),
  /账户 nextHealthCheckAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
  '非法 supplied 时间必须显式失败，不得静默变成 none'
)

const offsetPresentation = accountAvailabilityPresentation({
  id: 'account-offset-time',
  status: 'active',
  effectiveAvailability: {
    available: true,
    status: 'available',
    label: '正常'
  },
  lastHealthCheckAt: '2026-07-20T09:02:03.000+08:00',
  nextHealthCheckAt: '2026-07-20T09:12:03.000+08:00'
}, now)
assert.equal(offsetPresentation.probe?.lastObservation?.attemptedAt, '2026-07-20T01:02:03.000Z', 'health observation 必须 canonical UTC')
assert.equal(offsetPresentation.probe?.schedule.nextAttemptAt, '2026-07-20T01:12:03.000Z', 'health schedule 必须 canonical UTC')

assert.throws(
  () => accountAvailabilityPresentation({
    id: 'account-bare-observation',
    status: 'active',
    effectiveAvailability: {
      available: true,
      status: 'available',
      label: '正常'
    },
    lastHealthCheckAt: '2026-07-20T01:02:03.000'
  }, now),
  /账户 lastHealthCheckAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
  'health observation 裸时间必须显式失败'
)

const temporaryUnavailable = accountAvailabilityPresentation({
  id: 'account-temporary',
  status: 'temporary_unavailable',
  effectiveAvailability: {
    available: false,
    status: 'instance_temporary_unavailable',
    label: '账户临时不可用',
    color: 'red',
    reason: '上游连接失败'
  },
  cooldownRetestLastAt: '2026-07-20T01:03:00.000Z',
  cooldownRetestLastStatusCode: 503,
  cooldownUntil: '2026-07-20T01:08:00.000Z',
  lastErrorCode: 'upstream_unavailable',
  lastErrorMessage: '上游连接失败',
  lastErrorTraceId: 'trace-current'
}, now)
assert.equal(temporaryUnavailable.status, 'temporarily_unavailable')
assert.equal(temporaryUnavailable.probe?.kind, 'cooldown_retest')
assert.equal(temporaryUnavailable.probe?.lastObservation?.result, 'failed')
assert.equal(temporaryUnavailable.probe?.lastObservation?.traceId, 'trace-current')
assert.equal(temporaryUnavailable.probe?.schedule.nextAttemptAt, '2026-07-20T01:08:00.000Z')
assert.equal(temporaryUnavailable.statusBoundary, undefined)

const rateLimited = accountAvailabilityPresentation({
  id: 'account-rate-limited',
  status: 'rate_limited',
  effectiveAvailability: {
    available: false,
    status: 'instance_rate_limited',
    label: '账户限流中',
    color: 'orange',
    reason: '等待限流恢复'
  },
  cooldownUntil: '2026-07-20T01:10:00.000Z'
}, now)
assert.equal(rateLimited.probe?.kind, 'cooldown_retest')
assert.deepEqual(rateLimited.probe?.schedule, {
  state: 'scheduled',
  nextAttemptAt: '2026-07-20T01:10:00.000Z'
}, '限流账户必须展示 worker 实际使用的冷却复测时间')

const disabled = accountAvailabilityPresentation({
  id: 'account-disabled',
  status: 'disabled',
  effectiveAvailability: {
    available: false,
    status: 'instance_disabled',
    label: '账户已停用',
    color: 'default',
    reason: '管理员已停用账户'
  },
  lastHealthCheckAt: '2026-07-19T01:00:00.000Z',
  nextHealthCheckAt: '2026-07-20T01:10:00.000Z',
  lastHealthCheckErrorMessage: '旧检查失败',
  lastHealthCheckTraceId: 'trace-old'
}, now)
assert.equal(disabled.status, 'disabled')
assert.equal(disabled.probe, undefined, '动作驱动的停用状态不得伪造自动检查')
assert.equal(disabled.action, 'enable_account')

const authorizationExpired = accountAvailabilityPresentation({
  id: 'account-authorization-expired',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'authorization_expired',
    label: '授权到期',
    color: 'red',
    reason: '授权已到期'
  },
  authorizationExpiresAt: '2026-07-20T00:00:00.000Z',
  nextHealthCheckAt: '2026-07-20T01:10:00.000Z'
}, now)
assert.equal(authorizationExpired.status, 'expired')
assert.equal(authorizationExpired.statusBoundary?.kind, 'authorization_expired')
assert.equal(authorizationExpired.statusBoundary?.at, '2026-07-20T00:00:00.000Z')
assert.equal(authorizationExpired.probe, undefined, '业务到期边界不得写入探测时间线')

const authorizationExpiredOffset = accountAvailabilityPresentation({
  id: 'account-authorization-expired-offset',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'authorization_expired',
    label: '授权到期',
    color: 'red'
  },
  authorizationExpiresAt: '2026-07-20T08:00:00.000+08:00'
}, now)
assert.equal(authorizationExpiredOffset.statusBoundary?.at, '2026-07-20T00:00:00.000Z', 'status boundary 必须 canonical UTC')

assert.throws(
  () => accountAvailabilityPresentation({
    id: 'account-invalid-unused-boundary',
    status: 'active',
    effectiveAvailability: {
      available: true,
      status: 'available',
      label: '正常'
    },
    authorizationExpiresAt: '2026-07-20T00:00:00.000'
  }, now),
  /账户 authorizationExpiresAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
  '未被当前状态消费的 supplied status boundary 也必须显式失败'
)

const runtimeWithoutProbe = accountAvailabilityPresentation({
  id: 'account-runtime-pending',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'runtime_precheck_pending',
    label: '正在复核',
    color: 'orange',
    reason: '等待运行态探针'
  },
  lastHealthCheckAt: '2026-07-20T00:00:00.000Z',
  lastHealthCheckErrorMessage: '旧健康检查失败',
  lastHealthCheckTraceId: 'trace-unrelated'
}, now)
assert.equal(runtimeWithoutProbe.status, 'verifying')
assert.equal(runtimeWithoutProbe.probe, undefined, '缺少真实运行探针时不得用健康检查补位')
assert.equal(runtimeWithoutProbe.reason, '等待运行态探针')

const apiKeyPoolUnavailable = accountAvailabilityPresentation({
  id: 'account-key-pool',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'api_key_pool_unavailable',
    label: 'API Key 暂不可用',
    color: 'red',
    reason: '所有 Key 都暂不可用'
  },
  apiKeyProbe: {
    kind: 'api_key_retest',
    lastObservation: {
      observationId: 'api-key-observation',
      attemptedAt: '2026-07-20T01:01:00.000Z',
      result: 'failed',
      errorCode: 'rate_limit_exceeded',
      reason: 'Key 限流',
      traceId: 'trace-key'
    },
    schedule: {
      state: 'scheduled',
      nextAttemptAt: '2026-07-20T01:06:00.000Z'
    }
  }
}, now)
assert.equal(apiKeyPoolUnavailable.status, 'key_pool_unavailable')
assert.equal(apiKeyPoolUnavailable.probe?.kind, 'api_key_retest')
assert.equal(apiKeyPoolUnavailable.probe?.lastObservation?.traceId, 'trace-key')
assert.equal(apiKeyPoolUnavailable.probe?.schedule.nextAttemptAt, '2026-07-20T01:06:00.000Z')
assert.equal(apiKeyPoolUnavailable.statusBoundary, undefined)

const apiKeyOffsetPresentation = accountAvailabilityPresentation({
  id: 'account-key-pool-offset',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'api_key_pool_unavailable',
    label: 'API Key 暂不可用'
  },
  apiKeyRuntime: {
    total: 1,
    active: 0,
    temporaryUnavailable: 0,
    rateLimited: 1,
    error: 0,
    disabled: 0,
    unavailable: 1,
    allUnavailable: true,
    lastFailureAt: '2026-07-20T09:01:00.000+08:00',
    nextProbeAt: '2026-07-20T09:06:00.000+08:00'
  }
}, now)
assert.equal(apiKeyOffsetPresentation.probe?.lastObservation?.attemptedAt, '2026-07-20T01:01:00.000Z', 'API Key attemptedAt 必须 canonical UTC')
assert.equal(apiKeyOffsetPresentation.probe?.schedule.nextAttemptAt, '2026-07-20T01:06:00.000Z', 'API Key schedule 必须 canonical UTC')

assert.throws(
  () => accountAvailabilityPresentation({
    id: 'account-key-pool-invalid',
    status: 'active',
    effectiveAvailability: {
      available: false,
      status: 'api_key_pool_unavailable',
      label: 'API Key 暂不可用'
    },
    apiKeyRuntime: {
      total: 1,
      active: 0,
      temporaryUnavailable: 0,
      rateLimited: 1,
      error: 0,
      disabled: 0,
      unavailable: 1,
      allUnavailable: true,
      lastFailureAt: '2026-07-20T09:01:00.000'
    }
  }, now),
  /账户 API Key lastFailureAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
  'API Key attemptedAt 裸时间必须显式失败'
)

const sourcePending = accountAvailabilityPresentation({
  id: 'authorized-instance-source-pending',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'source_pending_test',
    label: '来源待检查',
    color: 'orange',
    reason: '来源账户尚未通过检查'
  }
}, now)
assert.equal(sourcePending.action, 'contact_authorizer', '授权实例不得对来源账户执行当前实例的恢复动作')
assert.equal(sourcePending.probe, undefined)

const sourceExpired = accountAvailabilityPresentation({
  id: 'authorized-instance-source-expired',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'source_expired',
    label: '来源已到期',
    color: 'red'
  },
  authorizationInstanceSourceAccountExpiresAt: '2026-07-20T00:30:00.000Z'
}, now)
assert.deepEqual(sourceExpired.statusBoundary, {
  at: '2026-07-20T00:30:00.000Z',
  kind: 'source_expired'
})

const stoppedInstance = accountAvailabilityPresentation({
  id: 'account-stopped',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'instance_unschedulable',
    label: '账户已停调',
    color: 'default'
  }
}, now)
assert.equal(stoppedInstance.status, 'disabled')
assert.equal(stoppedInstance.action, 'enable_account')

const sourceCooldown = accountAvailabilityPresentation({
  id: 'authorized-source-cooldown',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'source_cooldown',
    label: '来源冷却中',
    reason: '授权来源正在冷却'
  },
  cooldownUntil: '2026-07-20T01:07:00.000Z',
  authorizationInstanceSourceAccountCooldownUntil: '2026-07-20T01:09:00.000Z'
}, now)
assert.equal(sourceCooldown.probe, undefined, '来源冷却截止不是后台复测任务')
assert.deepEqual(sourceCooldown.statusBoundary, {
  at: '2026-07-20T01:09:00.000Z',
  kind: 'cooldown_expiry'
})
assert.equal(sourceCooldown.action, 'contact_authorizer')

const sourceRateLimitedWithObservation = accountAvailabilityPresentation({
  id: 'authorized-source-rate-limited',
  authorizationInstanceSourceAccountId: 'source-rate-limited',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'source_rate_limited',
    label: '来源限流中',
    reason: '来源复测失败'
  },
  authorizationInstanceSourceAccountCooldownUntil: '2026-07-20T01:12:00.000Z',
  authorizationInstanceSourceAccountCooldownRetestLastAt: '2026-07-20T00:12:00.000Z',
  authorizationInstanceSourceAccountCooldownRetestLastStatusCode: 429,
  authorizationInstanceSourceAccountLastErrorCode: 'rate_limit_exceeded',
  authorizationInstanceSourceAccountLastErrorMessage: '来源仍受限流',
  authorizationInstanceSourceAccountLastErrorTraceId: 'trace-source-rate-limit'
}, now)
assert.equal(sourceRateLimitedWithObservation.probe?.lastObservation?.traceId, 'trace-source-rate-limit')
assert.deepEqual(sourceRateLimitedWithObservation.probe?.schedule, {
  state: 'scheduled',
  nextAttemptAt: '2026-07-20T01:12:00.000Z'
}, '来源冷却账户必须展示来源 worker 实际使用的复测时间')

const sourceOffsetPresentation = accountAvailabilityPresentation({
  id: 'authorized-source-offset',
  authorizationInstanceSourceAccountId: 'source-offset',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'source_rate_limited',
    label: '来源限流中'
  },
  authorizationInstanceSourceAccountCooldownUntil: '2026-07-20T09:12:00.000+08:00',
  authorizationInstanceSourceAccountCooldownRetestLastAt: '2026-07-20T08:12:00.000+08:00',
  authorizationInstanceSourceAccountLastErrorCode: 'rate_limit_exceeded'
}, now)
assert.equal(sourceOffsetPresentation.probe?.lastObservation?.attemptedAt, '2026-07-20T00:12:00.000Z', 'source attemptedAt 必须 canonical UTC')
assert.equal(sourceOffsetPresentation.probe?.schedule.nextAttemptAt, '2026-07-20T01:12:00.000Z', 'source schedule 必须 canonical UTC')

assert.throws(
  () => accountAvailabilityPresentation({
    id: 'authorized-source-invalid',
    authorizationInstanceSourceAccountId: 'source-invalid',
    status: 'active',
    effectiveAvailability: {
      available: false,
      status: 'source_rate_limited',
      label: '来源限流中'
    },
    authorizationInstanceSourceAccountCooldownRetestLastAt: '2026-07-20T08:12:00.000'
  }, now),
  /授权来源 cooldownRetestLastAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
  'source attemptedAt 裸时间必须显式失败'
)

const terminalError = accountAvailabilityPresentation({
  id: 'account-terminal-error',
  status: 'error',
  effectiveAvailability: {
    available: false,
    status: 'instance_error',
    label: '账户异常',
    reason: '凭据已失效'
  },
  lastErrorCode: 'invalid_credentials',
  lastErrorMessage: '凭据已失效',
  lastErrorTraceId: 'trace-terminal',
  lastHealthCheckAt: '2026-07-19T01:00:00.000Z',
  lastHealthCheckStatusCode: 401,
  lastHealthCheckErrorCode: 'invalid_credentials',
  lastHealthCheckErrorMessage: '最近健康检查确认凭据已失效',
  lastHealthCheckTraceId: 'trace-terminal-health',
  nextHealthCheckAt: '2026-07-20T01:10:00.000Z'
}, now)
assert.equal(terminalError.probe?.kind, 'health_check', '通用账户异常必须保留最近健康检查事实')
assert.equal(terminalError.probe?.lastObservation?.attemptedAt, '2026-07-19T01:00:00.000Z')
assert.equal(terminalError.probe?.lastObservation?.httpStatus, 401)
assert.equal(terminalError.probe?.lastObservation?.errorCode, 'invalid_credentials')
assert.equal(terminalError.probe?.lastObservation?.traceId, 'trace-terminal-health')
assert.deepEqual(terminalError.probe?.schedule, { state: 'none' }, '通用账户异常不得伪造自动复检计划')

const instanceCooldownWithoutProbe = accountAvailabilityPresentation({
  id: 'account-instance-cooldown',
  status: 'temporary_unavailable',
  effectiveAvailability: {
    available: false,
    status: 'instance_cooldown',
    label: '账户冷却中',
    reason: '等待冷却结束'
  },
  cooldownUntil: '2026-07-20T01:11:00.000Z'
}, now)
assert.equal(instanceCooldownWithoutProbe.probe, undefined, '实例冷却截止不是后台复测任务')
assert.deepEqual(instanceCooldownWithoutProbe.statusBoundary, {
  at: '2026-07-20T01:11:00.000Z',
  kind: 'cooldown_expiry'
})

const derivedSourcePending = accountAvailabilityPresentation({
  id: 'authorized-source-pending',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'source_pending_test',
    label: '来源待检查',
    reason: '来源账户等待检查'
  },
  authorizationInstanceSourceAccountLastHealthCheckAt: '2026-07-20T00:20:00.000Z',
  authorizationInstanceSourceAccountNextHealthCheckAt: '2026-07-20T12:20:00.000Z',
  authorizationInstanceSourceAccountLastHealthCheckStatusCode: 503,
  authorizationInstanceSourceAccountLastHealthCheckErrorCode: 'model_not_found',
  authorizationInstanceSourceAccountLastHealthCheckErrorMessage: '来源模型不存在',
  authorizationInstanceSourceAccountLastHealthCheckTraceId: 'trace-source-pending'
}, now)
assert.equal(derivedSourcePending.probe?.kind, 'source_account_probe')
assert.equal(derivedSourcePending.probe?.lastObservation?.traceId, 'trace-source-pending')
assert.equal(derivedSourcePending.probe?.schedule.nextAttemptAt, '2026-07-20T12:20:00.000Z')

const halfOpen = accountAvailabilityPresentation({
  id: 'account-half-open',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'runtime_half_open',
    label: '正在确认',
    reason: '正在进行恢复确认'
  },
  runtimeProbe: {
    lastObservation: undefined,
    schedule: { state: 'running' },
    recoveryAt: '2026-07-20T01:20:00.000Z',
    recoveryAtKind: 'policy_ttl_expiry'
  }
}, now)
assert.equal(halfOpen.action, 'none')
assert.equal(halfOpen.probe?.kind, 'runtime_probe')
assert.equal('recoveryAt' in (halfOpen.probe ?? {}), false, '运行态恢复时间不得泄漏进探针摘要')

const localSuppressed = accountAvailabilityPresentation({
  id: 'account-local-suppressed',
  status: 'active',
  effectiveAvailability: {
    available: false,
    status: 'runtime_local_suppressed',
    label: '暂时避让',
    reason: '用户策略暂时避让',
    retryAt: '2026-07-20T01:15:00.000Z'
  }
}, now)
assert.equal(localSuppressed.action, 'restore_account')
assert.equal(localSuppressed.statusBoundary, undefined, '自动短暂避让的内部 TTL 不应显示为用户策略释放时间')

const invalidCredentialError = accountAvailabilityPresentation({
  id: 'account-invalid-credential',
  status: 'error',
  effectiveAvailability: {
    available: false,
    status: 'instance_error',
    label: '账户异常',
    reason: '凭据无效'
  },
  lastErrorCode: 'invalid_credentials',
  lastErrorMessage: '凭据无效'
}, now)
assert.equal(invalidCredentialError.action, 'fix_configuration')
assert.deepEqual(invalidCredentialError.probe?.schedule, { state: 'none' }, '没有检查事实的账户异常必须明确没有自动计划')
assert.equal(invalidCredentialError.probe?.lastObservation, undefined)

console.log('account status presentation regression passed')
