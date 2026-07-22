import {
  formatDateTime,
  formatMillisecondsAsSeconds,
  formatNumber,
  formatServerDateTimeInput,
  parseStrictDatePickerValue,
  serverDateTimeTimestamp
} from '@/shared/formatters'
import type { AccountStatus, AccountSummary, AccountTestResult } from '@/types/domain'
import {
  accountDiagnosticTooltipLines,
  splitAccountDiagnosticMessage,
  type AccountDiagnosticMessageParts
} from './accountDiagnosticMessages'
import { accountStatusTooltipLines as accountStatusPresentationTooltipLines } from './accountStatusPresentation'

export {
  accountClientCompatibilityText,
  accountDisplayExpiresAt,
  accountDisplayName,
  accountLastUsedAt,
  accountTypeDescription,
  accountTypeText,
  accountTypeTitle,
  asString,
  compareAccountConcurrency,
  compareAccountExpiresAt,
  compareAccountLastUsedAt,
  isAccountDisplayExpired,
  normalizeKeyword
} from './accountBasicFormatters'

export {
  formatAccountUsageSummary,
  formatCost,
  formatRelativeReset,
  formatUsageAmount,
  oauthUsageBars,
  type OAuthUsageBar
} from './accountUsageFormatters'

export interface AccountStatusTagInfo {
  color: string
  label: string
}

export function statusColor(status: AccountStatus) {
  if (status === 'active') return 'green'
  if (status === 'pending_test') return 'blue'
  if (status === 'error') return 'red'
  if (status === 'rate_limited') return 'orange'
  if (status === 'temporary_unavailable') return 'gold'
  return 'default'
}

export function statusText(status: AccountStatus) {
  if (status === 'active') return '正常'
  if (status === 'pending_test') return '待检查'
  if (status === 'error') return '异常'
  if (status === 'rate_limited') return '限流中'
  if (status === 'temporary_unavailable') return '临时不可调用'
  return '停用'
}

export function formatErrorPolicyAction(action: NonNullable<AccountTestResult['errorPolicyAction']>): string {
  if (action === 'retry_next') return '切换下一个账号'
  if (action === 'cooldown') return '账号冷却'
  if (action === 'disable') return '标记异常'
  return '无'
}

export function accountErrorCodeText(code?: string): string {
  if (code === 'oauth_token_refresh_failed') return 'OAuth Token 刷新失败'
  if (code === 'upstream_failure') return '上游调用失败'
  if (code === 'account_expired') return '账户过期'
  if (code === 'cooldown_retest_failed') return '后台复测失败'
  if (code === 'cooldown_retest_long_term_unavailable') return '长期不可用每小时复测'
  if (code === 'cooldown_retest_observation_timeout') return '长期不可用超过 7 天'
  if (code === 'account_activation_check_timeout') return '激活检查超过 24 小时'
  if (code === 'account_health_check_failed') return '后台健康检测失败'
  return code || '未分类异常'
}

export function accountStatusColor(account: AccountSummary) {
  if (shouldDisplayEffectiveAvailabilityAsStatus(account)) return account.effectiveAvailability.color
  if (isAuthorizationPaused(account)) return 'orange'
  if (isAuthorizationExpired(account) || isAuthorizationBindingUnavailable(account)) return 'red'
  if (isAccountPackageExpiredStatus(account)) return 'red'
  if (isAuthorizedAccount(account) && account.authorizationQuotaExceeded) return 'red'
  const runtimeStatus = activeRuntimeAvailabilityStatus(account)
  if (runtimeStatus === 'degraded') return 'gold'
  if (runtimeStatus === 'precheck_pending') return 'blue'
  if (runtimeStatus === 'local_suppressed') return 'gold'
  if (runtimeStatus === 'half_open') return 'blue'
  if (runtimeStatus === 'precheck_failed') return 'gold'
  if (account.effectiveAvailability?.available) {
    return account.effectiveAvailability.color
  }
  return statusColor(account.status)
}

export function accountStatusText(account: AccountSummary) {
  if (isAccountInstanceEffectiveAvailability(account) && isDirectAccountStatus(account.effectiveAvailability.status)) {
    return directAccountStatusText(account)
  }
  if (shouldDisplayEffectiveAvailabilityAsStatus(account)) return account.effectiveAvailability.label
  if (isAuthorizationPaused(account)) return '授权暂停'
  if (isAuthorizationExpired(account)) return '授权到期'
  if (isAuthorizationBindingUnavailable(account)) return '授权已失效'
  if (isAccountPackageExpiredStatus(account)) return '账户到期'
  if (isAuthorizedAccount(account) && account.authorizationQuotaExceeded) return '授权额度已用完'
  const runtimeStatus = activeRuntimeAvailabilityStatus(account)
  if (runtimeStatus === 'degraded') return '调度降级'
  if (runtimeStatus === 'precheck_pending') return '待探针确认'
  if (runtimeStatus === 'local_suppressed') return '短暂避让'
  if (runtimeStatus === 'half_open') return '半开探测'
  if (runtimeStatus === 'precheck_failed') return '探针确认失败'
  if (account.effectiveAvailability?.available) {
    return account.effectiveAvailability.label
  }
  return statusText(account.status)
}

