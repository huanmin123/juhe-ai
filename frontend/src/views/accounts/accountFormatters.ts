import {
  formatDateTime,
  formatMillisecondsAsSeconds,
  formatNumber,
  formatServerDateTimeInput,
  parseStrictDatePickerValue,
  serverDateTimeTimestamp
} from '@/shared/formatters'
import type { AccountListItem, AccountStatus, AccountTestResult } from '@/types/domain'
import {
  accountDiagnosticTooltipLines,
  conciseAccountLastErrorText,
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
  if (status === 'quality_isolated') return 'red'
  return 'default'
}

export function statusText(status: AccountStatus) {
  if (status === 'active') return '可调度'
  if (status === 'pending_test') return '待检查'
  if (status === 'error') return '异常'
  if (status === 'rate_limited') return '限流中'
  if (status === 'temporary_unavailable') return '临时不可调用'
  if (status === 'quality_isolated') return '质量隔离'
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

export function accountStatusColor(account: AccountListItem) {
  if (shouldDisplayEffectiveAvailabilityAsStatus(account)) return account.effectiveAvailability.color
  if (isAuthorizationPaused(account)) return 'orange'
  if (isAuthorizationExpired(account) || isAuthorizationBindingUnavailable(account)) return 'red'
  if (isAccountPackageExpiredStatus(account)) return 'red'
  if (isAuthorizedAccount(account) && account.authorizationQuotaExceeded) return 'red'
  const circuitStatus = activeCircuitStatus(account)
  if (circuitStatus === 'avoided') return 'gold'
  if (circuitStatus === 'verifying' || circuitStatus === 'recovering') return 'blue'
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

export function accountStatusText(account: AccountListItem) {
  if (isAccountInstanceEffectiveAvailability(account) && isDirectAccountStatus(account.effectiveAvailability.status)) {
    return directAccountStatusText(account)
  }
  if (shouldDisplayEffectiveAvailabilityAsStatus(account)) return account.effectiveAvailability.label
  if (isAuthorizationPaused(account)) return '授权暂停'
  if (isAuthorizationExpired(account)) return '授权到期'
  if (isAuthorizationBindingUnavailable(account)) return '授权已失效'
  if (isAccountPackageExpiredStatus(account)) return '账户到期'
  if (isAuthorizedAccount(account) && account.authorizationQuotaExceeded) return '授权额度已用完'
  const circuitStatus = activeCircuitStatus(account)
  if (circuitStatus === 'avoided') return '熔断避让'
  if (circuitStatus === 'verifying') return '熔断验证'
  if (circuitStatus === 'recovering') return '熔断恢复'
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

export function accountCooldownText(account: AccountListItem) {
  if (!isCoolingDown(account)) return ''
  return `暂停至 ${formatDateTime(account.cooldownUntil)}`
}

function accountRetestNextText(account: AccountListItem): string {
  if (!account.cooldownUntil) return ''
  const timestamp = serverDateTimeTimestamp(account.cooldownUntil)
  if (timestamp === undefined) return formatDateTime(account.cooldownUntil)
  if (timestamp <= Date.now()) {
    return `复测排队中（计划 ${formatDateTime(account.cooldownUntil)}）`
  }
  return formatDateTime(account.cooldownUntil)
}

function accountCooldownRetestText(account: AccountListItem): string {
  const parts: string[] = []
  if (account.cooldownRetestFailureCount) {
    parts.push(`连续失败 ${formatNumber(account.cooldownRetestFailureCount)} 次`)
  }
  if (account.cooldownRetestLastAt) {
    const status = account.cooldownRetestLastStatusCode ? `，HTTP ${account.cooldownRetestLastStatusCode}` : ''
    parts.push(`最近 ${formatDateTime(account.cooldownRetestLastAt)}${status}`)
  }
  const nextText = accountRetestNextText(account)
  if (nextText) {
    parts.push(`下次冷却复测：${nextText}`)
  }
  return parts.length ? `后台复测：${parts.join('，')}` : ''
}

export function accountStatusTooltipLines(account: AccountListItem): string[] {
  const lines = accountStatusPresentationTooltipLines(account)
  const circuitStatus = activeCircuitStatus(account)
  if (circuitStatus === 'avoided') {
    lines.push('账户电路正在避让失败作用域，请求不会继续命中该作用域')
  } else if (circuitStatus === 'verifying') {
    lines.push('账户电路正在执行受控验证，验证完成前不会恢复对应作用域')
  } else if (circuitStatus === 'recovering') {
    lines.push('账户电路已收到恢复信号，正在等待恢复流程完成')
  }
  if (circuitStatus && account.circuitSummary?.nextCheckAt) {
    lines.push(`下次电路检查：${formatDateTime(account.circuitSummary.nextCheckAt)}`)
  }
  return lines
}

function conciseAccountStatusTooltipLines(account: AccountListItem): string[] {
  const lines = [directAccountStatusText(account)]
  const effectiveStatus = account.effectiveAvailability?.status
  if (account.status === 'error') {
    lines.push(`异常类型：${accountErrorCodeText(account.lastErrorCode)}`)
  }
  if (effectiveStatus === 'instance_expired') {
    lines.push(account.accountExpiresAt ? `到期时间：${formatDateTime(account.accountExpiresAt)}` : '账户已到期，当前不可用')
  } else if (effectiveStatus === 'instance_pending_test') {
    lines.push(pendingHealthCheckStatusText(account))
  } else if (effectiveStatus === 'instance_disabled') {
    lines.push('已停用，不参与调度')
  } else if (effectiveStatus === 'instance_unschedulable') {
    lines.push('已关闭调度，不参与调度')
  }
  if (isTemporaryAccountStatus(account)) {
    const retestText = accountCooldownRetestText(account)
    if (retestText) lines.push(retestText)
    if (isLongTermUnavailableAccount(account)) {
      lines.push('已进入长期不可用每 1 小时复测；从观察开始满 7 天仍失败时转为异常')
    }
  } else if (account.effectiveAvailability?.status === 'instance_cooldown') {
    const cooldownText = accountCooldownText(account)
    if (cooldownText) {
      lines.push(cooldownText)
    } else {
      lines.push('正在冷却，不参与调度')
    }
  }
  lines.push(...accountHealthCheckTooltipLines(account))
  lines.push(...accountDiagnosticTooltipLines(account.lastErrorMessage, {
    reasonLabel: '最后错误',
    statusCode: account.cooldownRetestLastStatusCode,
    concise: true
  }))
  if (account.lastErrorTraceId) {
    lines.push(`最后错误 traceId：${account.lastErrorTraceId}`)
  }
  return lines
}

function accountHealthCheckTooltipLines(account: AccountListItem): string[] {
  const lines: string[] = []
  if (account.lastHealthCheckAt) {
    lines.push(`最近主动健康检查：${formatDateTime(account.lastHealthCheckAt)}`)
  }
  if (account.lastHealthCheckTraceId) {
    lines.push(`健康检查 traceId：${account.lastHealthCheckTraceId}`)
  }
  if (account.lastHealthSuccessAt) {
    lines.push(`最近健康成功信号：${formatDateTime(account.lastHealthSuccessAt)}`)
  }
  if ((account.status === 'active' || account.status === 'pending_test') && account.nextHealthCheckAt) {
    const nextTimestamp = serverDateTimeTimestamp(account.nextHealthCheckAt)
    const nextText = nextTimestamp !== undefined && nextTimestamp <= Date.now()
      ? `等待复核（计划 ${formatDateTime(account.nextHealthCheckAt)}）`
      : formatDateTime(account.nextHealthCheckAt)
    lines.push(`下次健康复核：${nextText}`)
  }
  if (account.healthCheckFailureCount) {
    const status = account.lastHealthCheckStatusCode ? `，HTTP ${account.lastHealthCheckStatusCode}` : ''
    const code = account.lastHealthCheckErrorCode ? `，${accountErrorCodeText(account.lastHealthCheckErrorCode)}` : ''
    lines.push(`后台健康检测连续失败：${formatNumber(account.healthCheckFailureCount)} 次${status}${code}`)
  }
  const message = formatAccountHealthCheckError(account.lastHealthCheckErrorMessage)
  if (message) {
    lines.push(`健康检测原因：${message}`)
  }
  return lines
}

function formatAccountHealthCheckError(message?: string): string {
  const value = conciseAccountLastErrorText(message)
  const maxLength = 120
  return value.length > maxLength ? `${value.slice(0, maxLength)}...` : value
}

function shouldShowEffectiveAvailabilitySummary(account: AccountListItem): boolean {
  const availability = account.effectiveAvailability
  if (!availability || availability.available) return false
  if (isDirectAccountStatus(availability.status)) return false
  if (availability.blockerScope === 'source_account' || availability.blockerScope === 'runtime') return false
  if (availability.status === 'authorization_expired'
    || availability.status === 'authorization_paused'
    || availability.status === 'authorization_quota_exceeded') {
    return false
  }
  return true
}

function shouldDisplayEffectiveAvailabilityAsStatus(account: AccountListItem): account is AccountListItem & { effectiveAvailability: NonNullable<AccountListItem['effectiveAvailability']> } {
  const availability = account.effectiveAvailability
  return Boolean(availability && !availability.available)
}

function authorizedInstanceLocalStatusTooltipLines(account: AccountListItem): string[] {
  if (!isAuthorizedInstanceLocalStatusHandledAsContext(account)) return []
  const lines = [`授权实例本地状态：${localAccountStatusText(account)}`]
  if (account.status === 'error') {
    lines.push(`本地异常类型：${accountErrorCodeText(account.lastErrorCode)}`)
  }
  if (isTemporaryAccountStatus(account)) {
    const retestText = accountCooldownRetestText(account)
    if (retestText) lines.push(`本地${retestText}`)
    if (isLongTermUnavailableAccount(account)) {
      lines.push('本地已进入长期不可用低频复测；后台仍会自动探活，成功后恢复可调度')
    }
  }
  lines.push(...accountDiagnosticTooltipLines(account.lastErrorMessage, {
    reasonLabel: '本地最后错误',
    idLabelPrefix: '本地',
    statusCode: account.cooldownRetestLastStatusCode,
    concise: true
  }))
  return lines
}

function isAuthorizedInstanceLocalStatusHandledAsContext(account: AccountListItem): boolean {
  return isAuthorizedAccount(account)
    && !isAccountInstanceEffectiveAvailability(account)
    && Boolean(localAccountStatusText(account))
}

function localAccountStatusText(account: AccountListItem): string {
  if (isAccountPackageExpiredStatus(account)) return '账户到期'
  if (isLongTermUnavailableAccount(account)) return '长期不可用'
  if (account.status !== 'active') return statusText(account.status)
  if (isFutureTime(account.cooldownUntil)) return '冷却中'
  if (!account.schedulable) return '停调'
  return ''
}

export function authorizationSourceAccountStatusTag(account: AccountListItem): AccountStatusTagInfo | undefined {
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
  if (sourceStatus === 'quality_isolated') return { color: 'red', label: '来源质量隔离' }
  if (isFutureTime(account.authorizationInstanceSourceAccountCooldownUntil)) return { color: 'gold', label: '来源冷却' }
  if (account.authorizationInstanceSourceAccountSchedulable === false) return { color: 'orange', label: '来源停调' }
  return undefined
}

export function authorizationSourceAccountTooltipLines(account: AccountListItem): string[] {
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

function isAuthorizationSourceAccountExpired(account: AccountListItem): boolean {
  if (!isAuthorizedAccount(account)) return false
  if (account.authorizationInstanceSourceAccountLastErrorCode === 'account_expired') return true
  if (!account.authorizationInstanceSourceAccountExpiresAt) return false
  const time = serverDateTimeTimestamp(account.authorizationInstanceSourceAccountExpiresAt)
  return time !== undefined && time <= Date.now()
}

function activeRuntimeAvailabilityStatus(account: AccountListItem) {
  const status = account.runtimeAvailability?.status
  if (!status || status === 'normal') return undefined
  if (account.status !== 'active') return undefined
  return status
}

function activeCircuitStatus(account: AccountListItem) {
  const status = account.circuitSummary?.status
  if (!status || status === 'normal') return undefined
  if (account.status !== 'active') return undefined
  return status
}

export function hasAccountRuntimeRecoveryState(account: AccountListItem): boolean {
  return Boolean(activeRuntimeAvailabilityStatus(account))
}

export function isTemporaryAccountStatus(account: AccountListItem) {
  return account.status === 'rate_limited' || account.status === 'temporary_unavailable'
}

function isLongTermUnavailableAccount(account: AccountListItem): boolean {
  return isTemporaryAccountStatus(account)
    && account.lastErrorCode === 'cooldown_retest_long_term_unavailable'
}

function isDirectAccountStatus(status: NonNullable<AccountListItem['effectiveAvailability']>['status']): boolean {
  return status.startsWith('instance_')
}

function isAccountInstanceEffectiveAvailability(account: AccountListItem): account is AccountListItem & { effectiveAvailability: NonNullable<AccountListItem['effectiveAvailability']> } {
  const scope = account.effectiveAvailability?.blockerScope
  return scope === 'account' || scope === 'authorized_instance'
}

function isConciseAccountStatus(status: NonNullable<AccountListItem['effectiveAvailability']>['status']): boolean {
  return isDirectAccountStatus(status)
}

function directAccountStatusText(account: AccountListItem): string {
  const status = account.effectiveAvailability?.status
  if (status === 'instance_expired') return '账户到期'
  if (status === 'instance_pending_test') return isPendingHealthCheckFailed(account) ? '检查失败' : '待检查'
  if (status === 'instance_disabled') return '停用'
  if (status === 'instance_error') return '异常'
  if (isLongTermUnavailableAccount(account)) return '长期不可用'
  if (status === 'instance_rate_limited') return '限流中'
  if (status === 'instance_temporary_unavailable') return '临时不可调用'
  if (status === 'instance_quality_isolated') return '质量隔离'
  if (status === 'instance_cooldown') return '冷却中'
  if (status === 'instance_unschedulable') return '停调'
  return statusText(account.status)
}

function pendingHealthCheckStatusText(account: AccountListItem): string {
  if (isPendingHealthCheckFailed(account)) {
    return '后台健康检查未通过，系统每 1 小时自动重试；首次失败持续 24 小时仍未通过时转为异常；人工测试仅用于诊断，不改变账户状态'
  }
  return '等待后台健康检查，通过后自动参与调度；人工测试仅用于诊断，不改变账户状态'
}

export function isPendingHealthCheckFailed(account: AccountListItem): boolean {
  return account.status === 'pending_test'
    && Boolean(account.lastHealthCheckAt)
    && Boolean(account.lastHealthCheckErrorCode || account.lastHealthCheckErrorMessage)
}

export function isCoolingDown(account: AccountListItem) {
  if (!account.cooldownUntil) return false
  return isFutureTime(account.cooldownUntil)
}

function isFutureTime(value?: string): boolean {
  if (!value) return false
  const time = serverDateTimeTimestamp(value)
  return time !== undefined && time > Date.now()
}

export function isAccountPackageExpired(account: AccountListItem) {
  if (!account.accountExpiresAt) return false
  const time = serverDateTimeTimestamp(account.accountExpiresAt)
  return time !== undefined && time <= Date.now()
}

export function isAccountPackageExpiredStatus(account: AccountListItem): boolean {
  return isAccountPackageExpired(account)
    || account.lastErrorCode === 'account_expired'
}

export function isAuthorizationExpired(account: AccountListItem): boolean {
  if (!isAuthorizedAccount(account)) return false
  if (account.authorizationStatus === 'expired') return true
  if (!account.authorizationExpiresAt) return false
  const time = serverDateTimeTimestamp(account.authorizationExpiresAt)
  return time !== undefined && time <= Date.now()
}

export function isAuthorizationPaused(account: AccountListItem): boolean {
  return isAuthorizedAccount(account) && account.authorizationStatus === 'paused'
}

export function isAuthorizationBindingUnavailable(account: AccountListItem): boolean {
  return isAuthorizedAccount(account)
    && account.groupBindStatus === 'authorization_unavailable'
    && !isAuthorizationExpired(account)
    && !isAuthorizationPaused(account)
}

export function isAuthorizedAccount(account: AccountListItem): boolean {
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
