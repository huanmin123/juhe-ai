import type {
  AccountEffectiveAvailability,
  AccountEffectiveAvailabilityBlockerScope,
  AccountEffectiveAvailabilityStatus,
  AccountSummary,
  AuthorizationStatus
} from './types.js'

export type AccountEffectiveAvailabilityInput = Pick<
  AccountSummary,
  | 'permissions'
  | 'accessType'
  | 'boundGroupId'
  | 'groupBindStatus'
  | 'authorizationStatus'
  | 'authorizationExpiresAt'
  | 'authorizationQuotaExceeded'
  | 'authorizationInstanceSourceAccountId'
  | 'authorizationInstanceSourceAccountStatus'
  | 'authorizationInstanceSourceAccountSchedulable'
  | 'authorizationInstanceSourceAccountScheduleActive'
  | 'authorizationInstanceSourceAccountExpiresAt'
  | 'authorizationInstanceSourceAccountCooldownUntil'
  | 'authorizationInstanceSourceAccountLastErrorCode'
  | 'authorizationInstanceSourceAccountLastErrorMessage'
  | 'accountExpiresAt'
  | 'status'
  | 'schedulable'
  | 'availabilityScheduleActive'
  | 'cooldownUntil'
  | 'lastErrorCode'
  | 'lastErrorMessage'
  | 'apiKeyRuntime'
  | 'runtimeAvailability'
>

export function accountSummaryWithEffectiveAvailability<T extends AccountEffectiveAvailabilityInput>(
  account: T,
  now = Date.now()
): T & { effectiveAvailability: AccountEffectiveAvailability } {
  return {
    ...account,
    effectiveAvailability: accountEffectiveAvailability(account, now)
  }
}

export function accountEffectiveAvailability(
  account: AccountEffectiveAvailabilityInput,
  now = Date.now()
): AccountEffectiveAvailability {
  if (account.permissions?.canUse === false) {
    return blocked('permission_denied', '无可用权限', 'red', 'permission', '当前账户无可用权限')
  }

  if (account.accessType === 'authorized') {
    const bindingBlocker = authorizedBindingAvailability(account)
    if (bindingBlocker) return bindingBlocker

    const authorizationBlocker = authorizationAvailability(account, now)
    if (authorizationBlocker) return authorizationBlocker

    if (account.authorizationQuotaExceeded) {
      return blocked('authorization_quota_exceeded', '授权额度已用完', 'red', 'authorization', '授权额度已用完，当前账户不能调用')
    }

    const sourceBlocker = sourceAccountAvailability(account, now)
    if (sourceBlocker) return sourceBlocker
  }

  const instanceBlocker = instanceAccountAvailability(account, now)
  if (instanceBlocker) return instanceBlocker

  const apiKeyPoolBlocker = apiKeyPoolAvailability(account)
  if (apiKeyPoolBlocker) return apiKeyPoolBlocker

  const runtimeBlocker = runtimeAvailability(account)
  if (runtimeBlocker) return runtimeBlocker

  return {
    available: true,
    status: 'available',
    label: '正常',
    color: 'green'
  }
}

function authorizedBindingAvailability(account: AccountEffectiveAvailabilityInput): AccountEffectiveAvailability | undefined {
  if (!account.boundGroupId) {
    return blocked('binding_missing', '未绑定分组', 'red', 'binding', '授权账户需要先绑定到你的分组')
  }
  if (account.groupBindStatus === 'authorization_unavailable') {
    return blocked('authorization_unavailable', '授权已失效', 'red', 'binding', '当前分组绑定的授权已失效，请重新绑定分组或联系授权人')
  }
  return undefined
}

function authorizationAvailability(account: AccountEffectiveAvailabilityInput, now: number): AccountEffectiveAvailability | undefined {
  if (account.authorizationStatus === 'expired' || isExpired(account.authorizationExpiresAt, now)) {
    return blocked('authorization_expired', '授权到期', 'red', 'authorization', '授权已到期，当前账户不能调用')
  }
  if (account.authorizationStatus === 'paused') {
    return blocked('authorization_paused', '授权暂停', 'orange', 'authorization', '授权已暂停，当前账户不能调用')
  }
  if (isUnavailableAuthorizationStatus(account.authorizationStatus)) {
    return blocked('authorization_unavailable', '授权已失效', 'red', 'authorization', '授权关系已失效，当前账户不能调用')
  }
  return undefined
}