export function accountCooldownText(account: AccountSummary) {
  if (!isCoolingDown(account)) return ''
  return `暂停至 ${formatDateTime(account.cooldownUntil)}`
}

export function accountStatusTooltipLines(account: AccountSummary): string[] {
  return accountStatusPresentationTooltipLines(account)
}

function shouldDisplayEffectiveAvailabilityAsStatus(account: AccountSummary): account is AccountSummary & { effectiveAvailability: NonNullable<AccountSummary['effectiveAvailability']> } {
  const availability = account.effectiveAvailability
  return Boolean(availability && !availability.available)
}

export function authorizationSourceAccountStatusTag(account: AccountSummary): AccountStatusTagInfo | undefined {
  if (!isAuthorizedAccount(account)) return undefined
  if (isAuthorizationSourceAccountExpired(account)) return { color: 'red', label: '来源到期' }
  const sourceStatus = account.authorizationInstanceSourceAccountStatus
  if (sourceStatus === 'pending_test') return { color: 'blue', label: '来源待检查' }
  if (sourceStatus === 'disabled') return { color: 'orange', label: '来源停用' }
  if (sourceStatus === 'error') return { color: 'red', label: '来源异常' }
  if ((sourceStatus === 'rate_limited' || sourceStatus === 'temporary_unavailable')
    && account.authorizationInstanceSourceAccountLastErrorCode === 'cooldown_retest_long_term_unavailable') {
    return { color: 'gold', label: '来源长期不可用' }
  }
  if (sourceStatus === 'rate_limited') return { color: 'orange', label: '来源限流中' }
  if (sourceStatus === 'temporary_unavailable') return { color: 'gold', label: '来源临时不可调用' }
  if (isFutureTime(account.authorizationInstanceSourceAccountCooldownUntil)) return { color: 'gold', label: '来源冷却' }
  if (account.authorizationInstanceSourceAccountSchedulable === false) return { color: 'orange', label: '来源停调' }
  return undefined
}

export function authorizationSourceAccountTooltipLines(account: AccountSummary): string[] {
  if (!isAuthorizedAccount(account)) return []
  const sourceStatus = account.authorizationInstanceSourceAccountStatus
  const lines: string[] = []
  if (isAuthorizationSourceAccountExpired(account)) {
    lines.push('授权方原账户已到期，授权实例实际不可调用')
  } else if (sourceStatus === 'pending_test') {
    lines.push('授权方原账户尚未通过后台健康检查，授权实例实际不可调用')
  } else if (sourceStatus === 'disabled') {
    lines.push('授权方原账户已停用，授权实例实际不可调用')
  } else if (sourceStatus === 'error') {
    lines.push('授权方原账户处于异常状态，授权实例实际不可调用')
  } else if (sourceStatus === 'rate_limited' || sourceStatus === 'temporary_unavailable') {
    const sourceStatusText = account.authorizationInstanceSourceAccountLastErrorCode === 'cooldown_retest_long_term_unavailable'
      ? '长期不可用'
      : statusText(sourceStatus)
    lines.push(`授权方原账户状态：${sourceStatusText}，授权实例实际不可调用`)
  } else if (sourceStatus && sourceStatus !== 'active') {
    lines.push(`授权方原账户状态：${statusText(sourceStatus)}，授权实例实际不可调用`)
  }
  if (account.authorizationInstanceSourceAccountSchedulable === false) {
    lines.push('授权方原账户已关闭调度，授权实例实际不可调用')
  }
  if (isFutureTime(account.authorizationInstanceSourceAccountCooldownUntil)) {
    lines.push(`授权方原账户冷却至 ${formatDateTime(account.authorizationInstanceSourceAccountCooldownUntil)}`)
  }
  if (account.authorizationInstanceSourceAccountExpiresAt) {
    lines.push(`授权方原账户到期时间：${formatDateTime(account.authorizationInstanceSourceAccountExpiresAt)}`)
  }
  const sourceLastErrorLines = accountDiagnosticTooltipLines(account.authorizationInstanceSourceAccountLastErrorMessage, {
    reasonLabel: '授权方原账户原因',
    idLabelPrefix: '授权方原账户'
  })
  if (sourceLastErrorLines.length) {
    lines.push(...sourceLastErrorLines)
  } else if (account.authorizationInstanceSourceAccountLastErrorCode) {
    lines.push(`授权方原账户异常类型：${accountErrorCodeText(account.authorizationInstanceSourceAccountLastErrorCode)}`)
  }
  return lines
}