function sourceAccountAvailability(account: AccountEffectiveAvailabilityInput, now: number): AccountEffectiveAvailability | undefined {
  if (!account.authorizationInstanceSourceAccountId || !account.authorizationInstanceSourceAccountStatus) {
    return blocked('source_deleted', '来源缺失', 'red', 'source_account', '授权方原账户不存在或已删除，当前账户不能调用')
  }
  if (account.authorizationInstanceSourceAccountLastErrorCode === 'account_expired' || isExpired(account.authorizationInstanceSourceAccountExpiresAt, now)) {
    return blocked('source_expired', '来源到期', 'red', 'source_account', '授权方原账户已到期，当前账户不能调用')
  }
  const sourceStatus = account.authorizationInstanceSourceAccountStatus
  if (sourceStatus === 'disabled') {
    return blocked('source_disabled', '来源停用', 'red', 'source_account', '授权方原账户已停用，当前账户不能调用')
  }
  if (sourceStatus === 'pending_test') {
    return blocked('source_pending_test', '来源待测试', 'blue', 'source_account', '授权方原账户尚未测试通过，当前账户不能调用')
  }
  if (sourceStatus === 'error') {
    return blocked('source_error', '来源异常', 'red', 'source_account', sourceReason(account, '授权方原账户处于异常状态，当前账户不能调用'))
  }
  if (sourceStatus === 'rate_limited') {
    return blocked('source_rate_limited', '来源限流中', 'orange', 'source_account', sourceReason(account, '授权方原账户限流中，当前账户不能调用'))
  }
  if (sourceStatus === 'temporary_unavailable') {
    return blocked('source_temporary_unavailable', '来源临时不可调用', 'gold', 'source_account', sourceReason(account, '授权方原账户临时不可调用，当前账户不能调用'))
  }
  if (isFuture(account.authorizationInstanceSourceAccountCooldownUntil, now)) {
    return blocked('source_cooldown', '来源冷却', 'gold', 'source_account', '授权方原账户正在冷却，恢复前当前账户不能调用', account.authorizationInstanceSourceAccountCooldownUntil)
  }
  if (account.authorizationInstanceSourceAccountSchedulable === false) {
    return blocked('source_unschedulable', '来源停调', 'orange', 'source_account', '授权方原账户已关闭调度，当前账户不能调用')
  }
  if (account.authorizationInstanceSourceAccountScheduleActive === false) {
    return blocked('source_schedule_inactive', '来源时段外', 'gold', 'source_account', '授权方原账户当前不在允许使用时段，当前账户不能调用')
  }
  return undefined
}

function instanceAccountAvailability(account: AccountEffectiveAvailabilityInput, now: number): AccountEffectiveAvailability | undefined {
  const instanceLabel = account.accessType === 'authorized' ? '授权实例' : '账户'
  const instanceReasonPrefix = account.accessType === 'authorized' ? '授权账户' : '账户'
  const blockerScope = account.accessType === 'authorized' ? 'authorized_instance' : 'account'
  if (account.lastErrorCode === 'account_expired' || isExpired(account.accountExpiresAt, now)) {
    return blocked('instance_expired', `${instanceLabel}到期`, 'red', blockerScope, `${instanceReasonPrefix}已到期，当前不可用`)
  }
  if (account.status === 'disabled') {
    return blocked('instance_disabled', `${instanceLabel}停用`, 'default', blockerScope, `${instanceReasonPrefix}已停用，当前不可用`)
  }
  if (account.status === 'pending_test') {
    return blocked('instance_pending_test', `${instanceLabel}待测试`, 'blue', blockerScope, `${instanceReasonPrefix}尚未测试通过，当前不会参与调度`)
  }
  if (account.status === 'error') {
    return blocked('instance_error', `${instanceLabel}异常`, 'red', blockerScope, account.lastErrorMessage || `${instanceReasonPrefix}处于异常状态，当前不可用`)
  }
  if (account.status === 'rate_limited') {
    return blocked('instance_rate_limited', `${instanceLabel}限流中`, 'orange', blockerScope, account.lastErrorMessage || `${instanceReasonPrefix}限流中，恢复前不会参与调度`)
  }
  if (account.status === 'temporary_unavailable') {
    return blocked('instance_temporary_unavailable', `${instanceLabel}临时不可调用`, 'gold', blockerScope, account.lastErrorMessage || `${instanceReasonPrefix}临时不可调用，恢复前不会参与调度`)
  }
  if (isFuture(account.cooldownUntil, now)) {
    return blocked('instance_cooldown', `${instanceLabel}冷却`, 'gold', blockerScope, `${instanceReasonPrefix}正在冷却，恢复前不会参与调度`, account.cooldownUntil)
  }
  if (account.availabilityScheduleActive === false) {
    return blocked('instance_schedule_inactive', `${instanceLabel}时段外`, 'gold', blockerScope, `${instanceReasonPrefix}当前不在允许使用时段，恢复前不会参与调度`)
  }
  if (!account.schedulable) {
    return blocked('instance_unschedulable', `${instanceLabel}停调`, 'orange', blockerScope, `${instanceReasonPrefix}暂时不可调用，恢复前不会参与调度`)
  }
  return undefined
}

function runtimeAvailability(account: AccountEffectiveAvailabilityInput): AccountEffectiveAvailability | undefined {
  const runtime = account.runtimeAvailability
  if (!runtime || runtime.status === 'normal') return undefined
  if (account.status !== 'active') return undefined
  const status = runtime.status
  if (status === 'degraded') {
    return {
      available: true,
      status: 'runtime_degraded',
      label: '调度降级',
      color: 'gold',
      blockerScope: 'runtime',
      reason: runtime.reason || '当前账号近期失败，正常候选不足时才会兜底尝试',
      retryAt: runtime.until
    }
  }
  if (status === 'precheck_pending') {
    return blocked('runtime_precheck_pending', '待探针确认', 'blue', 'runtime', runtime.reason || '当前网关正在执行事前探针确认', runtime.until)
  }
  if (status === 'local_suppressed') {
    return blocked('runtime_local_suppressed', '短暂避让', 'gold', 'runtime', runtime.reason || '当前网关短窗口内临时避让该账户', runtime.until)
  }
  if (status === 'half_open') {
    return blocked('runtime_half_open', '半开探测', 'blue', 'runtime', runtime.reason || '当前网关已放行一个请求确认账户是否恢复', runtime.until)
  }
  if (status === 'precheck_failed') {
    return blocked('runtime_precheck_failed', '探针确认失败', 'gold', 'runtime', runtime.reason || '最近事前探针确认失败，当前网关暂不调度该账户', runtime.until)
  }
  return undefined
}

function apiKeyPoolAvailability(account: AccountEffectiveAvailabilityInput): AccountEffectiveAvailability | undefined {
  const runtime = account.apiKeyRuntime
  if (!runtime?.allUnavailable) return undefined
  return blocked(
    'api_key_pool_unavailable',
    'Key 全部不可用',
    'red',
    'api_key_pool',
    `账户内 ${runtime.total} 个 API Key 均不可用，后台探测恢复前不会参与调度`,
    runtime.nextProbeAt
  )
}

function sourceReason(account: AccountEffectiveAvailabilityInput, fallback: string): string {
  return account.authorizationInstanceSourceAccountLastErrorMessage || fallback
}

function blocked(
  status: AccountEffectiveAvailabilityStatus,
  label: string,
  color: string,
  blockerScope: AccountEffectiveAvailabilityBlockerScope,
  reason: string,
  retryAt?: string
): AccountEffectiveAvailability {
  return {
    available: false,
    status,
    label,
    color,
    blockerScope,
    reason,
    retryAt
  }
}

function isUnavailableAuthorizationStatus(status?: AuthorizationStatus): boolean {
  return status === 'revoked' || status === 'returned'
}

function isExpired(value: string | undefined, now: number): boolean {
  if (!value) return false
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) && timestamp <= now
}

function isFuture(value: string | undefined, now: number): boolean {
  if (!value) return false
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) && timestamp > now
}