function isAuthorizationSourceAccountExpired(account: AccountSummary): boolean {
  if (!isAuthorizedAccount(account)) return false
  if (account.authorizationInstanceSourceAccountLastErrorCode === 'account_expired') return true
  if (!account.authorizationInstanceSourceAccountExpiresAt) return false
  const time = serverDateTimeTimestamp(account.authorizationInstanceSourceAccountExpiresAt)
  return time !== undefined && time <= Date.now()
}

function activeRuntimeAvailabilityStatus(account: AccountSummary) {
  const status = account.runtimeAvailability?.status
  if (!status || status === 'normal') return undefined
  if (account.status !== 'active') return undefined
  return status
}

export function hasAccountRuntimeRecoveryState(account: AccountSummary): boolean {
  return Boolean(activeRuntimeAvailabilityStatus(account))
}

export function isTemporaryAccountStatus(account: AccountSummary) {
  return account.status === 'rate_limited' || account.status === 'temporary_unavailable'
}

function isLongTermUnavailableAccount(account: AccountSummary): boolean {
  return isTemporaryAccountStatus(account)
    && account.lastErrorCode === 'cooldown_retest_long_term_unavailable'
}

function isDirectAccountStatus(status: NonNullable<AccountSummary['effectiveAvailability']>['status']): boolean {
  return status.startsWith('instance_')
}

function isAccountInstanceEffectiveAvailability(account: AccountSummary): account is AccountSummary & { effectiveAvailability: NonNullable<AccountSummary['effectiveAvailability']> } {
  const scope = account.effectiveAvailability?.blockerScope
  return scope === 'account' || scope === 'authorized_instance'
}

function directAccountStatusText(account: AccountSummary): string {
  const status = account.effectiveAvailability?.status
  if (status === 'instance_expired') return '账户到期'
  if (status === 'instance_pending_test') return isPendingHealthCheckFailed(account) ? '检查失败' : '待检查'
  if (status === 'instance_disabled') return '停用'
  if (status === 'instance_error') return '异常'
  if (isLongTermUnavailableAccount(account)) return '长期不可用'
  if (status === 'instance_rate_limited') return '限流中'
  if (status === 'instance_temporary_unavailable') return '临时不可调用'
  if (status === 'instance_cooldown') return '冷却中'
  if (status === 'instance_unschedulable') return '停调'
  return statusText(account.status)
}

export function isPendingHealthCheckFailed(account: AccountSummary): boolean {
  return account.status === 'pending_test'
    && Boolean(account.lastHealthCheckAt)
    && Boolean(account.lastHealthCheckErrorCode || account.lastHealthCheckErrorMessage)
}

export function isCoolingDown(account: AccountSummary) {
  if (!account.cooldownUntil) return false
  return isFutureTime(account.cooldownUntil)
}

function isFutureTime(value?: string): boolean {
  if (!value) return false
  const time = serverDateTimeTimestamp(value)
  return time !== undefined && time > Date.now()
}

export function isAccountPackageExpired(account: AccountSummary) {
  if (!account.accountExpiresAt) return false
  const time = serverDateTimeTimestamp(account.accountExpiresAt)
  return time !== undefined && time <= Date.now()
}

export function isAccountPackageExpiredStatus(account: AccountSummary): boolean {
  return isAccountPackageExpired(account)
    || account.lastErrorCode === 'account_expired'
}

export function isAuthorizationExpired(account: AccountSummary): boolean {
  if (!isAuthorizedAccount(account)) return false
  if (account.authorizationStatus === 'expired') return true
  if (!account.authorizationExpiresAt) return false
  const time = serverDateTimeTimestamp(account.authorizationExpiresAt)
  return time !== undefined && time <= Date.now()
}

export function isAuthorizationPaused(account: AccountSummary): boolean {
  return isAuthorizedAccount(account) && account.authorizationStatus === 'paused'
}

export function isAuthorizationBindingUnavailable(account: AccountSummary): boolean {
  return isAuthorizedAccount(account)
    && account.groupBindStatus === 'authorization_unavailable'
    && !isAuthorizationExpired(account)
    && !isAuthorizationPaused(account)
}

export function isAuthorizedAccount(account: AccountSummary): boolean {
  return account.accessType === 'authorized'
}

export { formatDateTime, formatNumber, formatServerDateTimeInput, parseStrictDatePickerValue }

export function formatTestTerminalResult(result: AccountTestResult): string {
  if (result.outputText?.trim()) return result.outputText.trim()
  if (result.success) return ''
  const rawText = result.responseText?.trim()
  if (!rawText || rawText === result.message.trim()) return ''
  return rawText
}

export function formatAccountTestDuration(value?: number): string {
  return formatMillisecondsAsSeconds(value)
}

export { splitAccountDiagnosticMessage, type AccountDiagnosticMessageParts }
